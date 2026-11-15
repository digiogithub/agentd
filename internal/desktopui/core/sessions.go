package core

import (
	"context"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/digiogithub/agentd/internal/desktopui/backend"
	"github.com/digiogithub/agentd/internal/proto"
)

// bcast fans a latest-value snapshot out to subscribers: each one gets the
// newest value (older unread ones are replaced), channels close when their
// ctx ends.
type bcast[T any] struct {
	mu   sync.Mutex
	subs map[chan T]struct{}
	last T
	has  bool
}

func newBcast[T any]() *bcast[T] { return &bcast[T]{subs: map[chan T]struct{}{}} }

func (b *bcast[T]) subscribe(ctx context.Context) <-chan T {
	ch := make(chan T, 1)
	b.mu.Lock()
	b.subs[ch] = struct{}{}
	if b.has {
		ch <- b.last
	}
	b.mu.Unlock()
	context.AfterFunc(ctx, func() {
		b.mu.Lock()
		delete(b.subs, ch)
		close(ch)
		b.mu.Unlock()
	})
	return ch
}

func (b *bcast[T]) publish(v T) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.last, b.has = v, true
	for ch := range b.subs {
		select {
		case <-ch:
		default:
		}
		ch <- v // never blocks: cap 1, sole sender under mu
	}
}

func (b *bcast[T]) count() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.subs)
}

// Labels the hc layer and the daemon use for harness and project.
const (
	labelHarness = "agentd.harness"
	labelProject = "herdr.space"
)

// agentdSession maps a daemon SessionInfo onto a list entry.
func agentdSession(hostID string, si proto.SessionInfo) backend.Session {
	harness, project := "", ""
	if i := strings.Index(si.Name, "@"); i > 0 {
		harness, project = si.Name[:i], si.Name[i+1:]
	}
	if l := si.Labels[labelHarness]; l != "" {
		harness = l
	}
	if l := si.Labels[labelProject]; l != "" {
		project = l
	}
	if harness == "" && len(si.Cmd) > 0 {
		harness = filepath.Base(si.Cmd[0])
	}
	if project == "" && si.Cwd != "" {
		project = filepath.Base(si.Cwd)
	}
	status := si.Status
	switch {
	case si.State == "exited":
		status = "exited"
	case status == "" && si.State == "starting":
		status = "starting"
	case status == "":
		status = "unknown"
	}
	updated := si.LastOutput
	if updated.IsZero() {
		updated = si.StartedAt
	}
	info := si
	return backend.Session{
		HostID:    hostID,
		Source:    backend.SourceAgentd,
		Name:      si.Name,
		Harness:   harness,
		Project:   project,
		Status:    status,
		Attention: si.Attention,
		Preview:   si.Preview,
		Detail:    si.Title,
		Updated:   updated,
		Info:      &info,
	}
}

func sessionRank(s backend.Session) int {
	switch {
	case s.Attention:
		return 0
	case s.Status == "working":
		return 1
	}
	return 2
}

// sortSessions orders the list: attention first, then working, then the
// rest; newest first inside each group; key as the tie breaker.
func sortSessions(ss []backend.Session) {
	sort.SliceStable(ss, func(i, j int) bool {
		ri, rj := sessionRank(ss[i]), sessionRank(ss[j])
		if ri != rj {
			return ri < rj
		}
		if !ss[i].Updated.Equal(ss[j].Updated) {
			return ss[i].Updated.After(ss[j].Updated)
		}
		return ss[i].Key() < ss[j].Key()
	})
}

// WatchSessions streams the merged, sorted session list of every watched
// host. zellij/tmux lists are polled only while at least one consumer is
// watching.
func (b *Backend) WatchSessions(ctx context.Context) <-chan []backend.Session {
	b.startWatching()
	b.mu.Lock()
	b.watchers++
	mons := b.monList()
	b.mu.Unlock()
	ch := b.sessB.subscribe(ctx)
	context.AfterFunc(ctx, func() {
		b.mu.Lock()
		b.watchers--
		b.mu.Unlock()
	})
	for _, m := range mons {
		m.kickMux()
	}
	b.publishSessions()
	return ch
}

func (b *Backend) watchingSessions() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.watchers > 0
}

func (b *Backend) monList() []*hostMon {
	out := make([]*hostMon, 0, len(b.mons))
	for _, m := range b.mons {
		out = append(out, m)
	}
	return out
}

func (b *Backend) sessionSnapshot() []backend.Session {
	b.mu.Lock()
	mons := b.monList()
	var out []backend.Session
	for _, m := range b.shells {
		for _, s := range m {
			out = append(out, s.session())
		}
	}
	b.mu.Unlock()
	for _, m := range mons {
		out = append(out, m.list()...)
	}
	sortSessions(out)
	if out == nil {
		out = []backend.Session{}
	}
	return out
}

func (b *Backend) publishSessions() {
	if b.ctx.Err() != nil {
		return
	}
	b.sessB.publish(b.sessionSnapshot())
	b.publishHosts() // session counts
}
