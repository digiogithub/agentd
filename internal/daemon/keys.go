package daemon

import (
	"fmt"
	"slices"
	"strings"

	"github.com/digiogithub/agentd/internal/config"
)

// Abstract key names accepted by op act (docs/design/api/daemon-ops.md §3.2).
// Raw bytes are never accepted: a client names keys, the daemon maps them.

// keyDef is one entry of the key table.
type keyDef struct {
	plain string // bytes sent normally
	app   string // bytes in application cursor mode (DECCKM); "" = same as plain
	// human: the bytes cannot leave a pending "draft" behind (escape
	// sequences, Enter, Ctrl-C), so the write is marked as human input for
	// idle/attention purposes. Printable keys, Tab, Backspace... are not
	// marked: a lone "y" would otherwise count as a draft for ten minutes.
	human bool
}

var keyTable = map[string]keyDef{
	"enter":     {plain: "", human: true}, // resolved to the profile submit sequence
	"esc":       {plain: "\x1b", human: true},
	"tab":       {plain: "\t"},
	"shift-tab": {plain: "\x1b[Z", human: true},
	"up":        {plain: "\x1b[A", app: "\x1bOA", human: true},
	"down":      {plain: "\x1b[B", app: "\x1bOB", human: true},
	"right":     {plain: "\x1b[C", app: "\x1bOC", human: true},
	"left":      {plain: "\x1b[D", app: "\x1bOD", human: true},
	"home":      {plain: "\x1b[H", app: "\x1bOH", human: true},
	"end":       {plain: "\x1b[F", app: "\x1bOF", human: true},
	"pgup":      {plain: "\x1b[5~", human: true},
	"pgdown":    {plain: "\x1b[6~", human: true},
	"backspace": {plain: "\x7f"},
	"delete":    {plain: "\x1b[3~", human: true},
	"ctrl-c":    {plain: "\x03", human: true},
	"ctrl-d":    {plain: "\x04"},
	"ctrl-l":    {plain: "\x0c"},
	"space":     {plain: " "},
	"y":         {plain: "y"},
	"n":         {plain: "n"},
}

func init() {
	for d := '0'; d <= '9'; d++ {
		keyTable[string(d)] = keyDef{plain: string(d)}
	}
}

// KeyNames lists the accepted key names, sorted.
func KeyNames() []string {
	out := make([]string, 0, len(keyTable))
	for k := range keyTable {
		out = append(out, k)
	}
	slices.Sort(out)
	return out
}

// keySeq is one resolved key: its bytes and whether the write is marked as
// human input.
type keySeq struct {
	name  string
	bytes []byte
	human bool
}

// resolveKeys maps key names to bytes. Cursor keys use SS3 when appCursor
// (DECCKM) is set; "enter" is submit (the profile's submit sequence, default
// "\r"). Unknown names, an empty list or more than config.MaxActKeys keys is
// an error and nothing is resolved.
func resolveKeys(names []string, appCursor bool, submit string) ([]keySeq, error) {
	if len(names) == 0 {
		return nil, fmt.Errorf("no keys given")
	}
	if len(names) > config.MaxActKeys {
		return nil, fmt.Errorf("too many keys: %d (max %d)", len(names), config.MaxActKeys)
	}
	if submit == "" {
		submit = config.DefaultSubmit
	}
	out := make([]keySeq, 0, len(names))
	for _, n := range names {
		d, ok := keyTable[n]
		if !ok {
			return nil, fmt.Errorf("unknown key %q (valid: %s)", n, strings.Join(KeyNames(), ", "))
		}
		b := d.plain
		if n == "enter" {
			b = submit
		}
		if appCursor && d.app != "" {
			b = d.app
		}
		out = append(out, keySeq{name: n, bytes: []byte(b), human: d.human})
	}
	return out, nil
}
