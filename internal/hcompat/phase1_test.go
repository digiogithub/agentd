package hcompat_test

import (
	"bytes"
	"encoding/json"
	"testing"
	"time"

	"github.com/digiogithub/agentd/internal/hcompat"
)

// eventsOf parses the NDJSON event lines of a subscription stream.
func eventsOf(t *testing.T, b []byte) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, line := range bytes.Split(bytes.TrimSpace(b), []byte("\n")) {
		var m map[string]any
		if err := json.Unmarshal(line, &m); err != nil {
			t.Fatalf("event line %q: %v", line, err)
		}
		out = append(out, m)
	}
	return out
}

// Labels, keep and documents live in the daemon: two independent hc
// connections (two `hc connect` processes) see each other's changes.
func TestStateSharedAcrossConnections(t *testing.T) {
	sock := startDaemon(t)
	runSession(t, sock, "sh@proj", "sh", "-c", "sleep 60")
	a := newHCClient(t, sock)
	a.helloFramed()
	b := newHCClient(t, sock)
	b.helloFramed()
	sub := result(t, b.call("events.subscribe", map[string]any{}))
	evCh := uint32(sub["channel"].(float64))

	result(t, a.call("terminal.label", map[string]any{"terminal": "sh@proj", "labels": map[string]string{"herdr.seen": "4"}}))
	result(t, a.call("terminal.keep", map[string]any{"terminal": "sh@proj", "keep": true}))
	got := b.readStream(evCh, func(x []byte) bool { return bytes.Contains(x, []byte(`"type":"keep"`)) })
	var sawLabels bool
	for _, ev := range eventsOf(t, got) {
		p := ev["params"].(map[string]any)
		if p["type"] == "labels" {
			l := p["data"].(map[string]any)["labels"].(map[string]any)
			sawLabels = l["herdr.seen"] == "4" && l["herdr.space"] == "proj"
			if _, leak := l["agentd.hc.keep"]; leak {
				t.Fatalf("reserved label leaked: %v", l)
			}
		}
	}
	if !sawLabels {
		t.Fatalf("no labels event in %s", got)
	}
	r := result(t, b.call("terminal.get", map[string]any{"terminal": "sh@proj"}))
	if r["keep"] != true || r["labels"].(map[string]any)["herdr.seen"] != "4" {
		t.Fatalf("record on b = %v", r)
	}

	v := result(t, a.call("doc.set", map[string]any{"name": "herdr-app", "value": map[string]any{"spaces": []any{}}, "version": 0}))
	b.readStream(evCh, func(x []byte) bool { return bytes.Contains(x, []byte(`"type":"doc"`)) })
	g := result(t, b.call("doc.get", map[string]any{"name": "herdr-app"}))
	if g["version"] != v["version"] {
		t.Fatalf("doc on b = %v, set %v", g, v)
	}
}

// Deleting a default label (Desktop clears herdr.tab when it moves a pane)
// sticks: the defaults are materialized on the first client change.
func TestDefaultLabelDeletionSticks(t *testing.T) {
	sock := startDaemon(t)
	runSession(t, sock, "sh@proj", "sh", "-c", "sleep 60")
	c := newHCClient(t, sock)
	c.helloFramed()
	result(t, c.call("terminal.label", map[string]any{"terminal": "sh@proj", "labels": map[string]string{"herdr.tab": ""}}))
	r := result(t, c.call("terminal.get", map[string]any{"terminal": "sh@proj"}))
	labels := r["labels"].(map[string]any)
	if _, ok := labels["herdr.tab"]; ok || labels["herdr.space"] != "proj" {
		t.Fatalf("labels = %v", labels)
	}
	if k := errKind(c.call("terminal.label", map[string]any{"terminal": "sh@proj", "labels": map[string]string{"agentd.hc.keep": "1"}})); k != "invalid_params" {
		t.Fatalf("reserved label: kind %q", k)
	}
}

// A viewer that stops crediting never makes the daemon drop the attach: its
// backlog is bounded and replaced by a repaint (CAN + redraw) once it reads
// again, and the stream stays interactive.
func TestSlowViewerGetsRepaint(t *testing.T) {
	sock := startDaemon(t)
	runSession(t, sock, "flood@proj", "sh", "-c", "sleep 1; i=0; while [ $i -lt 20000 ]; do echo line-$i-xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx; i=$((i+1)); done; exec cat")
	c := newHCClient(t, sock, func(s *hcompat.Server) { s.MaxPending = 32 * 1024 })
	c.helloFramed()
	att := result(t, c.call("terminal.attach", map[string]any{"terminal": "flood@proj", "cols": 80, "rows": 24}))
	ch := uint32(att["channel"].(float64))

	// Read without crediting: the initial window (256 KiB) is all we get.
	var got int
	deadline := time.After(4 * time.Second)
drain:
	for {
		select {
		case f := <-c.frames:
			if f.Channel == ch && f.Kind == hcompat.KindData {
				got += len(f.Payload)
			}
		case <-deadline:
			break drain
		}
	}
	if got > hcompat.Window {
		t.Fatalf("got %d bytes without credit (window %d)", got, hcompat.Window)
	}
	// Credit generously: a repaint (CAN) arrives instead of 200 KiB of backlog,
	// and the session still echoes input.
	c.send(ch, hcompat.KindCredit, hcompat.CreditPayload(64<<20))
	c.send(ch, hcompat.KindData, []byte("after-repaint-mark\r"))
	rest := c.readStream(ch, func(b []byte) bool { return bytes.Count(b, []byte("after-repaint-mark")) >= 2 })
	if !bytes.Contains(rest, []byte{0x18}) {
		t.Fatalf("no repaint after falling behind (%d bytes)", len(rest))
	}
	if bytes.Contains(rest, []byte("line-100-")) && bytes.Contains(rest, []byte("line-15000-")) {
		t.Fatal("the whole backlog was delivered instead of a repaint")
	}
}

// The daemon's turn counter drives hc's turn: status leaves working, the
// subscriber gets status + turn, and records carry it.
func TestTurnFromDaemon(t *testing.T) {
	sock := startDaemon(t)
	c := newHCClient(t, sock)
	c.helloFramed()
	sub := result(t, c.call("events.subscribe", map[string]any{}))
	evCh := uint32(sub["channel"].(float64))
	runSession(t, sock, "busy@proj", "sh", "-c",
		"echo ready; sleep 3.6; for i in 1 2 3 4 5 6; do echo step $i; sleep 0.15; done; sleep 60")
	got := c.readStream(evCh, func(b []byte) bool { return bytes.Contains(b, []byte(`"type":"turn"`)) })
	var turn float64
	for _, ev := range eventsOf(t, got) {
		p := ev["params"].(map[string]any)
		if p["type"] == "turn" {
			turn = p["data"].(map[string]any)["turn"].(float64)
		}
	}
	if turn < 1 {
		t.Fatalf("turn event = %v", turn)
	}
	r := result(t, c.call("terminal.get", map[string]any{"terminal": "busy@proj"}))
	if r["turn"].(float64) < turn {
		t.Fatalf("record turn = %v, event %v", r["turn"], turn)
	}
}
