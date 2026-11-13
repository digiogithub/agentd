package apitoken

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
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

// Pairing (docs/design/api/security-remote.md 3): `agentd serve pair` (CLI
// process) and the gateway cooperate through <dir>/pairing.json (0600, flock
// pairing.lock). The file holds only salted hashes of the one-time codes; a
// clear token is never written to disk (the gateway creates the device in
// devices.json when the host approves, and hands the token to the waiting
// HTTP request).
const (
	pairFile = "pairing.json"
	pairLock = "pairing.lock"

	pairFileVersion = 1
	// CodeLen is the number of Crockford base32 symbols of a code (60 bits).
	CodeLen = 12

	// DefaultPairTTL and MaxPairTTL bound the life of a code.
	DefaultPairTTL = 5 * time.Minute
	MaxPairTTL     = 30 * time.Minute
	// MaxPending is the number of live codes; a new one evicts the oldest.
	MaxPending = 3
	// MaxAttempts failed claims burn a code.
	MaxAttempts = 5
	// pairKeep is how long finished records stay (so the CLI sees the outcome).
	pairKeep = time.Minute
)

// Record states.
const (
	PairPending  = "pending"  // issued, waiting for a device
	PairClaimed  = "claimed"  // a device presented the code; waiting for the host
	PairApproved = "approved" // host said yes (or --no-approve); gateway may issue the token
	PairDenied   = "denied"
	PairDone     = "done"   // token issued
	PairBurned   = "burned" // attempts exhausted, abandoned or cancelled
)

// Pairing errors.
var (
	// ErrBadCode: unknown, malformed, expired, used or burned code. The
	// reason is deliberately not distinguished.
	ErrBadCode = errors.New("invalid pairing code")
	// ErrPairGone: no such pairing record (or it is not in the expected state).
	ErrPairGone = errors.New("pairing not found")
)

// Claim is what the device told the gateway.
type Claim struct {
	Name     string    `json:"name"`
	Platform string    `json:"platform,omitempty"`
	Addr     string    `json:"addr,omitempty"`
	At       time.Time `json:"at"`
}

// Pending is one pairing record.
type Pending struct {
	ID        string    `json:"id"`
	Salt      string    `json:"salt"`
	Hash      string    `json:"hash"` // hex sha256(salt || normalized code)
	Scope     string    `json:"scope"`
	NameHint  string    `json:"name_hint,omitempty"`
	NoApprove bool      `json:"no_approve,omitempty"`
	CreatedAt time.Time `json:"created_at"`
	ExpiresAt time.Time `json:"expires_at"`
	Attempts  int       `json:"attempts"`
	State     string    `json:"state"`
	StateAt   time.Time `json:"state_at"`
	Device    *Claim    `json:"device,omitempty"`
	DeviceID  string    `json:"device_id,omitempty"` // set when done
}

type pairData struct {
	Version int       `json:"version"`
	Pending []Pending `json:"pending"`
}

// PairOptions configure a Pairing. Zero values take the defaults.
type PairOptions struct {
	Clock func() time.Time
	Log   *slog.Logger
}

// Pairing is the pending-code store; safe for concurrent use and shared
// between processes through the directory.
type Pairing struct {
	dir  string
	opts PairOptions
	fmu  sync.Mutex
}

// OpenPairing prepares dir (0700, like the device store) and returns the store.
func OpenPairing(dir string, o PairOptions) (*Pairing, error) {
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
	return &Pairing{dir: dir, opts: o}, nil
}

func (p *Pairing) path() string { return filepath.Join(p.dir, pairFile) }

func (p *Pairing) now() time.Time { return p.opts.Clock().UTC() }

// readData reads pairing.json (missing = empty), refusing unsafe files.
func (p *Pairing) readData() (*pairData, error) {
	if err := checkPrivate(p.dir, true); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrBroken, err)
	}
	if _, err := os.Lstat(p.path()); errors.Is(err, os.ErrNotExist) {
		return &pairData{Version: pairFileVersion}, nil
	}
	if err := checkPrivate(p.path(), false); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrBroken, err)
	}
	b, err := os.ReadFile(p.path())
	if err != nil {
		return nil, err
	}
	var pd pairData
	if err := json.Unmarshal(b, &pd); err != nil {
		return nil, fmt.Errorf("%w: pairing.json: %v", ErrBroken, err)
	}
	if pd.Version != pairFileVersion {
		return nil, fmt.Errorf("%w: pairing.json version %d", ErrBroken, pd.Version)
	}
	return &pd, nil
}

func (p *Pairing) mutate(fn func(*pairData) error) error {
	p.fmu.Lock()
	defer p.fmu.Unlock()
	lf, err := os.OpenFile(filepath.Join(p.dir, pairLock), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return err
	}
	defer lf.Close()
	if err := syscall.Flock(int(lf.Fd()), syscall.LOCK_EX); err != nil {
		return err
	}
	defer func() { _ = syscall.Flock(int(lf.Fd()), syscall.LOCK_UN) }()
	pd, err := p.readData()
	if err != nil {
		return err
	}
	p.prune(pd)
	if err := fn(pd); err != nil {
		if errors.Is(err, errSkip) {
			return nil
		}
		// A failed claim still persists its attempt counters: fn reports
		// those through errPersist.
		var pe persistErr
		if !errors.As(err, &pe) {
			return err
		}
		if werr := p.write(pd); werr != nil {
			return werr
		}
		return pe.err
	}
	return p.write(pd)
}

// persistErr makes mutate write the file and then return err.
type persistErr struct{ err error }

func (e persistErr) Error() string { return e.err.Error() }

func (p *Pairing) write(pd *pairData) error {
	b, err := json.MarshalIndent(pd, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(p.dir, ".pairing-*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
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
	return os.Rename(tmp.Name(), p.path())
}

// prune expires live records and drops old finished ones.
func (p *Pairing) prune(pd *pairData) {
	now := p.now()
	pd.Pending = slices.DeleteFunc(pd.Pending, func(r Pending) bool {
		return !now.Before(r.ExpiresAt.Add(pairKeep)) || (finished(r.State) && !now.Before(r.StateAt.Add(pairKeep)))
	})
	for i := range pd.Pending {
		r := &pd.Pending[i]
		if live(r.State) && !now.Before(r.ExpiresAt) {
			r.State, r.StateAt = PairBurned, now
			r.Device = nil
		}
	}
}

func live(s string) bool     { return s == PairPending || s == PairClaimed || s == PairApproved }
func finished(s string) bool { return !live(s) }

// NormalizeCode maps user input to the canonical 12 symbols: upper case,
// separators dropped, Crockford aliases (I,L -> 1; O -> 0). ok is false for
// anything that is not exactly CodeLen valid symbols.
func NormalizeCode(in string) (string, bool) {
	var b strings.Builder
	for _, r := range strings.ToUpper(in) {
		switch {
		case r == '-' || r == ' ':
			continue
		case r == 'I' || r == 'L':
			r = '1'
		case r == 'O':
			r = '0'
		}
		if r > 0x7f || !strings.ContainsRune(crockford, r) {
			return "", false
		}
		b.WriteRune(r)
	}
	s := b.String()
	return s, len(s) == CodeLen
}

// FormatCode groups a canonical code as XXXX-XXXX-XXXX.
func FormatCode(c string) string {
	if len(c) != CodeLen {
		return c
	}
	return c[0:4] + "-" + c[4:8] + "-" + c[8:12]
}

func newCode() (string, error) {
	var b [CodeLen]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	out := make([]byte, CodeLen)
	for i, x := range b {
		out[i] = crockford[x&31]
	}
	return string(out), nil
}

func codeHash(salt, code string) string {
	h := sha256.Sum256([]byte(salt + code))
	return hex.EncodeToString(h[:])
}

func randHex(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// IssueArgs describe a new code.
type IssueArgs struct {
	Scope     string        // default write
	NameHint  string        // suggested device name (display only)
	TTL       time.Duration // default DefaultPairTTL, max MaxPairTTL
	NoApprove bool          // skip the host confirmation
}

// Issue creates a one-time code. The clear code is returned once (formatted
// XXXX-XXXX-XXXX) and only its salted hash is stored. More than MaxPending
// live codes evict the oldest.
func (p *Pairing) Issue(a IssueArgs) (code, id string, exp time.Time, err error) {
	if a.Scope == "" {
		a.Scope = ScopeWrite
	}
	if err = validScope(a.Scope); err != nil {
		return
	}
	if a.TTL <= 0 {
		a.TTL = DefaultPairTTL
	}
	if a.TTL > MaxPairTTL {
		err = fmt.Errorf("--ttl is capped at %s", MaxPairTTL)
		return
	}
	raw, err := newCode()
	if err != nil {
		return
	}
	salt, err := randHex(16)
	if err != nil {
		return
	}
	if id, err = randHex(6); err != nil {
		return
	}
	err = p.mutate(func(pd *pairData) error {
		now := p.now()
		var liveIdx []int
		for i, r := range pd.Pending {
			if live(r.State) {
				liveIdx = append(liveIdx, i)
			}
		}
		for len(liveIdx) >= MaxPending { // oldest first (append order)
			r := &pd.Pending[liveIdx[0]]
			r.State, r.StateAt, r.Device = PairBurned, now, nil
			liveIdx = liveIdx[1:]
		}
		exp = now.Add(a.TTL)
		pd.Pending = append(pd.Pending, Pending{
			ID: id, Salt: salt, Hash: codeHash(salt, raw), Scope: a.Scope, NameHint: a.NameHint,
			NoApprove: a.NoApprove, CreatedAt: now, ExpiresAt: exp, State: PairPending, StateAt: now,
		})
		return nil
	})
	if err != nil {
		return "", "", time.Time{}, err
	}
	return FormatCode(raw), id, exp, nil
}

// ClaimCode presents a code. On success the matching record becomes claimed
// (approved at once when it was issued with NoApprove) and is returned. A
// wrong, malformed, expired, used or burned code returns ErrBadCode and counts
// one failed attempt against every live pending code, burning those that reach
// MaxAttempts (the code is unknown on failure, so the attempt cannot be
// attributed). Every record is compared in constant time without early exit.
func (p *Pairing) ClaimCode(input string, c Claim) (Pending, error) {
	var out Pending
	err := p.mutate(func(pd *pairData) error {
		now := p.now()
		code, ok := NormalizeCode(input)
		match := -1
		for i := range pd.Pending {
			r := &pd.Pending[i]
			h := codeHash(r.Salt, code)
			eq := subtle.ConstantTimeCompare([]byte(h), []byte(r.Hash)) == 1
			if ok && eq && r.State == PairPending && now.Before(r.ExpiresAt) && r.Attempts < MaxAttempts && match < 0 {
				match = i
			}
		}
		if match >= 0 {
			r := &pd.Pending[match]
			c.At = now
			r.Device = &c
			r.State, r.StateAt = PairClaimed, now
			if r.NoApprove {
				r.State = PairApproved
			}
			out = *r
			return nil
		}
		for i := range pd.Pending {
			r := &pd.Pending[i]
			if r.State != PairPending {
				continue
			}
			r.Attempts++
			if r.Attempts >= MaxAttempts {
				r.State, r.StateAt = PairBurned, now
			}
		}
		return persistErr{ErrBadCode}
	})
	if err != nil {
		return Pending{}, err
	}
	return out, nil
}

// Get returns a fresh copy of the record (expired live ones show as burned).
func (p *Pairing) Get(id string) (Pending, error) {
	pd, err := p.readData()
	if err != nil {
		return Pending{}, err
	}
	now := p.now()
	for _, r := range pd.Pending {
		if r.ID == id {
			if live(r.State) && !now.Before(r.ExpiresAt) {
				r.State = PairBurned
			}
			return r, nil
		}
	}
	return Pending{}, ErrPairGone
}

// Decide records the host's answer for a claimed code.
func (p *Pairing) Decide(id string, approve bool) error {
	return p.transition(id, func(r *Pending) bool { return r.State == PairClaimed }, map[bool]string{true: PairApproved, false: PairDenied}[approve])
}

// Cancel burns a live record (host Ctrl-C, abandoned claim).
func (p *Pairing) Cancel(id string) error {
	err := p.transition(id, func(r *Pending) bool { return live(r.State) }, PairBurned)
	if errors.Is(err, ErrPairGone) {
		return nil
	}
	return err
}

// Take atomically moves an approved record to done, so exactly one caller
// issues the token. The caller then creates the device.
func (p *Pairing) Take(id string) (Pending, error) {
	var out Pending
	err := p.mutate(func(pd *pairData) error {
		for i := range pd.Pending {
			r := &pd.Pending[i]
			if r.ID == id && r.State == PairApproved {
				r.State, r.StateAt = PairDone, p.now()
				out = *r
				return nil
			}
		}
		return errSkip
	})
	if err != nil {
		return Pending{}, err
	}
	if out.ID == "" {
		return Pending{}, ErrPairGone
	}
	return out, nil
}

// SetDevice records the id of the device issued for a done record (display).
func (p *Pairing) SetDevice(id, deviceID string) error {
	return p.mutate(func(pd *pairData) error {
		for i := range pd.Pending {
			if pd.Pending[i].ID == id {
				pd.Pending[i].DeviceID = deviceID
				return nil
			}
		}
		return errSkip
	})
}

func (p *Pairing) transition(id string, from func(*Pending) bool, to string) error {
	found := false
	err := p.mutate(func(pd *pairData) error {
		for i := range pd.Pending {
			r := &pd.Pending[i]
			if r.ID == id && from(r) {
				r.State, r.StateAt = to, p.now()
				found = true
				return nil
			}
		}
		return errSkip
	})
	if err != nil {
		return err
	}
	if !found {
		return ErrPairGone
	}
	return nil
}
