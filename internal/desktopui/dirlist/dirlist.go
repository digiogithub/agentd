// Package dirlist is the folder browser's host-side half: one POSIX sh script
// that lists the directories under a path (run through a backend.Shell exec)
// and the parser of its NUL-separated output. No MyGo, no I/O of its own.
//
// Output of the script, all fields NUL-terminated: the marker `@@agentd-ls`
// (anything a login shell prints before it is ignored), a status (`ok`,
// `notfound`, `denied`), then for `ok` the absolute path, the home directory
// and one `D name flag` record per directory (flag 1 = readable and
// traversable, 0 = not), ended by `E` (complete) or `T` (cut at
// MaxEntries).
package dirlist

import (
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/digiogithub/agentd/internal/desktopui/backend"
)

const marker = "@@agentd-ls"

// quote single-quotes s for a POSIX shell.
func quote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

// Script returns the sh script listing path ("" = $HOME; a leading `~` or
// `~/` is expanded). The path is passed single-quoted, never interpolated.
func Script(path string, hidden bool) string {
	h := "0"
	if hidden {
		h = "1"
	}
	return "p=" + quote(path) + "; hidden=" + h + "; max=" + fmt.Sprint(backend.MaxDirEntries) + `
[ -n "$p" ] || p=$HOME
case "$p" in "~") p=$HOME;; "~/"*) p=$HOME/${p#"~/"};; esac
printf '` + marker + `\000'
if [ ! -d "$p" ]; then printf 'notfound\000'; exit 0; fi
if ! cd -- "$p" 2>/dev/null || [ ! -r . ]; then printf 'denied\000'; exit 0; fi
printf 'ok\000%s\000%s\000' "$(pwd)" "$HOME"
if [ "$hidden" = 1 ]; then set -- ./* ./.[!.]* ./..?*; else set -- ./*; fi
n=0; end=E
for e in "$@"; do
  [ -d "$e" ] || continue
  if [ "$n" -ge "$max" ]; then end=T; break; fi
  n=$((n+1))
  if [ -r "$e" ] && [ -x "$e" ]; then f=1; else f=0; fi
  printf 'D\000%s\000%s\000' "${e#./}" "$f"
done
printf '%s\000' "$end"
`
}

// Parse parses the output of Script. A missing or denied directory gives
// backend.ErrDirNotFound / ErrDirDenied. Entries are sorted case
// insensitively.
func Parse(out string) (backend.DirListing, error) {
	i := strings.Index(out, marker+"\x00")
	if i < 0 {
		return backend.DirListing{}, errors.New("dirlist: no listing in the output")
	}
	f := strings.Split(out[i+len(marker)+1:], "\x00")
	// A complete output ends with an empty field after the last NUL.
	if len(f) > 0 && f[len(f)-1] == "" {
		f = f[:len(f)-1]
	}
	if len(f) == 0 {
		return backend.DirListing{}, errors.New("dirlist: truncated output")
	}
	switch f[0] {
	case "notfound":
		return backend.DirListing{}, backend.ErrDirNotFound
	case "denied":
		return backend.DirListing{}, backend.ErrDirDenied
	case "ok":
	default:
		return backend.DirListing{}, fmt.Errorf("dirlist: unexpected status %q", f[0])
	}
	if len(f) < 3 {
		return backend.DirListing{}, errors.New("dirlist: truncated output")
	}
	l := backend.DirListing{Path: f[1], Home: f[2]}
	f = f[3:]
	for len(f) > 0 {
		switch f[0] {
		case "D":
			if len(f) < 3 {
				return backend.DirListing{}, errors.New("dirlist: truncated entry")
			}
			l.Entries = append(l.Entries, backend.DirEntry{Name: f[1], Enterable: f[2] == "1"})
			f = f[3:]
		case "E", "T":
			l.Truncated = f[0] == "T"
			sort.SliceStable(l.Entries, func(a, b int) bool {
				x, y := strings.ToLower(l.Entries[a].Name), strings.ToLower(l.Entries[b].Name)
				if x != y {
					return x < y
				}
				return l.Entries[a].Name < l.Entries[b].Name
			})
			return l, nil
		default:
			return backend.DirListing{}, fmt.Errorf("dirlist: unexpected record %q", f[0])
		}
	}
	return backend.DirListing{}, errors.New("dirlist: output ended before the end marker")
}
