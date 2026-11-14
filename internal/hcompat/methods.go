package hcompat

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/digiogithub/agentd/internal/proto"
)

type handler func(c *conn, params json.RawMessage) (any, func(), *rpcError)

var methods map[string]handler

// methodNames is what hello advertises: only methods this package
// implements. It covers the methods Herdr Desktop requires
// (herdr-app src/hc.rs USES).
var methodNames = []string{
	"hello", "server.status", "events.subscribe",
	"terminal.list", "terminal.get", "terminal.attach", "terminal.run",
	"terminal.kill", "terminal.rm", "terminal.label", "terminal.keep", "terminal.read",
	"terminal.send", "terminal.keys", "terminal.interrupt", "terminal.wait",
	"doc.get", "doc.set", "machine.list", "templates.list",
}

// writeMethods are refused when the Server is read-only.
var writeMethods = map[string]bool{
	"terminal.run": true, "terminal.kill": true, "terminal.rm": true, "terminal.label": true,
	"terminal.keep": true, "terminal.send": true, "terminal.keys": true, "terminal.interrupt": true,
	"doc.set": true,
}

// features is what hello advertises. automatic_input: AUTOMATIC_INPUT (kind
// 6) reaches the program as non-human input. resize_notifications is not
// offered: observers track size through size events.
var features = []string{"framed", "attach", "events", "status", "automatic_input"}

func init() {
	methods = map[string]handler{
		"hello":              hello,
		"server.status":      serverStatus,
		"events.subscribe":   eventsSubscribe,
		"terminal.list":      terminalList,
		"terminal.get":       terminalGet,
		"terminal.attach":    terminalAttach,
		"terminal.run":       terminalRun,
		"terminal.kill":      terminalKill,
		"terminal.rm":        terminalRm,
		"terminal.label":     terminalLabel,
		"terminal.keep":      terminalKeep,
		"terminal.read":      terminalRead,
		"terminal.send":      terminalSend,
		"terminal.keys":      terminalKeys,
		"terminal.interrupt": terminalInterrupt,
		"terminal.wait":      terminalWait,
		"doc.get":            docGet,
		"doc.set":            docSet,
		"machine.list":       machineList,
		"templates.list":     templatesList,
	}
}

func hello(c *conn, params json.RawMessage) (any, func(), *rpcError) {
	var p struct {
		Generation *uint32 `json:"generation"`
		Framed     bool    `json:"framed"`
	}
	if err := decode(params, &p); err != nil {
		return nil, nil, err
	}
	if p.Generation != nil && *p.Generation != Generation {
		e := newError("unsupported_generation", "protocol generation not supported")
		e.Data.Hint = "this core speaks generation 1"
		return nil, nil, e
	}
	if p.Framed {
		c.framed = true
	}
	return map[string]any{
		"generation": Generation,
		"version":    c.s.Version,
		"core":       c.s.Core,
		"framed":     c.framed,
		"methods":    methodNames,
		"features":   features,
	}, nil, nil
}

func serverStatus(c *conn, _ json.RawMessage) (any, func(), *rpcError) {
	all, err := c.s.sessions()
	if err != nil {
		return nil, nil, fromDaemon(err)
	}
	live, finished := 0, 0
	for _, si := range all {
		if si.State == "exited" {
			finished++
		} else {
			live++
		}
	}
	return map[string]any{
		"core":         c.s.Core,
		"version":      c.s.Version,
		"pid":          os.Getpid(),
		"uptime_ms":    time.Since(c.s.started).Milliseconds(),
		"holders":      []any{},
		"live":         live,
		"finished":     finished,
		"installed_by": "agentd", // non-null: clients do not try to self-update the core
	}, nil, nil
}

func terminalList(c *conn, params json.RawMessage) (any, func(), *rpcError) {
	var p struct {
		All    bool              `json:"all"`
		Labels map[string]string `json:"labels"`
	}
	if err := decode(params, &p); err != nil {
		return nil, nil, err
	}
	all, err := c.s.sessions()
	if err != nil {
		return nil, nil, fromDaemon(err)
	}
	terms := []Terminal{}
	hidden := 0
	for _, si := range all {
		t := c.s.record(si)
		if !matchLabels(t, p.Labels) {
			continue
		}
		if t.State == "finished" && !p.All {
			hidden++
			continue
		}
		terms = append(terms, t)
	}
	return map[string]any{"terminals": terms, "hidden_finished": hidden, "machines": []any{}}, nil, nil
}

func matchLabels(t Terminal, want map[string]string) bool {
	for k, v := range want {
		if t.Labels[k] != v {
			return false
		}
	}
	return true
}

type terminalParams struct {
	Terminal string `json:"terminal"`
}

func terminalGet(c *conn, params json.RawMessage) (any, func(), *rpcError) {
	var p terminalParams
	if err := decode(params, &p); err != nil {
		return nil, nil, err
	}
	si, err := c.s.lookup(p.Terminal)
	if err != nil {
		return nil, nil, err
	}
	return c.s.record(si), nil, nil
}

func terminalKill(c *conn, params json.RawMessage) (any, func(), *rpcError) {
	var p terminalParams
	if err := decode(params, &p); err != nil {
		return nil, nil, err
	}
	si, rerr := c.s.lookup(p.Terminal)
	if rerr != nil {
		return nil, nil, rerr
	}
	cl, err := c.s.Dial()
	if err != nil {
		return nil, nil, fromDaemon(err)
	}
	defer cl.Close()
	if si.State != "exited" {
		if err := cl.Kill(si.Name, ""); err != nil {
			return nil, nil, fromDaemon(err)
		}
	}
	t := c.s.record(si)
	if t.State == "live" {
		// The program is ending; report the explicit kill like herdr-core.
		reason := "killed"
		t.State, t.Reason, t.Pid, t.Status = "finished", &reason, nil, nil
	}
	return t, nil, nil
}

func terminalRm(c *conn, params json.RawMessage) (any, func(), *rpcError) {
	var p terminalParams
	if err := decode(params, &p); err != nil {
		return nil, nil, err
	}
	cl, err := c.s.Dial()
	if err != nil {
		return nil, nil, fromDaemon(err)
	}
	defer cl.Close()
	if err := cl.Rm(p.Terminal); err != nil {
		return nil, nil, fromDaemon(err)
	}
	return map[string]any{"removed": true}, nil, nil
}

// setLabels merges upd into a session's daemon labels. The first client
// change materializes the default labels and marks the session explicit, so
// deleting a default (herdr.tab) sticks.
func (c *conn) setLabels(si proto.SessionInfo, upd map[string]string) (proto.SessionInfo, *rpcError) {
	merged := map[string]string{}
	if si.Labels[labelExplicit] == "" {
		for k, v := range visibleLabels(si) {
			merged[k] = v
		}
		merged[labelExplicit] = "1"
	}
	for k, v := range upd {
		if reservedLabel(k) {
			return si, invalidParams("label " + k + " is reserved")
		}
		merged[k] = v
	}
	return c.applyLabels(si, merged)
}

func (c *conn) applyLabels(si proto.SessionInfo, upd map[string]string) (proto.SessionInfo, *rpcError) {
	cl, err := c.s.Dial()
	if err != nil {
		return si, fromDaemon(err)
	}
	defer cl.Close()
	labels, err := cl.Label(si.Name, upd)
	if err != nil {
		return si, fromDaemon(err)
	}
	si.Labels = labels
	return si, nil
}

func terminalLabel(c *conn, params json.RawMessage) (any, func(), *rpcError) {
	var p struct {
		Terminal string            `json:"terminal"`
		Labels   map[string]string `json:"labels"`
	}
	if err := decode(params, &p); err != nil {
		return nil, nil, err
	}
	si, err := c.s.lookup(p.Terminal)
	if err != nil {
		return nil, nil, err
	}
	if si, err = c.setLabels(si, p.Labels); err != nil {
		return nil, nil, err
	}
	return c.s.record(si), nil, nil
}

func terminalKeep(c *conn, params json.RawMessage) (any, func(), *rpcError) {
	var p struct {
		Terminal string `json:"terminal"`
		Keep     bool   `json:"keep"`
	}
	if err := decode(params, &p); err != nil {
		return nil, nil, err
	}
	si, err := c.s.lookup(p.Terminal)
	if err != nil {
		return nil, nil, err
	}
	// Stored only: agentd has no in-place shell recovery (documented deviation).
	v := ""
	if p.Keep {
		v = "1"
	}
	if si, err = c.applyLabels(si, map[string]string{labelKeep: v}); err != nil {
		return nil, nil, err
	}
	return c.s.record(si), nil, nil
}

func terminalRead(c *conn, params json.RawMessage) (any, func(), *rpcError) {
	var p struct {
		Terminal string `json:"terminal"`
		Lines    *int   `json:"lines"`
	}
	if err := decode(params, &p); err != nil {
		return nil, nil, err
	}
	cl, err := c.s.Dial()
	if err != nil {
		return nil, nil, fromDaemon(err)
	}
	defer cl.Close()
	text, err := cl.Dump(proto.DumpArgs{Name: p.Terminal, Scrollback: true})
	if err != nil {
		return nil, nil, fromDaemon(err)
	}
	text = strings.TrimRight(text, "\n ")
	if p.Lines != nil && *p.Lines >= 0 {
		lines := strings.Split(text, "\n")
		if len(lines) > *p.Lines {
			lines = lines[len(lines)-*p.Lines:]
		}
		text = strings.Join(lines, "\n")
	}
	return map[string]any{"text": text, "complete": true, "how": "history"}, nil, nil
}

func terminalRun(c *conn, params json.RawMessage) (any, func(), *rpcError) {
	var p struct {
		Argv   []string          `json:"argv"`
		Line   *string           `json:"line"`
		Cwd    *string           `json:"cwd"`
		Env    map[string]string `json:"env"`
		Labels map[string]string `json:"labels"`
		Name   *string           `json:"name"`
		Keep   bool              `json:"keep"`
		Size   *struct {
			Cols uint16 `json:"cols"`
			Rows uint16 `json:"rows"`
		} `json:"size"`
	}
	if err := decode(params, &p); err != nil {
		return nil, nil, err
	}
	argv := p.Argv
	if p.Line != nil {
		argv = []string{"sh", "-c", *p.Line}
	}
	if len(argv) == 0 {
		sh := os.Getenv("SHELL")
		if sh == "" {
			sh = "/bin/sh"
		}
		argv = []string{sh}
	}
	cwd := ""
	if p.Cwd != nil {
		cwd = *p.Cwd
	}
	if cwd == "" {
		cwd, _ = os.UserHomeDir()
	}
	if !filepath.IsAbs(cwd) {
		return nil, nil, invalidParams("cwd must be absolute")
	}
	if c.s.AllowRun != nil && !c.s.AllowRun(argv[0]) {
		e := newError("forbidden", "agentd does not allow starting "+filepath.Base(argv[0])+" over hc")
		e.Data.Hint = "add it to [herdr] allow_run in agentd's config.toml"
		return nil, nil, e
	}
	labels := map[string]string{}
	for k, v := range p.Labels {
		if reservedLabel(k) {
			return nil, nil, invalidParams("label " + k + " is reserved")
		}
		labels[k] = v
	}
	if len(labels) > 0 {
		labels[labelExplicit] = "1"
	}
	if p.Keep {
		labels[labelKeep] = "1"
	}
	a := proto.RunArgs{
		Target: cwd, Cwd: cwd, Harness: filepath.Base(argv[0]), Cmd: argv,
		EnvBase: c.s.EnvBase, Env: p.Env, Labels: labels,
	}
	if p.Name != nil {
		a.Name = *p.Name
	}
	if p.Size != nil {
		a.Cols, a.Rows = p.Size.Cols, p.Size.Rows
	}
	cl, err := c.s.Dial()
	if err != nil {
		return nil, nil, fromDaemon(err)
	}
	defer cl.Close()
	si, err := cl.Run(a)
	// hc names are optional and generated; agentd's default name
	// (<harness>@<project>) may be taken: number it like herdr-core does.
	for i := 2; err != nil && p.Name == nil && proto.CodeOf(err) == proto.CodeExists && i < 100; i++ {
		a.Name = a.Harness + "@" + filepath.Base(cwd) + "-" + itoa(uint64(i))
		si, err = cl.Run(a)
	}
	if err != nil {
		return nil, nil, fromDaemon(err)
	}
	return c.s.record(si), nil, nil
}

func docGet(c *conn, params json.RawMessage) (any, func(), *rpcError) {
	var p struct {
		Name string  `json:"name"`
		On   *string `json:"on"`
	}
	if err := decode(params, &p); err != nil {
		return nil, nil, err
	}
	if p.On != nil && *p.On != "" {
		return nil, nil, newError("not_found", "no machine "+*p.On)
	}
	cl, err := c.s.Dial()
	if err != nil {
		return nil, nil, fromDaemon(err)
	}
	defer cl.Close()
	r, err := cl.DocGet(p.Name)
	if err != nil {
		return nil, nil, fromDaemon(err)
	}
	return map[string]any{"value": r.Value, "version": r.Version}, nil, nil
}

func docSet(c *conn, params json.RawMessage) (any, func(), *rpcError) {
	var p struct {
		Name    string          `json:"name"`
		On      *string         `json:"on"`
		Value   json.RawMessage `json:"value"`
		Version uint64          `json:"version"`
	}
	if err := decode(params, &p); err != nil {
		return nil, nil, err
	}
	if p.On != nil && *p.On != "" {
		return nil, nil, newError("not_found", "no machine "+*p.On)
	}
	cl, err := c.s.Dial()
	if err != nil {
		return nil, nil, fromDaemon(err)
	}
	defer cl.Close()
	v, err := cl.DocSet(p.Name, p.Value, p.Version)
	if err != nil {
		return nil, nil, fromDaemon(err)
	}
	return map[string]any{"version": v}, nil, nil
}

func machineList(*conn, json.RawMessage) (any, func(), *rpcError) {
	return map[string]any{"machines": []any{}}, nil, nil
}

func templatesList(*conn, json.RawMessage) (any, func(), *rpcError) {
	return map[string]any{"templates": []any{}}, nil, nil
}

func itoa(n uint64) string {
	b, _ := json.Marshal(n)
	return string(b)
}
