package daemon

import (
	"bytes"
	"encoding/json"
	"errors"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"sync"
	"unicode"

	"github.com/digiogithub/agentd/internal/config"
	"github.com/digiogithub/agentd/internal/proto"
)

// Session labels and the document store: generic metadata clients keep in the
// daemon (the Herdr compatibility layer stores workspace layout and pane
// placement here). The daemon never interprets them.

const (
	maxLabels     = 64
	maxLabelKey   = 128
	maxLabelValue = 1024
	// MaxDocBytes caps one document's serialized JSON value.
	MaxDocBytes = 1 << 20
)

var docNameRE = regexp.MustCompile(`^[A-Za-z0-9._-]{1,64}$`)

func validLabelKey(k string) bool {
	if k == "" || len(k) > maxLabelKey {
		return false
	}
	for _, r := range k {
		if r == '=' || unicode.IsSpace(r) || unicode.IsControl(r) {
			return false
		}
	}
	return true
}

func validLabelValue(v string) bool {
	if len(v) > maxLabelValue {
		return false
	}
	for _, r := range v {
		if unicode.IsControl(r) {
			return false
		}
	}
	return true
}

// mergeLabels applies upd to cur (empty value deletes) and returns the
// result, or an invalid error. cur is not modified.
func mergeLabels(cur, upd map[string]string) (map[string]string, error) {
	out := maps.Clone(cur)
	if out == nil {
		out = map[string]string{}
	}
	for k, v := range upd {
		if !validLabelKey(k) {
			return nil, proto.Errorf(proto.CodeInvalid, "label: invalid key %q", k)
		}
		if !validLabelValue(v) {
			return nil, proto.Errorf(proto.CodeInvalid, "label: invalid value for %q", k)
		}
		if v == "" {
			delete(out, k)
		} else {
			out[k] = v
		}
	}
	if len(out) > maxLabels {
		return nil, proto.Errorf(proto.CodeInvalid, "label: at most %d labels", maxLabels)
	}
	return out, nil
}

func (e *entry) labelsCopy() map[string]string {
	e.mu.Lock()
	defer e.mu.Unlock()
	if len(e.labels) == 0 {
		return nil
	}
	return maps.Clone(e.labels)
}

// label serves op label: humans may label any session, a session only itself
// unless [security] agent_type (same rule as type).
func (s *Server) label(who caller, a proto.LabelArgs) (proto.LabelResult, error) {
	if who.session && who.id != a.Name && !s.Cfg.Security.AgentType {
		return proto.LabelResult{}, proto.Errorf(proto.CodeForbidden,
			"label: session %q may not label session %q (set [security] agent_type = true to allow)", who.id, a.Name)
	}
	e, err := s.lookup(a.Name)
	if err != nil {
		return proto.LabelResult{}, err
	}
	e.mu.Lock()
	next, err := mergeLabels(e.labels, a.Labels)
	if err != nil {
		e.mu.Unlock()
		return proto.LabelResult{}, err
	}
	changed := !maps.Equal(next, e.labels) && !(len(next) == 0 && len(e.labels) == 0)
	e.labels = next
	out := maps.Clone(next)
	// Published under e.mu so concurrent label ops publish in apply order.
	if changed {
		s.publish(proto.EvSessionLabels, a.Name, proto.LabelsData{Labels: maps.Clone(next)})
	}
	e.mu.Unlock()
	return proto.LabelResult{Labels: out}, nil
}

// ---- documents ----

// docStore keeps versioned opaque JSON documents in one file, written
// atomically on every change. A broken file is renamed to .bad and the store
// starts empty (documents are never worth making the daemon unusable).
type docStore struct {
	mu     sync.Mutex
	path   string
	loaded bool
	docs   map[string]storedDoc
	logf   func(msg string, args ...any)
}

type storedDoc struct {
	Value   json.RawMessage `json:"value"`
	Version uint64          `json:"version"`
}

func (d *docStore) loadLocked() {
	if d.loaded {
		return
	}
	d.loaded = true
	d.docs = map[string]storedDoc{}
	if d.path == "" {
		if p, err := config.DocsPath(); err == nil {
			d.path = p
		}
	}
	b, err := os.ReadFile(d.path)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) && d.logf != nil {
			d.logf("doc store unreadable", "path", d.path, "err", err)
		}
		return
	}
	if err := json.Unmarshal(b, &d.docs); err != nil {
		d.docs = map[string]storedDoc{}
		_ = os.Rename(d.path, d.path+".bad")
		if d.logf != nil {
			d.logf("doc store broken, renamed to .bad; starting empty", "path", d.path, "err", err)
		}
	}
}

func (d *docStore) saveLocked() error {
	b, err := json.Marshal(d.docs)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(d.path), 0o700); err != nil {
		return err
	}
	tmp := d.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, d.path)
}

func validDocName(name string) error {
	if !docNameRE.MatchString(name) {
		return proto.Errorf(proto.CodeInvalid, "doc: invalid name %q (1-64 of A-Z a-z 0-9 . _ -)", name)
	}
	return nil
}

func isNull(v json.RawMessage) bool {
	t := bytes.TrimSpace(v)
	return len(t) == 0 || string(t) == "null"
}

func (s *Server) docGet(a proto.DocGetArgs) (proto.DocGetResult, error) {
	if err := validDocName(a.Name); err != nil {
		return proto.DocGetResult{}, err
	}
	d := s.docs
	d.mu.Lock()
	defer d.mu.Unlock()
	d.loadLocked()
	doc := d.docs[a.Name]
	v := doc.Value
	if isNull(v) {
		v = json.RawMessage("null")
	}
	return proto.DocGetResult{Value: v, Version: doc.Version}, nil
}

// docSet serves op doc_set: humans only (sessions may read documents).
func (s *Server) docSet(who caller, a proto.DocSetArgs) (proto.DocSetResult, error) {
	if who.session {
		return proto.DocSetResult{}, proto.Errorf(proto.CodeForbidden, "doc_set: sessions may not write documents")
	}
	if err := validDocName(a.Name); err != nil {
		return proto.DocSetResult{}, err
	}
	if len(a.Value) > MaxDocBytes {
		return proto.DocSetResult{}, proto.Errorf(proto.CodeInvalid, "doc_set: value larger than %d bytes", MaxDocBytes)
	}
	if !isNull(a.Value) && !json.Valid(a.Value) {
		return proto.DocSetResult{}, proto.Errorf(proto.CodeInvalid, "doc_set: value is not JSON")
	}
	d := s.docs
	d.mu.Lock()
	defer d.mu.Unlock()
	d.loadLocked()
	cur := d.docs[a.Name]
	if a.Version != cur.Version {
		return proto.DocSetResult{}, proto.Errorf(proto.CodeConflict,
			"doc_set: document %q is at version %d (read it again before writing)", a.Name, cur.Version)
	}
	next := storedDoc{Version: cur.Version + 1}
	if !isNull(a.Value) {
		var buf bytes.Buffer
		if err := json.Compact(&buf, a.Value); err != nil {
			return proto.DocSetResult{}, proto.Errorf(proto.CodeInvalid, "doc_set: %v", err)
		}
		next.Value = buf.Bytes()
	}
	d.docs[a.Name] = next
	if err := d.saveLocked(); err != nil {
		d.docs[a.Name] = cur
		return proto.DocSetResult{}, proto.Errorf(proto.CodeInternal, "doc_set: save: %v", err)
	}
	s.publish(proto.EvDocChanged, "", proto.DocChangedData{Name: a.Name, Version: next.Version})
	return proto.DocSetResult{Version: next.Version}, nil
}
