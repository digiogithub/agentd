// Package notify delivers "something needs the human" notifications to ntfy
// and webhook sinks. It is transport only: callers (the daemon's event-bus tap,
// the HTTP gateway) map their events to Event and call Offer, which never
// blocks. One worker goroutine does the HTTP work with a bounded queue (drop
// oldest), per session+reason debounce, retries on 429/5xx/network errors, no
// redirects and a per-request timeout. URLs, secrets and bodies are never
// logged. Stdlib only.
package notify

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
)

// Notification reasons.
const (
	Question = "question"
	Waiting  = "waiting"
	Blocked  = "blocked"
	TurnDone = "turn_done"
	Exited   = "exited"
)

const (
	defaultQueueSize = 256
	defaultTimeout   = 10 * time.Second
	defaultBackoff   = time.Second
	retries          = 3
	// PreviewChars caps the text a payload may carry.
	PreviewChars = 160
	maxResponse  = 4 << 10
)

// SignatureHeader carries the webhook HMAC: "sha256=<hex>".
const SignatureHeader = "X-Agentd-Signature"

// Event is one thing to tell the human about.
type Event struct {
	// Session the event is about (may be empty for host-wide ones).
	Session string
	Reason  string
	// Status is the session status word for the webhook payload.
	Status string
	// Exit is the exit code of an `exited` event.
	Exit *int
	// At defaults to now.
	At time.Time
	// Text is agent-written text (a question or task title). It is sanitized
	// and capped, and only sent when IncludePreview is on.
	Text string
}

// Config configures a Notifier. Zero values pick the defaults.
type Config struct {
	NtfyURL, NtfyToken        string
	WebhookURL, WebhookSecret string
	// On lists the reasons that notify; empty = waiting, turn_done, exited.
	On []string
	// Debounce is the minimum gap per session and reason (0 = none).
	Debounce time.Duration
	// IncludePreview allows Event.Text / Preview in payloads.
	IncludePreview bool
	// HostLabel names this host in payloads ("" = os.Hostname).
	HostLabel string
	// DeepLinkBase is the deep link scheme and host (default agentd://open).
	DeepLinkBase string
	// Preview, if set, supplies the session's preview line at delivery time
	// for events without Text (only used when IncludePreview).
	Preview func(ctx context.Context, session string) string
	// Warn logs a delivery problem (message plus key/value pairs). It never
	// receives URLs, secrets or bodies.
	Warn func(msg string, args ...any)

	// Test knobs.
	Timeout   time.Duration
	Backoff   time.Duration
	QueueSize int
	Client    *http.Client
	Now       func() time.Time
}

// Enabled reports whether the config has any sink.
func (c Config) Enabled() bool { return c.NtfyURL != "" || c.WebhookURL != "" }

type job struct {
	sink string // "ntfy" | "webhook"
	ev   Event
}

// Notifier queues events and delivers them from one worker goroutine.
type Notifier struct {
	cfg  Config
	on   map[string]bool
	host string
	http *http.Client
	now  func() time.Time

	mu    sync.Mutex
	queue []job
	last  map[string]time.Time
	wake  chan struct{}

	ctx    context.Context
	cancel context.CancelFunc
	done   chan struct{}
}

// New starts a Notifier. The caller must not call it when no sink is set.
func New(cfg Config) *Notifier {
	n := &Notifier{cfg: cfg, on: map[string]bool{}, last: map[string]time.Time{},
		wake: make(chan struct{}, 1), done: make(chan struct{})}
	on := cfg.On
	if len(on) == 0 {
		on = []string{Waiting, TurnDone, Exited}
	}
	for _, r := range on {
		n.on[r] = true
	}
	n.host = cfg.HostLabel
	if n.host == "" {
		n.host, _ = os.Hostname()
	}
	if n.cfg.DeepLinkBase == "" {
		n.cfg.DeepLinkBase = "agentd://open"
	}
	if n.cfg.Timeout <= 0 {
		n.cfg.Timeout = defaultTimeout
	}
	if n.cfg.Backoff <= 0 {
		n.cfg.Backoff = defaultBackoff
	}
	if n.cfg.QueueSize <= 0 {
		n.cfg.QueueSize = defaultQueueSize
	}
	if n.cfg.Warn == nil {
		n.cfg.Warn = func(string, ...any) {}
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
	n.ctx, n.cancel = context.WithCancel(context.Background())
	go n.run()
	return n
}

// Close stops the worker (an in-flight delivery is cancelled) and waits for it.
func (n *Notifier) Close() {
	n.cancel()
	<-n.done
}

// Done is closed when the worker has stopped.
func (n *Notifier) Done() <-chan struct{} { return n.done }

// Offer queues ev for every sink unless its reason is filtered out or
// debounced. It never blocks.
func (n *Notifier) Offer(ev Event) {
	if !n.on[ev.Reason] {
		return
	}
	now := n.now()
	if ev.At.IsZero() {
		ev.At = now.UTC()
	}
	key := ev.Session + "\x00" + ev.Reason
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
			n.cfg.Warn("notify: queue full, dropped oldest notification")
		}
		n.queue = append(n.queue, job{sink: sink, ev: ev})
	}
	select {
	case n.wake <- struct{}{}:
	default:
	}
}

func (n *Notifier) pop() (job, bool) {
	n.mu.Lock()
	defer n.mu.Unlock()
	if len(n.queue) == 0 {
		return job{}, false
	}
	j := n.queue[0]
	n.queue = n.queue[1:]
	return j, true
}

func (n *Notifier) run() {
	defer close(n.done)
	for {
		j, ok := n.pop()
		if !ok {
			select {
			case <-n.wake:
				continue
			case <-n.ctx.Done():
				return
			}
		}
		if n.ctx.Err() != nil {
			return
		}
		n.deliver(n.ctx, j)
	}
}

// deliver sends one job with retries (network errors, 429 and 5xx retry; other
// statuses do not). Only the sink name, the status code and the error class are
// logged.
func (n *Notifier) deliver(ctx context.Context, j job) {
	preview := ""
	if n.cfg.IncludePreview {
		switch {
		case j.ev.Text != "":
			preview = CleanText(j.ev.Text)
		case n.cfg.Preview != nil && j.ev.Session != "":
			preview = CleanText(n.cfg.Preview(ctx, j.ev.Session))
		}
	}
	var lastErr string
	for attempt := 0; attempt <= retries; attempt++ {
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
	n.cfg.Warn("notify: delivery failed", "sink", j.sink, "session", j.ev.Session, "reason", j.ev.Reason, "err", lastErr)
}

// CleanText drops control characters and clips to PreviewChars runes.
func CleanText(s string) string {
	s = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || r == '\u2028' || r == '\u2029' {
			return ' '
		}
		return r
	}, s)
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > PreviewChars {
		s = string(r[:PreviewChars])
	}
	return s
}

func (n *Notifier) link(session string) string {
	sep := "?"
	if strings.Contains(n.cfg.DeepLinkBase, "?") {
		sep = "&"
	}
	l := n.cfg.DeepLinkBase + sep + "h=" + url.QueryEscape(n.host)
	if session != "" {
		l += "&s=" + url.QueryEscape(session)
	}
	return l
}

func subject(e Event) string {
	if e.Session == "" {
		return "a task"
	}
	return e.Session
}

// Message is the short human text of an event (never screen text).
func Message(e Event) string {
	switch e.Reason {
	case Waiting:
		return subject(e) + " is waiting for input"
	case TurnDone:
		return subject(e) + " finished its turn"
	case Question:
		return subject(e) + " asked you a question"
	case Blocked:
		if e.Session == "" {
			return "a task is blocked"
		}
		return e.Session + " has a blocked task"
	}
	if e.Exit != nil {
		return subject(e) + " exited (code " + strconv.Itoa(*e.Exit) + ")"
	}
	return subject(e) + " exited"
}

// Payload is the webhook JSON body.
type Payload struct {
	Host     string `json:"host"`
	Session  string `json:"session"`
	Reason   string `json:"reason"`
	Status   string `json:"status"`
	ExitCode *int   `json:"exit_code,omitempty"`
	At       string `json:"at"`
	Link     string `json:"link"`
	Preview  string `json:"preview,omitempty"`
}

func (n *Notifier) send(ctx context.Context, j job, preview string) (retry bool, err error) {
	var req *http.Request
	e := j.ev
	cctx, cancel := context.WithTimeout(ctx, n.cfg.Timeout)
	defer cancel()
	if j.sink == "ntfy" {
		msg := Message(e)
		if preview != "" {
			msg += "\n" + preview
		}
		req, err = http.NewRequestWithContext(cctx, http.MethodPost, n.cfg.NtfyURL, strings.NewReader(msg))
		if err != nil {
			return false, errors.New("bad ntfy request")
		}
		req.Header.Set("Content-Type", "text/plain; charset=utf-8")
		title := "agentd"
		if e.Session != "" {
			title += ": " + e.Session
		}
		req.Header.Set("Title", title)
		req.Header.Set("Click", n.link(e.Session))
		tags, prio := "hourglass_flowing_sand", "4"
		switch {
		case e.Reason == Question:
			tags, prio = "question", "4"
		case e.Reason == Blocked:
			tags, prio = "no_entry", "4"
		case e.Reason == TurnDone:
			tags, prio = "white_check_mark", "3"
		case e.Reason == Exited && e.Exit != nil && *e.Exit != 0:
			tags, prio = "x", "4"
		case e.Reason == Exited:
			tags, prio = "checkered_flag", "3"
		}
		req.Header.Set("Tags", tags)
		req.Header.Set("Priority", prio)
		if n.cfg.NtfyToken != "" {
			req.Header.Set("Authorization", "Bearer "+n.cfg.NtfyToken)
		}
	} else {
		body, _ := json.Marshal(Payload{Host: n.host, Session: e.Session, Reason: e.Reason, Status: e.Status,
			ExitCode: e.Exit, At: e.At.UTC().Format(time.RFC3339), Link: n.link(e.Session), Preview: preview})
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
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxResponse))
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
