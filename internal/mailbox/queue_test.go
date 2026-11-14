package mailbox

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func TestQueuePostAndClaim(t *testing.T) {
	s, _ := openTest(t)
	ctx := context.Background()
	p1 := mustSend(t, s, "human:alice", "queue:build", "t1", "first task")
	p2 := mustSend(t, s, "coord@x", "queue:build", "", "second task")
	if p1.State != StateUnclaimed || p1.Queue != "build" || p1.Recipient != "queue:build" || p1.ClaimedAt != nil {
		t.Fatalf("post = %+v", p1)
	}
	// Unclaimed items are never queued for delivery.
	if rs, err := s.QueuedRecipients(ctx); err != nil || len(rs) != 0 {
		t.Fatalf("queued recipients = %v %v", rs, err)
	}
	items, err := s.QueueItems(ctx, "build")
	if err != nil || fmt.Sprint(ids(items)) != fmt.Sprint([]int64{p1.ID, p2.ID}) {
		t.Fatalf("items = %v %v", ids(items), err)
	}
	qs, err := s.Queues(ctx)
	if err != nil || len(qs) != 1 || qs[0] != (QueueCount{Name: "build", Unclaimed: 2}) {
		t.Fatalf("queues = %+v %v", qs, err)
	}

	// Oldest first; the claim is the fetch (delivered).
	c1, err := s.Claim(ctx, ClaimOptions{Queue: "build", Claimer: "codex@w"})
	if err != nil {
		t.Fatal(err)
	}
	if c1.ID != p1.ID || c1.Recipient != "codex@w" || c1.State != StateDelivered || c1.Queue != "build" ||
		c1.ClaimedAt == nil || c1.DeliveredAt == nil || c1.Sender != "human:alice" {
		t.Fatalf("claim = %+v", c1)
	}
	// Inject leaves it queued for the delivery loop.
	c2, err := s.Claim(ctx, ClaimOptions{Queue: "build", Claimer: "claude@w", ID: p2.ID, Inject: true})
	if err != nil || c2.State != StateQueued || c2.DeliveredAt != nil {
		t.Fatalf("claim inject = %+v %v", c2, err)
	}
	if rs, _ := s.QueuedRecipients(ctx); fmt.Sprint(rs) != "[claude@w]" {
		t.Fatalf("queued recipients = %v", rs)
	}
	// Empty queue, claimed item, foreign item.
	if _, err := s.Claim(ctx, ClaimOptions{Queue: "build", Claimer: "x"}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("empty queue: %v", err)
	}
	if _, err := s.Claim(ctx, ClaimOptions{Queue: "build", Claimer: "x", ID: p1.ID}); !errors.Is(err, ErrClaimed) {
		t.Fatalf("double claim: %v", err)
	}
	other := mustSend(t, s, "a", "b", "", "direct")
	if _, err := s.Claim(ctx, ClaimOptions{Queue: "build", Claimer: "x", ID: other.ID}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("direct message claim: %v", err)
	}
	if _, err := s.Claim(ctx, ClaimOptions{Queue: "bad name", Claimer: "x"}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("bad queue: %v", err)
	}
	// After the claim it is a normal message: read and reply to the original sender.
	if _, err := s.MarkRead(ctx, "codex@w", c1.ID); err != nil {
		t.Fatal(err)
	}
	r, err := s.Reply(ctx, "codex@w", c1.ID, "done")
	if err != nil || r.Recipient != "human:alice" || r.Queue != "" || r.State != StateQueued {
		t.Fatalf("reply = %+v %v", r, err)
	}
	if qs, _ := s.Queues(ctx); len(qs) != 0 {
		t.Fatalf("queues after claims = %+v", qs)
	}
}

func TestQueueSendValidation(t *testing.T) {
	s, _ := openTest(t)
	for _, to := range []string{"queue:", "queue:a b", "queue:" + string(make([]byte, 65))} {
		if _, err := s.Send(context.Background(), Message{Sender: "a", Recipient: to, Body: "x"}); !errors.Is(err, ErrInvalid) {
			t.Errorf("send to %q: %v", to, err)
		}
	}
	// An unclaimed item is not readable or answerable by anyone.
	p := mustSend(t, s, "a", "queue:q", "", "x")
	if _, err := s.MarkRead(context.Background(), "b", p.ID); !errors.Is(err, ErrForbidden) {
		t.Fatalf("read unclaimed: %v", err)
	}
	if _, err := s.Reply(context.Background(), "b", p.ID, "x"); !errors.Is(err, ErrForbidden) {
		t.Fatalf("reply unclaimed: %v", err)
	}
}

func TestClaimConcurrent(t *testing.T) {
	s, _ := openTest(t)
	ctx := context.Background()
	const items, claimers = 5, 12
	for i := range items {
		mustSend(t, s, "coord", "queue:jobs", "", fmt.Sprintf("job %d", i))
	}
	var (
		mu     sync.Mutex
		got    = map[int64]string{}
		misses int
		wg     sync.WaitGroup
		start  = make(chan struct{})
	)
	for c := range claimers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			name := fmt.Sprintf("agent-%d", c)
			m, err := s.Claim(ctx, ClaimOptions{Queue: "jobs", Claimer: name})
			mu.Lock()
			defer mu.Unlock()
			switch {
			case err == nil:
				if prev, dup := got[m.ID]; dup {
					t.Errorf("#%d claimed by %s and %s", m.ID, prev, name)
				}
				got[m.ID] = name
			case errors.Is(err, ErrNotFound):
				misses++
			default:
				t.Errorf("claim: %v", err)
			}
		}()
	}
	close(start)
	wg.Wait()
	if len(got) != items || misses != claimers-items {
		t.Fatalf("claimed %d, misses %d", len(got), misses)
	}
	// Same item, many claimers: exactly one wins, the others see ErrClaimed.
	p := mustSend(t, s, "coord", "queue:one", "", "only")
	var wins, lost int
	start = make(chan struct{})
	for c := range claimers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, err := s.Claim(ctx, ClaimOptions{Queue: "one", Claimer: fmt.Sprintf("a%d", c), ID: p.ID})
			mu.Lock()
			defer mu.Unlock()
			switch {
			case err == nil:
				wins++
			case errors.Is(err, ErrClaimed):
				lost++
			default:
				t.Errorf("claim: %v", err)
			}
		}()
	}
	close(start)
	wg.Wait()
	if wins != 1 || lost != claimers-1 {
		t.Fatalf("wins %d lost %d", wins, lost)
	}
}

func TestPurgeQueueItems(t *testing.T) {
	s, _ := openTest(t)
	ctx := context.Background()
	now := time.UnixMilli(1_000_000)
	clockAt(s, &now)
	old := mustSend(t, s, "a", "queue:q", "", "old task")
	now = now.Add(time.Hour)
	fresh := mustSend(t, s, "a", "queue:q", "", "fresh task")
	// Retention (age only) covers unclaimed items.
	res, err := s.Purge(ctx, PurgeOptions{Before: now.Add(-time.Minute)})
	if err != nil || fmt.Sprint(res.IDs) != fmt.Sprint([]int64{old.ID}) {
		t.Fatalf("retention purge = %+v %v", res, err)
	}
	// By state and by queue address.
	if res, err := s.Purge(ctx, PurgeOptions{States: []State{StateUnclaimed}, DryRun: true}); err != nil || res.Count != 1 {
		t.Fatalf("dry run = %+v %v", res, err)
	}
	if res, err := s.Purge(ctx, PurgeOptions{Recipient: "queue:q"}); err != nil || fmt.Sprint(res.IDs) != fmt.Sprint([]int64{fresh.ID}) {
		t.Fatalf("queue purge = %+v %v", res, err)
	}
	if qs, _ := s.Queues(ctx); len(qs) != 0 {
		t.Fatalf("queues = %+v", qs)
	}
}

func TestDigest(t *testing.T) {
	s, _ := openTest(t)
	ctx := context.Background()
	empty, err := s.InboxDigest(ctx, "b", false)
	if err != nil || empty != Digest(nil) || len(empty) != 17 {
		t.Fatalf("empty digest = %q %v", empty, err)
	}
	m := mustSend(t, s, "a", "b", "", "x")
	d1, _ := s.InboxDigest(ctx, "b", false)
	msgs, _ := s.Inbox(ctx, "b", false)
	if d1 == empty || d1 != Digest(msgs) {
		t.Fatalf("digest %q vs listing %q", d1, Digest(msgs))
	}
	if d, _ := s.InboxDigest(ctx, "b", false); d != d1 {
		t.Fatal("digest not stable")
	}
	if err := s.MarkDelivered(ctx, m.ID); err != nil {
		t.Fatal(err)
	}
	d2, _ := s.InboxDigest(ctx, "b", false)
	if d2 == d1 {
		t.Fatal("state change kept the digest")
	}
	all, _ := s.InboxDigest(ctx, "b", true)
	if all != d2 {
		t.Fatal("all and unread differ with only unread messages")
	}
	if _, err := s.MarkRead(ctx, "b", m.ID); err != nil {
		t.Fatal(err)
	}
	if d, _ := s.InboxDigest(ctx, "b", false); d != empty {
		t.Fatal("read message still in the unread digest")
	}
	q0, _ := s.QueueDigest(ctx, "q")
	mustSend(t, s, "a", "queue:q", "", "task")
	q1, _ := s.QueueDigest(ctx, "q")
	items, _ := s.QueueItems(ctx, "q")
	if q0 == q1 || q1 != Digest(items) {
		t.Fatal("queue digest")
	}
}

func TestMigrateV2Database(t *testing.T) {
	path := filepath.Join(t.TempDir(), "v2.db")
	raw, err := sql.Open("sqlite", "file:"+path+"?_pragma=foreign_keys(1)&_pragma=journal_mode(WAL)")
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		`CREATE TABLE schema_migrations (version INTEGER PRIMARY KEY, applied_at INTEGER NOT NULL)`,
		migrations[0],
		`INSERT INTO schema_migrations VALUES (1, 0)`,
		migrations[1],
		`INSERT INTO schema_migrations VALUES (2, 0)`,
		`INSERT INTO messages(sender, recipient, subject, body, state, created_at, delivered_at, read_at, replied_at)
			VALUES ('human:alice', 'codex@models', 'hi', 'review', 'replied', 1000, 1050, 1100, 1200)`,
		`INSERT INTO messages(sender, recipient, subject, body, reply_to, state, attempts, created_at)
			VALUES ('codex@models', 'human:alice', 'Re: hi', 'done', 1, 'queued', 2, 1300)`,
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
		t.Fatalf("Open v2: %v", err)
	}
	defer s.Close()
	ctx := context.Background()
	if v, err := s.SchemaVersion(ctx); err != nil || v != 3 || len(migrations) != 3 {
		t.Fatalf("schema version = %d, %v", v, err)
	}
	m1, err := s.Get(ctx, 1)
	if err != nil || m1.State != StateReplied || m1.Subject != "hi" || m1.DeliveredAt.UnixMilli() != 1050 ||
		m1.RepliedAt.UnixMilli() != 1200 || m1.Queue != "" || m1.ClaimedAt != nil {
		t.Fatalf("message 1 after migration: %+v %v", m1, err)
	}
	m2, err := s.Get(ctx, 2)
	if err != nil || m2.ReplyTo == nil || *m2.ReplyTo != 1 || m2.Attempts != 2 || m2.State != StateQueued {
		t.Fatalf("message 2 after migration: %+v %v", m2, err)
	}
	if n := mustCount(t, s); n != 2 {
		t.Fatalf("%d rows after migration", n)
	}
	var idx, leftover int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type = 'index'
		AND name IN ('messages_recipient_state', 'messages_created_at')`).Scan(&idx); err != nil || idx != 2 {
		t.Fatalf("indexes = %d, %v", idx, err)
	}
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE name = 'messages_v3'`).Scan(&leftover); err != nil || leftover != 0 {
		t.Fatalf("messages_v3 left behind: %d %v", leftover, err)
	}
	// The counter survived the rebuild: id 3 is not reused, and the new state works.
	p := mustSend(t, s, "a", "queue:q", "", "task")
	if p.ID != 4 || p.State != StateUnclaimed {
		t.Fatalf("new post = %+v", p)
	}
	// ON DELETE SET NULL still holds on migrated rows.
	if _, err := s.Purge(ctx, PurgeOptions{Recipient: "codex@models"}); err != nil {
		t.Fatal(err)
	}
	if m2, err := s.Get(ctx, 2); err != nil || m2.ReplyTo != nil {
		t.Fatalf("reply after purging its original: %+v %v", m2, err)
	}
}
