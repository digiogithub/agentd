package daemon

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/digiogithub/agentd/internal/config"
	"github.com/digiogithub/agentd/internal/mailbox"
	"github.com/digiogithub/agentd/internal/proto"
)

// Mailbox retention and the purge op (AGENTS.md, "Mailbox states").

// Clock is the time source of the retention loop and of purge's older_than.
// Tests inject a fake one through Server.Clock.
type Clock interface {
	Now() time.Time
	// NewTicker returns a channel that fires every d and a stop function.
	NewTicker(d time.Duration) (<-chan time.Time, func())
}

type realClock struct{}

func (realClock) Now() time.Time { return time.Now() }

func (realClock) NewTicker(d time.Duration) (<-chan time.Time, func()) {
	t := time.NewTicker(d)
	return t.C, t.Stop
}

func (s *Server) clock() Clock {
	if s.Clock != nil {
		return s.Clock
	}
	return realClock{}
}

// retentionTimeout bounds one retention purge.
const retentionTimeout = time.Minute

// runRetention purges messages older than retention once now and then every
// interval, until ctx is done.
func (s *Server) runRetention(ctx context.Context, retention, interval time.Duration) {
	clk := s.clock()
	tick, stop := clk.NewTicker(interval)
	defer stop()
	for {
		s.retentionPurge(ctx, clk.Now(), retention)
		select {
		case <-ctx.Done():
			return
		case <-tick:
		}
	}
}

// retentionPurge deletes every message (any state) created before
// now-retention and logs the result.
func (s *Server) retentionPurge(ctx context.Context, now time.Time, retention time.Duration) {
	ctx, cancel := context.WithTimeout(ctx, retentionTimeout)
	defer cancel()
	before := now.Add(-retention)
	res, err := s.Store.Purge(ctx, mailbox.PurgeOptions{Before: before})
	switch {
	case errors.Is(err, context.Canceled):
		// shutting down
	case err != nil:
		s.Log.Error("mailbox retention purge", "err", err)
	default:
		s.Log.Info("mailbox retention purge", "purged", res.Count,
			"retention", retention.String(), "before", before.UTC().Format(time.RFC3339))
	}
	if s.retentionHook != nil {
		s.retentionHook(res.Count, err)
	}
}

// agentPurgeStates are the only states a session may purge.
var agentPurgeStates = []mailbox.State{mailbox.StateRead, mailbox.StateReplied}

// purge deletes messages per a. Humans may purge anything; a session only
// read/replied messages of its own mailbox, without vacuum.
func (s *Server) purge(ctx context.Context, who caller, a proto.PurgeArgs) (proto.PurgeResult, error) {
	if !a.All && a.Before == "" && a.OlderThan == "" && a.Recipient == "" && len(a.States) == 0 {
		return proto.PurgeResult{}, errors.New("purge: give a filter (before, older_than, recipient, states) or all=true to purge everything you may")
	}
	var o mailbox.PurgeOptions
	switch {
	case a.Before != "" && a.OlderThan != "":
		return proto.PurgeResult{}, errors.New("purge: before and older_than are mutually exclusive")
	case a.Before != "":
		t, err := time.Parse(time.RFC3339Nano, a.Before)
		if err != nil {
			return proto.PurgeResult{}, fmt.Errorf("purge: before %q is not an RFC 3339 time", a.Before)
		}
		o.Before = t
	case a.OlderThan != "":
		d, err := config.ParseDuration(a.OlderThan)
		if err != nil {
			return proto.PurgeResult{}, fmt.Errorf("purge: older_than: %w", err)
		}
		o.Before = s.clock().Now().Add(-d)
	}
	for _, st := range a.States {
		ms := mailbox.State(st)
		if !ms.Valid() {
			return proto.PurgeResult{}, fmt.Errorf("purge: unknown state %q (want queued, delivered, read or replied)", st)
		}
		if !slices.Contains(o.States, ms) {
			o.States = append(o.States, ms)
		}
	}
	o.Recipient = a.Recipient
	o.DryRun = a.DryRun

	if who.session {
		if a.Recipient != "" && a.Recipient != who.id {
			return proto.PurgeResult{}, fmt.Errorf("purge: session %q can only purge its own mailbox", who.id)
		}
		o.Recipient = who.id
		if len(o.States) == 0 {
			o.States = agentPurgeStates
		}
		for _, st := range o.States {
			if !slices.Contains(agentPurgeStates, st) {
				return proto.PurgeResult{}, fmt.Errorf("purge: session %q may only purge read or replied messages, not %s", who.id, st)
			}
		}
		if a.Vacuum {
			return proto.PurgeResult{}, errors.New("purge: vacuum is for humans only")
		}
	}

	res, err := s.Store.Purge(ctx, o)
	if err != nil {
		return proto.PurgeResult{}, err
	}
	if a.DryRun {
		return proto.PurgeResult{Count: res.Count, DryRun: true}, nil
	}
	s.Log.Info("mailbox purged", "by", who.id, "via", who.via, "purged", res.Count,
		"recipient", o.Recipient, "states", o.States, "before", o.Before)
	if a.Vacuum {
		if err := s.Store.Vacuum(ctx); err != nil {
			return proto.PurgeResult{Count: res.Count}, fmt.Errorf("purged %d messages, but vacuum failed: %w", res.Count, err)
		}
	}
	return proto.PurgeResult{Count: res.Count}, nil
}
