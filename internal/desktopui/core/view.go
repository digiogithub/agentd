package core

import (
	"context"
	"time"

	"github.com/digiogithub/agentd/internal/client"
	"github.com/digiogithub/agentd/internal/desktopui/backend"
	"github.com/digiogithub/agentd/internal/proto"
)

func (b *Backend) view(ctx context.Context, hostID string, a proto.ViewArgs) (proto.View, error) {
	var v proto.View
	err := b.withClient(ctx, hostID, func(c *client.Client) error {
		var err error
		v, err = c.View(a)
		return err
	})
	return v, err
}

// WatchView streams the semantic view of an agentd session: the current
// view first, then a new one whenever the host's event stream reports a
// change for the session (view/status/attention/input, exit, resync,
// reconnect), with a ViewPoll fallback. Unchanged revisions are not sent.
func (b *Backend) WatchView(ctx context.Context, s backend.Session) (<-chan *proto.View, error) {
	if err := needAgentd(s); err != nil {
		return nil, err
	}
	v, err := b.view(ctx, s.HostID, proto.ViewArgs{Name: s.Name})
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
			nv, err := b.view(ctx, s.HostID, proto.ViewArgs{Name: s.Name, SinceRevision: rev, Epoch: epoch})
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
