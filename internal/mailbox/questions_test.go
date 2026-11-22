package mailbox

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"reflect"
	"testing"
)

func TestMigrateV3Database(t *testing.T) {
	path := filepath.Join(t.TempDir(), "v3.db")
	raw, err := sql.Open("sqlite", "file:"+path+"?_pragma=foreign_keys(1)&_pragma=journal_mode(WAL)")
	if err != nil {
		t.Fatal(err)
	}
	stmts := []string{`CREATE TABLE schema_migrations (version INTEGER PRIMARY KEY, applied_at INTEGER NOT NULL)`}
	for i := 0; i < 3; i++ {
		stmts = append(stmts, migrations[i], `INSERT INTO schema_migrations VALUES (`+string(rune('1'+i))+`, 0)`)
	}
	stmts = append(stmts,
		`INSERT INTO messages(sender, recipient, subject, body, state, created_at) VALUES ('a', 'b', 's', 'one', 'read', 1000)`,
		`INSERT INTO messages(sender, recipient, subject, body, reply_to, state, created_at, queue)
			VALUES ('b', 'queue:q', '', 'two', 1, 'unclaimed', 1100, 'q')`,
		`INSERT INTO messages(sender, recipient, body, created_at) VALUES ('a', 'b', 'gone', 1200)`,
		`DELETE FROM messages WHERE id = 3`, // counter (3) above max(id)
	)
	for _, q := range stmts {
		if _, err := raw.Exec(q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	raw.Close()

	s, err := Open(path)
	if err != nil {
		t.Fatalf("Open v3: %v", err)
	}
	defer s.Close()
	ctx := context.Background()
	if v, err := s.SchemaVersion(ctx); err != nil || v != 4 {
		t.Fatalf("schema version = %d, %v", v, err)
	}
	m2, err := s.Get(ctx, 2)
	if err != nil || m2.Kind != "" || m2.Options != nil || m2.Task != "" || m2.Queue != "q" ||
		m2.ReplyTo == nil || *m2.ReplyTo != 1 || m2.State != StateUnclaimed {
		t.Fatalf("message 2 after migration: %+v %v", m2, err)
	}
	q, err := s.Send(ctx, Message{Sender: "s@x", Recipient: "human:bob", Body: "ok?", Kind: KindQuestion,
		Options: []string{"yes", "no"}, Task: "t1"})
	if err != nil || q.ID != 4 {
		t.Fatalf("question after migration: %+v %v", q, err)
	}
}

func TestQuestions(t *testing.T) {
	s, _ := openTest(t)
	ctx := context.Background()
	send := func(from, to string, opts []string, task string) Message {
		t.Helper()
		m, err := s.Send(ctx, Message{Sender: from, Recipient: to, Subject: "q", Body: "proceed?",
			Kind: KindQuestion, Options: opts, Task: task})
		if err != nil {
			t.Fatal(err)
		}
		return m
	}
	q1 := send("a@x", "human:bob", []string{"yes", "no"}, "t1")
	q2 := send("b@x", "human:bob", nil, "")
	mustSend(t, s, "a@x", "human:bob", "", "plain")
	if q1.Kind != KindQuestion || !reflect.DeepEqual(q1.Options, []string{"yes", "no"}) || q1.Task != "t1" || q2.Options != nil {
		t.Fatalf("stored questions: %+v %+v", q1, q2)
	}
	got, err := s.OpenQuestions(ctx, "", "")
	if err != nil || !reflect.DeepEqual(ids(got), []int64{q1.ID, q2.ID}) {
		t.Fatalf("open = %v %v", ids(got), err)
	}
	if got, _ := s.OpenQuestions(ctx, "b@x", ""); !reflect.DeepEqual(ids(got), []int64{q2.ID}) {
		t.Fatalf("by sender = %v", ids(got))
	}
	if got, _ := s.OpenQuestions(ctx, "", "human:nobody"); len(got) != 0 {
		t.Fatalf("by recipient = %v", ids(got))
	}
	// Replying closes the question.
	if _, err := s.Reply(ctx, "human:bob", q1.ID, "yes"); err != nil {
		t.Fatal(err)
	}
	got, _ = s.OpenQuestions(ctx, "", "human:bob")
	if !reflect.DeepEqual(ids(got), []int64{q2.ID}) {
		t.Fatalf("after reply = %v", ids(got))
	}

	for _, bad := range []Message{
		{Sender: "a", Recipient: "b", Body: "x", Kind: "weird"},
		{Sender: "a", Recipient: "b", Body: "x", Options: []string{"o"}},
		{Sender: "a", Recipient: "b", Body: "x", Task: "t"},
	} {
		if _, err := s.Send(ctx, bad); !errors.Is(err, ErrInvalid) {
			t.Fatalf("Send(%+v) = %v, want ErrInvalid", bad, err)
		}
	}
}

func TestValidTaskID(t *testing.T) {
	for id, want := range map[string]bool{"t1": true, "a.b_c-D": true, "": false, "a b": false, "é": false,
		string(make([]byte, 65)): false} {
		if ValidTaskID(id) != want {
			t.Errorf("ValidTaskID(%q) = %v", id, !want)
		}
	}
}
