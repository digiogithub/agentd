// Package screenfix loads recorded terminal captures ("fixtures") and replays
// them into a headless x/vt emulator to obtain a *screen.Snapshot, the input
// of the screen interpretation layers (docs/design/api/semantic-model.md §8).
//
// A fixture is two files in a directory (default internal/screen/testdata):
//
//	<name>.log   raw PTY output bytes, exactly as the child wrote them
//	<name>.json  Meta: cols, rows, profile, description, optional frame offsets
//
// Replay is deterministic: no clocks are read, so every timestamp of the
// Snapshot is zero and only terminal state is filled. Goldens
// (<name>.golden.json next to the fixture) are compared with Golden and
// rewritten with `go test -update`.
//
// Fixtures are never captured from real agent CLIs by this package; the
// recorder in ./record drives internal/testutil/fakeagent. Captures of real
// agents (see docs/ghost-text.md) must be scrubbed of paths, user names and
// secrets before they are committed.
package screenfix

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/digiogithub/agentd/internal/screen"
	"github.com/digiogithub/agentd/internal/session"
)

// Meta is the sidecar <name>.json of a fixture.
type Meta struct {
	Cols        int    `json:"cols"`
	Rows        int    `json:"rows"`
	Profile     string `json:"profile"`
	Description string `json:"description"`
	// Frames are optional byte offsets into the log at which a "frame"
	// ended (e.g. after each recorded step); ReplayFrames snapshots there.
	Frames []int `json:"frames,omitempty"`
	// Cmd documents how the capture was made (informational).
	Cmd []string `json:"cmd,omitempty"`
}

// Fixture is a loaded capture.
type Fixture struct {
	Name string
	Meta Meta
	Log  []byte
}

// Dir returns the default fixture directory, internal/screen/testdata,
// resolved from this source file so it works from any package's tests.
func Dir() string {
	_, file, _, _ := runtime.Caller(0)
	return filepath.Join(filepath.Dir(file), "..", "..", "screen", "testdata")
}

// Load reads a fixture from the default directory.
func Load(name string) (*Fixture, error) { return LoadFrom(Dir(), name) }

// LoadFrom reads <dir>/<name>.log and <dir>/<name>.json.
func LoadFrom(dir, name string) (*Fixture, error) {
	log, err := os.ReadFile(filepath.Join(dir, name+".log"))
	if err != nil {
		return nil, err
	}
	raw, err := os.ReadFile(filepath.Join(dir, name+".json"))
	if err != nil {
		return nil, err
	}
	f := &Fixture{Name: name, Log: log}
	if err := json.Unmarshal(raw, &f.Meta); err != nil {
		return nil, fmt.Errorf("fixture %s: %w", name, err)
	}
	if f.Meta.Cols <= 0 || f.Meta.Rows <= 0 {
		return nil, fmt.Errorf("fixture %s: invalid size %dx%d", name, f.Meta.Cols, f.Meta.Rows)
	}
	return f, nil
}

// MustLoad is Load that fails the test on error.
func MustLoad(t testing.TB, name string) *Fixture {
	t.Helper()
	f, err := Load(name)
	if err != nil {
		t.Fatalf("load fixture: %v", err)
	}
	return f
}

// Save writes a fixture into dir (used by the recorder and tests).
func Save(dir, name string, meta Meta, log []byte) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(meta, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, name+".json"), append(raw, '\n'), 0o644); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, name+".log"), log, 0o644)
}

// Names lists the fixtures (names of *.log files) in dir, sorted.
func Names(dir string) ([]string, error) {
	m, err := filepath.Glob(filepath.Join(dir, "*.log"))
	if err != nil {
		return nil, err
	}
	for i := range m {
		m[i] = strings.TrimSuffix(filepath.Base(m[i]), ".log")
	}
	return m, nil
}

// Replay feeds the whole log (in one chunk) and returns the final Snapshot.
func (f *Fixture) Replay() *screen.Snapshot {
	r := f.NewReplayer(f.Meta.Cols, f.Meta.Rows)
	defer r.Close()
	r.Feed(f.Log)
	return r.Snapshot(-1)
}

// ReplayAt replays at another terminal size (for reflow property tests).
func (f *Fixture) ReplayAt(cols, rows int) *screen.Snapshot {
	r := f.NewReplayer(cols, rows)
	defer r.Close()
	r.Feed(f.Log)
	return r.Snapshot(-1)
}

// ReplayFrames feeds the log up to each Meta.Frames offset (and the end) and
// calls fn with a Snapshot at every boundary; i is the 0-based frame index.
func (f *Fixture) ReplayFrames(fn func(i int, snap *screen.Snapshot)) {
	r := f.NewReplayer(f.Meta.Cols, f.Meta.Rows)
	defer r.Close()
	pos, i := 0, 0
	for _, off := range append(append([]int(nil), f.Meta.Frames...), len(f.Log)) {
		if off > len(f.Log) {
			off = len(f.Log)
		}
		if off < pos {
			continue
		}
		r.Feed(f.Log[pos:off])
		pos = off
		fn(i, r.Snapshot(-1))
		i++
	}
}

// Replayer is a thin wrapper over session.Replayer: the same input filter,
// emulator callbacks and cell conversion as a live session.
type Replayer struct{ r *session.Replayer }

// NewReplayer creates an empty terminal of the given size.
func (f *Fixture) NewReplayer(cols, rows int) *Replayer { return NewReplayer(cols, rows) }

// NewReplayer creates an empty terminal of the given size.
func NewReplayer(cols, rows int) *Replayer { return &Replayer{session.NewReplayer(cols, rows)} }

// Feed writes one chunk of PTY output (counts as one OutputSeq step).
func (r *Replayer) Feed(p []byte) { r.r.Feed(p) }

// Close stops the reply drain (race-safe, see session.Replayer.Close).
func (r *Replayer) Close() { r.r.Close() }

// Snapshot builds the Snapshot of the current state. tail < 0 selects
// screen.DefaultScrollbackTail.
func (r *Replayer) Snapshot(tail int) *screen.Snapshot {
	return r.r.Snapshot(screen.SnapOpts{ScrollbackTail: tail})
}
