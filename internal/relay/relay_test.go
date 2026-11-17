package relay

import (
	"strings"
	"testing"
	"time"
)

func TestParseAddress(t *testing.T) {
	known := func(h string) bool { return h == "laptop" }
	ok := map[string]Address{
		"remote:laptop/claude@web": {"laptop", "claude@web"},
		"codex@api@laptop":         {"laptop", "codex@api"},
	}
	for in, want := range ok {
		got, err := ParseAddress(in, known)
		if err != nil || got != want {
			t.Errorf("ParseAddress(%q) = %v, %v; want %v", in, got, err, want)
		}
	}
	for _, in := range []string{"remote:laptop", "remote:/s", "remote:la ptop/s", "claude@web", "x@other", "remote:h/"} {
		if _, err := ParseAddress(in, known); err == nil {
			t.Errorf("ParseAddress(%q) should fail", in)
		}
	}
	if a := (Address{"h", "s"}); a.String() != "remote:h/s" || !IsRemote(a.String()) || IsRemote("s@h") {
		t.Error("String/IsRemote")
	}
}

func env() Envelope {
	return Envelope{RelayID: "r1", Origin: Address{"a", "claude@x"}, Dest: Address{"b", "codex@y"},
		Body: "hello", CreatedAt: time.Unix(1, 0)}
}

func TestValidate(t *testing.T) {
	if err := env().Validate(); err != nil {
		t.Fatal(err)
	}
	bad := map[string]func(*Envelope){
		"id":   func(e *Envelope) { e.RelayID = "" },
		"host": func(e *Envelope) { e.Dest.Host = "a" },
		"body": func(e *Envelope) { e.Body = " \n" },
		"big":  func(e *Envelope) { e.Body = strings.Repeat("x", MaxBody+1) },
		"hops": func(e *Envelope) { e.Hops = 1 },
		"addr": func(e *Envelope) { e.Origin.Session = "" },
		"subj": func(e *Envelope) { e.Subject = strings.Repeat("s", MaxSubject+1) },
	}
	for name, mut := range bad {
		e := env()
		mut(&e)
		if e.Validate() == nil {
			t.Errorf("%s: want error", name)
		}
	}
}

func TestNotice(t *testing.T) {
	n := Notice(7, env())
	for _, w := range []string{"#7", "remote:a/claude@x", "untrusted", "hello", "agentd reply 7"} {
		if !strings.Contains(n, w) {
			t.Errorf("notice %q lacks %q", n, w)
		}
	}
	if env().Reply() != (Address{"a", "claude@x"}) {
		t.Error("Reply")
	}
}
