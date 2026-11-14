package mailbox

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"strconv"
	"strings"
)

// QueuePrefix starts the address of a shared work queue: "queue:<name>".
const QueuePrefix = "queue:"

// ValidQueueName reports whether name is a valid <name> in "queue:<name>":
// 1..64 characters of [a-zA-Z0-9._-] (the human-name charset).
func ValidQueueName(name string) bool {
	if name == "" || len(name) > 64 {
		return false
	}
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '.', r == '_', r == '-':
		default:
			return false
		}
	}
	return true
}

// QueueAddr returns the address of queue name ("queue:<name>").
func QueueAddr(name string) string { return QueuePrefix + name }

// QueueOf returns the queue name of addr and whether addr is a valid
// "queue:<name>" address.
func QueueOf(addr string) (string, bool) {
	name, ok := strings.CutPrefix(addr, QueuePrefix)
	return name, ok && ValidQueueName(name)
}

// ClaimOptions selects what Claim takes.
type ClaimOptions struct {
	// Queue is the queue name (without the "queue:" prefix).
	Queue string
	// Claimer becomes the recipient of the claimed message.
	Claimer string
	// ID claims that item; 0 claims the oldest unclaimed item.
	ID int64
	// Inject leaves the claimed message queued, so the delivery loop types its
	// notice into the claimer's terminal. By default the claim is the fetch:
	// the message is delivered (like listing one's own inbox).
	Inject bool
}

// Claim atomically assigns an unclaimed item of a queue to the claimer and
// returns it: the recipient becomes the claimer, the state queued (Inject) or
// delivered, claimed_at is set and Queue keeps the origin. Everything runs in
// one write transaction, so of concurrent claimers exactly one wins. An empty
// queue, or an ID that is not an item of the queue, is ErrNotFound; an item
// already claimed is ErrClaimed.
func (s *Store) Claim(ctx context.Context, o ClaimOptions) (Message, error) {
	if !ValidQueueName(o.Queue) {
		return Message{}, fmt.Errorf("%w: bad queue name %q", ErrInvalid, o.Queue)
	}
	o.Claimer = strings.TrimSpace(o.Claimer)
	if o.Claimer == "" {
		return Message{}, fmt.Errorf("%w: empty claimer", ErrInvalid)
	}
	if o.ID < 0 {
		return Message{}, fmt.Errorf("%w: negative id", ErrInvalid)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Message{}, fmt.Errorf("mailbox: claim: %w", err)
	}
	defer tx.Rollback()

	id := o.ID
	if id == 0 {
		err := tx.QueryRowContext(ctx, `SELECT id FROM messages
			WHERE recipient = ? AND state = 'unclaimed' ORDER BY id LIMIT 1`, QueueAddr(o.Queue)).Scan(&id)
		if errors.Is(err, sql.ErrNoRows) {
			return Message{}, fmt.Errorf("%w: queue %q has no unclaimed item", ErrNotFound, o.Queue)
		}
		if err != nil {
			return Message{}, fmt.Errorf("mailbox: claim: %w", err)
		}
	} else {
		m, err := getMessage(ctx, tx, id)
		if err != nil {
			return Message{}, err
		}
		if m.Queue != o.Queue {
			return Message{}, fmt.Errorf("%w: #%d is not an item of queue %q", ErrNotFound, id, o.Queue)
		}
		if m.State != StateUnclaimed {
			return Message{}, fmt.Errorf("%w: #%d (by %s)", ErrClaimed, id, m.Recipient)
		}
	}
	now := s.now().UnixMilli()
	state := StateDelivered
	var delivered sql.NullInt64
	if o.Inject {
		state = StateQueued
	} else {
		delivered = sql.NullInt64{Int64: now, Valid: true}
	}
	res, err := tx.ExecContext(ctx, `UPDATE messages SET
			recipient = ?, state = ?, claimed_at = ?, delivered_at = ?
		 WHERE id = ? AND state = 'unclaimed'`, o.Claimer, string(state), now, delivered, id)
	if err != nil {
		return Message{}, fmt.Errorf("mailbox: claim #%d: %w", id, err)
	}
	if n, err := res.RowsAffected(); err != nil {
		return Message{}, fmt.Errorf("mailbox: claim #%d: %w", id, err)
	} else if n == 0 {
		return Message{}, fmt.Errorf("%w: #%d", ErrClaimed, id)
	}
	out, err := getMessage(ctx, tx, id)
	if err != nil {
		return Message{}, err
	}
	if err := tx.Commit(); err != nil {
		return Message{}, fmt.Errorf("mailbox: claim: %w", err)
	}
	return out, nil
}

// QueueItems returns the unclaimed items of queue name, ordered by id.
func (s *Store) QueueItems(ctx context.Context, name string) ([]Message, error) {
	return s.list(ctx, `SELECT `+selectCols+` FROM messages
		WHERE recipient = ? AND state = 'unclaimed' ORDER BY id`, QueueAddr(name))
}

// QueueCount is a queue name and its number of unclaimed items.
type QueueCount struct {
	Name      string `json:"name"`
	Unclaimed int    `json:"unclaimed"`
}

// Queues returns every queue that has unclaimed items, sorted by name.
func (s *Store) Queues(ctx context.Context) ([]QueueCount, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT queue, COUNT(*) FROM messages
		WHERE state = 'unclaimed' GROUP BY queue ORDER BY queue`)
	if err != nil {
		return nil, fmt.Errorf("mailbox: queues: %w", err)
	}
	defer rows.Close()
	out := []QueueCount{}
	for rows.Next() {
		var q QueueCount
		if err := rows.Scan(&q.Name, &q.Unclaimed); err != nil {
			return nil, fmt.Errorf("mailbox: queues: %w", err)
		}
		out = append(out, q)
	}
	return out, rows.Err()
}

// digester hashes (id, state) pairs in order: two listings with the same
// messages in the same states have the same digest.
type digester struct{ h hash.Hash }

func newDigester() digester { return digester{h: sha256.New()} }

func (d digester) add(id int64, state string) {
	d.h.Write(strconv.AppendInt(nil, id, 10))
	d.h.Write([]byte{':'})
	d.h.Write([]byte(state))
	d.h.Write([]byte{';'})
}

func (d digester) sum() string { return "d" + hex.EncodeToString(d.h.Sum(nil)[:8]) }

// Digest returns the digest of a listing (Inbox, QueueItems): "d" followed by
// 16 hex digits of SHA-256 over the ids and states, in order. It changes
// whenever a message enters or leaves the listing or changes state.
func Digest(msgs []Message) string {
	d := newDigester()
	for _, m := range msgs {
		d.add(m.ID, string(m.State))
	}
	return d.sum()
}

// InboxDigest returns Digest(Inbox(recipient, includeRead)) without reading
// the message bodies.
func (s *Store) InboxDigest(ctx context.Context, recipient string, includeRead bool) (string, error) {
	q := `SELECT id, state FROM messages WHERE recipient = ? ORDER BY id`
	if !includeRead {
		q = `SELECT id, state FROM messages WHERE recipient = ? AND state IN ('queued','delivered') ORDER BY id`
	}
	return s.digest(ctx, q, recipient)
}

// QueueDigest returns Digest(QueueItems(name)) without reading the bodies.
func (s *Store) QueueDigest(ctx context.Context, name string) (string, error) {
	return s.digest(ctx, `SELECT id, state FROM messages
		WHERE recipient = ? AND state = 'unclaimed' ORDER BY id`, QueueAddr(name))
}

func (s *Store) digest(ctx context.Context, query string, args ...any) (string, error) {
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return "", fmt.Errorf("mailbox: digest: %w", err)
	}
	defer rows.Close()
	d := newDigester()
	for rows.Next() {
		var (
			id    int64
			state string
		)
		if err := rows.Scan(&id, &state); err != nil {
			return "", fmt.Errorf("mailbox: digest: %w", err)
		}
		d.add(id, state)
	}
	if err := rows.Err(); err != nil {
		return "", fmt.Errorf("mailbox: digest: %w", err)
	}
	return d.sum(), nil
}
