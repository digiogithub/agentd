package httpapi

import (
	"net"
	"net/http"
	"os"
	"slices"
	"strings"
	"sync"
	"time"
)

// ---- Host / Origin ----

// buildHostSet is the Host allow-list (security-remote 1): the loopback
// names, the bind address, the machine hostname, the certificate names and
// the configured extras. It is the DNS-rebinding defence: a page on
// evil.example resolving to the gateway's IP still sends Host: evil.example.
func (s *Server) buildHostSet() map[string]bool {
	set := map[string]bool{}
	add := func(h string) {
		h = normHost(h)
		if h != "" {
			set[h] = true
		}
	}
	for _, h := range []string{"localhost", "127.0.0.1", "::1"} {
		add(h)
	}
	if host, _, err := net.SplitHostPort(s.Config.BindAddr); err == nil {
		ip := net.ParseIP(strings.Trim(host, "[]"))
		if host == "" || (ip != nil && ip.IsUnspecified()) {
			if addrs, err := net.InterfaceAddrs(); err == nil {
				for _, a := range addrs {
					if ipn, ok := a.(*net.IPNet); ok {
						add(ipn.IP.String())
					}
				}
			}
		} else {
			add(host)
		}
	}
	hn := s.Config.Hostname
	if hn == "" {
		hn, _ = os.Hostname()
	}
	add(hn)
	for _, h := range s.Config.TLSHosts {
		add(h)
	}
	for _, h := range s.Config.AllowedHosts {
		add(h)
	}
	return set
}

func normHost(h string) string {
	h = strings.ToLower(strings.TrimSpace(h))
	if hh, _, err := net.SplitHostPort(h); err == nil {
		h = hh
	}
	return strings.TrimSuffix(strings.Trim(h, "[]"), ".")
}

func hostAllowed(set map[string]bool, hostport string) bool {
	return set[normHost(hostport)]
}

func (s *Server) originAllowed(r *http.Request) bool {
	vs := r.Header.Values("Origin")
	if len(vs) == 0 {
		return true
	}
	return len(vs) == 1 && vs[0] != "null" && slices.Contains(s.Config.CORSOrigins, vs[0])
}

// secure is the outermost gateway layer: security headers on every response,
// the Host allow-list (421) and the Origin check (403) before anything else,
// authentication included, and CORS answers for the configured origins.
func (s *Server) secure(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "no-referrer")
		if !hostAllowed(s.hosts, r.Host) {
			WriteError(w, &Error{Status: http.StatusMisdirectedRequest, Code: "misdirected_request", Message: "host not allowed"})
			return
		}
		if !s.originAllowed(r) {
			WriteError(w, &Error{Status: http.StatusForbidden, Code: "forbidden", Message: "origin not allowed"})
			return
		}
		if o := r.Header.Get("Origin"); o != "" {
			h.Set("Access-Control-Allow-Origin", o)
			h.Add("Vary", "Origin")
			if r.Method == http.MethodOptions && r.Header.Get("Access-Control-Request-Method") != "" {
				h.Set("Access-Control-Allow-Methods", "GET, POST, DELETE, OPTIONS")
				h.Set("Access-Control-Allow-Headers", "Authorization, Content-Type, If-Match, If-None-Match, Idempotency-Key, Last-Event-ID")
				h.Set("Access-Control-Max-Age", "600")
				w.WriteHeader(http.StatusNoContent)
				return
			}
			h.Set("Access-Control-Expose-Headers", "ETag, Retry-After, X-Request-Id, Idempotent-Replayed")
		}
		next.ServeHTTP(w, r)
	})
}

// ---- rate limits ----

type bucket struct {
	tokens float64
	last   time.Time
}

type limiter struct {
	now                 func() time.Time
	rate, burst         float64
	actRate, actBurst   float64
	maxDev, maxFailAddr int

	mu    sync.Mutex
	dev   map[string]*bucket
	act   map[string]*bucket
	fails map[string][]time.Time
}

const (
	failLimit  = 10
	failWindow = time.Minute
)

func newLimiter(c Config) *limiter {
	l := &limiter{now: time.Now, dev: map[string]*bucket{}, act: map[string]*bucket{}, fails: map[string][]time.Time{}}
	l.rate, l.burst = float64(orInt(c.RateLimit, 20)), float64(orInt(c.RateBurst, 40))
	l.actRate = float64(orInt(c.ActRateLimit, 5))
	l.actBurst = max(l.actRate*2, 1)
	return l
}

func orInt(v, d int) int {
	if v <= 0 {
		return d
	}
	return v
}

func (b *bucket) take(now time.Time, rate, burst float64) (retry int, ok bool) {
	if b.last.IsZero() {
		b.tokens, b.last = burst, now
	}
	b.tokens = min(burst, b.tokens+now.Sub(b.last).Seconds()*rate)
	b.last = now
	if b.tokens >= 1 {
		b.tokens--
		return 0, true
	}
	return int((1-b.tokens)/rate) + 1, false
}

func (l *limiter) bucketFor(m map[string]*bucket, key string) *bucket {
	b := m[key]
	if b == nil {
		if len(m) >= 1024 { // bound memory: drop buckets idle for a minute
			cut := l.now().Add(-time.Minute)
			for k, v := range m {
				if v.last.Before(cut) {
					delete(m, k)
				}
			}
		}
		b = &bucket{}
		m[key] = b
	}
	return b
}

// allow takes one token from the device's bucket (and from the stricter act
// bucket for act).
func (l *limiter) allow(device string, act bool) (retry int, ok bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	if retry, ok = l.bucketFor(l.dev, device).take(now, l.rate, l.burst); !ok {
		return retry, false
	}
	if act {
		return l.bucketFor(l.act, device).take(now, l.actRate, l.actBurst)
	}
	return 0, true
}

func keepSince(ts []time.Time, cut time.Time) []time.Time {
	i := 0
	for i < len(ts) && !ts[i].After(cut) {
		i++
	}
	return ts[i:]
}

// authBlocked reports whether addr already made failLimit failed
// authentications within failWindow.
func (l *limiter) authBlocked(addr string) (retry int, blocked bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	ts := keepSince(l.fails[addr], now.Add(-failWindow))
	if len(ts) == 0 {
		delete(l.fails, addr)
		return 0, false
	}
	l.fails[addr] = ts
	if len(ts) >= failLimit {
		return int(ts[0].Add(failWindow).Sub(now).Seconds()) + 1, true
	}
	return 0, false
}

func (l *limiter) authFailed(addr string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	if len(l.fails) >= 4096 {
		for a, ts := range l.fails {
			if len(keepSince(ts, now.Add(-failWindow))) == 0 {
				delete(l.fails, a)
			}
		}
	}
	l.fails[addr] = append(keepSince(l.fails[addr], now.Add(-failWindow)), now)
}

func remoteIP(r *http.Request) string {
	if h, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
		return h
	}
	return r.RemoteAddr
}

// ---- SSE stream caps ----

type streamCounter struct {
	mu     sync.Mutex
	total  int
	device map[string]int
}

// acquireStream reserves a stream slot for device (429 too_many_streams when
// the per-device or global cap is reached). Call the returned func once.
func (s *Server) acquireStream(device string) (func(), error) {
	perDev, total := orInt(s.Config.MaxStreamsPerDevice, 8), orInt(s.Config.MaxStreams, 64)
	sc := &s.streams
	sc.mu.Lock()
	defer sc.mu.Unlock()
	if sc.device == nil {
		sc.device = map[string]int{}
	}
	if sc.total >= total || sc.device[device] >= perDev {
		return nil, &Error{Status: http.StatusTooManyRequests, Code: "too_many_streams", Retryable: true, RetryAfter: 5,
			Message: "too many open event streams"}
	}
	sc.total++
	sc.device[device]++
	var once sync.Once
	return func() {
		once.Do(func() {
			sc.mu.Lock()
			defer sc.mu.Unlock()
			sc.total--
			if sc.device[device]--; sc.device[device] <= 0 {
				delete(sc.device, device)
			}
		})
	}, nil
}
