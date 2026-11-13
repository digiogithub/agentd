package httpapi

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/digiogithub/agentd/internal/client"
	"github.com/digiogithub/agentd/internal/proto"
)

// Notification reasons (the values of [api.notify] on).
const (
	NotifyWaiting  = "waiting"
	NotifyTurnDone = "turn_done"
	NotifyExited   = "exited"
)

const (
	notifyQueueSize    = 256
	notifyTimeout      = 10 * time.Second
	notifyRetries      = 3
	notifyBackoff      = time.Second
	notifyPreviewChars = 160
	notifyMaxResponse  = 4 << 10
)

// SignatureHeader carries the webhook HMAC: "sha256=<hex>".
const SignatureHeader = "X-Agentd-Signature"

// NotifyConfig is the gateway's view of [api.notify]. The zero value is
// disabled; cmd/agentd maps config.NotifyConfig onto it.
type NotifyConfig struct {
	Enabled       bool
	NtfyURL       string
	NtfyToken     string
	WebhookURL    string
	WebhookSecret string
	// On lists the reasons that notify (waiting, turn_done, exited);
	// empty = all three.
	On []string
	// Debounce is the minimum gap per session and reason (0 = none).
	Debounce time.Duration
	// IncludePreview adds the session's preview line (never other screen
	// content). Off by default: payloads carry no terminal text.
	IncludePreview bool
	// HostLabel names this host in payloads; "" = Config.Hostname / os.Hostname.
	HostLabel string
	// DeepLinkBase is the deep link scheme and host ("agentd://open").
	DeepLinkBase string

	// Test knobs.
	Timeout   time.Duration
	Backoff   time.Duration
	QueueSize int
	Client    *http.Client
	Now       func() time.Time
}

// notifyJob is one delivery to one sink.
type notifyJob struct {
	sink    string // "ntfy" | "webhook"
	session string
	reason  string
	status  string
	exit    *int
	at      time.Time
}

// notifier turns hub events into ntfy / webhook deliveries. offer is called
// from the hub and never blocks (a mutex and a bounded slice); the HTTP work
// happens on one worker goroutine.
type notifier struct {
	s    *Server
	cfg  NotifyConfig
	on   map[string]bool
	host string
	http *http.Client
	now  func() time.Time

	mu    sync.Mutex
	queue []notifyJob
	last  map[string]time.Time
	wake  chan struct{}
	done  chan struct{}
}

// startNotifier starts the notification worker and taps the hub. It is a
// no-op (no goroutine, no hub subscription) unless [api.notify] is enabled,
// and idempotent. Serve calls it; tests that use Handler call it directly.
func (s *Server) startNotifier() {
	s.init()
	s.notifyOnce.Do(func() {
		cfg := s.Config.Notify
		if !cfg.Enabled || (cfg.NtfyURL == "" && cfg.WebhookURL == "") {
			return
		}
		n := &notifier{s: s, cfg: cfg, on: map[string]bool{}, last: map[string]time.Time{},
			wake: make(chan struct{}, 1), done: make(chan struct{})}
		on := cfg.On
		if len(on) == 0 {
			on = []string{NotifyWaiting, NotifyTurnDone, NotifyExited}
		}
		for _, r := range on {
			n.on[r] = true
		}
		n.host = cfg.HostLabel
		if n.host == "" {
			n.host = s.Config.Hostname
		}
		if n.host == "" {
			n.host, _ = os.Hostname()
		}
		if n.cfg.DeepLinkBase == "" {
			n.cfg.DeepLinkBase = "agentd://open"
		}
		if n.cfg.Timeout <= 0 {
			n.cfg.Timeout = notifyTimeout
		}
		if n.cfg.Backoff <= 0 {
			n.cfg.Backoff = notifyBackoff
		}
		if n.cfg.QueueSize <= 0 {
			n.cfg.QueueSize = notifyQueueSize
		}
		n.http = cfg.Client
		if n.http == nil {
			// No redirects: a sink must not bounce a secret to another host.
			n.http = &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
		}
		n.now = cfg.Now
		if n.now == nil {
			n.now = time.Now
		}
		s.notify = n
		h := s.hub
		h.mu.Lock()
		h.tap = n.offer
		h.mu.Unlock()
		go n.run()
		h.start()
	})
}

// mapEvent maps a hub event to a notification reason and status; ok=false
// means the event does not notify.
func mapEvent(ev *hubEvent) (reason, status string, exit *int, ok bool) {
	switch ev.kind {
	case proto.EvAttentionChanged:
		var d proto.AttentionChangedData
		if json.Unmarshal(ev.data, &d) != nil || !d.Attention {
			return "", "", nil, false
		}
		if d.Reason == NotifyTurnDone {
			return NotifyTurnDone, "idle", nil, true
		}
		// waiting, bell and notify all mean "needs a human now".
		return NotifyWaiting, "waiting", nil, true
	case proto.EvSessionExited:
		var d proto.ExitedData
		_ = json.Unmarshal(ev.data, &d)
		return NotifyExited, "exited", &d.ExitCode, true
	}
	return "", "", nil, false
}

// offer is the hub tap. It must not block.
func (n *notifier) offer(ev *hubEvent) {
	reason, status, exit, ok := mapEvent(ev)
	if !ok || !n.on[reason] || ev.session == "" {
		return
	}
	now := n.now()
	key := ev.session + "\x00" + reason
	n.mu.Lock()
	defer n.mu.Unlock()
	if d := n.cfg.Debounce; d > 0 {
		if t, seen := n.last[key]; seen && now.Sub(t) < d {
			return
		}
		if len(n.last) > 1024 {
			for k, t := range n.last {
				if now.Sub(t) >= d {
					delete(n.last, k)
				}
			}
		}
	}
	n.last[key] = now
	for _, sink := range []string{"ntfy", "webhook"} {
		if (sink == "ntfy" && n.cfg.NtfyURL == "") || (sink == "webhook" && n.cfg.WebhookURL == "") {
			continue
		}
		if len(n.queue) >= n.cfg.QueueSize {
			n.queue = n.queue[1:]
			n.s.Log.Warn("notify: queue full, dropped oldest notification")
		}
		n.queue = append(n.queue, notifyJob{sink: sink, session: ev.session, reason: reason, status: status, exit: exit, at: ev.time()})
	}
	select {
	case n.wake <- struct{}{}:
	default:
	}
}

func (ev *hubEvent) time() time.Time {
	var e struct {
		TS time.Time `json:"ts"`
	}
	if json.Unmarshal(ev.payload, &e) == nil && !e.TS.IsZero() {
		return e.TS
	}
	return time.Now().UTC()
}

func (n *notifier) pop() (notifyJob, bool) {
	n.mu.Lock()
	defer n.mu.Unlock()
	if len(n.queue) == 0 {
		return notifyJob{}, false
	}
	j := n.queue[0]
	n.queue = n.queue[1:]
	return j, true
}

func (n *notifier) run() {
	defer close(n.done)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		select {
		case <-n.s.draining:
			cancel()
		case <-n.done:
		}
	}()
	for {
		j, ok := n.pop()
		if !ok {
			select {
			case <-n.wake:
				continue
			case <-ctx.Done():
				return
			}
		}
		if ctx.Err() != nil {
			return
		}
		n.deliver(ctx, j)
	}
}

// deliver sends one job with retries (network errors, 429 and 5xx retry; other
// statuses do not). Secrets and URLs never reach the log: only the sink name,
// the status code and the error class.
func (n *notifier) deliver(ctx context.Context, j notifyJob) {
	preview := ""
	if n.cfg.IncludePreview {
		preview = n.preview(ctx, j.session)
	}
	var lastErr string
	for attempt := 0; attempt <= notifyRetries; attempt++ {
		if attempt > 0 {
			t := time.NewTimer(n.cfg.Backoff << (attempt - 1))
			select {
			case <-t.C:
			case <-ctx.Done():
				t.Stop()
				return
			}
		}
		retry, err := n.send(ctx, j, preview)
		if err == nil {
			return
		}
		lastErr = err.Error()
		if !retry || ctx.Err() != nil {
			break
		}
	}
	n.s.Log.Warn("notify: delivery failed", "sink", j.sink, "session", j.session, "reason", j.reason, "err", lastErr)
}

func (n *notifier) preview(ctx context.Context, session string) string {
	var si proto.SessionInfo
	err := n.s.WithDaemon(ctx, func(c *client.Client) (e error) {
		si, e = findSession(c, session)
		return e
	})
	if err != nil {
		return ""
	}
	return cleanPreview(si.Preview)
}

// cleanPreview drops control characters and clips to notifyPreviewChars runes.
func cleanPreview(s string) string {
	s = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || r == ' ' || r == ' ' {
			return ' '
		}
		return r
	}, s)
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > notifyPreviewChars {
		s = string(r[:notifyPreviewChars])
	}
	return s
}

func (n *notifier) link(session string) string {
	sep := "?"
	if strings.Contains(n.cfg.DeepLinkBase, "?") {
		sep = "&"
	}
	return n.cfg.DeepLinkBase + sep + "h=" + url.QueryEscape(n.host) + "&s=" + url.QueryEscape(session)
}

func (j notifyJob) message() string {
	switch j.reason {
	case NotifyWaiting:
		return j.session + " is waiting for input"
	case NotifyTurnDone:
		return j.session + " finished its turn"
	}
	if j.exit != nil {
		return j.session + " exited (code " + strconv.Itoa(*j.exit) + ")"
	}
	return j.session + " exited"
}

// webhookPayload is the webhook JSON body.
type webhookPayload struct {
	Host     string `json:"host"`
	Session  string `json:"session"`
	Reason   string `json:"reason"`
	Status   string `json:"status"`
	ExitCode *int   `json:"exit_code,omitempty"`
	At       string `json:"at"`
	Link     string `json:"link"`
	Preview  string `json:"preview,omitempty"`
}

func (n *notifier) send(ctx context.Context, j notifyJob, preview string) (retry bool, err error) {
	var req *http.Request
	cctx, cancel := context.WithTimeout(ctx, n.cfg.Timeout)
	defer cancel()
	if j.sink == "ntfy" {
		msg := j.message()
		if preview != "" {
			msg += "\n" + preview
		}
		req, err = http.NewRequestWithContext(cctx, http.MethodPost, n.cfg.NtfyURL, strings.NewReader(msg))
		if err != nil {
			return false, errors.New("bad ntfy request")
		}
		req.Header.Set("Content-Type", "text/plain; charset=utf-8")
		req.Header.Set("Title", "agentd: "+j.session)
		req.Header.Set("Click", n.link(j.session))
		tags, prio := "hourglass_flowing_sand", "4"
		switch {
		case j.reason == NotifyTurnDone:
			tags, prio = "white_check_mark", "3"
		case j.reason == NotifyExited && j.exit != nil && *j.exit != 0:
			tags, prio = "x", "4"
		case j.reason == NotifyExited:
			tags, prio = "checkered_flag", "3"
		}
		req.Header.Set("Tags", tags)
		req.Header.Set("Priority", prio)
		if n.cfg.NtfyToken != "" {
			req.Header.Set("Authorization", "Bearer "+n.cfg.NtfyToken)
		}
	} else {
		body, _ := json.Marshal(webhookPayload{Host: n.host, Session: j.session, Reason: j.reason, Status: j.status,
			ExitCode: j.exit, At: j.at.UTC().Format(time.RFC3339), Link: n.link(j.session), Preview: preview})
		req, err = http.NewRequestWithContext(cctx, http.MethodPost, n.cfg.WebhookURL, bytes.NewReader(body))
		if err != nil {
			return false, errors.New("bad webhook request")
		}
		req.Header.Set("Content-Type", "application/json")
		if n.cfg.WebhookSecret != "" {
			req.Header.Set(SignatureHeader, Sign(n.cfg.WebhookSecret, body))
		}
	}
	req.Header.Set("User-Agent", "agentd-notify")
	resp, err := n.http.Do(req)
	if err != nil {
		var ue *url.Error
		if errors.As(err, &ue) {
			err = ue.Err // drop the URL: it may embed a secret topic
		}
		return true, err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, notifyMaxResponse))
	switch {
	case resp.StatusCode/100 == 2:
		return false, nil
	case resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500:
		return true, fmt.Errorf("status %d", resp.StatusCode)
	}
	return false, fmt.Errorf("status %d", resp.StatusCode)
}

// Sign returns the webhook signature header value for body.
func Sign(secret string, body []byte) string {
	m := hmac.New(sha256.New, []byte(secret))
	m.Write(body)
	return "sha256=" + hex.EncodeToString(m.Sum(nil))
}

// waitNotifier blocks until the worker has stopped (after draining).
func (s *Server) waitNotifier() {
	if s.notify != nil {
		<-s.notify.done
	}
}
