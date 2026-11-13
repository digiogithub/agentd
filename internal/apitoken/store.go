package apitoken

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"syscall"
	"time"
)

const (
	fileName = "devices.json"
	lockName = "devices.lock"
	// fileVersion is the devices.json schema version.
	fileVersion = 1

	// DefaultIdleExpiry: a device not seen for this long is rejected.
	DefaultIdleExpiry = 90 * 24 * time.Hour
	// DefaultTouchEvery: last_seen is persisted at most this often per device.
	DefaultTouchEvery = time.Minute
	// DefaultReloadEvery: the file is re-stat'ed at most this often per request.
	DefaultReloadEvery = time.Second
	// PollEvery is the interval of Run (revocation and expiry watcher).
	PollEvery = 500 * time.Millisecond
)

// Errors.
var (
	// ErrInvalid: unknown, revoked, expired or wrong token.
	ErrInvalid = errors.New("invalid token")
	// ErrNotFound: no such device.
	ErrNotFound = errors.New("no such device")
	// ErrBroken: the store file is unusable (malformed or unsafe permissions);
	// every token is denied until it is fixed.
	ErrBroken = errors.New("device store unusable")
)

// ctCompare is the comparison used for hashes (a variable so tests can
// observe that the constant-time path is taken).
var ctCompare = subtle.ConstantTimeCompare

// Device is one record of devices.json. The token itself is never stored,
// only the SHA-256 of its secret.
type Device struct {
	ID           string    `json:"id"`
	Name         string    `json:"name"`
	Platform     string    `json:"platform,omitempty"`
	Scope        string    `json:"scope"`
	Hash         string    `json:"hash"`
	CreatedAt    time.Time `json:"created_at"`
	ExpiresAt    time.Time `json:"expires_at,omitzero"`
	LastSeenAt   time.Time `json:"last_seen_at,omitzero"`
	LastSeenAddr string    `json:"last_seen_addr,omitempty"`
	PairedVia    string    `json:"paired_via"` // qr | code | cli
}

type fileData struct {
	Version int      `json:"version"`
	Devices []Device `json:"devices"`
}

// Options configure a Store. Zero values take the defaults.
type Options struct {
	// IdleExpiry rejects devices unseen for this long; 0 = default 90 days,
	// negative = disabled.
	IdleExpiry  time.Duration
	TouchEvery  time.Duration
	ReloadEvery time.Duration
	Clock       func() time.Time
	// Log receives errors and warnings. Tokens are never logged, only ids.
	Log *slog.Logger
}

// Store is the device-token store. Safe for concurrent use; several
// processes (serve, CLI) may use the same directory.
type Store struct {
	dir  string
	opts Options

	fmu sync.Mutex // serialises file mutations inside this process (plus flock across processes)

	mu       sync.Mutex
	devices  map[string]*Device
	broken   error
	lastStat os.FileInfo // nil = file absent
	loaded   bool
	lastChk  time.Time
	active   map[string]bool // ids valid at the last refresh
	hooks    []func(ids []string)
	lastErr  string // last logged load error (log once per distinct error)

	tq touchQueue
}

// touchQueue coalesces last-seen writes: Verify only records the newest touch
// per device and a single background worker persists them, so a request never
// waits for the flock + fsync of devices.json.
type touchQueue struct {
	mu      sync.Mutex
	cond    *sync.Cond
	pending map[string]Device
	running bool
}

const maxPendingTouches = 256

// Open prepares the store directory (created 0700 if missing; an existing one
// must not be group/world accessible) and loads devices.json. A missing file
// is an empty store. A malformed or unsafe file does not fail Open: the store
// then denies every token and Err reports why.
func Open(dir string, o Options) (*Store, error) {
	if o.IdleExpiry == 0 {
		o.IdleExpiry = DefaultIdleExpiry
	}
	if o.TouchEvery <= 0 {
		o.TouchEvery = DefaultTouchEvery
	}
	if o.ReloadEvery <= 0 {
		o.ReloadEvery = DefaultReloadEvery
	}
	if o.Clock == nil {
		o.Clock = time.Now
	}
	if o.Log == nil {
		o.Log = slog.New(slog.DiscardHandler)
	}
	if err := os.MkdirAll(filepath.Dir(dir), 0o700); err != nil {
		return nil, err
	}
	if err := os.Mkdir(dir, 0o700); err != nil && !errors.Is(err, os.ErrExist) {
		return nil, err
	}
	if err := checkPrivate(dir, true); err != nil {
		return nil, err
	}
	s := &Store{dir: dir, opts: o, devices: map[string]*Device{}, active: map[string]bool{}}
	s.refresh(true)
	return s, nil
}

func (s *Store) path() string { return filepath.Join(s.dir, fileName) }

// Path returns the devices.json path.
func (s *Store) Path() string { return s.path() }

// Err reports why the store is unusable (nil when fine).
func (s *Store) Err() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.broken
}

// Subscribe registers fn, called (outside any lock) with the ids that were
// valid and no longer are: revoked, expired, or all of them when the file
// became unusable. Register before Run.
func (s *Store) Subscribe(fn func(ids []string)) {
	s.mu.Lock()
	s.hooks = append(s.hooks, fn)
	s.mu.Unlock()
}

// Run polls the file (and the clock, for expiry) every PollEvery until ctx
// ends, so revocations done by another process reach Subscribe hooks within
// about a second.
func (s *Store) Run(ctx context.Context) {
	t := time.NewTicker(PollEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s.refresh(true)
		}
	}
}

// checkPrivate verifies owner and mode: no group/world access, owned by the
// current user, no symlink.
func checkPrivate(path string, dir bool) error {
	fi, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if dir && !fi.IsDir() {
		return fmt.Errorf("%s is not a directory", path)
	}
	if !dir && !fi.Mode().IsRegular() {
		return fmt.Errorf("%s is not a regular file", path)
	}
	if fi.Mode().Perm()&0o077 != 0 {
		return fmt.Errorf("%s has mode %04o: group/world access is not allowed (chmod %s)", path, fi.Mode().Perm(), map[bool]string{true: "700", false: "600"}[dir])
	}
	if st, ok := fi.Sys().(*syscall.Stat_t); ok && int(st.Uid) != os.Geteuid() {
		return fmt.Errorf("%s is not owned by the current user", path)
	}
	return nil
}

// readFile reads and validates devices.json. A missing file is an empty store.
func (s *Store) readFile() (*fileData, os.FileInfo, error) {
	if err := checkPrivate(s.dir, true); err != nil {
		return nil, nil, fmt.Errorf("%w: %v", ErrBroken, err)
	}
	fi, err := os.Lstat(s.path())
	if errors.Is(err, os.ErrNotExist) {
		return &fileData{Version: fileVersion}, nil, nil
	}
	if err != nil {
		return nil, nil, fmt.Errorf("%w: %v", ErrBroken, err)
	}
	if err := checkPrivate(s.path(), false); err != nil {
		return nil, nil, fmt.Errorf("%w: %v", ErrBroken, err)
	}
	b, err := os.ReadFile(s.path())
	if err != nil {
		return nil, nil, fmt.Errorf("%w: %v", ErrBroken, err)
	}
	var fd fileData
	if err := json.Unmarshal(b, &fd); err != nil {
		return nil, nil, fmt.Errorf("%w: malformed %s: %v", ErrBroken, fileName, err)
	}
	if fd.Version != fileVersion {
		return nil, nil, fmt.Errorf("%w: %s version %d not supported", ErrBroken, fileName, fd.Version)
	}
	seen := map[string]bool{}
	for _, d := range fd.Devices {
		switch {
		case !validID(d.ID), seen[d.ID]:
			return nil, nil, fmt.Errorf("%w: bad or duplicate device id", ErrBroken)
		case !validHash(d.Hash):
			return nil, nil, fmt.Errorf("%w: device %s has a bad hash", ErrBroken, d.ID)
		case validScope(d.Scope) != nil:
			return nil, nil, fmt.Errorf("%w: device %s has a bad scope", ErrBroken, d.ID)
		}
		seen[d.ID] = true
	}
	return &fd, fi, nil
}

func sameFile(a, b os.FileInfo) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return os.SameFile(a, b) && a.ModTime().Equal(b.ModTime()) && a.Size() == b.Size() && a.Mode() == b.Mode()
}

// expired reports whether d may no longer be used at now.
func (s *Store) expired(d *Device, now time.Time) bool {
	if !d.ExpiresAt.IsZero() && !now.Before(d.ExpiresAt) {
		return true
	}
	if s.opts.IdleExpiry > 0 {
		last := d.LastSeenAt
		if last.IsZero() || d.CreatedAt.After(last) {
			last = d.CreatedAt
		}
		if now.Sub(last) > s.opts.IdleExpiry {
			return true
		}
	}
	return false
}

// refresh re-reads the file when it changed (or force-less throttled by the
// caller) and fires hooks for devices that stopped being valid. force skips
// the ReloadEvery throttle.
func (s *Store) refresh(force bool) {
	now := s.opts.Clock()
	s.mu.Lock()
	if !force && s.loaded && now.Sub(s.lastChk) < s.opts.ReloadEvery {
		s.mu.Unlock()
		return
	}
	s.lastChk = now
	fi, serr := os.Lstat(s.path())
	if serr != nil && !errors.Is(serr, os.ErrNotExist) {
		fi = nil
	}
	if !s.loaded || s.broken != nil || !sameFile(fi, s.lastStat) {
		fd, st, err := s.readFile()
		s.loaded = true
		s.lastStat = st
		if err != nil {
			s.devices = map[string]*Device{}
			s.broken = err
			if msg := err.Error(); msg != s.lastErr {
				s.lastErr = msg
				s.opts.Log.Error("device store unusable: denying all tokens", "path", s.path(), "err", err)
			}
		} else {
			s.broken, s.lastErr = nil, ""
			m := make(map[string]*Device, len(fd.Devices))
			for i := range fd.Devices {
				d := fd.Devices[i]
				// Keep the fresher in-memory last-seen.
				if old := s.devices[d.ID]; old != nil && old.LastSeenAt.After(d.LastSeenAt) {
					d.LastSeenAt, d.LastSeenAddr = old.LastSeenAt, old.LastSeenAddr
				}
				m[d.ID] = &d
			}
			s.devices = m
		}
	}
	nowActive := map[string]bool{}
	for id, d := range s.devices {
		if !s.expired(d, now) {
			nowActive[id] = true
		}
	}
	var gone []string
	for id := range s.active {
		if !nowActive[id] {
			gone = append(gone, id)
		}
	}
	s.active = nowActive
	hooks := slices.Clone(s.hooks)
	s.mu.Unlock()
	if len(gone) > 0 {
		slices.Sort(gone)
		for _, h := range hooks {
			h(gone)
		}
	}
}

// IsActive reports whether the device id is currently valid.
func (s *Store) IsActive(id string) bool {
	s.refresh(false)
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.active[id]
}

// Verify checks a presented token: lookup by id, then a constant-time
// comparison of SHA-256(secret) with the stored hash, then expiry. addr is the
// peer address recorded as last seen (host only is kept). The returned Device
// is a copy.
func (s *Store) Verify(token, addr string) (Device, error) {
	id, secret, err := ParseToken(token)
	if err != nil {
		return Device{}, ErrInvalid
	}
	s.refresh(false)
	got := hashSecret(secret)
	now := s.opts.Clock()

	s.mu.Lock()
	d := s.devices[id]
	stored := strings.Repeat("0", 64) // dummy: same work for unknown ids
	if d != nil {
		stored = d.Hash
	}
	ok := ctCompare([]byte(got), []byte(stored)) == 1
	if s.broken != nil || d == nil || !ok || s.expired(d, now) {
		s.mu.Unlock()
		return Device{}, ErrInvalid
	}
	touch := d.LastSeenAt.IsZero() || now.Sub(d.LastSeenAt) >= s.opts.TouchEvery
	if touch {
		d.LastSeenAt, d.LastSeenAddr = now, hostOf(addr)
	}
	out := *d
	s.mu.Unlock()
	if touch {
		s.persistLastSeen(out)
	}
	return out, nil
}

func hostOf(addr string) string {
	if i := strings.LastIndex(addr, ":"); i > 0 {
		addr = addr[:i]
	}
	return strings.Trim(addr, "[]")
}

// persistLastSeen queues the last_seen write of d and returns at once (at
// most once per TouchEvery per device, guaranteed by Verify). A full queue
// drops the touch: the next request after TouchEvery records it again.
func (s *Store) persistLastSeen(d Device) {
	q := &s.tq
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.cond == nil {
		q.cond = sync.NewCond(&q.mu)
		q.pending = map[string]Device{}
	}
	if _, ok := q.pending[d.ID]; !ok && len(q.pending) >= maxPendingTouches {
		return
	}
	q.pending[d.ID] = d
	if !q.running {
		q.running = true
		go s.touchWorker()
	}
}

func (s *Store) touchWorker() {
	q := &s.tq
	for {
		q.mu.Lock()
		if len(q.pending) == 0 {
			q.running = false
			q.cond.Broadcast()
			q.mu.Unlock()
			return
		}
		batch := q.pending
		q.pending = map[string]Device{}
		q.mu.Unlock()
		for _, d := range batch {
			s.persistLastSeenNow(d)
		}
	}
}

// FlushTouches waits until queued last-seen writes reached the file (tests,
// clean shutdown).
func (s *Store) FlushTouches() {
	q := &s.tq
	q.mu.Lock()
	defer q.mu.Unlock()
	for q.cond != nil && (q.running || len(q.pending) > 0) {
		q.cond.Wait()
	}
}

// persistLastSeenNow writes last_seen for d. Only that field is changed, on
// top of the file's current content, so a concurrent revoke is never undone.
func (s *Store) persistLastSeenNow(d Device) {
	err := s.mutate(func(fd *fileData) error {
		for i := range fd.Devices {
			if fd.Devices[i].ID == d.ID {
				fd.Devices[i].LastSeenAt, fd.Devices[i].LastSeenAddr = d.LastSeenAt, d.LastSeenAddr
				return nil
			}
		}
		return errSkip // revoked meanwhile
	})
	if err != nil && !errors.Is(err, errSkip) {
		s.opts.Log.Warn("could not persist last_seen", "device", d.ID, "err", err)
	}
}

var errSkip = errors.New("skip write")

// mutate applies fn to the file content under the process mutex and an
// exclusive flock, writing atomically (temp file + rename, 0600). fn returning
// errSkip writes nothing and returns errSkip.
func (s *Store) mutate(fn func(*fileData) error) error {
	s.fmu.Lock()
	defer s.fmu.Unlock()
	lf, err := os.OpenFile(filepath.Join(s.dir, lockName), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return err
	}
	defer lf.Close()
	if err := syscall.Flock(int(lf.Fd()), syscall.LOCK_EX); err != nil {
		return err
	}
	defer func() { _ = syscall.Flock(int(lf.Fd()), syscall.LOCK_UN) }()

	fd, _, err := s.readFile()
	if err != nil {
		return err
	}
	if err := fn(fd); err != nil {
		return err
	}
	b, err := json.MarshalIndent(fd, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(s.dir, ".devices-*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name()) // no-op after a successful rename
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(append(b, '\n')); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmp.Name(), s.path()); err != nil {
		return err
	}
	if d, err := os.Open(s.dir); err == nil {
		_ = d.Sync()
		_ = d.Close()
	}
	return nil
}

// CreateArgs describe a new device token.
type CreateArgs struct {
	Name      string
	Platform  string
	Scope     string
	TTL       time.Duration // 0 = no absolute expiry
	PairedVia string        // default "cli"
}

// Create adds a device and returns its record and the clear token, which is
// shown once and never stored.
func (s *Store) Create(a CreateArgs) (Device, string, error) {
	if err := validScope(a.Scope); err != nil {
		return Device{}, "", err
	}
	a.Name = strings.TrimSpace(a.Name)
	if a.Name == "" || len(a.Name) > 64 || strings.ContainsFunc(a.Name, func(r rune) bool { return r < 0x20 || r == 0x7f }) {
		return Device{}, "", errors.New("device name must be 1-64 printable characters")
	}
	if a.PairedVia == "" {
		a.PairedVia = "cli"
	}
	var out Device
	var token string
	err := s.mutate(func(fd *fileData) error {
		tok, id, hash, err := newToken()
		if err != nil {
			return err
		}
		for slices.ContainsFunc(fd.Devices, func(d Device) bool { return d.ID == id }) {
			if tok, id, hash, err = newToken(); err != nil {
				return err
			}
		}
		now := s.opts.Clock().UTC()
		out = Device{ID: id, Name: a.Name, Platform: a.Platform, Scope: a.Scope, Hash: hash, CreatedAt: now, PairedVia: a.PairedVia}
		if a.TTL > 0 {
			out.ExpiresAt = now.Add(a.TTL)
		}
		fd.Devices = append(fd.Devices, out)
		token = tok
		return nil
	})
	if err != nil {
		return Device{}, "", err
	}
	s.refresh(true)
	return out, token, nil
}

// List returns the devices as currently stored (fresh read).
func (s *Store) List() ([]Device, error) {
	fd, _, err := s.readFile()
	if err != nil {
		return nil, err
	}
	return fd.Devices, nil
}

// State is "active" or "expired" at the store's clock.
func (s *Store) State(d Device) string {
	if s.expired(&d, s.opts.Clock()) {
		return "expired"
	}
	return "active"
}

// Revoke deletes the device whose id or (unique) name is ref. In this process
// Subscribe hooks fire at once; other processes' gateways notice the file
// change within about a second.
func (s *Store) Revoke(ref string) (Device, error) {
	var out Device
	err := s.mutate(func(fd *fileData) error {
		idx := -1
		for i, d := range fd.Devices {
			if d.ID == ref {
				idx = i
				break
			}
		}
		if idx < 0 {
			var hits []int
			for i, d := range fd.Devices {
				if d.Name == ref {
					hits = append(hits, i)
				}
			}
			if len(hits) > 1 {
				return fmt.Errorf("name %q is ambiguous (%d devices): use the id", ref, len(hits))
			}
			if len(hits) == 1 {
				idx = hits[0]
			}
		}
		if idx < 0 {
			return fmt.Errorf("%w: %s", ErrNotFound, ref)
		}
		out = fd.Devices[idx]
		fd.Devices = slices.Delete(fd.Devices, idx, idx+1)
		return nil
	})
	if err != nil {
		return Device{}, err
	}
	s.refresh(true)
	return out, nil
}
