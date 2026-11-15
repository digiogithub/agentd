package remote

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"os"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/creack/pty"

	"github.com/digiogithub/agentd/internal/desktopui/backend"
)

// LocalShell is backend.Shell on this computer (/bin/sh -lc).
type LocalShell struct {
	// Shell is the POSIX shell (default /bin/sh).
	Shell string
}

var _ backend.Shell = (*LocalShell)(nil)

func (l *LocalShell) sh() string {
	if l.Shell != "" {
		return l.Shell
	}
	return "/bin/sh"
}

// Exec runs cmd through `sh -lc`; cancelling ctx kills it.
func (l *LocalShell) Exec(ctx context.Context, cmd string, stdin []byte) (backend.ExecResult, error) {
	c := exec.CommandContext(ctx, l.sh(), "-lc", cmd)
	// Kill the whole process group on cancel: a grandchild holding the
	// output pipes would otherwise keep Run waiting.
	c.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	c.Cancel = func() error { return syscall.Kill(-c.Process.Pid, syscall.SIGKILL) }
	c.WaitDelay = 2 * time.Second
	var out, errb bytes.Buffer
	c.Stdout, c.Stderr = &out, &errb
	if stdin != nil {
		c.Stdin = bytes.NewReader(stdin)
	}
	err := c.Run()
	if ctx.Err() != nil {
		return backend.ExecResult{Exit: -1}, ctx.Err()
	}
	res := backend.ExecResult{Stdout: out.String(), Stderr: errb.String()}
	var ee *exec.ExitError
	switch {
	case err == nil:
	case errors.As(err, &ee):
		res.Exit = ee.ExitCode()
	default:
		res.Exit = -1
		return res, err
	}
	return res, nil
}

// PTY starts cmd (empty = the user's login shell) in a local pseudo
// terminal. ctx bounds the program's lifetime.
func (l *LocalShell) PTY(ctx context.Context, cmd string, cols, rows int, env map[string]string) (backend.PTY, error) {
	if cols <= 0 {
		cols = 80
	}
	if rows <= 0 {
		rows = 24
	}
	if strings.TrimSpace(cmd) == "" {
		cmd = `exec "${SHELL:-sh}" -l`
	}
	c := exec.Command(l.sh(), "-lc", cmd)
	c.Env = append(os.Environ(), "TERM=xterm-256color")
	for k, v := range env {
		c.Env = append(c.Env, k+"="+v)
	}
	f, err := pty.StartWithSize(c, &pty.Winsize{Cols: uint16(cols), Rows: uint16(rows)})
	if err != nil {
		return nil, err
	}
	p := &localPTY{f: f, cmd: c, done: make(chan struct{})}
	go func() {
		werr := c.Wait()
		p.code = -1
		var ee *exec.ExitError
		switch {
		case werr == nil:
			p.code = 0
		case errors.As(werr, &ee):
			p.code = ee.ExitCode()
		default:
			p.err = werr
		}
		close(p.done)
	}()
	stop := context.AfterFunc(ctx, func() { _ = p.Close() })
	go func() {
		<-p.done
		stop()
	}()
	return p, nil
}

type localPTY struct {
	f    *os.File
	cmd  *exec.Cmd
	done chan struct{}
	code int
	err  error
	once sync.Once
}

func (p *localPTY) Read(b []byte) (int, error) {
	n, err := p.f.Read(b)
	if err != nil && (errors.Is(err, syscall.EIO) || errors.Is(err, os.ErrClosed)) {
		err = io.EOF
	}
	return n, err
}

func (p *localPTY) Write(b []byte) (int, error) { return p.f.Write(b) }

func (p *localPTY) Resize(cols, rows int) error {
	if cols <= 0 || rows <= 0 {
		return errors.New("remote: invalid size")
	}
	return pty.Setsize(p.f, &pty.Winsize{Cols: uint16(cols), Rows: uint16(rows)})
}

func (p *localPTY) Wait() (int, error) {
	<-p.done
	return p.code, p.err
}

func (p *localPTY) Close() error {
	p.once.Do(func() {
		select {
		case <-p.done:
		default:
			if p.cmd.Process != nil {
				_ = p.cmd.Process.Signal(syscall.SIGHUP)
			}
		}
		_ = p.f.Close()
	})
	return nil
}

// DialUnix connects to a local unix socket.
func (l *LocalShell) DialUnix(ctx context.Context, path string) (net.Conn, error) {
	var d net.Dialer
	return d.DialContext(ctx, "unix", path)
}

// Close is a no-op.
func (l *LocalShell) Close() error { return nil }
