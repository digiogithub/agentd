package daemon

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/digiogithub/agentd/internal/notify"
	"github.com/digiogithub/agentd/internal/proto"
)

// daemonNotify is the daemon's own notification tap ([notify], AGED-US-0044):
// an in-process bus subscriber that maps events onto internal/notify sinks
// (ntfy, webhook). Disabled (the default) = no goroutine and no subscription.
type daemonNotify struct {
	mu     sync.Mutex
	cancel context.CancelFunc
	done   chan struct{}
	n      *notify.Notifier
}

// notifyKinds are the only bus kinds the tap looks at.
var notifyKinds = []string{proto.EvAttentionChanged, proto.EvSessionExited, proto.EvMailQueued, "task.changed"}

// startNotify starts the tap when [notify] is enabled with a sink. A secret
// problem (named env var unset, plain secret in a group/other-readable file)
// leaves it off and logs the cause (never the secret).
func (s *Server) startNotify() {
	if s.Cfg == nil {
		return
	}
	cfg := s.Cfg.Notify
	if !cfg.Enabled || (cfg.NtfyURL == "" && cfg.WebhookURL == "") {
		return
	}
	if err := cfg.ResolveSecretsFor("notify", os.Getenv, s.Cfg.Path); err != nil {
		s.Log.Error("notify disabled", "err", err)
		return
	}
	d := &s.ntf
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.cancel != nil {
		return
	}
	sub, _ := s.bus.subscribe(eventFilter{kinds: notifyKinds}, 0, "")
	if sub == nil {
		return // bus already closed
	}
	host := cfg.HostLabel
	if host == "" {
		host, _ = os.Hostname()
	}
	d.n = notify.New(notify.Config{
		NtfyURL: cfg.NtfyURL, NtfyToken: cfg.NtfyToken,
		WebhookURL: cfg.WebhookURL, WebhookSecret: cfg.WebhookSecret,
		On: cfg.On, Debounce: time.Duration(cfg.Debounce), IncludePreview: cfg.IncludePreview,
		HostLabel: host, DeepLinkBase: cfg.DeepLinkBase,
		Preview: func(_ context.Context, session string) string {
			e, err := s.lookup(session)
			if err != nil || e.view == nil {
				return ""
			}
			_, _, _, preview, _ := e.view.summary()
			return preview
		},
		Warn: func(msg string, args ...any) { s.Log.Warn(msg, args...) },
	})
	// questionText reads a question's text from the store: mail events never
	// carry bodies, and the sink drops it again unless include_preview.
	questionText := func(id int64) string {
		if !cfg.IncludePreview || s.Store == nil {
			return ""
		}
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		m, err := s.Store.Get(ctx, id)
		if err != nil {
			return ""
		}
		return m.Body
	}
	ctx, cancel := context.WithCancel(context.Background())
	d.cancel = cancel
	d.done = make(chan struct{})
	go func() {
		defer close(d.done)
		defer s.bus.unsubscribe(sub)
		for {
			select {
			case ev := <-sub.ch:
				if ne, ok := notifyEvent(ev, questionText); ok {
					d.n.Offer(ne)
				}
			case <-sub.quit:
				return
			case <-ctx.Done():
				return
			}
		}
	}()
	s.Log.Info("notify enabled", "ntfy", cfg.NtfyURL != "", "webhook", cfg.WebhookURL != "", "on", cfg.On)
}

// stopNotify stops the tap and its worker; Shutdown calls it before the bus
// closes. Safe when never started.
func (s *Server) stopNotify() {
	d := &s.ntf
	d.mu.Lock()
	cancel, done, n := d.cancel, d.done, d.n
	d.cancel = nil
	d.mu.Unlock()
	if cancel == nil {
		return
	}
	cancel()
	<-done
	n.Close()
}

// notifyEvent maps a bus event to a notification; ok=false = no notification.
// Agent-written text (Text) is carried for question and blocked only; the
// sink drops it unless include_preview is on. Fields another producer adds
// (mail `kind`, task `changed`) are read defensively. questionText, when not
// nil, returns the text of the question with the given message id.
func notifyEvent(ev proto.Event, questionText func(id int64) string) (notify.Event, bool) {
	out := notify.Event{Session: ev.Session, At: ev.Time}
	switch ev.Kind {
	case proto.EvAttentionChanged:
		var d proto.AttentionChangedData
		if ev.Session == "" || json.Unmarshal(ev.Data, &d) != nil || !d.Attention {
			return out, false
		}
		if d.Reason == notify.TurnDone {
			out.Reason, out.Status = notify.TurnDone, "idle"
		} else { // waiting, bell and notify all mean "needs a human now"
			out.Reason, out.Status = notify.Waiting, "waiting"
		}
		return out, true
	case proto.EvSessionExited:
		var d proto.ExitedData
		if ev.Session == "" {
			return out, false
		}
		_ = json.Unmarshal(ev.Data, &d)
		out.Reason, out.Status, out.Exit = notify.Exited, "exited", &d.ExitCode
		return out, true
	case proto.EvMailQueued:
		var d struct {
			ID   int64  `json:"id"`
			From string `json:"from"`
			To   string `json:"to"`
			Kind string `json:"kind"`
		}
		if json.Unmarshal(ev.Data, &d) != nil || d.Kind != "question" || !strings.HasPrefix(d.To, "human:") {
			return out, false
		}
		out.Session, out.Reason, out.Status = d.From, notify.Question, "waiting"
		if questionText != nil {
			out.Text = questionText(d.ID)
		}
		return out, true
	case "task.changed":
		var d struct {
			Changed []struct {
				Title  string `json:"title"`
				Status string `json:"status"`
				From   string `json:"from"`
			} `json:"changed"`
		}
		if json.Unmarshal(ev.Data, &d) != nil {
			return out, false
		}
		for _, c := range d.Changed {
			if c.Status == "blocked" && c.From != "blocked" {
				out.Reason, out.Status, out.Text = notify.Blocked, "blocked", c.Title
				return out, true
			}
		}
	}
	return out, false
}
