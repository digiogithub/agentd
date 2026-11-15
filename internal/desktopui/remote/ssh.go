package remote

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"

	"github.com/digiogithub/agentd/internal/desktopui/backend"
)

// DefaultKeepalive is how often SSHShell sends keepalive@openssh.com.
const DefaultKeepalive = 15 * time.Second

// SSHConfig is how DialSSH logs in and checks the host key.
type SSHConfig struct {
	// Dir is the desktop dir (app key). "" = Dir().
	Dir string
	// HostKeys checks host keys; nil = NewHostKeys(Dir).
	HostKeys *HostKeys
	// Trust decides about an unknown host key (TOFU); nil rejects it.
	Trust TrustFunc
	// Accepted is told the fingerprint of the accepted host key (store it in
	// Host.HostKey).
	Accepted func(host backend.Host, fingerprint string)
	// Password asks for the password (AuthPassword, and as a last resort
	// for an empty Auth). Never stored.
	Password func(host backend.Host) (string, error)
	// Passphrase asks for an encrypted key file's passphrase.
	Passphrase func(path string) ([]byte, error)
	// AgentSocket overrides $SSH_AUTH_SOCK ("-" disables the agent).
	AgentSocket string
	// Timeout bounds the TCP connect + handshake (default 15 s).
	Timeout time.Duration
	// Keepalive is the keepalive period (default 15 s; < 0 disables).
	Keepalive time.Duration
}

// SSHShell is backend.Shell over one SSH connection.
type SSHShell struct {
	host   backend.Host
	client *ssh.Client
	done   chan struct{}
	once   sync.Once
	err    error
	mu     sync.Mutex
}

var _ backend.Shell = (*SSHShell)(nil)

// DialSSH connects and authenticates to host.
func DialSSH(ctx context.Context, host backend.Host, cfg SSHConfig) (*SSHShell, error) {
	if host.Local() || host.Addr == "" {
		return nil, errors.New("remote: DialSSH needs a remote host address")
	}
	if cfg.Dir == "" {
		d, err := Dir()
		if err != nil {
			return nil, err
		}
		cfg.Dir = d
	}
	if cfg.HostKeys == nil {
		cfg.HostKeys = NewHostKeys(cfg.Dir)
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = 15 * time.Second
	}
	user := host.User
	if user == "" {
		user = currentUser()
	}
	port := host.Port
	if port == 0 {
		port = 22
	}
	addr := net.JoinHostPort(host.Addr, strconv.Itoa(port))

	auths, closeAgent, err := authMethods(host, cfg)
	if err != nil {
		return nil, err
	}
	defer closeAgent()

	var accepted func(string)
	if cfg.Accepted != nil {
		accepted = func(fp string) { cfg.Accepted(host, fp) }
	}
	ccfg := &ssh.ClientConfig{
		User:            user,
		Auth:            auths,
		HostKeyCallback: cfg.HostKeys.Callback(host, cfg.Trust, accepted),
		Timeout:         cfg.Timeout,
	}
	dctx, cancel := context.WithTimeout(ctx, cfg.Timeout)
	defer cancel()
	var d net.Dialer
	conn, err := d.DialContext(dctx, "tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("remote: connect %s: %w", addr, err)
	}
	// The handshake has no context: bound it with a deadline and close the
	// socket if ctx ends first.
	_ = conn.SetDeadline(time.Now().Add(cfg.Timeout))
	stop := context.AfterFunc(dctx, func() { _ = conn.Close() })
	c, chans, reqs, err := ssh.NewClientConn(conn, addr, ccfg)
	stop()
	if err != nil {
		_ = conn.Close()
		var mm *HostKeyMismatch
		if errors.As(err, &mm) {
			return nil, mm
		}
		if errors.Is(err, ErrHostKeyRejected) {
			var hk *backend.HostKeyError
			if errors.As(err, &hk) {
				return nil, fmt.Errorf("%w: %w", ErrHostKeyRejected, hk)
			}
			return nil, ErrHostKeyRejected
		}
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, fmt.Errorf("remote: ssh %s: %w", addr, err)
	}
	_ = conn.SetDeadline(time.Time{})
	s := &SSHShell{host: host, client: ssh.NewClient(c, chans, reqs), done: make(chan struct{})}
	go func() {
		werr := s.client.Wait()
		s.finish(werr)
	}()
	ka := cfg.Keepalive
	if ka == 0 {
		ka = DefaultKeepalive
	}
	if ka > 0 {
		go s.keepalive(ka)
	}
	return s, nil
}

func currentUser() string {
	for _, k := range []string{"USER", "LOGNAME"} {
		if u := os.Getenv(k); u != "" {
			return u
		}
	}
	return "root"
}

// authMethods builds the auth chain for host.Auth: agent, the app key, a
// key file, a password. An empty Auth tries agent + app key (+ password if
// a prompt is set).
func authMethods(host backend.Host, cfg SSHConfig) ([]ssh.AuthMethod, func(), error) {
	closeFn := func() {}
	var signers []func() ([]ssh.Signer, error)
	useAgent := func() {
		sock := cfg.AgentSocket
		if sock == "" {
			sock = os.Getenv("SSH_AUTH_SOCK")
		}
		if sock == "" || sock == "-" {
			return
		}
		conn, err := net.Dial("unix", sock)
		if err != nil {
			return
		}
		closeFn = func() { _ = conn.Close() }
		signers = append(signers, agent.NewClient(conn).Signers)
	}
	appKey := func() error {
		s, err := AppKey(cfg.Dir)
		if err != nil {
			return err
		}
		signers = append(signers, func() ([]ssh.Signer, error) { return []ssh.Signer{s}, nil })
		return nil
	}
	var methods []ssh.AuthMethod
	password := func() {
		if cfg.Password == nil {
			return
		}
		ask := func() (string, error) { return cfg.Password(host) }
		methods = append(methods, ssh.PasswordCallback(ask),
			ssh.KeyboardInteractive(func(_, _ string, questions []string, echos []bool) ([]string, error) {
				answers := make([]string, len(questions))
				for i := range questions {
					if echos[i] {
						continue
					}
					p, err := ask()
					if err != nil {
						return nil, err
					}
					answers[i] = p
				}
				return answers, nil
			}))
	}
	switch host.Auth {
	case backend.AuthAgent:
		useAgent()
		if len(signers) == 0 {
			return nil, closeFn, errors.New("remote: ssh-agent not available ($SSH_AUTH_SOCK)")
		}
	case backend.AuthGenerated:
		if err := appKey(); err != nil {
			return nil, closeFn, err
		}
	case backend.AuthKeyFile:
		if host.KeyFile == "" {
			return nil, closeFn, errors.New("remote: no key file set for this host")
		}
		s, err := LoadKeyFile(host.KeyFile, cfg.Passphrase)
		if err != nil {
			return nil, closeFn, err
		}
		signers = append(signers, func() ([]ssh.Signer, error) { return []ssh.Signer{s}, nil })
	case backend.AuthPassword:
		if cfg.Password == nil {
			return nil, closeFn, errors.New("remote: password login needs a password prompt")
		}
	case "":
		useAgent()
		if err := appKey(); err != nil {
			return nil, closeFn, err
		}
	default:
		return nil, closeFn, fmt.Errorf("remote: unknown auth kind %q", host.Auth)
	}
	if len(signers) > 0 {
		all := signers
		methods = append([]ssh.AuthMethod{ssh.PublicKeysCallback(func() ([]ssh.Signer, error) {
			var out []ssh.Signer
			for _, f := range all {
				ss, err := f()
				if err == nil {
					out = append(out, ss...)
				}
			}
			return out, nil
		})}, methods...)
	}
	if host.Auth == backend.AuthPassword || host.Auth == "" {
		password()
	}
	return methods, closeFn, nil
}

func (s *SSHShell) finish(err error) {
	s.once.Do(func() {
		s.mu.Lock()
		if err == nil {
			err = errors.New("remote: ssh connection closed")
		}
		s.err = err
		s.mu.Unlock()
		close(s.done)
	})
}

// Done is closed when the connection ends (Close, network error, keepalive
// failure).
func (s *SSHShell) Done() <-chan struct{} { return s.done }

// Err says why the connection ended (valid after Done).
func (s *SSHShell) Err() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.err
}

// Host is the host this shell is connected to.
func (s *SSHShell) Host() backend.Host { return s.host }

func (s *SSHShell) keepalive(every time.Duration) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-s.done:
			return
		case <-t.C:
		}
		res := make(chan error, 1)
		go func() {
			_, _, err := s.client.SendRequest("keepalive@openssh.com", true, nil)
			res <- err
		}()
		select {
		case err := <-res:
			if err != nil {
				s.finish(fmt.Errorf("remote: keepalive: %w", err))
				_ = s.client.Close()
				return
			}
		case <-time.After(every):
			s.finish(errors.New("remote: keepalive timed out"))
			_ = s.client.Close()
			return
		case <-s.done:
			return
		}
	}
}

// Close closes the connection.
func (s *SSHShell) Close() error {
	err := s.client.Close()
	s.finish(errors.New("remote: closed"))
	return err
}

// loginCommand is what the server runs: the user's shell gets
// `sh -lc '<cmd>'`, so cmd runs under a POSIX login shell whatever the
// account's shell is.
func loginCommand(cmd string) string { return "sh -lc " + ShellQuote(cmd) }

// envPrefix exports env inside the command (sshd usually refuses env
// requests). Keys are sorted for stable commands.
func envPrefix(env map[string]string) string {
	if len(env) == 0 {
		return ""
	}
	keys := make([]string, 0, len(env))
	for k := range env {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	for _, k := range keys {
		if !validEnvName(k) {
			continue
		}
		b.WriteString("export " + k + "=" + ShellQuote(env[k]) + "; ")
	}
	return b.String()
}

func validEnvName(k string) bool {
	if k == "" {
		return false
	}
	for i, r := range k {
		if !(r == '_' || r >= 'A' && r <= 'Z' || r >= 'a' && r <= 'z' || i > 0 && r >= '0' && r <= '9') {
			return false
		}
	}
	return true
}

func exitCode(err error) (int, error) {
	if err == nil {
		return 0, nil
	}
	var ee *ssh.ExitError
	if errors.As(err, &ee) {
		return ee.ExitStatus(), nil
	}
	var em *ssh.ExitMissingError
	if errors.As(err, &em) {
		return -1, nil
	}
	return -1, err
}

// Exec runs cmd through `sh -lc` and waits. Cancelling ctx kills the remote
// command (signal KILL + channel close) and returns ctx's error.
func (s *SSHShell) Exec(ctx context.Context, cmd string, stdin []byte) (backend.ExecResult, error) {
	sess, err := s.client.NewSession()
	if err != nil {
		return backend.ExecResult{Exit: -1}, fmt.Errorf("remote: new session: %w", err)
	}
	defer sess.Close()
	var out, errb bytes.Buffer
	sess.Stdout = &out
	sess.Stderr = &errb
	if stdin != nil {
		sess.Stdin = bytes.NewReader(stdin)
	}
	if err := sess.Start(loginCommand(cmd)); err != nil {
		return backend.ExecResult{Exit: -1}, fmt.Errorf("remote: exec: %w", err)
	}
	waitc := make(chan error, 1)
	go func() { waitc <- sess.Wait() }()
	select {
	case werr := <-waitc:
		code, err := exitCode(werr)
		return backend.ExecResult{Exit: code, Stdout: out.String(), Stderr: errb.String()}, err
	case <-ctx.Done():
		_ = sess.Signal(ssh.SIGKILL)
		_ = sess.Close()
		<-waitc
		return backend.ExecResult{Exit: -1}, ctx.Err()
	}
}

// PTY starts cmd in a remote pseudo terminal (TERM xterm-256color). env is
// exported inside the command. ctx bounds the program's lifetime: when it
// ends the session is closed (pass a long-lived context).
func (s *SSHShell) PTY(ctx context.Context, cmd string, cols, rows int, env map[string]string) (backend.PTY, error) {
	sess, err := s.client.NewSession()
	if err != nil {
		return nil, fmt.Errorf("remote: new session: %w", err)
	}
	if cols <= 0 {
		cols = 80
	}
	if rows <= 0 {
		rows = 24
	}
	modes := ssh.TerminalModes{ssh.ECHO: 1, ssh.TTY_OP_ISPEED: 38400, ssh.TTY_OP_OSPEED: 38400}
	if err := sess.RequestPty("xterm-256color", rows, cols, modes); err != nil {
		_ = sess.Close()
		return nil, fmt.Errorf("remote: request pty: %w", err)
	}
	stdin, err := sess.StdinPipe()
	if err != nil {
		_ = sess.Close()
		return nil, err
	}
	pr, pw := io.Pipe()
	sess.Stdout = pw
	sess.Stderr = pw
	full := envPrefix(env) + cmd
	if strings.TrimSpace(cmd) == "" {
		full = envPrefix(env) + `exec "${SHELL:-sh}" -l`
	}
	if err := sess.Start(loginCommand(full)); err != nil {
		_ = sess.Close()
		return nil, fmt.Errorf("remote: start pty command: %w", err)
	}
	p := &sshPTY{sess: sess, stdin: stdin, r: pr, done: make(chan struct{})}
	go func() {
		werr := sess.Wait()
		p.code, p.err = exitCode(werr)
		_ = pw.Close()
		close(p.done)
	}()
	stop := context.AfterFunc(ctx, func() { _ = p.Close() })
	go func() {
		<-p.done
		stop()
	}()
	return p, nil
}

type sshPTY struct {
	sess  *ssh.Session
	stdin io.WriteCloser
	r     *io.PipeReader
	done  chan struct{}
	code  int
	err   error
	once  sync.Once
}

func (p *sshPTY) Read(b []byte) (int, error)  { return p.r.Read(b) }
func (p *sshPTY) Write(b []byte) (int, error) { return p.stdin.Write(b) }

func (p *sshPTY) Resize(cols, rows int) error {
	if cols <= 0 || rows <= 0 {
		return errors.New("remote: invalid size")
	}
	return p.sess.WindowChange(rows, cols)
}

func (p *sshPTY) Wait() (int, error) {
	<-p.done
	return p.code, p.err
}

func (p *sshPTY) Close() error {
	p.once.Do(func() {
		_ = p.sess.Signal(ssh.SIGHUP)
		_ = p.sess.Close()
		_ = p.r.Close()
	})
	return nil
}

// DialUnix opens a unix socket on the host (direct-streamlocal@openssh.com).
func (s *SSHShell) DialUnix(ctx context.Context, path string) (net.Conn, error) {
	c, err := s.client.DialContext(ctx, "unix", path)
	if err != nil {
		return nil, fmt.Errorf("remote: dial %s on %s: %w", path, s.host.Name, err)
	}
	return c, nil
}
