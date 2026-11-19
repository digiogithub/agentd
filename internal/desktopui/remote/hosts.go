package remote

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"

	"github.com/digiogithub/agentd/internal/desktopui/backend"
)

// ErrLocalHost is returned when trying to save over or remove the implicit
// local host.
var ErrLocalHost = errors.New("remote: the local host is implicit and cannot be changed")

// ErrUnknownHost is returned for an id that is not saved.
var ErrUnknownHost = errors.New("remote: unknown host")

// LocalHost is the implicit entry for this computer.
func LocalHost() backend.Host {
	name, err := os.Hostname()
	if err != nil || name == "" {
		name = "This computer"
	}
	return backend.Host{ID: backend.LocalHostID, Name: name}
}

// HostStore is hosts.json: the saved hosts (mode 0600, atomic writes). The
// local host is never stored; List always returns it first.
type HostStore struct {
	path string
	mu   sync.Mutex
}

type hostsFile struct {
	Version int            `json:"version"`
	Hosts   []backend.Host `json:"hosts"`
}

// NewHostStore returns the store at <dir>/hosts.json.
func NewHostStore(dir string) *HostStore {
	return &HostStore{path: filepath.Join(dir, HostsFile)}
}

// Path is the file the store reads and writes.
func (s *HostStore) Path() string { return s.path }

func (s *HostStore) load() ([]backend.Host, error) {
	data, err := os.ReadFile(s.path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("remote: read hosts: %w", err)
	}
	var f hostsFile
	if err := json.Unmarshal(data, &f); err != nil {
		return nil, fmt.Errorf("remote: parse %s: %w", s.path, err)
	}
	out := f.Hosts[:0]
	for _, h := range f.Hosts {
		if h.ID != "" && h.ID != backend.LocalHostID {
			out = append(out, h)
		}
	}
	return out, nil
}

func (s *HostStore) store(hosts []backend.Host) error {
	if hosts == nil {
		hosts = []backend.Host{}
	}
	data, err := json.MarshalIndent(hostsFile{Version: 1, Hosts: hosts}, "", "  ")
	if err != nil {
		return err
	}
	return writeFileAtomic(s.path, append(data, '\n'), 0o600)
}

// List returns the local host then the saved hosts in file order.
func (s *HostStore) List() ([]backend.Host, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	hosts, err := s.load()
	if err != nil {
		return []backend.Host{LocalHost()}, err
	}
	return append([]backend.Host{LocalHost()}, hosts...), nil
}

// Get returns one host (the local one included).
func (s *HostStore) Get(id string) (backend.Host, error) {
	if id == backend.LocalHostID {
		return LocalHost(), nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	hosts, err := s.load()
	if err != nil {
		return backend.Host{}, err
	}
	for _, h := range hosts {
		if h.ID == id {
			return h, nil
		}
	}
	return backend.Host{}, fmt.Errorf("%w %q", ErrUnknownHost, id)
}

var slugRe = regexp.MustCompile(`[^a-z0-9._-]+`)

// Slug turns a name into a host id candidate.
func Slug(name string) string {
	s := strings.Trim(slugRe.ReplaceAllString(strings.ToLower(name), "-"), "-.")
	if s == "" {
		s = "host"
	}
	return s
}

// Save inserts or replaces h (by ID) and returns it as stored. An empty ID
// is derived from Name (or Addr), made unique. The local id is refused.
func (s *HostStore) Save(h backend.Host) (backend.Host, error) {
	if h.ID == backend.LocalHostID {
		return h, ErrLocalHost
	}
	if h.Relay != nil {
		if h.Relay.URL == "" || h.Relay.Route == "" || h.Relay.HostKey == "" {
			return h, errors.New("remote: relay host needs relay url, route and host key")
		}
		if h.Name == "" {
			h.Name = h.Relay.Route
		}
	} else if h.Addr == "" {
		return h, errors.New("remote: host address is required")
	}
	if h.Name == "" {
		h.Name = h.Addr
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	hosts, err := s.load()
	if err != nil {
		return h, err
	}
	if h.ID == "" {
		base := Slug(h.Name)
		if base == backend.LocalHostID {
			base = "local-host"
		}
		id := base
		for n := 2; ; n++ {
			taken := false
			for _, o := range hosts {
				if o.ID == id {
					taken = true
				}
			}
			if !taken {
				break
			}
			id = fmt.Sprintf("%s-%d", base, n)
		}
		h.ID = id
	}
	replaced := false
	for i := range hosts {
		if hosts[i].ID == h.ID {
			hosts[i] = h
			replaced = true
		}
	}
	if !replaced {
		hosts = append(hosts, h)
	}
	return h, s.store(hosts)
}

// Update applies fn to the saved host id and stores the result.
func (s *HostStore) Update(id string, fn func(*backend.Host)) error {
	if id == backend.LocalHostID {
		return ErrLocalHost
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	hosts, err := s.load()
	if err != nil {
		return err
	}
	for i := range hosts {
		if hosts[i].ID == id {
			fn(&hosts[i])
			hosts[i].ID = id
			return s.store(hosts)
		}
	}
	return fmt.Errorf("%w %q", ErrUnknownHost, id)
}

// Remove deletes a saved host (unknown ids are not an error).
func (s *HostStore) Remove(id string) error {
	if id == backend.LocalHostID {
		return ErrLocalHost
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	hosts, err := s.load()
	if err != nil {
		return err
	}
	out := hosts[:0]
	for _, h := range hosts {
		if h.ID != id {
			out = append(out, h)
		}
	}
	return s.store(out)
}
