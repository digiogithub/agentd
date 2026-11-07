// Package mailbox is the persistent message store used by agentd to let
// agents exchange messages. Messages move through the states
// queued → delivered → read → (optional) replied.
//
// The store is backed by SQLite (modernc.org/sqlite, WAL mode) and is safe for
// concurrent use from multiple goroutines.
package mailbox

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"slices"
	"strings"
	"time"

	_ "modernc.org/sqlite" // registers the "sqlite" driver
)

// Sentinel errors. Use errors.Is to test for them.
var (
	ErrNotFound  = errors.New("mailbox: message not found")
	ErrForbidden = errors.New("mailbox: forbidden")
	ErrInvalid   = errors.New("mailbox: invalid message")
)

// State is the delivery state of a message.
type State string

// Message states.
const (
	StateQueued    State = "queued"
	StateDelivered State = "delivered"
	StateRead      State = "read"
	StateReplied   State = "replied"
)

// Message is a mailbox message. It is serialized over the wire and via MCP.
type Message struct {
	ID          int64      `json:"id"`
	Sender      string     `json:"sender"`
	Recipient   string     `json:"recipient"`
	Subject     string     `json:"subject"`
	Body        string     `json:"body"`
	ReplyTo     *int64     `json:"reply_to,omitempty"`
	State       State      `json:"state"`
	Attempts    int        `json:"attempts"`
	CreatedAt   time.Time  `json:"created_at"`
	DeliveredAt *time.Time `json:"delivered_at,omitempty"`
	ReadAt      *time.Time `json:"read_at,omitempty"`
	RepliedAt   *time.Time `json:"replied_at,omitempty"`
}

// Unread reports whether the message has not been read yet (queued or delivered).
func (m Message) Unread() bool {
	return m.State == StateQueued || m.State == StateDelivered
}

// Store is a SQLite-backed mailbox.
type Store struct {
	db  *sql.DB
	now func() time.Time
}

// busyTimeoutMs is how long a connection waits on a locked database.
const busyTimeoutMs = 10000

// Open opens (creating if needed) the mailbox database at path and applies
// pending schema migrations.
func Open(path string) (*Store, error) {
	if path == "" {
		return nil, fmt.Errorf("%w: empty database path", ErrInvalid)
	}
	q := url.Values{}
	q.Add("_pragma", "journal_mode(WAL)")
	q.Add("_pragma", fmt.Sprintf("busy_timeout(%d)", busyTimeoutMs))
	q.Add("_pragma", "foreign_keys(1)")
	q.Add("_pragma", "synchronous(NORMAL)")
	// BEGIN IMMEDIATE takes the write lock up front, so concurrent
	// read-then-write transactions wait on busy_timeout instead of failing
	// with SQLITE_BUSY on lock upgrade.
	q.Set("_txlock", "immediate")
	dsn := "file:" + path + "?" + q.Encode()

	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("mailbox: open %s: %w", path, err)
	}
	s := &Store{db: db, now: time.Now}
	if err := s.migrate(context.Background()); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

// Close closes the underlying database.
func (s *Store) Close() error {
	return s.db.Close()
}

// migrations are applied in order; index i brings the schema to version i+1.
// Never edit an existing entry: append a new one.
var migrations = []string{
	`CREATE TABLE messages (
		id           INTEGER PRIMARY KEY AUTOINCREMENT,
		sender       TEXT    NOT NULL,
		recipient    TEXT    NOT NULL,
		subject      TEXT    NOT NULL DEFAULT '',
		body         TEXT    NOT NULL,
		reply_to     INTEGER REFERENCES messages(id),
		state        TEXT    NOT NULL DEFAULT 'queued'
		             CHECK (state IN ('queued','delivered','read','replied')),
		attempts     INTEGER NOT NULL DEFAULT 0,
		created_at   INTEGER NOT NULL,
		delivered_at INTEGER,
		read_at      INTEGER,
		replied_at   INTEGER
	);
	CREATE INDEX messages_recipient_state ON messages(recipient, state);`,

	// v2: reply_to becomes ON DELETE SET NULL so purging a message never
	// breaks the replies that survive it, plus an index for retention.
	// SQLite cannot alter a constraint, so the table is rebuilt (ids kept)
	// and its AUTOINCREMENT counter carried over before the old one is
	// dropped. The self reference is rewritten by the RENAME.
	`CREATE TABLE messages_v2 (
		id           INTEGER PRIMARY KEY AUTOINCREMENT,
		sender       TEXT    NOT NULL,
		recipient    TEXT    NOT NULL,
		subject      TEXT    NOT NULL DEFAULT '',
		body         TEXT    NOT NULL,
		reply_to     INTEGER REFERENCES messages_v2(id) ON DELETE SET NULL,
		state        TEXT    NOT NULL DEFAULT 'queued'
		             CHECK (state IN ('queued','delivered','read','replied')),
		attempts     INTEGER NOT NULL DEFAULT 0,
		created_at   INTEGER NOT NULL,
		delivered_at INTEGER,
		read_at      INTEGER,
		replied_at   INTEGER
	);
	INSERT INTO messages_v2(id, sender, recipient, subject, body, reply_to, state, attempts,
		created_at, delivered_at, read_at, replied_at)
		SELECT id, sender, recipient, subject, body, reply_to, state, attempts,
			created_at, delivered_at, read_at, replied_at
		FROM messages ORDER BY id;
	INSERT INTO sqlite_sequence(name, seq)
		SELECT 'messages_v2', 0 WHERE NOT EXISTS (SELECT 1 FROM sqlite_sequence WHERE name = 'messages_v2');
	UPDATE sqlite_sequence SET seq = MAX(seq,
		COALESCE((SELECT seq FROM sqlite_sequence WHERE name = 'messages'), 0))
		WHERE name = 'messages_v2';
	DROP TABLE messages;
	ALTER TABLE messages_v2 RENAME TO messages;
	CREATE INDEX messages_recipient_state ON messages(recipient, state);
	CREATE INDEX messages_created_at ON messages(created_at);`,
}

func (s *Store) migrate(ctx context.Context) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("mailbox: migrate: %w", err)
	}
	defer tx.Rollback()

	if _, err := tx.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (
		version    INTEGER PRIMARY KEY,
		applied_at INTEGER NOT NULL
	)`); err != nil {
		return fmt.Errorf("mailbox: migrate: %w", err)
	}
	var current int
	if err := tx.QueryRowContext(ctx,
		`SELECT COALESCE(MAX(version), 0) FROM schema_migrations`).Scan(&current); err != nil {
		return fmt.Errorf("mailbox: migrate: %w", err)
	}
	if current > len(migrations) {
		return fmt.Errorf("mailbox: database schema version %d is newer than supported %d",
			current, len(migrations))
	}
	for v := current; v < len(migrations); v++ {
		if _, err := tx.ExecContext(ctx, migrations[v]); err != nil {
			return fmt.Errorf("mailbox: migration %d: %w", v+1, err)
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO schema_migrations(version, applied_at) VALUES (?, ?)`,
			v+1, s.now().UnixMilli()); err != nil {
			return fmt.Errorf("mailbox: migration %d: %w", v+1, err)
		}
	}
	if current < len(migrations) && current > 0 {
		// Table rebuilds must leave every reference intact.
		if err := foreignKeyCheck(ctx, tx); err != nil {
			return fmt.Errorf("mailbox: migrate: %w", err)
		}
	}
	return tx.Commit()
}

// foreignKeyCheck fails if PRAGMA foreign_key_check reports any violation.
func foreignKeyCheck(ctx context.Context, tx *sql.Tx) error {
	rows, err := tx.QueryContext(ctx, `PRAGMA foreign_key_check`)
	if err != nil {
		return err
	}
	defer rows.Close()
	if rows.Next() {
		return errors.New("foreign key violations after migration")
	}
	return rows.Err()
}

// SetClock replaces the store's clock (time.Now by default), used for the
// timestamps of new messages and state changes. For tests and tools; call it
// before the store is used concurrently.
func (s *Store) SetClock(now func() time.Time) {
	if now == nil {
		now = time.Now
	}
	s.now = now
}

// SchemaVersion returns the current schema version of the database.
func (s *Store) SchemaVersion(ctx context.Context) (int, error) {
	var v int
	err := s.db.QueryRowContext(ctx,
		`SELECT COALESCE(MAX(version), 0) FROM schema_migrations`).Scan(&v)
	return v, err
}

const selectCols = `id, sender, recipient, subject, body, reply_to, state, attempts,
	created_at, delivered_at, read_at, replied_at`

type rowScanner interface {
	Scan(dest ...any) error
}

func scanMessage(r rowScanner) (Message, error) {
	var (
		m                     Message
		replyTo               sql.NullInt64
		state                 string
		created               int64
		delivered, read, repl sql.NullInt64
	)
	if err := r.Scan(&m.ID, &m.Sender, &m.Recipient, &m.Subject, &m.Body, &replyTo,
		&state, &m.Attempts, &created, &delivered, &read, &repl); err != nil {
		return Message{}, err
	}
	if replyTo.Valid {
		v := replyTo.Int64
		m.ReplyTo = &v
	}
	m.State = State(state)
	m.CreatedAt = time.UnixMilli(created)
	m.DeliveredAt = msPtr(delivered)
	m.ReadAt = msPtr(read)
	m.RepliedAt = msPtr(repl)
	return m, nil
}

func msPtr(v sql.NullInt64) *time.Time {
	if !v.Valid {
		return nil
	}
	t := time.UnixMilli(v.Int64)
	return &t
}

type querier interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

func getMessage(ctx context.Context, q querier, id int64) (Message, error) {
	m, err := scanMessage(q.QueryRowContext(ctx,
		`SELECT `+selectCols+` FROM messages WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return Message{}, fmt.Errorf("%w: #%d", ErrNotFound, id)
	}
	if err != nil {
		return Message{}, fmt.Errorf("mailbox: get #%d: %w", id, err)
	}
	return m, nil
}

func (s *Store) list(ctx context.Context, query string, args ...any) ([]Message, error) {
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("mailbox: query: %w", err)
	}
	defer rows.Close()
	out := []Message{}
	for rows.Next() {
		m, err := scanMessage(rows)
		if err != nil {
			return nil, fmt.Errorf("mailbox: scan: %w", err)
		}
		out = append(out, m)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("mailbox: query: %w", err)
	}
	return out, nil
}

// Send stores a new message in state queued and returns it with its id and
// timestamps set. Sender, recipient and body must be non-empty. If ReplyTo is
// set it must reference an existing message. ID, State, Attempts and the
// timestamps of m are ignored.
func (s *Store) Send(ctx context.Context, m Message) (Message, error) {
	m.Sender = strings.TrimSpace(m.Sender)
	m.Recipient = strings.TrimSpace(m.Recipient)
	if m.Sender == "" {
		return Message{}, fmt.Errorf("%w: empty sender", ErrInvalid)
	}
	if m.Recipient == "" {
		return Message{}, fmt.Errorf("%w: empty recipient", ErrInvalid)
	}
	if strings.TrimSpace(m.Body) == "" {
		return Message{}, fmt.Errorf("%w: empty body", ErrInvalid)
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Message{}, fmt.Errorf("mailbox: send: %w", err)
	}
	defer tx.Rollback()

	if m.ReplyTo != nil {
		if _, err := getMessage(ctx, tx, *m.ReplyTo); err != nil {
			if errors.Is(err, ErrNotFound) {
				return Message{}, fmt.Errorf("%w: reply_to #%d does not exist", ErrInvalid, *m.ReplyTo)
			}
			return Message{}, err
		}
	}
	id, err := insert(ctx, tx, m, s.now())
	if err != nil {
		return Message{}, err
	}
	out, err := getMessage(ctx, tx, id)
	if err != nil {
		return Message{}, err
	}
	if err := tx.Commit(); err != nil {
		return Message{}, fmt.Errorf("mailbox: send: %w", err)
	}
	return out, nil
}

func insert(ctx context.Context, tx *sql.Tx, m Message, now time.Time) (int64, error) {
	var replyTo sql.NullInt64
	if m.ReplyTo != nil {
		replyTo = sql.NullInt64{Int64: *m.ReplyTo, Valid: true}
	}
	res, err := tx.ExecContext(ctx,
		`INSERT INTO messages(sender, recipient, subject, body, reply_to, state, attempts, created_at)
		 VALUES (?, ?, ?, ?, ?, 'queued', 0, ?)`,
		m.Sender, m.Recipient, m.Subject, m.Body, replyTo, now.UnixMilli())
	if err != nil {
		return 0, fmt.Errorf("mailbox: insert: %w", err)
	}
	return res.LastInsertId()
}

// Get returns the message with the given id, or ErrNotFound.
func (s *Store) Get(ctx context.Context, id int64) (Message, error) {
	return getMessage(ctx, s.db, id)
}

// Inbox returns the messages addressed to recipient, ordered by id. Unless
// includeRead is set, only unread messages (queued or delivered) are returned.
func (s *Store) Inbox(ctx context.Context, recipient string, includeRead bool) ([]Message, error) {
	if includeRead {
		return s.list(ctx, `SELECT `+selectCols+` FROM messages
			WHERE recipient = ? ORDER BY id`, recipient)
	}
	return s.list(ctx, `SELECT `+selectCols+` FROM messages
		WHERE recipient = ? AND state IN ('queued','delivered') ORDER BY id`, recipient)
}

// Queued returns the queued messages for recipient, ordered by id. It is used
// by the delivery loop.
func (s *Store) Queued(ctx context.Context, recipient string) ([]Message, error) {
	return s.list(ctx, `SELECT `+selectCols+` FROM messages
		WHERE recipient = ? AND state = 'queued' ORDER BY id`, recipient)
}

// QueuedRecipients returns the distinct recipients that have queued messages,
// sorted by name.
func (s *Store) QueuedRecipients(ctx context.Context) ([]string, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT DISTINCT recipient FROM messages WHERE state = 'queued' ORDER BY recipient`)
	if err != nil {
		return nil, fmt.Errorf("mailbox: queued recipients: %w", err)
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var r string
		if err := rows.Scan(&r); err != nil {
			return nil, fmt.Errorf("mailbox: queued recipients: %w", err)
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// MarkDelivered records a delivery attempt: it increments attempts and moves a
// queued message to delivered (setting delivered_at). Messages already past
// queued keep their state, so calling it repeatedly is safe.
func (s *Store) MarkDelivered(ctx context.Context, id int64) error {
	res, err := s.db.ExecContext(ctx,
		`UPDATE messages SET
			attempts = attempts + 1,
			state = CASE WHEN state = 'queued' THEN 'delivered' ELSE state END,
			delivered_at = COALESCE(delivered_at, ?)
		 WHERE id = ?`, s.now().UnixMilli(), id)
	if err != nil {
		return fmt.Errorf("mailbox: mark delivered #%d: %w", id, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("mailbox: mark delivered #%d: %w", id, err)
	}
	if n == 0 {
		return fmt.Errorf("%w: #%d", ErrNotFound, id)
	}
	return nil
}

// MarkRead marks the message as read by recipient and returns it. It returns
// ErrNotFound if the message does not exist and ErrForbidden if it is not
// addressed to recipient. Replied messages stay replied.
func (s *Store) MarkRead(ctx context.Context, recipient string, id int64) (Message, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Message{}, fmt.Errorf("mailbox: mark read: %w", err)
	}
	defer tx.Rollback()

	m, err := getMessage(ctx, tx, id)
	if err != nil {
		return Message{}, err
	}
	if m.Recipient != recipient {
		return Message{}, fmt.Errorf("%w: message #%d is not addressed to %q", ErrForbidden, id, recipient)
	}
	now := s.now().UnixMilli()
	if _, err := tx.ExecContext(ctx,
		`UPDATE messages SET
			state = CASE WHEN state IN ('queued','delivered') THEN 'read' ELSE state END,
			delivered_at = COALESCE(delivered_at, ?),
			read_at = COALESCE(read_at, ?)
		 WHERE id = ?`, now, now, id); err != nil {
		return Message{}, fmt.Errorf("mailbox: mark read #%d: %w", id, err)
	}
	m, err = getMessage(ctx, tx, id)
	if err != nil {
		return Message{}, err
	}
	if err := tx.Commit(); err != nil {
		return Message{}, fmt.Errorf("mailbox: mark read: %w", err)
	}
	return m, nil
}

// replySubjectMax is the number of body characters used as subject when the
// original message has none.
const replySubjectMax = 60

// ReplySubject returns the subject of a reply to orig.
func ReplySubject(orig Message) string {
	subj := strings.TrimSpace(orig.Subject)
	if subj == "" {
		subj = strings.Join(strings.Fields(orig.Body), " ")
		if r := []rune(subj); len(r) > replySubjectMax {
			subj = string(r[:replySubjectMax])
		}
	}
	if strings.HasPrefix(subj, "Re: ") {
		return subj
	}
	return "Re: " + subj
}

// Reply creates a new queued message from `from` to the sender of message id,
// with reply_to set to id, and marks the original as replied, atomically.
// Only the original recipient may reply (ErrForbidden).
func (s *Store) Reply(ctx context.Context, from string, id int64, body string) (Message, error) {
	from = strings.TrimSpace(from)
	if from == "" {
		return Message{}, fmt.Errorf("%w: empty sender", ErrInvalid)
	}
	if strings.TrimSpace(body) == "" {
		return Message{}, fmt.Errorf("%w: empty body", ErrInvalid)
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Message{}, fmt.Errorf("mailbox: reply: %w", err)
	}
	defer tx.Rollback()

	orig, err := getMessage(ctx, tx, id)
	if err != nil {
		return Message{}, err
	}
	if orig.Recipient != from {
		return Message{}, fmt.Errorf("%w: only %q may reply to message #%d", ErrForbidden, orig.Recipient, id)
	}
	now := s.now()
	replyTo := id
	newID, err := insert(ctx, tx, Message{
		Sender:    from,
		Recipient: orig.Sender,
		Subject:   ReplySubject(orig),
		Body:      body,
		ReplyTo:   &replyTo,
	}, now)
	if err != nil {
		return Message{}, err
	}
	ms := now.UnixMilli()
	if _, err := tx.ExecContext(ctx,
		`UPDATE messages SET
			state = 'replied',
			delivered_at = COALESCE(delivered_at, ?),
			read_at = COALESCE(read_at, ?),
			replied_at = COALESCE(replied_at, ?)
		 WHERE id = ?`, ms, ms, ms, id); err != nil {
		return Message{}, fmt.Errorf("mailbox: reply: mark #%d replied: %w", id, err)
	}
	out, err := getMessage(ctx, tx, newID)
	if err != nil {
		return Message{}, err
	}
	if err := tx.Commit(); err != nil {
		return Message{}, fmt.Errorf("mailbox: reply: %w", err)
	}
	return out, nil
}

// UnreadCount returns the number of unread (queued or delivered) messages for
// recipient.
func (s *Store) UnreadCount(ctx context.Context, recipient string) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM messages WHERE recipient = ? AND state IN ('queued','delivered')`,
		recipient).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("mailbox: unread count: %w", err)
	}
	return n, nil
}

// Valid reports whether st is one of the known states.
func (st State) Valid() bool {
	switch st {
	case StateQueued, StateDelivered, StateRead, StateReplied:
		return true
	}
	return false
}

// PurgeOptions selects the messages Purge deletes. All set filters must
// match (AND); zero values do not filter. The zero PurgeOptions deletes every
// message: callers decide whether that is allowed.
type PurgeOptions struct {
	// Before deletes messages created strictly before it (zero = any age).
	Before time.Time
	// Recipient restricts the purge to one mailbox ("" = all mailboxes).
	Recipient string
	// States restricts the purge to these states (empty = any state).
	States []State
	// DryRun counts the matching messages without deleting them.
	DryRun bool
}

// PurgeResult reports what Purge deleted (or would delete, with DryRun).
type PurgeResult struct {
	Count int     `json:"count"`
	IDs   []int64 `json:"ids,omitempty"` // ascending
}

// Purge deletes the messages selected by o in one transaction. Replies to a
// purged message survive with reply_to cleared (ON DELETE SET NULL). Ids are
// never reused: new messages keep counting from the highest id ever issued.
func (s *Store) Purge(ctx context.Context, o PurgeOptions) (PurgeResult, error) {
	var (
		where []string
		args  []any
	)
	if !o.Before.IsZero() {
		where = append(where, "created_at < ?")
		args = append(args, o.Before.UnixMilli())
	}
	if r := strings.TrimSpace(o.Recipient); r != "" {
		where = append(where, "recipient = ?")
		args = append(args, r)
	}
	if len(o.States) > 0 {
		ph := make([]string, len(o.States))
		for i, st := range o.States {
			if !st.Valid() {
				return PurgeResult{}, fmt.Errorf("%w: unknown state %q", ErrInvalid, st)
			}
			ph[i] = "?"
			args = append(args, string(st))
		}
		where = append(where, "state IN ("+strings.Join(ph, ",")+")")
	}
	cond := ""
	if len(where) > 0 {
		cond = " WHERE " + strings.Join(where, " AND ")
	}
	query := `DELETE FROM messages` + cond + ` RETURNING id`
	if o.DryRun {
		query = `SELECT id FROM messages` + cond
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return PurgeResult{}, fmt.Errorf("mailbox: purge: %w", err)
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx, query, args...)
	if err != nil {
		return PurgeResult{}, fmt.Errorf("mailbox: purge: %w", err)
	}
	res := PurgeResult{IDs: []int64{}}
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return PurgeResult{}, fmt.Errorf("mailbox: purge: %w", err)
		}
		res.IDs = append(res.IDs, id)
	}
	if err := rows.Close(); err != nil {
		return PurgeResult{}, fmt.Errorf("mailbox: purge: %w", err)
	}
	if err := rows.Err(); err != nil {
		return PurgeResult{}, fmt.Errorf("mailbox: purge: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return PurgeResult{}, fmt.Errorf("mailbox: purge: %w", err)
	}
	slices.Sort(res.IDs)
	res.Count = len(res.IDs)
	return res, nil
}

// Vacuum rebuilds the database file to return the space freed by purges to
// the filesystem, then truncates the WAL.
func (s *Store) Vacuum(ctx context.Context) error {
	if _, err := s.db.ExecContext(ctx, `VACUUM`); err != nil {
		return fmt.Errorf("mailbox: vacuum: %w", err)
	}
	if _, err := s.db.ExecContext(ctx, `PRAGMA wal_checkpoint(TRUNCATE)`); err != nil {
		return fmt.Errorf("mailbox: vacuum: checkpoint: %w", err)
	}
	return nil
}
