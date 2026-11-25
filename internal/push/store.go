package push

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"time"
)

// MaxTokens is the most tokens a host keeps (the oldest is evicted).
const MaxTokens = 32

// ErrNotFound is returned when no token matches.
var ErrNotFound = errors.New("push: no such token")

// Entry is a registered device token.
type Entry struct {
	ID        string     `json:"id"`
	Token     string     `json:"token"`
	Platform  string     `json:"platform"`
	Name      string     `json:"name"`
	HostRef   string     `json:"host_ref,omitempty"`
	Device    string     `json:"device,omitempty"`
	Mode      string     `json:"mode,omitempty"`
	CreatedAt time.Time  `json:"created_at"`
	LastOKAt  *time.Time `json:"last_ok_at,omitempty"`
	LastError string     `json:"last_error,omitempty"`
}

// TokenID is "p" + the first 12 hex of SHA-256(token).
func TokenID(token string) string {
	sum := sha256.Sum256([]byte(token))
	return "p" + hex.EncodeToString(sum[:])[:12]
}

// Store keeps the tokens in one JSON file (dir 0700, file 0600, atomic rename).
type Store struct {
	path string
	mu   sync.Mutex
	list []Entry
	now  func() time.Time
}

// OpenStore loads path (missing = empty; an unreadable file is renamed
// ".bad" and the store starts empty).
func OpenStore(path string) (*Store, error) {
	s := &Store{path: path, now: time.Now}
	data, err := os.ReadFile(path)
	switch {
	case errors.Is(err, os.ErrNotExist):
	case err != nil:
		return nil, err
	default:
		if json.Unmarshal(data, &s.list) != nil {
			_ = os.Rename(path, path+".bad")
			s.list = nil
		}
	}
	return s, nil
}

func (s *Store) saveLocked() error {
	dir := filepath.Dir(s.path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(s.list, "", " ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".tokens-*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), s.path)
}

// Upsert registers e (Token, Platform, Name, HostRef, Device, Mode); the same token
// again updates the entry in place (created_at kept). At most MaxTokens: the
// oldest is evicted.
func (s *Store) Upsert(e Entry) (Entry, error) {
	e.ID = TokenID(e.Token)
	s.mu.Lock()
	defer s.mu.Unlock()
	if i := slices.IndexFunc(s.list, func(x Entry) bool { return x.ID == e.ID }); i >= 0 {
		e.CreatedAt, e.LastOKAt, e.LastError = s.list[i].CreatedAt, s.list[i].LastOKAt, s.list[i].LastError
		s.list[i] = e
	} else {
		e.CreatedAt = s.now().UTC()
		s.list = append(s.list, e)
		for len(s.list) > MaxTokens {
			oldest := 0
			for i, x := range s.list {
				if x.CreatedAt.Before(s.list[oldest].CreatedAt) {
					oldest = i
				}
			}
			s.list = slices.Delete(s.list, oldest, oldest+1)
		}
	}
	return e, s.saveLocked()
}

// List returns a copy of every entry (tokens included: for sending only).
func (s *Store) List() []Entry {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.list)
}

// Get returns the entry with the given id.
func (s *Store) Get(id string) (Entry, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if i := slices.IndexFunc(s.list, func(x Entry) bool { return x.ID == id }); i >= 0 {
		return s.list[i], true
	}
	return Entry{}, false
}

func (s *Store) removeWhere(fn func(Entry) bool) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := len(s.list)
	s.list = slices.DeleteFunc(s.list, fn)
	if removed := n - len(s.list); removed > 0 {
		return removed, s.saveLocked()
	}
	return 0, nil
}

// Remove deletes the token with the given id.
func (s *Store) Remove(id string) (int, error) {
	return s.removeWhere(func(e Entry) bool { return e.ID == id })
}

// RemoveDevice deletes every token registered by a relay device.
func (s *Store) RemoveDevice(device string) (int, error) {
	if device == "" {
		return 0, nil
	}
	return s.removeWhere(func(e Entry) bool { return e.Device == device })
}

// MarkOK records a successful send.
func (s *Store) MarkOK(id string) {
	s.mark(id, func(e *Entry) { t := s.now().UTC(); e.LastOKAt, e.LastError = &t, "" })
}

// MarkError records a failed send (msg must not contain secrets).
func (s *Store) MarkError(id, msg string) { s.mark(id, func(e *Entry) { e.LastError = msg }) }

func (s *Store) mark(id string, fn func(*Entry)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if i := slices.IndexFunc(s.list, func(x Entry) bool { return x.ID == id }); i >= 0 {
		fn(&s.list[i])
		_ = s.saveLocked()
	}
}
