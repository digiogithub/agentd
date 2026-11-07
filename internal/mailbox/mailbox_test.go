package mailbox

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func openTest(t *testing.T) (*Store, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "mailbox.db")
	s, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s, path
}

func mustSend(t *testing.T, s *Store, from, to, subject, body string) Message {
	t.Helper()
	m, err := s.Send(context.Background(), Message{Sender: from, Recipient: to, Subject: subject, Body: body})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	return m
}

func ids(ms []Message) []int64 {
	out := make([]int64, len(ms))
	for i, m := range ms {
		out[i] = m.ID
	}
	return out
}

func TestSendAndGet(t *testing.T) {
	s, _ := openTest(t)
	ctx := context.Background()
	m := mustSend(t, s, "lead", "codex@models", "hello", "review PR #12")
	if m.ID <= 0 || m.State != StateQueued || m.Attempts != 0 {
		t.Fatalf("unexpected sent message: %+v", m)
	}
	if m.CreatedAt.IsZero() || m.DeliveredAt != nil || m.ReadAt != nil || m.RepliedAt != nil || m.ReplyTo != nil {
		t.Fatalf("unexpected timestamps: %+v", m)
	}
	got, err := s.Get(ctx, m.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Sender != "lead" || got.Recipient != "codex@models" || got.Subject != "hello" || got.Body != "review PR #12" {
		t.Fatalf("Get mismatch: %+v", got)
	}
	if !got.CreatedAt.Equal(m.CreatedAt) {
		t.Fatalf("created_at mismatch")
	}
	if _, err := s.Get(ctx, 9999); !errors.Is(err, ErrNotFound) {
		t.Fatalf("want ErrNotFound, got %v", err)
	}
}

func TestSendValidation(t *testing.T) {
	s, _ := openTest(t)
	ctx := context.Background()
	cases := []Message{
		{Sender: "", Recipient: "b", Body: "x"},
		{Sender: "  ", Recipient: "b", Body: "x"},
		{Sender: "a", Recipient: "", Body: "x"},
		{Sender: "a", Recipient: "b", Body: ""},
		{Sender: "a", Recipient: "b", Body: " \n\t"},
	}
	for i, c := range cases {
		if _, err := s.Send(ctx, c); !errors.Is(err, ErrInvalid) {
			t.Errorf("case %d: want ErrInvalid, got %v", i, err)
		}
	}
	bad := int64(42)
	if _, err := s.Send(ctx, Message{Sender: "a", Recipient: "b", Body: "x", ReplyTo: &bad}); !errors.Is(err, ErrInvalid) {
		t.Errorf("dangling reply_to: want ErrInvalid, got %v", err)
	}
	// Client-supplied id/state/attempts are ignored.
	m, err := s.Send(ctx, Message{ID: 77, Sender: "a", Recipient: "b", Body: "x", State: StateRead, Attempts: 5})
	if err != nil {
		t.Fatal(err)
	}
	if m.ID == 77 || m.State != StateQueued || m.Attempts != 0 {
		t.Fatalf("client fields not ignored: %+v", m)
	}
	// Valid reply_to is kept.
	ref := m.ID
	r, err := s.Send(ctx, Message{Sender: "b", Recipient: "a", Body: "y", ReplyTo: &ref})
	if err != nil {
		t.Fatal(err)
	}
	if r.ReplyTo == nil || *r.ReplyTo != ref {
		t.Fatalf("reply_to not stored: %+v", r)
	}
}

func TestStateTransitions(t *testing.T) {
	s, _ := openTest(t)
	ctx := context.Background()
	m := mustSend(t, s, "lead", "bob", "", "hi")

	if err := s.MarkDelivered(ctx, m.ID); err != nil {
		t.Fatal(err)
	}
	got, _ := s.Get(ctx, m.ID)
	if got.State != StateDelivered || got.Attempts != 1 || got.DeliveredAt == nil {
		t.Fatalf("after deliver: %+v", got)
	}
	first := *got.DeliveredAt

	// Idempotent on state; attempts keep counting; delivered_at kept.
	if err := s.MarkDelivered(ctx, m.ID); err != nil {
		t.Fatal(err)
	}
	got, _ = s.Get(ctx, m.ID)
	if got.State != StateDelivered || got.Attempts != 2 || !got.DeliveredAt.Equal(first) {
		t.Fatalf("after second deliver: %+v", got)
	}

	read, err := s.MarkRead(ctx, "bob", m.ID)
	if err != nil {
		t.Fatal(err)
	}
	if read.State != StateRead || read.ReadAt == nil {
		t.Fatalf("after read: %+v", read)
	}
	// MarkDelivered after read does not regress state.
	if err := s.MarkDelivered(ctx, m.ID); err != nil {
		t.Fatal(err)
	}
	got, _ = s.Get(ctx, m.ID)
	if got.State != StateRead || got.Attempts != 3 {
		t.Fatalf("deliver after read regressed: %+v", got)
	}
	// MarkRead again is idempotent.
	again, err := s.MarkRead(ctx, "bob", m.ID)
	if err != nil {
		t.Fatal(err)
	}
	if again.State != StateRead || !again.ReadAt.Equal(*read.ReadAt) {
		t.Fatalf("second read: %+v", again)
	}

	if err := s.MarkDelivered(ctx, 12345); !errors.Is(err, ErrNotFound) {
		t.Fatalf("want ErrNotFound, got %v", err)
	}
}

func TestMarkReadFromQueuedSetsDelivered(t *testing.T) {
	s, _ := openTest(t)
	ctx := context.Background()
	m := mustSend(t, s, "a", "b", "", "x")
	r, err := s.MarkRead(ctx, "b", m.ID)
	if err != nil {
		t.Fatal(err)
	}
	if r.State != StateRead || r.DeliveredAt == nil || r.ReadAt == nil {
		t.Fatalf("read from queued: %+v", r)
	}
}

func TestMarkReadErrors(t *testing.T) {
	s, _ := openTest(t)
	ctx := context.Background()
	m := mustSend(t, s, "a", "b", "", "x")
	if _, err := s.MarkRead(ctx, "b", 999); !errors.Is(err, ErrNotFound) {
		t.Fatalf("want ErrNotFound, got %v", err)
	}
	if _, err := s.MarkRead(ctx, "mallory", m.ID); !errors.Is(err, ErrForbidden) {
		t.Fatalf("want ErrForbidden, got %v", err)
	}
	// The sender cannot mark its own outgoing message read either.
	if _, err := s.MarkRead(ctx, "a", m.ID); !errors.Is(err, ErrForbidden) {
		t.Fatalf("want ErrForbidden for sender, got %v", err)
	}
	got, _ := s.Get(ctx, m.ID)
	if got.State != StateQueued {
		t.Fatalf("forbidden read changed state: %+v", got)
	}
}

func TestReplyFlow(t *testing.T) {
	s, _ := openTest(t)
	ctx := context.Background()
	orig := mustSend(t, s, "lead", "codex@models", "PR review", "Please review PR #12")
	if err := s.MarkDelivered(ctx, orig.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.MarkRead(ctx, "codex@models", orig.ID); err != nil {
		t.Fatal(err)
	}

	r, err := s.Reply(ctx, "codex@models", orig.ID, "Done, 3 comments")
	if err != nil {
		t.Fatal(err)
	}
	if r.Sender != "codex@models" || r.Recipient != "lead" || r.Body != "Done, 3 comments" {
		t.Fatalf("reply fields: %+v", r)
	}
	if r.Subject != "Re: PR review" {
		t.Fatalf("reply subject = %q", r.Subject)
	}
	if r.ReplyTo == nil || *r.ReplyTo != orig.ID || r.State != StateQueued || r.ID <= orig.ID {
		t.Fatalf("reply meta: %+v", r)
	}
	o, _ := s.Get(ctx, orig.ID)
	if o.State != StateReplied || o.RepliedAt == nil {
		t.Fatalf("original not replied: %+v", o)
	}
	// MarkRead on a replied message keeps it replied.
	o2, err := s.MarkRead(ctx, "codex@models", orig.ID)
	if err != nil || o2.State != StateReplied {
		t.Fatalf("read after reply: %+v %v", o2, err)
	}
	// The reply lands in the original sender's inbox.
	in, err := s.Inbox(ctx, "lead", false)
	if err != nil || len(in) != 1 || in[0].ID != r.ID {
		t.Fatalf("lead inbox: %+v %v", in, err)
	}
	// Reply to the reply: subject does not stack "Re: Re:".
	rr, err := s.Reply(ctx, "lead", r.ID, "thanks")
	if err != nil {
		t.Fatal(err)
	}
	if rr.Subject != "Re: PR review" || rr.Recipient != "codex@models" {
		t.Fatalf("reply-to-reply: %+v", rr)
	}
	// Multiple replies to the same message are allowed.
	if _, err := s.Reply(ctx, "codex@models", orig.ID, "one more thing"); err != nil {
		t.Fatal(err)
	}
}

func TestReplyFromQueuedSetsTimestamps(t *testing.T) {
	s, _ := openTest(t)
	ctx := context.Background()
	orig := mustSend(t, s, "a", "b", "", "x")
	if _, err := s.Reply(ctx, "b", orig.ID, "y"); err != nil {
		t.Fatal(err)
	}
	o, _ := s.Get(ctx, orig.ID)
	if o.State != StateReplied || o.DeliveredAt == nil || o.ReadAt == nil || o.RepliedAt == nil {
		t.Fatalf("replied from queued: %+v", o)
	}
}

func TestReplySubjectFromBody(t *testing.T) {
	long := strings.Repeat("abcdefghij", 10) // 100 chars
	cases := []struct {
		subject, body, want string
	}{
		{"", "short body", "Re: short body"},
		{"", long, "Re: " + long[:60]},
		{"", "line one\nline   two", "Re: line one line two"},
		{"", strings.Repeat("ñ", 70), "Re: " + strings.Repeat("ñ", 60)},
		{"Topic", "body", "Re: Topic"},
		{"Re: Topic", "body", "Re: Topic"},
	}
	for _, c := range cases {
		got := ReplySubject(Message{Subject: c.subject, Body: c.body})
		if got != c.want {
			t.Errorf("ReplySubject(%q,%q) = %q, want %q", c.subject, c.body, got, c.want)
		}
	}
	s, _ := openTest(t)
	orig := mustSend(t, s, "a", "b", "", long)
	r, err := s.Reply(context.Background(), "b", orig.ID, "ok")
	if err != nil {
		t.Fatal(err)
	}
	if r.Subject != "Re: "+long[:60] {
		t.Fatalf("stored subject = %q", r.Subject)
	}
}

func TestReplyErrors(t *testing.T) {
	s, _ := openTest(t)
	ctx := context.Background()
	orig := mustSend(t, s, "a", "b", "s", "x")
	if _, err := s.Reply(ctx, "b", 999, "y"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("want ErrNotFound, got %v", err)
	}
	if _, err := s.Reply(ctx, "mallory", orig.ID, "y"); !errors.Is(err, ErrForbidden) {
		t.Fatalf("want ErrForbidden, got %v", err)
	}
	if _, err := s.Reply(ctx, "a", orig.ID, "y"); !errors.Is(err, ErrForbidden) {
		t.Fatalf("sender replying own msg: want ErrForbidden, got %v", err)
	}
	if _, err := s.Reply(ctx, "b", orig.ID, "  "); !errors.Is(err, ErrInvalid) {
		t.Fatalf("empty body: want ErrInvalid, got %v", err)
	}
	if _, err := s.Reply(ctx, "", orig.ID, "y"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("empty from: want ErrInvalid, got %v", err)
	}
	// Failed replies must not change the original nor create messages.
	o, _ := s.Get(ctx, orig.ID)
	if o.State != StateQueued {
		t.Fatalf("failed reply changed original: %+v", o)
	}
	all, _ := s.Inbox(ctx, "a", true)
	if len(all) != 0 {
		t.Fatalf("failed reply created messages: %+v", all)
	}
}

func TestInboxOrderingAndFilters(t *testing.T) {
	s, _ := openTest(t)
	ctx := context.Background()
	var want []int64
	for i := 0; i < 5; i++ {
		m := mustSend(t, s, fmt.Sprintf("s%d", i), "bob", "", fmt.Sprintf("m%d", i))
		want = append(want, m.ID)
	}
	mustSend(t, s, "x", "alice", "", "not for bob")

	in, err := s.Inbox(ctx, "bob", false)
	if err != nil {
		t.Fatal(err)
	}
	if fmt.Sprint(ids(in)) != fmt.Sprint(want) {
		t.Fatalf("inbox ids = %v, want %v", ids(in), want)
	}

	if err := s.MarkDelivered(ctx, want[1]); err != nil {
		t.Fatal(err)
	}
	if _, err := s.MarkRead(ctx, "bob", want[2]); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Reply(ctx, "bob", want[3], "ok"); err != nil {
		t.Fatal(err)
	}

	in, _ = s.Inbox(ctx, "bob", false)
	if got := fmt.Sprint(ids(in)); got != fmt.Sprint([]int64{want[0], want[1], want[4]}) {
		t.Fatalf("unread inbox = %v", got)
	}
	all, _ := s.Inbox(ctx, "bob", true)
	if fmt.Sprint(ids(all)) != fmt.Sprint(want) {
		t.Fatalf("full inbox = %v", ids(all))
	}
	n, err := s.UnreadCount(ctx, "bob")
	if err != nil || n != 3 {
		t.Fatalf("UnreadCount = %d, %v", n, err)
	}

	q, _ := s.Queued(ctx, "bob")
	if got := fmt.Sprint(ids(q)); got != fmt.Sprint([]int64{want[0], want[4]}) {
		t.Fatalf("queued = %v", got)
	}

	empty, err := s.Inbox(ctx, "nobody", true)
	if err != nil || empty == nil || len(empty) != 0 {
		t.Fatalf("empty inbox should be non-nil empty slice: %#v %v", empty, err)
	}
}

func TestQueuedRecipients(t *testing.T) {
	s, _ := openTest(t)
	ctx := context.Background()
	r, err := s.QueuedRecipients(ctx)
	if err != nil || len(r) != 0 {
		t.Fatalf("initial: %v %v", r, err)
	}
	m1 := mustSend(t, s, "a", "zed", "", "1")
	mustSend(t, s, "a", "bob", "", "2")
	mustSend(t, s, "c", "bob", "", "3")
	r, _ = s.QueuedRecipients(ctx)
	if fmt.Sprint(r) != "[bob zed]" {
		t.Fatalf("recipients = %v", r)
	}
	if err := s.MarkDelivered(ctx, m1.ID); err != nil {
		t.Fatal(err)
	}
	r, _ = s.QueuedRecipients(ctx)
	if fmt.Sprint(r) != "[bob]" {
		t.Fatalf("recipients after deliver = %v", r)
	}
}

func TestReopenPersistence(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mb.db")
	ctx := context.Background()
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	m := mustSend(t, s, "a", "b", "subj", "body")
	if err := s.MarkDelivered(ctx, m.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	s2, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()
	v, err := s2.SchemaVersion(ctx)
	if err != nil || v != len(migrations) {
		t.Fatalf("schema version = %d, %v", v, err)
	}
	got, err := s2.Get(ctx, m.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.State != StateDelivered || got.Attempts != 1 || got.Subject != "subj" {
		t.Fatalf("after reopen: %+v", got)
	}
	// Ids remain monotonic across reopen (AUTOINCREMENT).
	m2 := mustSend(t, s2, "a", "b", "", "again")
	if m2.ID <= m.ID {
		t.Fatalf("id not monotonic: %d <= %d", m2.ID, m.ID)
	}
}

func TestOpenRejectsNewerSchema(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mb.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`INSERT INTO schema_migrations(version, applied_at) VALUES (?, 0)`, len(migrations)+1); err != nil {
		t.Fatal(err)
	}
	s.Close()
	if s2, err := Open(path); err == nil {
		s2.Close()
		t.Fatal("expected error opening newer schema")
	}
}

func TestPragmas(t *testing.T) {
	s, _ := openTest(t)
	var mode string
	if err := s.db.QueryRow(`PRAGMA journal_mode`).Scan(&mode); err != nil || mode != "wal" {
		t.Fatalf("journal_mode = %q, %v", mode, err)
	}
	var fk int
	if err := s.db.QueryRow(`PRAGMA foreign_keys`).Scan(&fk); err != nil || fk != 1 {
		t.Fatalf("foreign_keys = %d, %v", fk, err)
	}
	var bt int
	if err := s.db.QueryRow(`PRAGMA busy_timeout`).Scan(&bt); err != nil || bt != busyTimeoutMs {
		t.Fatalf("busy_timeout = %d, %v", bt, err)
	}
}

func TestJSONShape(t *testing.T) {
	s, _ := openTest(t)
	m := mustSend(t, s, "a", "b", "s", "x")
	raw, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	var obj map[string]any
	if err := json.Unmarshal(raw, &obj); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"id", "sender", "recipient", "subject", "body", "state", "attempts", "created_at"} {
		if _, ok := obj[k]; !ok {
			t.Errorf("missing JSON key %q in %s", k, raw)
		}
	}
	for _, k := range []string{"reply_to", "delivered_at", "read_at", "replied_at"} {
		if _, ok := obj[k]; ok {
			t.Errorf("unexpected JSON key %q for nil value in %s", k, raw)
		}
	}
	var back Message
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatal(err)
	}
	if back.ID != m.ID || back.State != m.State || !back.CreatedAt.Equal(m.CreatedAt) {
		t.Fatalf("round trip mismatch: %+v vs %+v", back, m)
	}
}

func TestContextCanceled(t *testing.T) {
	s, _ := openTest(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := s.Send(ctx, Message{Sender: "a", Recipient: "b", Body: "x"}); err == nil {
		t.Fatal("expected error with canceled context")
	}
}

func TestConcurrentSend(t *testing.T) {
	s, path := openTest(t)
	ctx := context.Background()
	const workers, perWorker = 16, 25

	// A second Store on the same file exercises cross-connection locking too.
	s2, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()

	var wg sync.WaitGroup
	errCh := make(chan error, workers*perWorker*2)
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			st := s
			if w%2 == 1 {
				st = s2
			}
			for i := 0; i < perWorker; i++ {
				m, err := st.Send(ctx, Message{
					Sender:    fmt.Sprintf("w%d", w),
					Recipient: fmt.Sprintf("r%d", i%3),
					Body:      fmt.Sprintf("msg %d/%d", w, i),
				})
				if err != nil {
					errCh <- err
					continue
				}
				// Interleave reads and writes.
				if err := st.MarkDelivered(ctx, m.ID); err != nil {
					errCh <- err
				}
				if i%5 == 0 {
					if _, err := st.Reply(ctx, m.Recipient, m.ID, "ack"); err != nil {
						errCh <- err
					}
				}
				if _, err := st.Inbox(ctx, m.Recipient, false); err != nil {
					errCh <- err
				}
			}
		}(w)
	}
	wg.Wait()
	close(errCh)
	for err := range errCh {
		t.Error(err)
	}

	total := 0
	seen := map[int64]bool{}
	for r := 0; r < 3; r++ {
		all, err := s.Inbox(ctx, fmt.Sprintf("r%d", r), true)
		if err != nil {
			t.Fatal(err)
		}
		for i, m := range all {
			if seen[m.ID] {
				t.Fatalf("duplicate id %d", m.ID)
			}
			seen[m.ID] = true
			if i > 0 && all[i-1].ID >= m.ID {
				t.Fatalf("inbox not ordered by id")
			}
			if m.Attempts != 1 {
				t.Fatalf("message %d attempts = %d", m.ID, m.Attempts)
			}
		}
		total += len(all)
	}
	if total != workers*perWorker {
		t.Fatalf("total messages = %d, want %d", total, workers*perWorker)
	}
	replies := 0
	for w := 0; w < workers; w++ {
		in, _ := s.Inbox(ctx, fmt.Sprintf("w%d", w), true)
		replies += len(in)
	}
	if want := workers * ((perWorker + 4) / 5); replies != want {
		t.Fatalf("replies = %d, want %d", replies, want)
	}
}
