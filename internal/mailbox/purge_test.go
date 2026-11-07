package mailbox

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"testing"
	"time"
)

// clockAt makes the store stamp new messages at *t.
func clockAt(s *Store, t *time.Time) { s.SetClock(func() time.Time { return *t }) }

func mustCount(t *testing.T, s *Store) int {
	t.Helper()
	var n int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM messages`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestPurgeFilters(t *testing.T) {
	s, _ := openTest(t)
	ctx := context.Background()
	base := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	now := base
	clockAt(s, &now)

	old1 := mustSend(t, s, "a", "bob", "", "old to bob") // 1, queued
	old2 := mustSend(t, s, "a", "eve", "", "old to eve") // 2
	if _, err := s.MarkRead(ctx, "eve", old2.ID); err != nil {
		t.Fatal(err)
	}
	now = base.Add(72 * time.Hour)
	new1 := mustSend(t, s, "a", "bob", "", "new to bob") // 3
	if _, err := s.MarkRead(ctx, "bob", new1.ID); err != nil {
		t.Fatal(err)
	}
	new2 := mustSend(t, s, "a", "eve", "", "new to eve") // 4, queued

	cutoff := base.Add(24 * time.Hour)
	cases := []struct {
		name string
		o    PurgeOptions
		want []int64
	}{
		{"before", PurgeOptions{Before: cutoff}, []int64{old1.ID, old2.ID}},
		{"recipient", PurgeOptions{Recipient: "bob"}, []int64{old1.ID, new1.ID}},
		{"states", PurgeOptions{States: []State{StateRead}}, []int64{old2.ID, new1.ID}},
		{"before+recipient", PurgeOptions{Before: cutoff, Recipient: "eve"}, []int64{old2.ID}},
		{"recipient+states", PurgeOptions{Recipient: "eve", States: []State{StateQueued, StateDelivered}}, []int64{new2.ID}},
		{"exact boundary excluded", PurgeOptions{Before: base}, []int64{}},
		{"nothing", PurgeOptions{Recipient: "nobody"}, []int64{}},
		{"all", PurgeOptions{}, []int64{old1.ID, old2.ID, new1.ID, new2.ID}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			o := c.o
			o.DryRun = true
			res, err := s.Purge(ctx, o)
			if err != nil {
				t.Fatal(err)
			}
			if fmt.Sprint(res.IDs) != fmt.Sprint(c.want) || res.Count != len(c.want) {
				t.Fatalf("dry run = %+v, want %v", res, c.want)
			}
			if n := mustCount(t, s); n != 4 {
				t.Fatalf("dry run deleted rows: %d left", n)
			}
		})
	}

	res, err := s.Purge(ctx, PurgeOptions{Before: cutoff})
	if err != nil || res.Count != 2 {
		t.Fatalf("purge = %+v, %v", res, err)
	}
	if _, err := s.Get(ctx, old1.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("old message survived: %v", err)
	}
	if n := mustCount(t, s); n != 2 {
		t.Fatalf("%d rows left, want 2", n)
	}
	if _, err := s.Purge(ctx, PurgeOptions{States: []State{"bogus"}}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("bogus state: %v", err)
	}
}

// Purging a message that surviving replies point to clears their reply_to
// instead of failing on the foreign key.
func TestPurgeKeepsRepliesValid(t *testing.T) {
	s, _ := openTest(t)
	ctx := context.Background()
	orig := mustSend(t, s, "alice", "bob", "q", "question")
	rep, err := s.Reply(ctx, "bob", orig.ID, "answer")
	if err != nil {
		t.Fatal(err)
	}
	rep2, err := s.Reply(ctx, "alice", rep.ID, "thanks")
	if err != nil {
		t.Fatal(err)
	}
	res, err := s.Purge(ctx, PurgeOptions{Recipient: "bob", States: []State{StateReplied}}) // the original only
	if err != nil || res.Count != 1 || res.IDs[0] != orig.ID {
		t.Fatalf("purge = %+v, %v", res, err)
	}
	got, err := s.Get(ctx, rep.ID)
	if err != nil || got.ReplyTo != nil {
		t.Fatalf("reply after purge: %+v %v", got, err)
	}
	got2, err := s.Get(ctx, rep2.ID)
	if err != nil || got2.ReplyTo == nil || *got2.ReplyTo != rep.ID {
		t.Fatalf("second reply after purge: %+v %v", got2, err)
	}
	rows, err := s.db.Query(`PRAGMA foreign_key_check`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	if rows.Next() {
		t.Fatal("foreign key violations after purge")
	}
}

// Ids are never reused after a purge, not even after purging everything,
// reopening or vacuuming.
func TestPurgeIDsKeepGrowing(t *testing.T) {
	s, path := openTest(t)
	ctx := context.Background()
	var last int64
	for range 3 {
		last = mustSend(t, s, "a", "b", "", "x").ID
	}
	if res, err := s.Purge(ctx, PurgeOptions{}); err != nil || res.Count != 3 {
		t.Fatalf("purge all = %+v, %v", res, err)
	}
	if err := s.Vacuum(ctx); err != nil {
		t.Fatal(err)
	}
	m := mustSend(t, s, "a", "b", "", "after purge")
	if m.ID != last+1 {
		t.Fatalf("id after purge = %d, want %d", m.ID, last+1)
	}
	if _, err := s.Purge(ctx, PurgeOptions{}); err != nil {
		t.Fatal(err)
	}
	s.Close()
	s2, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()
	if m2 := mustSend(t, s2, "a", "b", "", "after reopen"); m2.ID != m.ID+1 {
		t.Fatalf("id after reopen = %d, want %d", m2.ID, m.ID+1)
	}
}

// A database created by schema v1 (as deployed) migrates to the current
// schema keeping rows, references and the id counter.
func TestMigrateV1Database(t *testing.T) {
	path := filepath.Join(t.TempDir(), "v1.db")
	raw, err := sql.Open("sqlite", "file:"+path+"?_pragma=foreign_keys(1)&_pragma=journal_mode(WAL)")
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		`CREATE TABLE schema_migrations (version INTEGER PRIMARY KEY, applied_at INTEGER NOT NULL)`,
		migrations[0],
		`INSERT INTO schema_migrations VALUES (1, 0)`,
		`INSERT INTO messages(sender, recipient, subject, body, state, created_at, read_at, replied_at)
			VALUES ('human:alice', 'codex@models', 'hi', 'review', 'replied', 1000, 1100, 1200)`,
		`INSERT INTO messages(sender, recipient, subject, body, reply_to, state, created_at)
			VALUES ('codex@models', 'human:alice', 'Re: hi', 'done', 1, 'queued', 1300)`,
		`INSERT INTO messages(sender, recipient, body, created_at) VALUES ('a', 'b', 'gone', 1400)`,
		`DELETE FROM messages WHERE id = 3`, // the counter (3) is now above max(id)
	} {
		if _, err := raw.Exec(q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	raw.Close()

	s, err := Open(path)
	if err != nil {
		t.Fatalf("Open v1: %v", err)
	}
	defer s.Close()
	ctx := context.Background()
	if v, err := s.SchemaVersion(ctx); err != nil || v != len(migrations) {
		t.Fatalf("schema version = %d, %v", v, err)
	}
	m1, err := s.Get(ctx, 1)
	if err != nil || m1.State != StateReplied || m1.Subject != "hi" || m1.RepliedAt == nil ||
		m1.RepliedAt.UnixMilli() != 1200 || !m1.CreatedAt.Equal(time.UnixMilli(1000)) {
		t.Fatalf("message 1 after migration: %+v %v", m1, err)
	}
	m2, err := s.Get(ctx, 2)
	if err != nil || m2.ReplyTo == nil || *m2.ReplyTo != 1 {
		t.Fatalf("message 2 after migration: %+v %v", m2, err)
	}
	if n := mustCount(t, s); n != 2 {
		t.Fatalf("%d rows after migration", n)
	}
	var idx int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type = 'index'
		AND name IN ('messages_recipient_state', 'messages_created_at')`).Scan(&idx); err != nil || idx != 2 {
		t.Fatalf("indexes = %d, %v", idx, err)
	}
	var leftover int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE name = 'messages_v2'`).Scan(&leftover); err != nil || leftover != 0 {
		t.Fatalf("messages_v2 left behind: %d %v", leftover, err)
	}
	// The counter survived the rebuild: id 3 is not reused.
	if m := mustSend(t, s, "a", "b", "", "new"); m.ID != 4 {
		t.Fatalf("new id = %d, want 4", m.ID)
	}
	// The new constraint works on migrated rows.
	if _, err := s.Purge(ctx, PurgeOptions{Recipient: "codex@models"}); err != nil {
		t.Fatal(err)
	}
	if m2, err := s.Get(ctx, 2); err != nil || m2.ReplyTo != nil {
		t.Fatalf("reply after purging its parent: %+v %v", m2, err)
	}
	// Reply/Send still enforce existing references.
	bad := int64(1)
	if _, err := s.Send(ctx, Message{Sender: "a", Recipient: "b", Body: "x", ReplyTo: &bad}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("reply_to purged message: %v", err)
	}
}

// An empty v1 database (no sqlite_sequence row yet) migrates too.
func TestMigrateEmptyV1Database(t *testing.T) {
	path := filepath.Join(t.TempDir(), "v1.db")
	raw, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		`CREATE TABLE schema_migrations (version INTEGER PRIMARY KEY, applied_at INTEGER NOT NULL)`,
		migrations[0],
		`INSERT INTO schema_migrations VALUES (1, 0)`,
	} {
		if _, err := raw.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	raw.Close()
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if m := mustSend(t, s, "a", "b", "", "first"); m.ID != 1 {
		t.Fatalf("first id = %d", m.ID)
	}
}

func TestVacuumKeepsData(t *testing.T) {
	s, _ := openTest(t)
	ctx := context.Background()
	m := mustSend(t, s, "a", "b", "", "keep")
	if err := s.Vacuum(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Get(ctx, m.ID); err != nil {
		t.Fatal(err)
	}
}
