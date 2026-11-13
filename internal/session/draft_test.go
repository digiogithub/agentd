package session_test

import (
	"strings"
	"testing"
	"time"

	"github.com/digiogithub/agentd/internal/session"
)

// Regression (review): a nested end marker split by another marker must not
// terminate the paste early.
func TestPasteNestedMarkerBreakout(t *testing.T) {
	s := start(t, session.Spec{Name: "p", Cmd: []string{"sh", "-c", "stty raw -echo; exec cat -v"}})
	time.Sleep(300 * time.Millisecond)
	if err := s.Paste("A\x1b[20\x1b[201~1~B"); err != nil {
		t.Fatal(err)
	}
	waitScreen(t, s, "^[[201~")
	scr := s.Screen()
	if !strings.Contains(scr, "^[[200~AB^[[201~") {
		t.Fatalf("paste terminated early or ESC leaked: %q", strings.TrimSpace(scr))
	}
}

func TestHumanDraftTracking(t *testing.T) {
	s := start(t, session.Spec{Name: "d", Cmd: []string{"cat"}})
	if !s.HumanDraftSince().IsZero() {
		t.Fatal("no draft at start")
	}
	if _, err := s.Write([]byte("half a line"), true); err != nil {
		t.Fatal(err)
	}
	if s.HumanDraftSince().IsZero() {
		t.Fatal("draft expected after uncommitted human input")
	}
	if _, err := s.Write([]byte("\x1b[A"), true); err != nil { // cursor key: no change
		t.Fatal(err)
	}
	if s.HumanDraftSince().IsZero() {
		t.Fatal("cursor key must not clear the draft")
	}
	if _, err := s.Write([]byte("\r"), true); err != nil {
		t.Fatal(err)
	}
	if !s.HumanDraftSince().IsZero() {
		t.Fatal("submit must clear the draft")
	}
	if _, err := s.Write([]byte("injected"), false); err != nil {
		t.Fatal(err)
	}
	if !s.HumanDraftSince().IsZero() {
		t.Fatal("non-human input must not create a draft")
	}
}

// WriteAct never counts as a real human keystroke (LastHumanInput) but is
// activity (LastActInput / LastInput) and clears drafts like human input.
func TestWriteActIsNotHumanInput(t *testing.T) {
	s := start(t, session.Spec{Name: "wa", Cmd: []string{"sh", "-c", "stty raw -echo; exec cat"}})
	time.Sleep(200 * time.Millisecond)
	if _, err := s.WriteAct([]byte("\x1b[A")); err != nil {
		t.Fatal(err)
	}
	if !s.LastHumanInput().IsZero() {
		t.Errorf("WriteAct set LastHumanInput: %v", s.LastHumanInput())
	}
	if s.LastActInput().IsZero() || s.LastInput() != s.LastActInput() {
		t.Errorf("act=%v input=%v", s.LastActInput(), s.LastInput())
	}
	// A real keystroke is the latest input, and only it moves LastHumanInput.
	time.Sleep(5 * time.Millisecond)
	if _, err := s.Write([]byte("x"), true); err != nil {
		t.Fatal(err)
	}
	if s.LastHumanInput().IsZero() || !s.LastInput().Equal(s.LastHumanInput()) || !s.LastHumanInput().After(s.LastActInput()) {
		t.Errorf("human=%v act=%v input=%v", s.LastHumanInput(), s.LastActInput(), s.LastInput())
	}
	// WriteAct Enter clears a draft; MarkDraft / ClearDraft round-trip.
	if s.HumanDraftSince().IsZero() {
		t.Fatal("typed text should be a draft")
	}
	if _, err := s.WriteAct([]byte("\r")); err != nil {
		t.Fatal(err)
	}
	if !s.HumanDraftSince().IsZero() {
		t.Error("act Enter did not clear the draft")
	}
	s.MarkDraft()
	if s.HumanDraftSince().IsZero() {
		t.Error("MarkDraft")
	}
	s.ClearDraft()
	if !s.HumanDraftSince().IsZero() {
		t.Error("ClearDraft")
	}
}
