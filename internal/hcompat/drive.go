package hcompat

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/digiogithub/agentd/internal/proto"
)

// Driving terminals: terminal.send, terminal.keys, terminal.interrupt and
// terminal.wait map onto daemon op act (which keeps every guard: drafts,
// paste verification, permissions, audit) plus the event stream for waits.

const (
	// draftWait is how long a send waits for a person's unsent draft or
	// recent keystrokes to clear (herdr-core config draft_wait default).
	draftWait = 60 * time.Second
	// defaultWait is herdr-core's default wait check-in (wait_timeout).
	defaultWait = 180 * time.Second
	// startedWithin is how long a send watches for the program to start working.
	startedWithin = 3 * time.Second
	// retryEvery paces retries while the daemon answers busy.
	retryEvery = 250 * time.Millisecond
)

// statusWord is the hc status word of a session (nil: none).
func statusWord(si proto.SessionInfo) *string {
	if st, _, _ := statusOf(si); st != nil {
		return &st.State
	}
	return nil
}

func seconds(v *float64, def time.Duration) time.Duration {
	if v == nil || *v <= 0 {
		return def
	}
	return time.Duration(*v * float64(time.Second))
}

// act runs one daemon act, retrying while the daemon answers busy (a
// person's draft or recent keystrokes) until the deadline.
func (c *conn) act(a proto.ActArgs, until time.Time) (proto.ActResult, *rpcError) {
	for {
		cl, err := c.s.Dial()
		if err != nil {
			return proto.ActResult{}, fromDaemon(err)
		}
		r, err := cl.Act(a)
		_ = cl.Close()
		if err == nil {
			return r, nil
		}
		if proto.CodeOf(err) != proto.CodeBusy || time.Now().After(until) {
			return r, fromDaemon(err)
		}
		select {
		case <-c.ctx.Done():
			return r, newError("cancelled", "connection closed")
		case <-time.After(retryEvery):
		}
	}
}

func terminalSend(c *conn, params json.RawMessage) (any, func(), *rpcError) {
	var p struct {
		Terminal string   `json:"terminal"`
		Text     *string  `json:"text"`
		Force    bool     `json:"force"`
		If       *string  `json:"if"`
		Wait     bool     `json:"wait"`
		Timeout  *float64 `json:"timeout"`
	}
	if err := decode(params, &p); err != nil {
		return nil, nil, err
	}
	if p.Text == nil {
		return nil, nil, invalidParams("text is required")
	}
	si, rerr := c.s.lookup(p.Terminal)
	if rerr != nil {
		return nil, nil, rerr
	}
	if si.State == "exited" {
		return nil, nil, newError("finished", "terminal "+si.Name+" has finished")
	}
	word := statusWord(si)
	if p.If != nil && (word == nil || *word != *p.If) {
		e := newError("status_mismatch", "terminal "+si.Name+" is not "+*p.If)
		return nil, nil, e
	}
	if word != nil && *word == "blocked" && !p.Force {
		e := newError("blocked", "terminal "+si.Name+" is blocked on a question")
		e.Data.Hint = "answer it, or send with force to type anyway"
		return nil, nil, e
	}
	// hc types into a working program (agents queue the message); agentd's
	// prompt refuses that unless forced. force never overrides a draft.
	force := p.Force || word == nil || *word == "working"
	if _, err := c.act(proto.ActArgs{Name: si.Name, Action: proto.ActPrompt, Text: *p.Text, Force: force, Client: "hc"},
		time.Now().Add(draftWait)); err != nil {
		return nil, nil, err
	}
	res := map[string]any{"turn": si.Turn, "status": word}
	started := c.startedWorking(si.Name, si.Turn, startedWithin)
	res["started"] = started
	if p.Wait {
		w := c.waitFor(si.Name, &si.Turn, seconds(p.Timeout, defaultWait))
		if !started && w["reason"] == "status" {
			w["reason"] = "not_started"
		}
		res["wait"] = w
	}
	return res, nil, nil
}

// startedWorking reports whether the session went working (or finished a
// turn) within d after a send.
func (c *conn) startedWorking(name string, turn uint64, d time.Duration) bool {
	deadline := time.Now().Add(d)
	for {
		if si, err := c.s.lookup(name); err == nil {
			if si.Status == "working" || si.Turn > turn {
				return true
			}
			if si.State == "exited" {
				return false
			}
		}
		if time.Now().After(deadline) {
			return false
		}
		select {
		case <-c.ctx.Done():
			return false
		case <-time.After(100 * time.Millisecond):
		}
	}
}

// waitFor waits until the session is not working (or, with afterTurn, until
// the turn after it ended), it exits, or timeout: a WaitResult.
func (c *conn) waitFor(name string, afterTurn *uint64, timeout time.Duration) map[string]any {
	res := func(si proto.SessionInfo, ended bool, reason string) map[string]any {
		r := map[string]any{"terminal": name, "ended": ended, "reason": reason, "turn": si.Turn,
			"children": []any{}, "status": statusWord(si), "exit_code": si.ExitCode, "signal": nil}
		return r
	}
	ctx, cancel := context.WithTimeout(c.ctx, timeout)
	defer cancel()
	cl, err := c.s.Dial()
	if err != nil {
		return res(proto.SessionInfo{Name: name}, false, "timeout")
	}
	sub, err := cl.Subscribe(proto.SubscribeArgs{Sessions: []string{name}, Kinds: []string{"session", "status"}}, 0)
	if err != nil {
		_ = cl.Close()
		return res(proto.SessionInfo{Name: name}, false, "timeout")
	}
	defer sub.Close()
	done := func(si proto.SessionInfo) (map[string]any, bool) {
		switch {
		case si.State == "exited":
			return res(si, true, "exit"), true
		case si.Status == "starting":
			return nil, false
		case afterTurn != nil && si.Turn > *afterTurn && si.Status != "working":
			return res(si, true, "status"), true
		case afterTurn == nil && si.Status != "working":
			return res(si, true, "status"), true
		}
		return nil, false
	}
	var last proto.SessionInfo
	for _, si := range sub.Info.Sessions {
		if si.Name == name {
			last = si
		}
	}
	if r, ok := done(last); ok {
		return r
	}
	for {
		select {
		case <-ctx.Done():
			if si, err := c.s.lookup(name); err == nil {
				last = si
			}
			return res(last, false, "timeout")
		case ev, ok := <-sub.Events:
			if !ok {
				return res(last, false, "timeout")
			}
			switch ev.Kind {
			case proto.EvStatusChanged:
				var d proto.StatusChangedData
				if json.Unmarshal(ev.Data, &d) == nil {
					last.Status, last.Turn = d.Status, d.Turn
				}
			case proto.EvSessionExited:
				var d proto.ExitedData
				_ = json.Unmarshal(ev.Data, &d)
				code := d.ExitCode
				last.State, last.ExitCode = "exited", &code
			case proto.EvSessionRemoved:
				return res(last, true, "exit")
			default:
				continue
			}
			if r, ok := done(last); ok {
				return r
			}
		}
	}
}

func terminalWait(c *conn, params json.RawMessage) (any, func(), *rpcError) {
	var p struct {
		Terminal *string  `json:"terminal"`
		Children *string  `json:"children"`
		Output   *string  `json:"output"`
		Timeout  *float64 `json:"timeout"`
	}
	if err := decode(params, &p); err != nil {
		return nil, nil, err
	}
	if p.Children != nil || p.Output != nil {
		return nil, nil, invalidParams("agentd supports terminal.wait on one terminal's status (no children, no output match)")
	}
	if p.Terminal == nil || *p.Terminal == "" {
		return nil, nil, newError("needs_target", "terminal is required")
	}
	if _, err := c.s.lookup(*p.Terminal); err != nil {
		return nil, nil, err
	}
	return c.waitFor(*p.Terminal, nil, seconds(p.Timeout, defaultWait)), nil, nil
}

// keyAliases maps herdr-core key names onto agentd's.
var keyAliases = map[string]string{
	"escape": "esc", "return": "enter", "pageup": "pgup", "page_up": "pgup", "pagedown": "pgdown",
	"page_down": "pgdown", "backtab": "shift-tab", "bs": "backspace", "del": "delete", "ins": "insert",
	" ": "space",
}

// hcKey translates one hc key name ("ctrl+c", "alt+x", "f5", "a").
func hcKey(name string) string {
	k := strings.ToLower(strings.TrimSpace(name))
	if a, ok := keyAliases[k]; ok {
		return a
	}
	return strings.ReplaceAll(k, "+", "-")
}

func terminalKeys(c *conn, params json.RawMessage) (any, func(), *rpcError) {
	var p struct {
		Terminal string   `json:"terminal"`
		Keys     []string `json:"keys"`
	}
	if err := decode(params, &p); err != nil {
		return nil, nil, err
	}
	si, rerr := c.s.lookup(p.Terminal)
	if rerr != nil {
		return nil, nil, rerr
	}
	if si.State == "exited" {
		return nil, nil, newError("finished", "terminal "+si.Name+" has finished")
	}
	keys := make([]string, len(p.Keys))
	for i, k := range p.Keys {
		keys[i] = hcKey(k)
	}
	// hc keys never wait for a draft; agentd refuses with busy instead of
	// typing over it (no retry: it would not clear by itself soon).
	if _, err := c.act(proto.ActArgs{Name: si.Name, Action: proto.ActKeys, Keys: keys, Force: true, Client: "hc"}, time.Now()); err != nil {
		return nil, nil, err
	}
	return map[string]any{"turn": si.Turn, "status": statusWord(si)}, nil, nil
}

func terminalInterrupt(c *conn, params json.RawMessage) (any, func(), *rpcError) {
	var p struct {
		Terminal string   `json:"terminal"`
		Timeout  *float64 `json:"timeout"`
	}
	if err := decode(params, &p); err != nil {
		return nil, nil, err
	}
	si, rerr := c.s.lookup(p.Terminal)
	if rerr != nil {
		return nil, nil, rerr
	}
	if si.State == "exited" {
		return nil, nil, newError("finished", "terminal "+si.Name+" has finished")
	}
	// The profile's interrupt keys (esc for claude/codex, ctrl-c otherwise);
	// forced: hc interrupts whatever the status, never escalates.
	if _, err := c.act(proto.ActArgs{Name: si.Name, Action: proto.ActInterrupt, Force: true, Client: "hc"}, time.Now()); err != nil {
		return nil, nil, err
	}
	key := "ctrl+c"
	if si.Profile == "claude" || si.Profile == "codex" {
		key = "esc"
	}
	if si.Status != "working" {
		return map[string]any{"outcome": "stopped", "key": key}, nil, nil
	}
	w := c.waitFor(si.Name, nil, seconds(p.Timeout, 10*time.Second))
	outcome := "still_working"
	switch w["reason"] {
	case "exit":
		outcome = "exited"
	case "status":
		outcome = "stopped"
	}
	return map[string]any{"outcome": outcome, "key": key}, nil, nil
}
