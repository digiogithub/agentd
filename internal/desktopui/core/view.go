package core

import (
	"context"
	"strings"
	"time"

	"github.com/digiogithub/agentd/internal/client"
	"github.com/digiogithub/agentd/internal/desktopui/backend"
	"github.com/digiogithub/agentd/internal/proto"
)

func (b *Backend) view(ctx context.Context, hostID string, a proto.ViewArgs) (proto.View, error) {
	plain := false // a daemon without markup: lines get escaped text as md
	if a.Markup {
		if _, old := b.noMarkup.Load(hostID); old {
			a.Markup, plain = false, true
		}
	}
	var v proto.View
	err := b.withClient(ctx, hostID, func(c *client.Client) error {
		var err error
		v, err = c.View(a)
		return err
	})
	if err != nil && a.Markup && markupRefused(err) {
		// A daemon before 0.6.3 rejects the unknown "markup" argument:
		// remember it for this host and show plain text.
		b.noMarkup.Store(hostID, true)
		return b.view(ctx, hostID, a)
	}
	if plain {
		for i, l := range v.Lines {
			if md := escapeMarkup(l.Text); md != l.Text {
				v.Lines[i].Md = md
			}
		}
	}
	return v, err
}

// escapeMarkup makes text CommonMark that shows as itself (the escaping of
// screen.Markup, for daemons that send no md).
func escapeMarkup(text string) string {
	var bl strings.Builder
	for _, r := range text {
		if r < 128 && strings.ContainsRune("\\*_`[]<>|~#&", r) {
			bl.WriteByte('\\')
		}
		bl.WriteRune(r)
	}
	out := bl.String()
	if t := strings.TrimSpace(out); t != "" && strings.Trim(t, "-= ") == "" {
		i := strings.IndexAny(out, "-=")
		out = out[:i] + "\\" + out[i:]
	}
	return out
}

// markupRefused reports the error of a daemon that does not know view's
// "markup" argument (it rejects unknown fields as invalid).
func markupRefused(err error) bool {
	return proto.CodeOf(err) == proto.CodeInvalid && strings.Contains(err.Error(), "markup")
}

// WatchView streams the semantic view of an agentd session: the current
// view first, then a new one whenever the host's event stream reports a
// change for the session (view/status/attention/input, exit, resync,
// reconnect), with a ViewPoll fallback. Unchanged revisions are not sent.
func (b *Backend) WatchView(ctx context.Context, s backend.Session) (<-chan *proto.View, error) {
	if err := needAgentd(s); err != nil {
		return nil, err
	}
	v, err := b.view(ctx, s.HostID, proto.ViewArgs{Name: s.Name, Markup: true})
	if err != nil {
		return nil, err
	}
	m := b.activate(s.HostID)
	trig := make(chan struct{}, 1)
	m.addView(s.Name, trig)
	out := make(chan *proto.View, 1)
	out <- &v
	go func() {
		defer close(out)
		defer m.removeView(s.Name, trig)
		rev, epoch := v.Revision, v.Epoch
		poll := time.NewTicker(b.opts.ViewPoll)
		defer poll.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-b.ctx.Done():
				return
			case <-trig:
			case <-poll.C:
			}
			nv, err := b.view(ctx, s.HostID, proto.ViewArgs{Name: s.Name, SinceRevision: rev, Epoch: epoch, Markup: true})
			if err != nil || nv.NotModified {
				continue
			}
			rev, epoch = nv.Revision, nv.Epoch
			select { // latest value wins
			case <-out:
			default:
			}
			out <- &nv
		}
	}()
	return out, nil
}
