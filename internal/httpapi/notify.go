package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"time"

	"github.com/digiogithub/agentd/internal/client"
	"github.com/digiogithub/agentd/internal/notify"
	"github.com/digiogithub/agentd/internal/proto"
)

// Notification reasons (the values of [api.notify] on).
const (
	NotifyWaiting  = notify.Waiting
	NotifyTurnDone = notify.TurnDone
	NotifyExited   = notify.Exited
)

// SignatureHeader carries the webhook HMAC: "sha256=<hex>".
const SignatureHeader = notify.SignatureHeader

// Sign returns the webhook signature header value for body.
func Sign(secret string, body []byte) string { return notify.Sign(secret, body) }

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

// notifier adapts hub events onto internal/notify. offer is called from the
// hub and never blocks.
type notifier struct {
	n *notify.Notifier
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
		host := cfg.HostLabel
		if host == "" {
			host = s.Config.Hostname
		}
		if host == "" {
			host, _ = os.Hostname()
		}
		nn := notify.New(notify.Config{
			NtfyURL: cfg.NtfyURL, NtfyToken: cfg.NtfyToken,
			WebhookURL: cfg.WebhookURL, WebhookSecret: cfg.WebhookSecret,
			On: cfg.On, Debounce: cfg.Debounce, IncludePreview: cfg.IncludePreview,
			HostLabel: host, DeepLinkBase: cfg.DeepLinkBase,
			Preview: s.notifyPreview,
			Warn:    func(msg string, args ...any) { s.Log.Warn(msg, args...) },
			Timeout: cfg.Timeout, Backoff: cfg.Backoff, QueueSize: cfg.QueueSize, Client: cfg.Client, Now: cfg.Now,
		})
		n := &notifier{n: nn}
		s.notify = n
		go func() {
			select {
			case <-s.draining:
				nn.Close()
			case <-nn.Done():
			}
		}()
		h := s.hub
		h.mu.Lock()
		h.tap = n.offer
		h.mu.Unlock()
		h.start()
	})
}

func (s *Server) notifyPreview(ctx context.Context, session string) string {
	var si proto.SessionInfo
	err := s.WithDaemon(ctx, func(c *client.Client) (e error) {
		si, e = findSession(c, session)
		return e
	})
	if err != nil {
		return ""
	}
	return si.Preview
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
	if !ok || ev.session == "" {
		return
	}
	n.n.Offer(notify.Event{Session: ev.session, Reason: reason, Status: status, Exit: exit, At: ev.time()})
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

// waitNotifier blocks until the worker has stopped (after draining).
func (s *Server) waitNotifier() {
	if s.notify != nil {
		<-s.notify.n.Done()
	}
}
