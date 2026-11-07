// Package client is the Go client for the agentd socket API, used by the CLI
// and the MCP server. Dial connects to a running daemon; DialAuto also starts
// one if needed.
package client

import (
	"bufio"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/digiogithub/agentd/internal/proto"
)

// AutostartTimeout is how long DialAuto waits for an auto-started daemon.
const AutostartTimeout = 3 * time.Second

// Client is one connection to the daemon. Calls are serialized. After Attach
// the client must not be used for other calls.
type Client struct {
	conn  net.Conn
	codec *proto.Codec
	mu    sync.Mutex

	// Auth is sent with every request (nil = none). Dial fills it from
	// $AGENTD_ID / $AGENTD_TOKEN when set (see AuthFromEnv).
	Auth *proto.Auth
}

// Environment variables carrying the session identity hint and token.
const (
	EnvID    = "AGENTD_ID"
	EnvToken = "AGENTD_TOKEN"
)

// AuthFromEnv returns the identity hint and token from the environment, or
// nil when neither is set.
func AuthFromEnv() *proto.Auth {
	a := proto.Auth{ID: os.Getenv(EnvID), Token: os.Getenv(EnvToken)}
	if a.ID == "" && a.Token == "" {
		return nil
	}
	return &a
}

// Dial connects to the daemon listening on path.
func Dial(path string) (*Client, error) {
	conn, err := net.DialTimeout("unix", path, 2*time.Second)
	if err != nil {
		return nil, err
	}
	return &Client{conn: conn, codec: proto.NewCodec(conn), Auth: AuthFromEnv()}, nil
}

// AutoOptions configures DialAuto.
type AutoOptions struct {
	// Exe is the agentd binary to start; "" means os.Executable().
	Exe string
	// Args are the daemon arguments; nil means ["daemon"].
	Args []string
	// Timeout overrides AutostartTimeout.
	Timeout time.Duration
	// SocketPath, if set, is passed to the daemon as AGENTD_SOCKET (DialAuto
	// sets it to the path it dials).
	SocketPath string
}

// DialAuto connects to the daemon on path, starting it if the socket is
// missing or nobody answers a ping: the daemon is exec'ed detached (new
// session, stdio on /dev/null) and DialAuto waits for it to answer.
//
// Concurrent starters are fine: the daemon takes an exclusive lock before
// binding, so extra daemons exit with "already running" and every caller just
// waits for the winner to answer.
func DialAuto(path string, opts AutoOptions) (*Client, error) {
	if c, err := dialAndPing(path); err == nil {
		return c, nil
	}
	opts.SocketPath = path
	if err := StartDaemon(opts); err != nil {
		return nil, err
	}
	timeout := opts.Timeout
	if timeout <= 0 {
		timeout = AutostartTimeout
	}
	deadline := time.Now().Add(timeout)
	var lastErr error
	for time.Now().Before(deadline) {
		c, err := dialAndPing(path)
		if err == nil {
			return c, nil
		}
		lastErr = err
		time.Sleep(50 * time.Millisecond)
	}
	return nil, fmt.Errorf("agentd daemon did not start within %s: %v", timeout, lastErr)
}

func dialAndPing(path string) (*Client, error) {
	c, err := Dial(path)
	if err != nil {
		return nil, err
	}
	_ = c.conn.SetDeadline(time.Now().Add(2 * time.Second))
	if _, err := c.Ping(); err != nil {
		_ = c.Close()
		return nil, err
	}
	_ = c.conn.SetDeadline(time.Time{})
	return c, nil
}

// StartDaemon execs the daemon detached from the caller's terminal.
func StartDaemon(opts AutoOptions) error {
	exe := opts.Exe
	if exe == "" {
		var err error
		if exe, err = os.Executable(); err != nil {
			return fmt.Errorf("find agentd binary: %w", err)
		}
	}
	args := opts.Args
	if args == nil {
		args = []string{"daemon"}
	}
	devnull, err := os.OpenFile(os.DevNull, os.O_RDWR, 0)
	if err != nil {
		return err
	}
	defer func() { _ = devnull.Close() }()
	cmd := exec.Command(exe, args...)
	cmd.Dir = "/"
	cmd.Env = DaemonEnv(os.Environ(), opts.SocketPath)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = devnull, devnull, devnull
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start agentd daemon: %w", err)
	}
	return cmd.Process.Release()
}

// daemonPathEnv are the AGENTD_* variables that configure the daemon itself
// (paths) and therefore survive DaemonEnv.
var daemonPathEnv = map[string]bool{
	"AGENTD_SOCKET":       true,
	"AGENTD_STATE_DIR":    true,
	"AGENTD_CONFIG":       true,
	"AGENTD_ALIASES_FILE": true,
	"AGENTD_ALIASES":      true,
}

// DaemonEnv returns the environment for an auto-started daemon: env without
// session-scoped AGENTD_* variables (AGENTD_ID, AGENTD_TOKEN...; path
// overrides are kept) and without Claude Code markers (CLAUDECODE*,
// CLAUDE_CODE_ENTRYPOINT), which would otherwise leak into every session the
// daemon starts. A non-empty socket is set as AGENTD_SOCKET. Relative path
// overrides are made absolute (the daemon runs from "/").
func DaemonEnv(env []string, socket string) []string {
	out := make([]string, 0, len(env)+1)
	for _, kv := range env {
		name, val, _ := strings.Cut(kv, "=")
		switch {
		case strings.HasPrefix(name, "CLAUDECODE"), name == "CLAUDE_CODE_ENTRYPOINT":
			continue
		case strings.HasPrefix(name, "AGENTD_"):
			if !daemonPathEnv[name] || (name == "AGENTD_SOCKET" && socket != "") {
				continue
			}
			if name != "AGENTD_ALIASES" && val != "" && !filepath.IsAbs(val) {
				if abs, err := filepath.Abs(val); err == nil {
					kv = name + "=" + abs
				}
			}
		}
		out = append(out, kv)
	}
	if socket != "" {
		if abs, err := filepath.Abs(socket); err == nil {
			socket = abs
		}
		out = append(out, "AGENTD_SOCKET="+socket)
	}
	return out
}

// Close closes the connection.
func (c *Client) Close() error { return c.conn.Close() }

// Call sends op with args and decodes the response data into out (if non-nil).
// Daemon-side failures are returned as *proto.RemoteError.
func (c *Client) Call(op string, args, out any) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.callLocked(op, args, out)
}

func (c *Client) callLocked(op string, args, out any) error {
	req, err := proto.NewRequest(op, args)
	if err != nil {
		return err
	}
	req.Auth = c.Auth
	if err := c.codec.WriteRequest(req); err != nil {
		return fmt.Errorf("send %s: %w", op, err)
	}
	resp, err := c.codec.ReadResponse()
	if err != nil {
		return fmt.Errorf("read %s response: %w", op, err)
	}
	return resp.Decode(out)
}

// Ping checks the daemon.
func (c *Client) Ping() (proto.PingResult, error) {
	var r proto.PingResult
	err := c.Call(proto.OpPing, nil, &r)
	return r, err
}

// Run starts a session.
func (c *Client) Run(a proto.RunArgs) (proto.SessionInfo, error) {
	var r proto.SessionInfo
	err := c.Call(proto.OpRun, a, &r)
	return r, err
}

// Ls lists sessions.
func (c *Client) Ls() ([]proto.SessionInfo, error) {
	var r proto.LsResult
	err := c.Call(proto.OpLs, nil, &r)
	return r.Sessions, err
}

// Kill signals a session (signal "" = TERM).
func (c *Client) Kill(name, signal string) error {
	return c.Call(proto.OpKill, proto.KillArgs{Name: name, Signal: signal}, nil)
}

// Rm removes an exited session.
func (c *Client) Rm(name string) error {
	return c.Call(proto.OpRm, proto.NameArgs{Name: name}, nil)
}

// Dump returns a session's screen (or scrollback).
func (c *Client) Dump(a proto.DumpArgs) (string, error) {
	r, err := c.DumpResult(a)
	return r.Text, err
}

// DumpResult is Dump returning the full result (with the ghost text found).
func (c *Client) DumpResult(a proto.DumpArgs) (proto.DumpResult, error) {
	var r proto.DumpResult
	err := c.Call(proto.OpDump, a, &r)
	return r, err
}

// Type writes raw text into a session, optionally followed by the submit key.
func (c *Client) Type(a proto.TypeArgs) error { return c.Call(proto.OpType, a, nil) }

// Send queues a message.
func (c *Client) Send(a proto.SendArgs) (proto.Message, error) {
	var r proto.Message
	err := c.Call(proto.OpSend, a, &r)
	return r, err
}

// Inbox lists the caller's messages (unread only unless all). A non-empty
// as lists another mailbox (humans only, without changing message states).
func (c *Client) Inbox(as string, all bool) ([]proto.Message, error) {
	var r proto.InboxResult
	err := c.Call(proto.OpInbox, proto.InboxArgs{As: as, All: all}, &r)
	return r.Messages, err
}

// Read returns message id (addressed to the caller) and marks it read. A
// non-empty as reads from another mailbox (humans only, without marking).
func (c *Client) Read(as string, id int64) (proto.Message, error) {
	var r proto.Message
	err := c.Call(proto.OpRead, proto.ReadArgs{As: as, ID: id}, &r)
	return r, err
}

// Reply replies to message id as the caller.
func (c *Client) Reply(id int64, body string) (proto.Message, error) {
	var r proto.Message
	err := c.Call(proto.OpReply, proto.ReplyArgs{ID: id, Body: body}, &r)
	return r, err
}

// Whoami returns the caller's identity as resolved by the daemon.
func (c *Client) Whoami() (proto.WhoamiResult, error) {
	var r proto.WhoamiResult
	err := c.Call(proto.OpWhoami, nil, &r)
	return r, err
}

// Purge deletes mailbox messages (see proto.PurgeArgs).
func (c *Client) Purge(a proto.PurgeArgs) (proto.PurgeResult, error) {
	var r proto.PurgeResult
	err := c.Call(proto.OpPurge, a, &r)
	return r, err
}

// Peers lists addressable sessions.
func (c *Client) Peers() ([]proto.Peer, error) {
	var r proto.PeersResult
	err := c.Call(proto.OpPeers, nil, &r)
	return r.Peers, err
}

// Shutdown asks the daemon to stop.
func (c *Client) Shutdown() error { return c.Call(proto.OpShutdown, nil, nil) }

// Attach attaches to a session and returns the connection, already switched
// to binary frames (see proto.ReadFrame / proto.FrameWriter). The first frame
// received is the full redraw. The Client must not be used afterwards; close
// the returned connection when done.
func (c *Client) Attach(name string, cols, rows uint16) (net.Conn, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.callLocked(proto.OpAttach, proto.AttachArgs{Name: name, Cols: cols, Rows: rows}, nil); err != nil {
		return nil, err
	}
	return &frameConn{Conn: c.conn, r: c.codec.Reader()}, nil
}

// frameConn reads through the codec's buffer so that frames already buffered
// together with the attach response are not lost.
type frameConn struct {
	net.Conn
	r *bufio.Reader
}

func (f *frameConn) Read(p []byte) (int, error) { return f.r.Read(p) }

// IsRemote reports whether err is a daemon-side error.
func IsRemote(err error) bool {
	var re *proto.RemoteError
	return errors.As(err, &re)
}
