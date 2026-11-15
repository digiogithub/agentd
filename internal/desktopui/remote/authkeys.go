package remote

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"golang.org/x/crypto/ssh"

	"github.com/digiogithub/agentd/internal/desktopui/backend"
)

// installAuthorizedKeyScript is the agentd-app (mobile) script, same text:
// it reads one public key line on stdin, creates ~/.ssh (0700) and
// authorized_keys (0600) when missing, fixes their modes, and appends the
// line only when its key blob is not there yet. Prints `added` or `present`.
// The key comes on stdin, so nothing is quoted into the command.
const installAuthorizedKeyScript = `
umask 077
IFS= read -r k || [ -n "$k" ] || { echo "no key on stdin" >&2; exit 2; }
case $k in ssh-*|ecdsa-*|sk-*) ;; *) echo "not a public key line" >&2; exit 2 ;; esac
b=$(printf '%s\n' "$k" | awk '{print $2}')
[ -n "$b" ] || { echo "no key blob" >&2; exit 2; }
d=$HOME/.ssh
f=$d/authorized_keys
mkdir -p "$d" && chmod 700 "$d" || exit 1
[ -f "$f" ] || : > "$f" || exit 1
chmod 600 "$f" || exit 1
if grep -qF -- "$b" "$f"; then echo present; exit 0; fi
if [ -s "$f" ] && [ -n "$(tail -c 1 "$f")" ]; then printf '\n' >> "$f"; fi
printf '%s\n' "$k" >> "$f" && echo added
`

// ErrNotPublicKey is returned by InstallAuthorizedKey for input that is not
// one OpenSSH public key line (a private key, several lines, options...).
var ErrNotPublicKey = errors.New("remote: not an OpenSSH public key line")

// InstallAuthorizedKey appends the public key line to ~/.ssh/authorized_keys
// on the host behind sh (idempotent: nothing is written when the key blob
// is already there). added reports whether the line was appended.
func InstallAuthorizedKey(ctx context.Context, sh backend.Shell, line string) (added bool, err error) {
	line = strings.TrimSpace(line)
	if line == "" || strings.ContainsAny(line, "\r\n") {
		return false, ErrNotPublicKey
	}
	pub, comment, options, rest, err := ssh.ParseAuthorizedKey([]byte(line))
	if err != nil || len(options) > 0 || len(rest) > 0 {
		return false, ErrNotPublicKey
	}
	// Re-marshal: the line written is canonical `<type> <blob> [comment]`.
	canon := strings.TrimSpace(string(ssh.MarshalAuthorizedKey(pub)))
	if c := strings.TrimSpace(comment); c != "" && !strings.ContainsAny(c, "\x00") {
		canon += " " + c
	}
	r, err := sh.Exec(ctx, installAuthorizedKeyScript, []byte(canon+"\n"))
	if err != nil {
		return false, fmt.Errorf("remote: install public key: %w", err)
	}
	if r.Exit != 0 {
		return false, fmt.Errorf("remote: install public key: exit %d: %s", r.Exit, strings.TrimSpace(r.Stderr))
	}
	out := strings.TrimSpace(r.Stdout)
	if i := strings.LastIndexByte(out, '\n'); i >= 0 {
		out = strings.TrimSpace(out[i+1:]) // a login shell may print first
	}
	switch out {
	case "added":
		return true, nil
	case "present":
		return false, nil
	}
	return false, fmt.Errorf("remote: install public key: unexpected output %q", out)
}
