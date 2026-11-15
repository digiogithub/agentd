package core

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/digiogithub/agentd/internal/client"
	"github.com/digiogithub/agentd/internal/desktopui/backend"
	"github.com/digiogithub/agentd/internal/proto"
)

type clientConn = client.Client

// DefaultSocketProbe prints the first agentd socket that exists on a host:
// $AGENTD_SOCKET, $XDG_RUNTIME_DIR/agentd, /run/user/<uid>/agentd, then
// /tmp/agentd-<uid> (the daemon's own resolution order plus the systemd
// runtime dir, which non-interactive SSH sessions may lack in the env).
const DefaultSocketProbe = `uid=$(id -u)
for p in "$AGENTD_SOCKET" "${XDG_RUNTIME_DIR:+$XDG_RUNTIME_DIR/agentd/agentd.sock}" "/run/user/$uid/agentd/agentd.sock" "/tmp/agentd-$uid/agentd.sock"; do
  if [ -n "$p" ] && [ -S "$p" ]; then printf '%s\n' "$p"; exit 0; fi
done
exit 1`

func (b *Backend) forgetSocket(id string) {
	b.mu.Lock()
	delete(b.sockets, id)
	b.mu.Unlock()
}

// agentdSocket finds (and caches) the daemon socket of a remote host.
func (b *Backend) agentdSocket(ctx context.Context, id string, sh backend.Shell) (string, error) {
	b.mu.Lock()
	sock := b.sockets[id]
	b.mu.Unlock()
	if sock != "" {
		return sock, nil
	}
	pctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	probe := b.opts.SocketProbe
	if probe == "" {
		probe = DefaultSocketProbe
	}
	res, err := sh.Exec(pctx, probe, nil)
	if err != nil {
		return "", err
	}
	sock = strings.TrimSpace(res.Stdout)
	if res.Exit != 0 || sock == "" {
		return "", ErrNoAgentd
	}
	if i := strings.LastIndexByte(sock, '\n'); i >= 0 { // login noise before it
		sock = sock[i+1:]
	}
	b.mu.Lock()
	b.sockets[id] = sock
	b.mu.Unlock()
	return sock, nil
}

// dialAgentd opens a fresh daemon connection on host id.
func (b *Backend) dialAgentd(ctx context.Context, id string) (*client.Client, error) {
	if id == backend.LocalHostID {
		c, err := client.Dial(b.localSocket())
		if err != nil {
			return nil, fmt.Errorf("%w (%v)", ErrNoAgentd, err)
		}
		// The desktop speaks for the human: never forward a session's
		// AGENTD_ID/AGENTD_TOKEN inherited from a launching terminal (a stale
		// token would fail every op). The daemon still resolves identity by
		// process ancestry.
		c.Auth = nil
		return c, nil
	}
	sh, err := b.shell(ctx, id)
	if err != nil {
		return nil, err
	}
	sock, err := b.agentdSocket(ctx, id, sh)
	if err != nil {
		return nil, err
	}
	conn, err := sh.DialUnix(ctx, sock)
	if err != nil {
		b.forgetSocket(id) // daemon moved or stopped: probe again next time
		return nil, fmt.Errorf("%w (%v)", ErrNoAgentd, err)
	}
	return client.NewConn(conn, nil), nil
}

// withClient runs fn on a fresh daemon connection; ctx cancels it by closing
// the connection.
func (b *Backend) withClient(ctx context.Context, id string, fn func(*client.Client) error) error {
	if _, err := b.hosts.Get(id); err != nil {
		return err
	}
	b.activate(id)
	c, err := b.dialAgentd(ctx, id)
	if err != nil {
		return err
	}
	stop := context.AfterFunc(ctx, func() { _ = c.Close() })
	err = fn(c)
	stop()
	_ = c.Close()
	if err != nil && ctx.Err() != nil {
		return ctx.Err()
	}
	return err
}

func needAgentd(s backend.Session) error {
	if s.Source != backend.SourceAgentd {
		return fmt.Errorf("%w (%s)", ErrUnsupported, s.Source)
	}
	return nil
}

// Act passes an act through to the daemon. A conflict error keeps the
// daemon's *proto.RemoteError (Data = proto.ActConflict).
func (b *Backend) Act(ctx context.Context, s backend.Session, args proto.ActArgs) (*proto.ActResult, error) {
	if err := needAgentd(s); err != nil {
		return nil, err
	}
	if args.Name == "" {
		args.Name = s.Name
	}
	if args.Client == "" {
		args.Client = "desktop"
	}
	var res proto.ActResult
	err := b.withClient(ctx, s.HostID, func(c *client.Client) error {
		var err error
		res, err = c.Act(args)
		return err
	})
	if err != nil {
		return nil, err
	}
	return &res, nil
}

// ViewAck clears the session's attention flag.
func (b *Backend) ViewAck(ctx context.Context, s backend.Session) error {
	if err := needAgentd(s); err != nil {
		return err
	}
	return b.withClient(ctx, s.HostID, func(c *client.Client) error {
		_, err := c.ViewAck(s.Name)
		return err
	})
}

// Inbox lists the human mailbox of the host's user (read messages
// included).
func (b *Backend) Inbox(ctx context.Context, hostID string) ([]proto.Message, error) {
	var msgs []proto.Message
	err := b.withClient(ctx, hostID, func(c *client.Client) error {
		r, err := c.InboxWith(proto.InboxArgs{All: true})
		msgs = r.Messages
		return err
	})
	return msgs, err
}

// Queues lists the work queues with unclaimed items.
func (b *Backend) Queues(ctx context.Context, hostID string) ([]proto.QueueCount, error) {
	var qs []proto.QueueCount
	err := b.withClient(ctx, hostID, func(c *client.Client) error {
		r, err := c.Queue(proto.QueueArgs{})
		qs = r.Queues
		return err
	})
	return qs, err
}

// Send sends a message as the host's human user.
func (b *Backend) Send(ctx context.Context, hostID string, args proto.SendArgs) (*proto.Message, error) {
	var m proto.Message
	err := b.withClient(ctx, hostID, func(c *client.Client) error {
		var err error
		m, err = c.Send(args)
		return err
	})
	if err != nil {
		return nil, err
	}
	return &m, nil
}

// Projects lists the host's agentd aliases (empty without agentd).
func (b *Backend) Projects(ctx context.Context, hostID string) ([]proto.AliasInfo, error) {
	var out []proto.AliasInfo
	err := b.withClient(ctx, hostID, func(c *client.Client) error {
		info, err := c.Info()
		out = info.Aliases
		return err
	})
	if errors.Is(err, ErrNoAgentd) {
		return []proto.AliasInfo{}, nil
	}
	if out == nil && err == nil {
		out = []proto.AliasInfo{}
	}
	return out, err
}

// home is the working directory for launches: the host user's home.
func (b *Backend) home(ctx context.Context, id string) (string, error) {
	b.mu.Lock()
	h := b.homes[id]
	b.mu.Unlock()
	if h != "" {
		return h, nil
	}
	if id == backend.LocalHostID {
		return os.UserHomeDir()
	}
	sh, err := b.shell(ctx, id)
	if err != nil {
		return "", err
	}
	res, err := sh.Exec(ctx, `printf '%s' "$HOME"`, nil)
	if err != nil {
		return "", err
	}
	h = strings.TrimSpace(res.Stdout)
	if i := strings.LastIndexByte(h, '\n'); i >= 0 {
		h = h[i+1:]
	}
	if res.Exit != 0 || !strings.HasPrefix(h, "/") {
		return "", fmt.Errorf("core: cannot read $HOME on %s", id)
	}
	b.mu.Lock()
	b.homes[id] = h
	b.mu.Unlock()
	return h, nil
}

// Launch starts a session: agentd run (target = project, harness, cwd =
// the user's home), a zellij/tmux session or a plain login shell. A first
// prompt is typed in the background once the agentd session is idle.
func (b *Backend) Launch(ctx context.Context, spec backend.LaunchSpec) (backend.Session, error) {
	if spec.HostID == "" {
		spec.HostID = backend.LocalHostID
	}
	if _, err := b.hosts.Get(spec.HostID); err != nil {
		return backend.Session{}, err
	}
	switch spec.Where {
	case backend.SourceAgentd, "":
	case backend.SourceShell:
		return b.launchShell(ctx, spec)
	default:
		a, err := b.muxAdapter(ctx, spec.HostID, spec.Where)
		if err != nil {
			return backend.Session{}, err
		}
		s, err := a.Launch(ctx, spec)
		if err == nil {
			s.HostID, s.Source = spec.HostID, spec.Where
			b.wakeMonitor(spec.HostID)
		}
		return s, err
	}
	home, err := b.home(ctx, spec.HostID)
	if err != nil {
		return backend.Session{}, err
	}
	args := proto.RunArgs{Target: spec.Project, Harness: spec.Harness, Cwd: home}
	if args.Target == "" {
		args.Target = home
	}
	var info proto.SessionInfo
	err = b.withClient(ctx, spec.HostID, func(c *client.Client) error {
		var err error
		info, err = c.Run(args)
		return err
	})
	if err != nil {
		return backend.Session{}, err
	}
	b.activate(spec.HostID).upsert(info)
	if p := strings.TrimSpace(spec.FirstPrompt); p != "" {
		b.wg.Add(1)
		go func() {
			defer b.wg.Done()
			if err := b.firstPrompt(spec.HostID, info.Name, spec.FirstPrompt); err != nil {
				b.log.Warn("first prompt", "host", spec.HostID, "session", info.Name, "err", err)
			}
		}()
	}
	return agentdSession(spec.HostID, info), nil
}

// firstPrompt waits for the session to accept a prompt and types it.
func (b *Backend) firstPrompt(hostID, name, text string) error {
	ctx, cancel := context.WithTimeout(b.ctx, b.opts.FirstPromptTimeout)
	defer cancel()
	tick := time.NewTicker(500 * time.Millisecond)
	defer tick.Stop()
	for {
		var v proto.View
		err := b.withClient(ctx, hostID, func(c *client.Client) error {
			var err error
			v, err = c.View(proto.ViewArgs{Name: name, LinesLimit: 1})
			return err
		})
		if err == nil && v.ExitCode != nil {
			return errors.New("session exited before the first prompt")
		}
		if err == nil && (v.Status == "idle" || v.Status == "waiting" && len(v.Options) == 0) {
			err = b.withClient(ctx, hostID, func(c *client.Client) error {
				_, err := c.Act(proto.ActArgs{Name: name, Action: proto.ActPrompt, Text: text, Revision: v.Revision, Epoch: v.Epoch, Client: "desktop"})
				return err
			})
			if err == nil {
				return nil
			}
			code := client.ErrorCode(err)
			if code != proto.CodeNotReady && code != proto.CodeBusy && code != proto.CodeConflict && code != proto.CodeVerifyFailed {
				return err
			}
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("session not ready for the first prompt: %w", ctx.Err())
		case <-tick.C:
		}
	}
}

// Kill stops a session (agentd kill, mux kill, or closing the shell).
func (b *Backend) Kill(ctx context.Context, s backend.Session) error {
	switch s.Source {
	case backend.SourceAgentd:
		return b.withClient(ctx, s.HostID, func(c *client.Client) error { return c.Kill(s.Name, "") })
	case backend.SourceShell:
		return b.killShell(s)
	default:
		a, err := b.muxAdapter(ctx, s.HostID, s.Source)
		if err != nil {
			return err
		}
		if err := a.Kill(ctx, s.Name); err != nil {
			return err
		}
		b.wakeMonitor(s.HostID)
		return nil
	}
}
