// Package muxtest holds test doubles for the mux and provision packages.
package muxtest

import (
	"context"
	"errors"
	"io"
	"net"
	"regexp"
	"strings"
	"sync"

	"github.com/digiogithub/agentd/internal/desktopui/backend"
)

// PTYCall is one recorded FakeShell.PTY call.
type PTYCall struct {
	Command    string
	Cols, Rows int
	Env        map[string]string
}

type rule struct {
	sub string
	re  *regexp.Regexp
	res backend.ExecResult
	err error
}

func (r rule) match(cmd string) bool {
	if r.re != nil {
		return r.re.MatchString(cmd)
	}
	return strings.Contains(cmd, r.sub)
}

// FakeShell is a backend.Shell that records commands and answers with canned
// results. Rules are matched in order; the first whose substring is contained
// in the command (or whose regexp matches it) wins. Unmatched commands get
// Fallback (exit 0, no output by default).
type FakeShell struct {
	Fallback backend.ExecResult

	mu       sync.Mutex
	rules    []rule
	commands []string
	ptys     []PTYCall
	closed   bool
}

// On answers commands containing sub with res.
func (f *FakeShell) On(sub string, res backend.ExecResult) *FakeShell {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.rules = append(f.rules, rule{sub: sub, res: res})
	return f
}

// OnRegexp answers commands matching re with res.
func (f *FakeShell) OnRegexp(re *regexp.Regexp, res backend.ExecResult) *FakeShell {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.rules = append(f.rules, rule{re: re, res: res})
	return f
}

// OnError makes Exec fail with err for commands containing sub.
func (f *FakeShell) OnError(sub string, err error) *FakeShell {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.rules = append(f.rules, rule{sub: sub, err: err})
	return f
}

// Commands returns the Exec commands, in order.
func (f *FakeShell) Commands() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.commands...)
}

// PTYs returns the PTY calls, in order.
func (f *FakeShell) PTYs() []PTYCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]PTYCall(nil), f.ptys...)
}

// Closed reports whether Close was called.
func (f *FakeShell) Closed() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.closed
}

// Exec records cmd and answers from the rules.
func (f *FakeShell) Exec(ctx context.Context, cmd string, stdin []byte) (backend.ExecResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.commands = append(f.commands, cmd)
	if err := ctx.Err(); err != nil {
		return backend.ExecResult{}, err
	}
	for _, r := range f.rules {
		if r.match(cmd) {
			return r.res, r.err
		}
	}
	return f.Fallback, nil
}

// PTY records the call and returns a FakePTY.
func (f *FakeShell) PTY(ctx context.Context, cmd string, cols, rows int, env map[string]string) (backend.PTY, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.ptys = append(f.ptys, PTYCall{Command: cmd, Cols: cols, Rows: rows, Env: env})
	return NewFakePTY(), nil
}

// DialUnix is not supported.
func (f *FakeShell) DialUnix(ctx context.Context, path string) (net.Conn, error) {
	return nil, errors.New("muxtest: DialUnix not supported")
}

// Close marks the shell closed.
func (f *FakeShell) Close() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.closed = true
	return nil
}

// FakePTY is a PTY with no output that ends (exit 0) when closed.
type FakePTY struct {
	once sync.Once
	done chan struct{}
}

// NewFakePTY returns an open FakePTY.
func NewFakePTY() *FakePTY { return &FakePTY{done: make(chan struct{})} }

// Read blocks until Close, then returns io.EOF.
func (p *FakePTY) Read([]byte) (int, error) {
	<-p.done
	return 0, io.EOF
}

// Write discards b.
func (p *FakePTY) Write(b []byte) (int, error) { return len(b), nil }

// Close ends the PTY.
func (p *FakePTY) Close() error {
	p.once.Do(func() { close(p.done) })
	return nil
}

// Resize does nothing.
func (p *FakePTY) Resize(cols, rows int) error { return nil }

// Wait blocks until Close and returns 0.
func (p *FakePTY) Wait() (int, error) {
	<-p.done
	return 0, nil
}

var (
	_ backend.Shell = (*FakeShell)(nil)
	_ backend.PTY   = (*FakePTY)(nil)
)
