package backlog

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/digiogithub/agentd/internal/proto"
)

func open(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "backlog.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func TestAddGetListOrder(t *testing.T) {
	ctx := context.Background()
	s := open(t)
	base := time.Now()
	tick := 0
	s.now = func() time.Time { tick++; return base.Add(time.Duration(tick) * time.Second) }
	a, _ := s.Add(ctx, "a", "body a")
	b, _ := s.Add(ctx, "b", "")
	c, _ := s.Add(ctx, "c", "")
	if a.State != proto.NoteOpen || a.Body != "body a" || a.ID != 1 {
		t.Fatalf("add: %+v", a)
	}
	if _, err := s.Assign(ctx, a.ID, "s1", "n1", base.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Assign(ctx, b.ID, "s1", "n2", base.Add(2*time.Hour)); err != nil {
		t.Fatal(err)
	}
	done, err := s.MarkDone(ctx, "s1", "n1", base.Add(3*time.Hour))
	if err != nil || len(done) != 1 || done[0].State != proto.NoteDone || done[0].DoneAt == nil {
		t.Fatalf("done: %+v %v", done, err)
	}
	all, err := s.List(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	var ids []int64
	for _, n := range all {
		ids = append(ids, n.ID)
	}
	// open c, assigned b, done a
	if len(ids) != 3 || ids[0] != c.ID || ids[1] != b.ID || ids[2] != a.ID {
		t.Fatalf("order %v", ids)
	}
	open, _ := s.List(ctx, []string{proto.NoteOpen, proto.NoteAssigned})
	if len(open) != 2 {
		t.Fatalf("filter: %d", len(open))
	}
	if _, err := s.Get(ctx, 99); !errors.Is(err, ErrNotFound) {
		t.Fatalf("get: %v", err)
	}
}

func TestUpdateReopenRemoveDigest(t *testing.T) {
	ctx := context.Background()
	s := open(t)
	n, _ := s.Add(ctx, "t", "b")
	d1 := Digest([]proto.Note{n})
	if _, err := s.Assign(ctx, n.ID, "s", "n1", time.Now()); err != nil {
		t.Fatal(err)
	}
	title := "new"
	u, err := s.Update(ctx, n.ID, &title, nil, false)
	if err != nil || u.Title != "new" || u.State != proto.NoteAssigned || u.Body != "b" {
		t.Fatalf("update: %+v %v", u, err)
	}
	if Digest([]proto.Note{u}) == d1 {
		t.Fatal("digest must change")
	}
	empty := " "
	if _, err := s.Update(ctx, n.ID, &empty, nil, false); !errors.Is(err, ErrInvalid) {
		t.Fatalf("blank title: %v", err)
	}
	r, err := s.Update(ctx, n.ID, nil, nil, true)
	if err != nil || r.State != proto.NoteOpen || r.AssignedTo != "" || r.TaskID != "" || r.AssignedAt != nil {
		t.Fatalf("reopen: %+v %v", r, err)
	}
	if got, err := s.MarkDone(ctx, "s", "n1", time.Now()); err != nil || len(got) != 0 {
		t.Fatalf("markdone after reopen: %v %v", got, err)
	}
	rm, err := s.Remove(ctx, n.ID)
	if err != nil || rm.ID != n.ID {
		t.Fatal(err)
	}
	if _, err := s.Remove(ctx, n.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("second rm: %v", err)
	}
	// ids are never reused
	n2, _ := s.Add(ctx, "again", "")
	if n2.ID <= n.ID {
		t.Fatalf("id reused: %d", n2.ID)
	}
}

func TestAssignDoneAndReassign(t *testing.T) {
	ctx := context.Background()
	s := open(t)
	n, _ := s.Add(ctx, "t", "")
	if _, err := s.Assign(ctx, n.ID, "s1", "n1", time.Now()); err != nil {
		t.Fatal(err)
	}
	moved, err := s.Assign(ctx, n.ID, "s2", "n1", time.Now())
	if err != nil || moved.AssignedTo != "s2" {
		t.Fatalf("reassign: %+v %v", moved, err)
	}
	if got, _ := s.MarkDone(ctx, "s1", "n1", time.Now()); len(got) != 0 {
		t.Fatal("old session must not complete it")
	}
	if got, _ := s.MarkDone(ctx, "s2", "n1", time.Now()); len(got) != 1 {
		t.Fatal("new session completes it")
	}
	if _, err := s.Assign(ctx, n.ID, "s3", "n1", time.Now()); !errors.Is(err, ErrInvalid) {
		t.Fatalf("assign done: %v", err)
	}
}

func TestLimitAndMode(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "b.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if st, err := os.Stat(path); err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("mode: %v %v", st, err)
	}
	for i := 0; i < proto.MaxNotes; i++ {
		if _, err := s.Add(ctx, "x", ""); err != nil {
			t.Fatal(i, err)
		}
	}
	if _, err := s.Add(ctx, "x", ""); !errors.Is(err, ErrFull) {
		t.Fatalf("limit: %v", err)
	}
	if _, err := s.Add(ctx, "  ", ""); !errors.Is(err, ErrInvalid) {
		t.Fatalf("blank: %v", err)
	}
}

func TestReopenKeepsData(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "b.db")
	s, _ := Open(path)
	_, _ = s.Add(ctx, "keep", "me")
	_ = s.Close()
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	got, _ := s.List(ctx, nil)
	if len(got) != 1 || got[0].Title != "keep" {
		t.Fatalf("%+v", got)
	}
}
