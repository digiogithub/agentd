// Package backend is the contract between the agentd-desktop UI and the code
// that reaches hosts. The UI (internal/desktopui/app, kit, shell) imports only
// this package and proto types; implementations live in
// internal/desktopui/remote (SSH + local), internal/desktopui/mux (zellij,
// tmux) and internal/desktopui/provision (detect + installs). A fake for UI
// work and tests lives in internal/desktopui/backend/fake.
//
// Change this file only together with every implementation.
package backend

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"time"

	"github.com/digiogithub/agentd/internal/proto"
)

// AuthKind is how the app logs in to a host.
type AuthKind string

const (
	AuthAgent     AuthKind = "agent"     // ssh-agent ($SSH_AUTH_SOCK)
	AuthGenerated AuthKind = "generated" // the app's own ed25519 key
	AuthKeyFile   AuthKind = "keyfile"   // an existing private key file
	AuthPassword  AuthKind = "password"  // asked each time, never stored in hosts.json
)

// Host is a saved machine. ID "local" is this computer (no SSH).
type Host struct {
	ID      string   `json:"id"`
	Name    string   `json:"name"`
	Addr    string   `json:"addr"` // hostname or IP; "" for local
	Port    int      `json:"port,omitempty"`
	User    string   `json:"user,omitempty"`
	Auth    AuthKind `json:"auth,omitempty"`
	KeyFile string   `json:"key_file,omitempty"`
	// HostKey is the accepted SHA256 fingerprint (trust on first use).
	HostKey string `json:"host_key,omitempty"`
	// SSHAlias is the ~/.ssh/config Host entry it was imported from.
	SSHAlias string `json:"ssh_alias,omitempty"`
}

// Local reports whether h is this computer.
func (h Host) Local() bool { return h.ID == LocalHostID }

const LocalHostID = "local"

// ExecResult is a finished non-interactive command.
type ExecResult struct {
	Exit   int
	Stdout string
	Stderr string
}

// PTY is an interactive program in a pseudo terminal (remote or local).
type PTY interface {
	io.ReadWriteCloser
	Resize(cols, rows int) error
	// Wait returns the exit code (-1 if unknown) once the program ends.
	Wait() (int, error)
}

// Shell runs things on one host. The SSH implementation runs commands through
// `sh -lc`; the local one through /bin/sh -lc.
type Shell interface {
	Exec(ctx context.Context, cmd string, stdin []byte) (ExecResult, error)
	PTY(ctx context.Context, cmd string, cols, rows int, env map[string]string) (PTY, error)
	// DialUnix opens a unix socket on the host (SSH direct-streamlocal).
	DialUnix(ctx context.Context, path string) (net.Conn, error)
	Close() error
}

// HostState is the connection state of a host.
type HostState string

const (
	HostOffline    HostState = "offline"
	HostConnecting HostState = "connecting"
	HostOnline     HostState = "online"
	HostError      HostState = "error"
)

// Tool is one program found on a host.
type Tool struct {
	Name      string // agentd zellij tmux node npm claude codex pando opencode gemini
	Installed bool
	Version   string
	Path      string
}

// Capabilities is what Detect found.
type Capabilities struct {
	OS, Arch, Distro string
	SystemdUser      bool
	RuntimeDir       string
	AgentdSocket     string // live daemon socket, "" when none
	Home             string
	LocalBinOnPath   bool
	Tools            map[string]Tool
}

// Has reports whether tool is installed.
func (c Capabilities) Has(tool string) bool { return c.Tools[tool].Installed }

// HostKeyError is returned by Connect (and ConnectWithPassword) when the
// host presents a key that was never accepted (trust on first use). Nothing
// blocks waiting for the user: the UI shows Fingerprint and, once the user
// accepts it, calls TrustHostKey and connects again.
type HostKeyError struct {
	HostID      string
	Fingerprint string // SHA256:...
	KeyType     string // ssh-ed25519, ecdsa-sha2-nistp256, ...
}

func (e *HostKeyError) Error() string {
	return fmt.Sprintf("unknown host key for %s: %s %s", e.HostID, e.KeyType, e.Fingerprint)
}

// HostKeyFingerprint is the fingerprint to confirm.
func (e *HostKeyError) HostKeyFingerprint() string { return e.Fingerprint }

// ErrHostKeyChanged matches (errors.Is) a host presenting a key other than
// the accepted one. It is a hard error: TrustHostKey never overrides it; the
// user removes the host (and its old key) on purpose.
var ErrHostKeyChanged = errors.New("host key changed")

// ErrPasswordRequired is returned by Connect for a host that needs a
// password (AuthPassword, or no key accepted): the UI asks for it and calls
// ConnectWithPassword.
var ErrPasswordRequired = errors.New("password required")

// Host error codes (HostStatus.ErrCode).
const (
	HostErrKeyUnknown = "host_key_unknown" // HostKeyError
	HostErrKeyChanged = "host_key_changed" // ErrHostKeyChanged
	HostErrPassword   = "password_required"
)

// HostStatus is a host plus its live state.
type HostStatus struct {
	Host  Host
	State HostState
	Err   string
	// ErrCode classifies Err (HostErr*; "" = other).
	ErrCode      string
	Capabilities *Capabilities
	Sessions     int
	AgentdVer    string
}

// Source says where a session runs.
type Source string

const (
	SourceAgentd Source = "agentd"
	SourceZellij Source = "zellij"
	SourceTmux   Source = "tmux"
	// SourceShell is a plain login shell over SSH (or locally), no multiplexer.
	SourceShell Source = "shell"
)

// Session is one entry of the agents list.
type Session struct {
	HostID  string
	Source  Source
	Name    string // agentd session name, zellij session, tmux session
	Harness string // claude codex pando ... ("" unknown)
	Project string
	Status  string // working waiting idle starting exited unknown (agentd view statuses)
	// Attention is set when the agent waits for the user.
	Attention bool
	Preview   string
	Detail    string // "3 panes", title, ...
	Updated   time.Time
	// Info is the agentd SessionInfo (nil for zellij/tmux).
	Info *proto.SessionInfo
}

// Key identifies a session across hosts.
func (s Session) Key() string { return s.HostID + "/" + string(s.Source) + "/" + s.Name }

// LaunchSpec is a new session request.
type LaunchSpec struct {
	HostID      string
	Where       Source // agentd, zellij, tmux or shell
	Project     string // agentd alias or directory
	Harness     string // claude codex pando opencode gemini bash
	FirstPrompt string
	// TerminalOnly opens the user's login shell in Project instead of an
	// agent (Harness and FirstPrompt are ignored): agentd runs harness
	// "shell" (`sh -c 'exec "${SHELL:-/bin/sh}" -l'`), zellij/tmux open their
	// default shell, SSH cds to Project first. Sessions are named
	// `shell@<project label>` (agentd, zellij, tmux).
	TerminalOnly bool
}

// DirEntry is one directory of a ListDir answer.
type DirEntry struct {
	Name string
	// Enterable is false for a directory the user cannot read or traverse.
	Enterable bool
}

// DirListing is the folder browser's view of one directory on a host.
type DirListing struct {
	Path      string // absolute path of the directory
	Home      string // the host's $HOME
	Entries   []DirEntry
	Truncated bool // more directories than MaxDirEntries
}

// MaxDirEntries caps a DirListing.
const MaxDirEntries = 2000

// ListDir failures (errors.Is).
var (
	ErrDirNotFound = errors.New("directory does not exist")
	ErrDirDenied   = errors.New("directory not accessible")
)

// PlannedStep is one provisioning step, shown before it runs.
type PlannedStep struct {
	Step    string // download verify install service hc_listener mcp cli:<name>
	Title   string
	Command string
}

// ProvisionOptions selects what Install does.
type ProvisionOptions struct {
	Agentd     bool
	HCListener bool
	MCP        bool
	CLIs       []string // claude codex pando opencode gemini
}

// ProvisionEvent reports install progress. Skipped steps carry Skipped.
type ProvisionEvent struct {
	Step    string
	Message string
	Done    bool
	Skipped bool
	Err     string
}

// Backend is everything the UI needs. Channels returned by Watch* stay open
// until ctx ends; each value is a full replacement snapshot.
type Backend interface {
	// Hosts
	WatchHosts(ctx context.Context) <-chan []HostStatus
	SaveHost(ctx context.Context, h Host) error
	RemoveHost(ctx context.Context, id string) error
	Connect(ctx context.Context, id string) error
	// TrustHostKey accepts fingerprint (from a HostKeyError) as the key of
	// host id, so the next Connect succeeds. It refuses to replace a
	// different key already accepted (ErrHostKeyChanged).
	TrustHostKey(ctx context.Context, id, fingerprint string) error
	// ConnectWithPassword connects once with password (never stored). With
	// installKey it then appends the app's public key to the host's
	// ~/.ssh/authorized_keys (idempotent) and switches the host to
	// AuthGenerated.
	ConnectWithPassword(ctx context.Context, id, password string, installKey bool) error
	PublicKey(ctx context.Context) (string, error) // the app key, OpenSSH line
	ImportSSHConfig(ctx context.Context) ([]Host, error)
	// Projects lists the agentd aliases of a host (op info), for New session.
	Projects(ctx context.Context, hostID string) ([]proto.AliasInfo, error)

	// Sessions
	WatchSessions(ctx context.Context) <-chan []Session
	// WatchView streams the agentd semantic view of s (agentd sessions only).
	WatchView(ctx context.Context, s Session) (<-chan *proto.View, error)
	Act(ctx context.Context, s Session, args proto.ActArgs) (*proto.ActResult, error)
	ViewAck(ctx context.Context, s Session) error
	Terminal(ctx context.Context, s Session, cols, rows int, observe bool) (PTY, error)
	Launch(ctx context.Context, spec LaunchSpec) (Session, error)
	Kill(ctx context.Context, s Session) error
	// Terminate force-closes and deletes s: agentd kill KILL, wait until it
	// has exited (<= 5 s), rm (an exited session is only rm'ed); zellij kills
	// and deletes the session; tmux kill-session; an SSH shell is closed.
	Terminate(ctx context.Context, s Session) error
	// ListDir lists the directories under path on a host (the folder
	// browser). path "" = the host's $HOME; hidden includes dot folders.
	// Errors match ErrDirNotFound / ErrDirDenied.
	ListDir(ctx context.Context, hostID, path string, hidden bool) (DirListing, error)

	// Mailbox (agentd hosts)
	Inbox(ctx context.Context, hostID string) ([]proto.Message, error)
	Queues(ctx context.Context, hostID string) ([]proto.QueueCount, error)
	Send(ctx context.Context, hostID string, args proto.SendArgs) (*proto.Message, error)

	// Provisioning
	Detect(ctx context.Context, hostID string) (*Capabilities, error)
	Plan(caps *Capabilities, opts ProvisionOptions) []PlannedStep
	Install(ctx context.Context, hostID string, opts ProvisionOptions) (<-chan ProvisionEvent, error)
}
