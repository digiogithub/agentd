// Package core is the real backend.Backend of agentd-desktop: it keeps the
// saved hosts and one Shell per host (internal/desktopui/remote), finds each
// host's agentd daemon socket, aggregates sessions from agentd (ls snapshot +
// subscribe stream per host), zellij/tmux (MuxAdapter, polled) and plain
// login shells opened by the app, streams semantic views, and maps the
// mailbox, act, attach, run and kill onto daemon ops.
//
// Every daemon op uses a fresh connection: client.Dial for the local host,
// a direct-streamlocal channel (Shell.DialUnix) wrapped with client.NewConn
// for SSH hosts, where the daemon sees the SSH user's human identity.
package core

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/digiogithub/agentd/internal/config"
	"github.com/digiogithub/agentd/internal/desktopui/backend"
	"github.com/digiogithub/agentd/internal/desktopui/remote"
)

// MuxAdapter is one terminal multiplexer (zellij, tmux) on one host.
// Implemented in package mux.
type MuxAdapter interface {
	Source() backend.Source
	List(ctx context.Context) ([]backend.Session, error)
	Attach(ctx context.Context, name string, cols, rows int) (backend.PTY, error)
	Launch(ctx context.Context, spec backend.LaunchSpec) (backend.Session, error)
	Kill(ctx context.Context, name string) error
}

// MuxFactory returns the adapters for a host reached through sh.
type MuxFactory func(sh backend.Shell, hostID string) []MuxAdapter

// Provisioner detects and installs things on a host. Implemented in package
// provision.
type Provisioner interface {
	Detect(ctx context.Context, sh backend.Shell) (*backend.Capabilities, error)
	Plan(caps *backend.Capabilities, opts backend.ProvisionOptions) []backend.PlannedStep
	Install(ctx context.Context, sh backend.Shell, caps *backend.Capabilities, opts backend.ProvisionOptions) <-chan backend.ProvisionEvent
}

// Options configures New. Zero values mean the defaults.
type Options struct {
	// Dir is the desktop state dir (hosts.json, known_hosts, app key);
	// "" = remote.Dir().
	Dir string
	// LocalSocket is the local daemon socket; "" = config.SocketPath().
	LocalSocket string
	// SocketProbe is the shell script that prints a remote host's daemon
	// socket; "" = DefaultSocketProbe.
	SocketProbe string
	// Dial overrides how SSH hosts are reached (tests); nil = remote.DialSSH.
	Dial remote.DialFunc
	// TrustHost decides about unknown host keys (TOFU); nil rejects them:
	// Connect then fails with a *backend.HostKeyError at once (nothing
	// blocks on the user) and the UI accepts the key with TrustHostKey.
	TrustHost remote.TrustFunc
	// Password and Passphrase prompt the user; never stored. A nil Password
	// makes a password login fail with backend.ErrPasswordRequired unless
	// ConnectWithPassword supplied one for that dial.
	Password   func(host backend.Host) (string, error)
	Passphrase func(path string) ([]byte, error)
	// AgentSocket overrides $SSH_AUTH_SOCK ("-" = no agent).
	AgentSocket string
	// Keepalive overrides the SSH keepalive period.
	Keepalive time.Duration
	// Mux builds the zellij/tmux adapters of a host (nil = none).
	Mux MuxFactory
	// Provisioner runs Detect/Plan/Install (nil = unsupported).
	Provisioner Provisioner
	// AutoConnect connects to every saved host when watching starts;
	// otherwise SSH hosts connect on Connect or on their first op.
	AutoConnect bool
	// MuxPoll is the zellij/tmux list period while sessions are watched
	// (default 5 s).
	MuxPoll time.Duration
	// ViewPoll is the fallback view refresh period (default 5 s).
	ViewPoll time.Duration
	// PreviewRefresh debounces the ls reload that refreshes previews after
	// view/status events (default 750 ms).
	PreviewRefresh time.Duration
	// FirstPromptTimeout bounds the wait for a launched session to become
	// idle before its first prompt (default 60 s).
	FirstPromptTimeout time.Duration
	// Logger receives diagnostics (never secrets); nil = discarded.
	Logger *slog.Logger
}

// Backend implements backend.Backend.
type Backend struct {
	opts  Options
	dir   string
	hosts *remote.HostStore
	keys  *remote.HostKeys
	pool  *remote.Pool
	local *remote.LocalShell
	log   *slog.Logger

	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup

	mu       sync.Mutex
	mons     map[string]*hostMon
	caps     map[string]*backend.Capabilities
	sockets  map[string]string
	homes    map[string]string
	adapters map[string]*adapterSet
	shells   map[string]map[string]*shellSession // host -> name
	shellSeq int
	watchers int // WatchSessions consumers
	hostList []backend.Host
	hostOK   bool
	// passwords are one-time passwords of ConnectWithPassword, only
	// while its dial runs (never stored or logged).
	passwords map[string]string

	hostsB *bcast[[]backend.HostStatus]
	sessB  *bcast[[]backend.Session]
}

var _ backend.Backend = (*Backend)(nil)

type adapterSet struct {
	shell backend.Shell
	list  []MuxAdapter
}

// ErrNoAgentd is returned for agentd ops on a host without a running daemon.
var ErrNoAgentd = errors.New("core: agentd is not running on this host")

// ErrUnsupported is returned for an op the session's source cannot do.
var ErrUnsupported = errors.New("core: not supported for this session")

// New returns a Backend. Call Close when done.
func New(opts Options) *Backend {
	if opts.MuxPoll <= 0 {
		opts.MuxPoll = 5 * time.Second
	}
	if opts.ViewPoll <= 0 {
		opts.ViewPoll = 5 * time.Second
	}
	if opts.PreviewRefresh <= 0 {
		opts.PreviewRefresh = 750 * time.Millisecond
	}
	if opts.FirstPromptTimeout <= 0 {
		opts.FirstPromptTimeout = 60 * time.Second
	}
	log := opts.Logger
	if log == nil {
		log = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	dir := opts.Dir
	if dir == "" {
		d, err := remote.Dir()
		if err != nil {
			log.Warn("desktop dir", "err", err)
		}
		dir = d
	}
	ctx, cancel := context.WithCancel(context.Background())
	b := &Backend{
		opts:      opts,
		dir:       dir,
		hosts:     remote.NewHostStore(dir),
		keys:      remote.NewHostKeys(dir),
		local:     &remote.LocalShell{},
		log:       log,
		ctx:       ctx,
		cancel:    cancel,
		mons:      map[string]*hostMon{},
		caps:      map[string]*backend.Capabilities{},
		sockets:   map[string]string{},
		homes:     map[string]string{},
		adapters:  map[string]*adapterSet{},
		shells:    map[string]map[string]*shellSession{},
		passwords: map[string]string{},
		hostsB:    newBcast[[]backend.HostStatus](),
		sessB:     newBcast[[]backend.Session](),
	}
	dial := opts.Dial
	if dial == nil {
		dial = b.dialSSH
	}
	b.pool = remote.NewPool(b.hosts.Get, dial)
	b.pool.OnChange = func(id string, connected bool, err error) {
		if !connected {
			b.forgetSocket(id)
		}
		b.wakeMonitor(id)
		b.publishHosts()
	}
	return b
}

// Close stops every watcher, monitor and connection.
func (b *Backend) Close() error {
	b.cancel()
	b.mu.Lock()
	var shells []*shellSession
	for _, m := range b.shells {
		for _, s := range m {
			shells = append(shells, s)
		}
	}
	b.mu.Unlock()
	for _, s := range shells {
		_ = s.pty.Close()
	}
	b.wg.Wait()
	b.pool.Close()
	return nil
}

func (b *Backend) dialSSH(ctx context.Context, h backend.Host) (backend.Shell, error) {
	cfg := remote.SSHConfig{
		Dir:         b.dir,
		HostKeys:    b.keys,
		Trust:       b.opts.TrustHost,
		Password:    b.password,
		Passphrase:  b.opts.Passphrase,
		AgentSocket: b.opts.AgentSocket,
		Keepalive:   b.opts.Keepalive,
		Accepted: func(host backend.Host, fp string) {
			if host.HostKey == "" && !host.Local() {
				if err := b.hosts.Update(host.ID, func(x *backend.Host) { x.HostKey = fp }); err != nil {
					b.log.Warn("store host key", "host", host.ID, "err", err)
				}
				b.invalidateHosts()
			}
		},
	}
	s, err := remote.DialSSH(ctx, h, cfg)
	if err != nil {
		return nil, err
	}
	return s, nil
}

// password answers the SSH password prompt: the one-time password of a
// running ConnectWithPassword, else Options.Password, else
// backend.ErrPasswordRequired.
func (b *Backend) password(h backend.Host) (string, error) {
	b.mu.Lock()
	pw, ok := b.passwords[h.ID]
	b.mu.Unlock()
	if ok {
		return pw, nil
	}
	if b.opts.Password != nil {
		return b.opts.Password(h)
	}
	return "", backend.ErrPasswordRequired
}

// shell returns the Shell of a host: the local shell, or the pooled SSH one.
func (b *Backend) shell(ctx context.Context, id string) (backend.Shell, error) {
	if id == backend.LocalHostID {
		return b.local, nil
	}
	if !b.pool.Connected(id) {
		go b.publishHosts() // shows "connecting"
	}
	return b.pool.Get(ctx, id)
}

func (b *Backend) localSocket() string {
	if b.opts.LocalSocket != "" {
		return b.opts.LocalSocket
	}
	return config.SocketPath()
}

// ---- Hosts ----

// WatchHosts streams host statuses.
func (b *Backend) WatchHosts(ctx context.Context) <-chan []backend.HostStatus {
	b.startWatching()
	ch := b.hostsB.subscribe(ctx)
	b.publishHosts()
	return ch
}

func (b *Backend) startWatching() {
	b.activate(backend.LocalHostID)
	if !b.opts.AutoConnect {
		return
	}
	for _, h := range b.savedHosts() {
		b.activate(h.ID)
	}
}

// savedHosts is hosts.json (local first), cached until a write.
func (b *Backend) savedHosts() []backend.Host {
	b.mu.Lock()
	if b.hostOK {
		l := b.hostList
		b.mu.Unlock()
		return l
	}
	b.mu.Unlock()
	hosts, err := b.hosts.List()
	if err != nil {
		b.log.Warn("hosts", "err", err)
	}
	b.mu.Lock()
	b.hostList, b.hostOK = hosts, err == nil
	b.mu.Unlock()
	return hosts
}

func (b *Backend) invalidateHosts() {
	b.mu.Lock()
	b.hostOK = false
	b.mu.Unlock()
}

func (b *Backend) hostStatuses() []backend.HostStatus {
	hosts := b.savedHosts()
	out := make([]backend.HostStatus, 0, len(hosts))
	for _, h := range hosts {
		st := backend.HostStatus{Host: h, State: backend.HostOffline}
		switch {
		case h.Local():
			st.State = backend.HostOnline
		case b.pool.Connected(h.ID):
			st.State = backend.HostOnline
		case b.pool.Dialing(h.ID):
			st.State = backend.HostConnecting
		default:
			if err := b.pool.LastErr(h.ID); err != nil {
				st.State = backend.HostError
				st.Err = err.Error()
				st.ErrCode = hostErrCode(err)
			}
		}
		b.mu.Lock()
		if c := b.caps[h.ID]; c != nil {
			cc := *c
			st.Capabilities = &cc
		}
		m := b.mons[h.ID]
		st.Sessions = len(b.shells[h.ID])
		b.mu.Unlock()
		if m != nil {
			n, ver := m.counts()
			st.Sessions += n
			st.AgentdVer = ver
		}
		out = append(out, st)
	}
	return out
}

func (b *Backend) publishHosts() {
	if b.ctx.Err() != nil {
		return
	}
	b.hostsB.publish(b.hostStatuses())
}

// SaveHost adds or replaces a saved host; an edited host reconnects.
func (b *Backend) SaveHost(ctx context.Context, h backend.Host) error {
	saved, err := b.hosts.Save(h)
	b.invalidateHosts()
	if err != nil {
		return err
	}
	b.pool.Forget(saved.ID)
	b.forgetSocket(saved.ID)
	b.mu.Lock()
	_, active := b.mons[saved.ID]
	b.mu.Unlock()
	if active {
		b.wakeMonitor(saved.ID)
	}
	b.publishHosts()
	return nil
}

// RemoveHost deletes a saved host and drops everything about it.
func (b *Backend) RemoveHost(ctx context.Context, id string) error {
	err := b.hosts.Remove(id)
	b.invalidateHosts()
	if err != nil {
		return err
	}
	b.mu.Lock()
	m := b.mons[id]
	delete(b.mons, id)
	delete(b.caps, id)
	delete(b.sockets, id)
	delete(b.homes, id)
	delete(b.adapters, id)
	shells := b.shells[id]
	delete(b.shells, id)
	b.mu.Unlock()
	if m != nil {
		m.stop()
	}
	for _, s := range shells {
		_ = s.pty.Close()
	}
	b.pool.Forget(id)
	b.publishHosts()
	b.publishSessions()
	return nil
}

// Connect connects to a host now (skipping any backoff) and starts
// watching it. The error is the connection's (an SSH host) or nil; a host
// without agentd still connects.
func (b *Backend) Connect(ctx context.Context, id string) error {
	if _, err := b.hosts.Get(id); err != nil {
		return err
	}
	b.pool.Reset(id)
	b.activate(id)
	if _, err := b.shell(ctx, id); err != nil {
		b.publishHosts()
		return err
	}
	b.wakeMonitor(id)
	b.publishHosts()
	return nil
}

// hostErrCode classifies a connection error (backend.HostErr*).
func hostErrCode(err error) string {
	var hk *backend.HostKeyError
	switch {
	case errors.Is(err, backend.ErrHostKeyChanged):
		return backend.HostErrKeyChanged
	case errors.As(err, &hk):
		return backend.HostErrKeyUnknown
	case errors.Is(err, backend.ErrPasswordRequired):
		return backend.HostErrPassword
	}
	return ""
}

// TrustHostKey accepts fingerprint as the key of SSH host id (stored in
// Host.HostKey, checked before any known_hosts file) and clears the
// connection backoff so the next Connect dials at once.
func (b *Backend) TrustHostKey(ctx context.Context, id, fingerprint string) error {
	h, err := b.hosts.Get(id)
	if err != nil {
		return err
	}
	if h.Local() {
		return errors.New("core: this computer has no host key")
	}
	if !strings.HasPrefix(fingerprint, "SHA256:") || len(fingerprint) <= len("SHA256:") || strings.ContainsAny(fingerprint, " \t\r\n") {
		return fmt.Errorf("core: not a SHA256 host key fingerprint: %q", fingerprint)
	}
	if h.HostKey == fingerprint {
		b.pool.Reset(id)
		return nil
	}
	if h.HostKey != "" {
		return fmt.Errorf("%w: %s already has key %s; remove the host to accept another one", backend.ErrHostKeyChanged, id, h.HostKey)
	}
	if err := b.hosts.Update(id, func(x *backend.Host) { x.HostKey = fingerprint }); err != nil {
		return err
	}
	b.invalidateHosts()
	b.pool.Reset(id)
	b.publishHosts()
	return nil
}

// ConnectWithPassword connects to SSH host id with a one-time password
// (kept in memory only while the dial runs). With installKey it then adds
// the app's public key to the host's ~/.ssh/authorized_keys (idempotent)
// and switches the host to AuthGenerated, so later logins need no
// password.
func (b *Backend) ConnectWithPassword(ctx context.Context, id, password string, installKey bool) error {
	h, err := b.hosts.Get(id)
	if err != nil {
		return err
	}
	if h.Local() {
		return errors.New("core: this computer needs no password")
	}
	if password == "" {
		return backend.ErrPasswordRequired
	}
	b.mu.Lock()
	b.passwords[id] = password
	b.mu.Unlock()
	// A connection already held was opened another way: the password
	// is for a new one.
	b.pool.Forget(id)
	err = b.Connect(ctx, id)
	if errors.Is(err, remote.ErrBackoff) {
		// A background dial without the password ended meanwhile.
		err = b.Connect(ctx, id)
	}
	b.mu.Lock()
	delete(b.passwords, id)
	b.mu.Unlock()
	if err != nil || !installKey {
		return err
	}
	line, err := remote.AppPublicKey(b.dir)
	if err != nil {
		return err
	}
	sh, err := b.shell(ctx, id)
	if err != nil {
		return err
	}
	added, err := remote.InstallAuthorizedKey(ctx, sh, line)
	if err != nil {
		return err
	}
	b.log.Info("app key installed", "host", id, "added", added)
	if err := b.hosts.Update(id, func(x *backend.Host) { x.Auth, x.KeyFile = backend.AuthGenerated, "" }); err != nil {
		return err
	}
	b.invalidateHosts()
	b.publishHosts()
	return nil
}

// PublicKey returns the app key's OpenSSH public line.
func (b *Backend) PublicKey(ctx context.Context) (string, error) {
	return remote.AppPublicKey(b.dir)
}

// ImportSSHConfig returns the hosts of ~/.ssh/config (not saved).
func (b *Backend) ImportSSHConfig(ctx context.Context) ([]backend.Host, error) {
	return remote.ImportSSHConfig()
}

// ---- Provisioning ----

// Detect runs the provisioner's detection and caches the result.
func (b *Backend) Detect(ctx context.Context, hostID string) (*backend.Capabilities, error) {
	if b.opts.Provisioner == nil {
		return nil, fmt.Errorf("%w: no provisioner", ErrUnsupported)
	}
	sh, err := b.shell(ctx, hostID)
	if err != nil {
		return nil, err
	}
	caps, err := b.opts.Provisioner.Detect(ctx, sh)
	if err != nil {
		return nil, err
	}
	b.mu.Lock()
	b.caps[hostID] = caps
	if caps.AgentdSocket != "" && hostID != backend.LocalHostID {
		b.sockets[hostID] = caps.AgentdSocket
	}
	if caps.Home != "" {
		b.homes[hostID] = caps.Home
	}
	b.mu.Unlock()
	b.publishHosts()
	return caps, nil
}

// Plan lists what Install would do.
func (b *Backend) Plan(caps *backend.Capabilities, opts backend.ProvisionOptions) []backend.PlannedStep {
	if b.opts.Provisioner == nil {
		return nil
	}
	return b.opts.Provisioner.Plan(caps, opts)
}

// Install runs the provisioner on a host (detecting first when nothing is
// cached). When it ends, cached capabilities and the daemon socket are
// dropped and the host's monitor reconnects.
func (b *Backend) Install(ctx context.Context, hostID string, opts backend.ProvisionOptions) (<-chan backend.ProvisionEvent, error) {
	if b.opts.Provisioner == nil {
		return nil, fmt.Errorf("%w: no provisioner", ErrUnsupported)
	}
	sh, err := b.shell(ctx, hostID)
	if err != nil {
		return nil, err
	}
	b.mu.Lock()
	caps := b.caps[hostID]
	b.mu.Unlock()
	if caps == nil {
		if caps, err = b.Detect(ctx, hostID); err != nil {
			return nil, err
		}
	}
	in := b.opts.Provisioner.Install(ctx, sh, caps, opts)
	out := make(chan backend.ProvisionEvent, 16)
	go func() {
		defer close(out)
		for ev := range in {
			select {
			case out <- ev:
			case <-ctx.Done():
			}
		}
		b.mu.Lock()
		delete(b.caps, hostID)
		b.mu.Unlock()
		b.forgetSocket(hostID)
		b.activate(hostID)
		b.wakeMonitor(hostID)
		b.publishHosts()
	}()
	return out, nil
}
