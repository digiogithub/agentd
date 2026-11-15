// Package sshtest is an in-process SSH server for tests of the desktop
// backend: password and public-key auth, exec (through /bin/sh -c, like
// sshd), pty-req + window-change (creack/pty), signals,
// direct-streamlocal@openssh.com to local unix sockets and keepalive global
// requests. It runs only what the tests ask for (sh, cat, stty...), never
// real agent CLIs.
package sshtest

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"os"
	"os/exec"
	"strconv"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"

	"github.com/creack/pty"
	"golang.org/x/crypto/ssh"
)

// Server is a running test SSH server.
type Server struct {
	Addr string // 127.0.0.1:port
	Host string
	Port int
	// HostKey is the server's public host key.
	HostKey ssh.PublicKey

	// Env is added to every command's environment.
	Env []string

	ln         net.Listener
	cfg        *ssh.ServerConfig
	wg         sync.WaitGroup
	mu         sync.Mutex
	conns      []net.Conn
	closed     bool
	Keepalives atomic.Int64
	Execs      atomic.Int64
}

// Options configures New.
type Options struct {
	// Password accepted for any user ("" = password auth off).
	Password string
	// AuthorizedKeys are accepted public keys (nil and no Password = any
	// client is accepted without auth).
	AuthorizedKeys []ssh.PublicKey
	// HostSigner overrides the generated host key.
	HostSigner ssh.Signer
}

// NewSigner returns a fresh ed25519 signer.
func NewSigner(t testing.TB) ssh.Signer {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	s, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// New starts a server on 127.0.0.1 and stops it at test cleanup.
func New(t testing.TB, opts Options) *Server {
	t.Helper()
	cfg := &ssh.ServerConfig{}
	if opts.Password == "" && opts.AuthorizedKeys == nil {
		cfg.NoClientAuth = true
	}
	if opts.Password != "" {
		pw := opts.Password
		cfg.PasswordCallback = func(_ ssh.ConnMetadata, p []byte) (*ssh.Permissions, error) {
			if string(p) == pw {
				return nil, nil
			}
			return nil, errors.New("bad password")
		}
	}
	if opts.AuthorizedKeys != nil {
		keys := opts.AuthorizedKeys
		cfg.PublicKeyCallback = func(_ ssh.ConnMetadata, k ssh.PublicKey) (*ssh.Permissions, error) {
			for _, a := range keys {
				if string(a.Marshal()) == string(k.Marshal()) {
					return nil, nil
				}
			}
			return nil, errors.New("unknown key")
		}
	}
	hs := opts.HostSigner
	if hs == nil {
		hs = NewSigner(t)
	}
	cfg.AddHostKey(hs)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ta := ln.Addr().(*net.TCPAddr)
	s := &Server{Addr: ta.String(), Host: "127.0.0.1", Port: ta.Port, HostKey: hs.PublicKey(), ln: ln, cfg: cfg}
	s.wg.Add(1)
	go s.serve()
	t.Cleanup(s.Close)
	return s
}

// Close stops the server and drops every connection.
func (s *Server) Close() {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	s.closed = true
	conns := s.conns
	s.mu.Unlock()
	_ = s.ln.Close()
	for _, c := range conns {
		_ = c.Close()
	}
	s.wg.Wait()
}

// DropConns closes every open connection (simulates a network failure).
func (s *Server) DropConns() {
	s.mu.Lock()
	conns := s.conns
	s.conns = nil
	s.mu.Unlock()
	for _, c := range conns {
		_ = c.Close()
	}
}

func (s *Server) serve() {
	defer s.wg.Done()
	for {
		c, err := s.ln.Accept()
		if err != nil {
			return
		}
		s.mu.Lock()
		if s.closed {
			s.mu.Unlock()
			_ = c.Close()
			return
		}
		s.conns = append(s.conns, c)
		s.mu.Unlock()
		s.wg.Add(1)
		go func() {
			defer s.wg.Done()
			s.handleConn(c)
		}()
	}
}

func (s *Server) handleConn(nc net.Conn) {
	sc, chans, reqs, err := ssh.NewServerConn(nc, s.cfg)
	if err != nil {
		_ = nc.Close()
		return
	}
	defer sc.Close()
	go func() {
		for r := range reqs {
			if r.Type == "keepalive@openssh.com" {
				s.Keepalives.Add(1)
			}
			if r.WantReply {
				_ = r.Reply(r.Type == "keepalive@openssh.com", nil)
			}
		}
	}()
	var wg sync.WaitGroup
	for nch := range chans {
		switch nch.ChannelType() {
		case "session":
			ch, creqs, err := nch.Accept()
			if err != nil {
				continue
			}
			wg.Add(1)
			go func() {
				defer wg.Done()
				s.session(ch, creqs)
			}()
		case "direct-streamlocal@openssh.com":
			var m struct {
				SocketPath string
				Reserved0  string
				Reserved1  uint32
			}
			if err := ssh.Unmarshal(nch.ExtraData(), &m); err != nil {
				_ = nch.Reject(ssh.ConnectionFailed, "bad payload")
				continue
			}
			uc, err := net.Dial("unix", m.SocketPath)
			if err != nil {
				_ = nch.Reject(ssh.ConnectionFailed, err.Error())
				continue
			}
			ch, creqs, err := nch.Accept()
			if err != nil {
				_ = uc.Close()
				continue
			}
			go ssh.DiscardRequests(creqs)
			wg.Add(1)
			go func() {
				defer wg.Done()
				proxy(ch, uc)
			}()
		default:
			_ = nch.Reject(ssh.UnknownChannelType, "unsupported")
		}
	}
	wg.Wait()
}

func proxy(ch ssh.Channel, uc net.Conn) {
	done := make(chan struct{}, 2)
	go func() {
		_, _ = io.Copy(uc, ch)
		if u, ok := uc.(*net.UnixConn); ok {
			_ = u.CloseWrite()
		}
		done <- struct{}{}
	}()
	go func() {
		_, _ = io.Copy(ch, uc)
		_ = ch.CloseWrite()
		done <- struct{}{}
	}()
	<-done
	<-done
	_ = uc.Close()
	_ = ch.Close()
}

type ptyReq struct {
	Term       string
	Cols, Rows uint32
	W, H       uint32
	Modes      string
}

type winReq struct {
	Cols, Rows uint32
	W, H       uint32
}

func (s *Server) session(ch ssh.Channel, reqs <-chan *ssh.Request) {
	defer ch.Close()
	var (
		mu      sync.Mutex
		ptmx    *os.File
		winsize *pty.Winsize
		term    string
		cmd     *exec.Cmd
		exited  = make(chan struct{})
		started bool
	)
	for r := range reqs {
		switch r.Type {
		case "pty-req":
			var p ptyReq
			if err := ssh.Unmarshal(r.Payload, &p); err != nil {
				_ = r.Reply(false, nil)
				continue
			}
			mu.Lock()
			winsize = &pty.Winsize{Cols: uint16(p.Cols), Rows: uint16(p.Rows)}
			term = p.Term
			mu.Unlock()
			_ = r.Reply(true, nil)
		case "window-change":
			var w winReq
			if err := ssh.Unmarshal(r.Payload, &w); err == nil {
				mu.Lock()
				if ptmx != nil {
					_ = pty.Setsize(ptmx, &pty.Winsize{Cols: uint16(w.Cols), Rows: uint16(w.Rows)})
				}
				mu.Unlock()
			}
			if r.WantReply {
				_ = r.Reply(true, nil)
			}
		case "env":
			_ = r.Reply(false, nil) // like a default sshd
		case "signal":
			var sig struct{ Name string }
			_ = ssh.Unmarshal(r.Payload, &sig)
			mu.Lock()
			if cmd != nil && cmd.Process != nil {
				switch sig.Name {
				case "KILL":
					_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
				case "HUP":
					_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGHUP)
				default:
					_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM)
				}
			}
			mu.Unlock()
			if r.WantReply {
				_ = r.Reply(true, nil)
			}
		case "exec", "shell":
			if started {
				_ = r.Reply(false, nil)
				continue
			}
			var e struct{ Command string }
			if r.Type == "exec" {
				if err := ssh.Unmarshal(r.Payload, &e); err != nil {
					_ = r.Reply(false, nil)
					continue
				}
			} else {
				e.Command = "exec sh -l"
			}
			started = true
			s.Execs.Add(1)
			c := exec.Command("/bin/sh", "-c", e.Command)
			c.Env = append(os.Environ(), s.Env...)
			mu.Lock()
			if term != "" {
				c.Env = append(c.Env, "TERM="+term)
			}
			var err error
			if winsize != nil {
				ptmx, err = pty.StartWithSize(c, winsize)
				if err == nil {
					go func() { _, _ = io.Copy(ptmx, ch) }()
				}
			} else {
				c.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
				c.Stdout, c.Stderr = ch, ch.Stderr()
				stdin, perr := c.StdinPipe()
				if perr == nil {
					go func() {
						_, _ = io.Copy(stdin, ch)
						_ = stdin.Close()
					}()
				}
				err = c.Start()
			}
			cmd = c
			mu.Unlock()
			if err != nil {
				_ = r.Reply(false, nil)
				return
			}
			_ = r.Reply(true, nil)
			outDone := make(chan struct{})
			if ptmx != nil {
				go func() {
					_, _ = io.Copy(ch, ptmx)
					close(outDone)
				}()
			} else {
				close(outDone)
			}
			go func() {
				werr := c.Wait()
				if ptmx != nil {
					<-outDone
					_ = ptmx.Close()
				}
				code := 0
				var ee *exec.ExitError
				if errors.As(werr, &ee) {
					code = ee.ExitCode()
					if code < 0 {
						code = 128 + int(syscall.SIGKILL)
					}
				}
				b := make([]byte, 4)
				binary.BigEndian.PutUint32(b, uint32(code))
				_, _ = ch.SendRequest("exit-status", false, b)
				_ = ch.Close()
				close(exited)
			}()
		default:
			if r.WantReply {
				_ = r.Reply(false, nil)
			}
		}
	}
	// Channel closed by the client: end the program.
	mu.Lock()
	if cmd != nil && cmd.Process != nil {
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGHUP)
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
	mu.Unlock()
	if started {
		<-exited
	}
}

// Port as a string, for convenience.
func (s *Server) PortString() string { return strconv.Itoa(s.Port) }
