// Package config resolves agentd's filesystem paths (XDG), loads config.toml
// and resolves harness profiles and the mailbox ACL.
package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
)

// Environment variables that override the default paths.
const (
	EnvSocket   = "AGENTD_SOCKET"
	EnvStateDir = "AGENTD_STATE_DIR"
	EnvConfig   = "AGENTD_CONFIG"
)

// File and directory names inside the state directory.
const (
	socketName    = "agentd.sock"
	mailboxName   = "mailbox.db"
	logDirName    = "logs"
	daemonLogName = "daemon.log"
	docsName      = "docs.json"
	configName    = "config.toml"
	appDirName    = "agentd"
)

// SocketPath returns the daemon socket path: $AGENTD_SOCKET if set, else
// $XDG_RUNTIME_DIR/agentd/agentd.sock, else /tmp/agentd-$UID/agentd.sock.
func SocketPath() string {
	if p := os.Getenv(EnvSocket); p != "" {
		return p
	}
	if d := os.Getenv("XDG_RUNTIME_DIR"); d != "" {
		return filepath.Join(d, appDirName, socketName)
	}
	return filepath.Join(fallbackRuntimeDir(), socketName)
}

func fallbackRuntimeDir() string {
	return filepath.Join(os.TempDir(), "agentd-"+strconv.Itoa(os.Getuid()))
}

// StateDir returns the state directory: $AGENTD_STATE_DIR if set, else
// $XDG_STATE_HOME/agentd, else ~/.local/state/agentd.
func StateDir() (string, error) {
	if p := os.Getenv(EnvStateDir); p != "" {
		return p, nil
	}
	if d := os.Getenv("XDG_STATE_HOME"); d != "" {
		return filepath.Join(d, appDirName), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("config: resolve state dir: %w", err)
	}
	return filepath.Join(home, ".local", "state", appDirName), nil
}

// ConfigPath returns the config file path: $AGENTD_CONFIG if set, else
// $XDG_CONFIG_HOME/agentd/config.toml, else ~/.config/agentd/config.toml.
func ConfigPath() (string, error) {
	if p := os.Getenv(EnvConfig); p != "" {
		return p, nil
	}
	if d := os.Getenv("XDG_CONFIG_HOME"); d != "" {
		return filepath.Join(d, appDirName, configName), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("config: resolve config path: %w", err)
	}
	return filepath.Join(home, ".config", appDirName, configName), nil
}

func inStateDir(elem ...string) (string, error) {
	d, err := StateDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(append([]string{d}, elem...)...), nil
}

// MailboxPath returns <state>/mailbox.db.
func MailboxPath() (string, error) { return inStateDir(mailboxName) }

// DocsPath returns the path of the document store (ops doc_get/doc_set).
func DocsPath() (string, error) { return inStateDir(docsName) }

// LogDir returns <state>/logs.
func LogDir() (string, error) { return inStateDir(logDirName) }

// DaemonLogPath returns <state>/daemon.log.
func DaemonLogPath() (string, error) { return inStateDir(daemonLogName) }

// SessionLogPath returns <state>/logs/<name>.log after validating name.
func SessionLogPath(name string) (string, error) {
	if err := ValidName(name); err != nil {
		return "", err
	}
	return inStateDir(logDirName, name+".log")
}

// EnsureDirs creates the socket directory, the state directory and the log
// directory with mode 0700 if they do not exist. Existing directories are not
// chmod'ed (the socket dir may be a shared one when AGENTD_SOCKET is set),
// except the /tmp/agentd-$UID fallback, which must be a real directory owned
// by the current user and not accessible by group or others.
func EnsureDirs() error {
	sockDir := filepath.Dir(SocketPath())
	if err := os.MkdirAll(sockDir, 0o700); err != nil {
		return fmt.Errorf("config: create socket dir: %w", err)
	}
	if os.Getenv(EnvSocket) == "" && os.Getenv("XDG_RUNTIME_DIR") == "" {
		if err := checkPrivateDir(sockDir); err != nil {
			return err
		}
	}
	logDir, err := LogDir()
	if err != nil {
		return err
	}
	// MkdirAll on logs also creates the state dir with 0700.
	if err := os.MkdirAll(logDir, 0o700); err != nil {
		return fmt.Errorf("config: create state dirs: %w", err)
	}
	return nil
}

// checkPrivateDir verifies that dir is not a symlink, is a directory, is
// owned by the current user and has no group/other permission bits.
func checkPrivateDir(dir string) error {
	fi, err := os.Lstat(dir)
	if err != nil {
		return fmt.Errorf("config: stat %s: %w", dir, err)
	}
	if !fi.IsDir() {
		return fmt.Errorf("config: %s is not a directory", dir)
	}
	if fi.Mode().Perm()&0o077 != 0 {
		return fmt.Errorf("config: %s has insecure permissions %v (want 0700)", dir, fi.Mode().Perm())
	}
	if uid, ok := ownerUID(fi); ok && uid != os.Getuid() {
		return fmt.Errorf("config: %s is owned by uid %d, not %d", dir, uid, os.Getuid())
	}
	return nil
}

// validNameRe is the session name grammar from AGENTS.md.
var validNameRe = regexp.MustCompile(`^[a-zA-Z0-9._@-]+$`)

// ErrInvalidName is returned (wrapped) by ValidName.
var ErrInvalidName = errors.New("invalid session name")

// ValidName reports whether s is a valid session name ([a-zA-Z0-9._@-]+).
// "." and ".." are rejected because names are used as file names.
func ValidName(s string) error {
	if !validNameRe.MatchString(s) || s == "." || s == ".." {
		return fmt.Errorf("%w %q: must match [a-zA-Z0-9._@-]+", ErrInvalidName, s)
	}
	return nil
}
