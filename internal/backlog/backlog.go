// Package backlog is the persistent store of backlog notes (plan
// docs/plans/agentd/backlog-notes_plan.md): ideas and future tasks the human
// writes down on a host and later assigns to a session. It is backed by
// SQLite (modernc.org/sqlite, WAL mode, like internal/mailbox) and is safe for
// concurrent use. Text sanitizing and identity rules live in the daemon.
package backlog

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"os"
	"strings"
	"time"

	_ "modernc.org/sqlite" // registers the "sqlite" driver

	"github.com/digiogithub/agentd/internal/proto"
)

// Sentinel errors. Use errors.Is to test for them.
var (
	ErrNotFound = errors.New("backlog: note not found")
	ErrInvalid  = errors.New("backlog: invalid request")
	// ErrFull is returned by Add when proto.MaxNotes notes exist.
	ErrFull = errors.New("backlog: too many notes")
)

// Store is a SQLite-backed backlog.
type Store struct {
	db  *sql.DB
	now func() time.Time
}

const busyTimeoutMs = 10000

// Open opens (creating if needed, mode 0600) the database at path and applies
// pending schema migrations.
func Open(path string) (*Store, error) {
	if path == "" {
		return nil, fmt.Errorf("%w: empty database path", ErrInvalid)
	}
	if f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600); err == nil {
		_ = f.Close()
	}
	q := url.Values{}
	q.Add("_pragma", "journal_mode(WAL)")
	q.Add("_pragma", fmt.Sprintf("busy_timeout(%d)", busyTimeoutMs))
	q.Add("_pragma", "synchronous(NORMAL)")
	q.Set("_txlock", "immediate")
	db, err := sql.Open("sqlite", "file:"+path+"?"+q.Encode())
	if err != nil {
		return nil, fmt.Errorf("backlog: open %s: %w", path, err)
	}
	s := &Store{db: db, now: time.Now}
	if err := s.migrate(context.Background()); err != nil {
		_ = db.Close()
		return nil, err
	}
	return s, nil
}

// Close closes the underlying database.
func (s *Store) Close() error { return s.db.Close() }

// migrations are applied in order; index i brings the schema to version i+1.
// Never edit an existing entry: append a new one.
var migrations = []string{
	`CREATE TABLE notes (
		id          INTEGER PRIMARY KEY AUTOINCREMENT,
		title       TEXT    NOT NULL,
		body        TEXT    NOT NULL DEFAULT '',
		state       TEXT    NOT NULL DEFAULT 'open' CHECK (state IN ('open','assigned','done')),
		assigned_to TEXT    NOT NULL DEFAULT '',
		task_id     TEXT    NOT NULL DEFAULT '',
		created_at  INTEGER NOT NULL,
		updated_at  INTEGER NOT NULL,
		assigned_at INTEGER,
		done_at     INTEGER
	);
	CREATE INDEX notes_task ON notes(assigned_to, task_id) WHERE state = 'assigned';`,
}

func (s *Store) migrate(ctx context.Context) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("backlog: migrate: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (
		version    INTEGER PRIMARY KEY,
		applied_at INTEGER NOT NULL
	)`); err != nil {
		return fmt.Errorf("backlog: migrate: %w", err)
	}
	var current int
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(version), 0) FROM schema_migrations`).Scan(&current); err != nil {
		return fmt.Errorf("backlog: migrate: %w", err)
	}
	if current > len(migrations) {
		return fmt.Errorf("backlog: database schema version %d is newer than supported %d", current, len(migrations))
	}
	for v := current; v < len(migrations); v++ {
		if _, err := tx.ExecContext(ctx, migrations[v]); err != nil {
			return fmt.Errorf("backlog: migration %d: %w", v+1, err)
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO schema_migrations(version, applied_at) VALUES (?, ?)`,
			v+1, s.now().UnixMilli()); err != nil {
			return fmt.Errorf("backlog: migration %d: %w", v+1, err)
		}
	}
	return tx.Commit()
}

const cols = `id, title, body, state, assigned_to, task_id, created_at, updated_at, assigned_at, done_at`

type scanner interface{ Scan(dest ...any) error }

func ms(t time.Time) int64 { return t.UnixMilli() }

func tm(v int64) time.Time { return time.UnixMilli(v).UTC() }

func tmp(v sql.NullInt64) *time.Time {
	if !v.Valid {
		return nil
	}
	t := tm(v.Int64)
	return &t
}

func scan(r scanner) (proto.Note, error) {
	var n proto.Note
	var created, updated int64
	var assigned, done sql.NullInt64
	if err := r.Scan(&n.ID, &n.Title, &n.Body, &n.State, &n.AssignedTo, &n.TaskID,
		&created, &updated, &assigned, &done); err != nil {
		return proto.Note{}, err
	}
	n.CreatedAt, n.UpdatedAt = tm(created), tm(updated)
	n.AssignedAt, n.DoneAt = tmp(assigned), tmp(done)
	return n, nil
}

type querier interface {
	QueryRowContext(ctx context.Context, q string, args ...any) *sql.Row
}

func get(ctx context.Context, q querier, id int64) (proto.Note, error) {
	n, err := scan(q.QueryRowContext(ctx, `SELECT `+cols+` FROM notes WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return proto.Note{}, fmt.Errorf("%w: #%d", ErrNotFound, id)
	}
	return n, err
}

// Get returns one note.
func (s *Store) Get(ctx context.Context, id int64) (proto.Note, error) {
	return get(ctx, s.db, id)
}

// Add stores a new open note. At most proto.MaxNotes notes exist.
func (s *Store) Add(ctx context.Context, title, body string) (proto.Note, error) {
	if strings.TrimSpace(title) == "" {
		return proto.Note{}, fmt.Errorf("%w: empty title", ErrInvalid)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return proto.Note{}, err
	}
	defer func() { _ = tx.Rollback() }()
	var count int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM notes`).Scan(&count); err != nil {
		return proto.Note{}, err
	}
	if count >= proto.MaxNotes {
		return proto.Note{}, fmt.Errorf("%w (at most %d)", ErrFull, proto.MaxNotes)
	}
	now := ms(s.now())
	res, err := tx.ExecContext(ctx, `INSERT INTO notes(title, body, state, created_at, updated_at)
		VALUES (?, ?, 'open', ?, ?)`, title, body, now, now)
	if err != nil {
		return proto.Note{}, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return proto.Note{}, err
	}
	n, err := get(ctx, tx, id)
	if err != nil {
		return proto.Note{}, err
	}
	return n, tx.Commit()
}

// Update changes title and/or body (nil = unchanged); reopen also takes an
// assigned or done note back to open (clearing the assignment).
func (s *Store) Update(ctx context.Context, id int64, title, body *string, reopen bool) (proto.Note, error) {
	if title != nil && strings.TrimSpace(*title) == "" {
		return proto.Note{}, fmt.Errorf("%w: empty title", ErrInvalid)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return proto.Note{}, err
	}
	defer func() { _ = tx.Rollback() }()
	n, err := get(ctx, tx, id)
	if err != nil {
		return proto.Note{}, err
	}
	if title != nil {
		n.Title = *title
	}
	if body != nil {
		n.Body = *body
	}
	if reopen {
		n.State, n.AssignedTo, n.TaskID, n.AssignedAt, n.DoneAt = proto.NoteOpen, "", "", nil, nil
	}
	if _, err := tx.ExecContext(ctx, `UPDATE notes SET title = ?, body = ?, state = ?, assigned_to = ?, task_id = ?,
		assigned_at = ?, done_at = ?, updated_at = ? WHERE id = ?`,
		n.Title, n.Body, n.State, n.AssignedTo, n.TaskID, nullTime(n.AssignedAt), nullTime(n.DoneAt), ms(s.now()), id); err != nil {
		return proto.Note{}, err
	}
	n, err = get(ctx, tx, id)
	if err != nil {
		return proto.Note{}, err
	}
	return n, tx.Commit()
}

func nullTime(t *time.Time) any {
	if t == nil {
		return nil
	}
	return ms(*t)
}

// Remove deletes a note and returns it.
func (s *Store) Remove(ctx context.Context, id int64) (proto.Note, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return proto.Note{}, err
	}
	defer func() { _ = tx.Rollback() }()
	n, err := get(ctx, tx, id)
	if err != nil {
		return proto.Note{}, err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM notes WHERE id = ?`, id); err != nil {
		return proto.Note{}, err
	}
	return n, tx.Commit()
}

// Assign marks an open or assigned note as assigned to session as task taskID
// at now (a done note is ErrInvalid).
func (s *Store) Assign(ctx context.Context, id int64, session, taskID string, now time.Time) (proto.Note, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return proto.Note{}, err
	}
	defer func() { _ = tx.Rollback() }()
	n, err := get(ctx, tx, id)
	if err != nil {
		return proto.Note{}, err
	}
	if n.State == proto.NoteDone {
		return proto.Note{}, fmt.Errorf("%w: note #%d is done (reopen it first)", ErrInvalid, id)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE notes SET state = 'assigned', assigned_to = ?, task_id = ?,
		assigned_at = ?, done_at = NULL, updated_at = ? WHERE id = ?`,
		session, taskID, ms(now), ms(now), id); err != nil {
		return proto.Note{}, err
	}
	n, err = get(ctx, tx, id)
	if err != nil {
		return proto.Note{}, err
	}
	return n, tx.Commit()
}

// MarkDone marks the assigned notes whose assignment is (session, taskID) as
// done at now and returns them (none when nothing matches).
func (s *Store) MarkDone(ctx context.Context, session, taskID string, now time.Time) ([]proto.Note, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	rows, err := tx.QueryContext(ctx, `SELECT id FROM notes WHERE state = 'assigned' AND assigned_to = ? AND task_id = ?`,
		session, taskID)
	if err != nil {
		return nil, err
	}
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			_ = rows.Close()
			return nil, err
		}
		ids = append(ids, id)
	}
	_ = rows.Close()
	var out []proto.Note
	for _, id := range ids {
		if _, err := tx.ExecContext(ctx, `UPDATE notes SET state = 'done', done_at = ?, updated_at = ? WHERE id = ?`,
			ms(now), ms(now), id); err != nil {
			return nil, err
		}
		n, err := get(ctx, tx, id)
		if err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, tx.Commit()
}

const orderBy = ` ORDER BY CASE state WHEN 'open' THEN 0 WHEN 'assigned' THEN 1 ELSE 2 END,
	CASE state WHEN 'open' THEN created_at WHEN 'assigned' THEN COALESCE(assigned_at, 0) ELSE COALESCE(done_at, 0) END DESC,
	id DESC`

// List returns the notes in the given states (empty = all): open newest
// first, then assigned (most recently assigned first), then done (most
// recently done first).
func (s *Store) List(ctx context.Context, states []string) ([]proto.Note, error) {
	q := `SELECT ` + cols + ` FROM notes`
	var args []any
	if len(states) > 0 {
		q += ` WHERE state IN (?` + strings.Repeat(",?", len(states)-1) + `)`
		for _, st := range states {
			args = append(args, st)
		}
	}
	rows, err := s.db.QueryContext(ctx, q+orderBy, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := []proto.Note{}
	for rows.Next() {
		n, err := scan(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

// Digest returns "d" + 16 hex digits of SHA-256 over (id, state, updated_at)
// of the listing, in order.
func Digest(notes []proto.Note) string {
	h := sha256.New()
	for _, n := range notes {
		fmt.Fprintf(h, "%d:%s:%d;", n.ID, n.State, n.UpdatedAt.UnixMilli())
	}
	return "d" + hex.EncodeToString(h.Sum(nil))[:16]
}
