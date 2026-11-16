package core

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/digiogithub/agentd/internal/client"
	"github.com/digiogithub/agentd/internal/desktopui/backend"
	"github.com/digiogithub/agentd/internal/desktopui/dirlist"
	"github.com/digiogithub/agentd/internal/desktopui/remote"
	"github.com/digiogithub/agentd/internal/proto"
)

// shellDir quotes a directory for the host shell, keeping a leading `~`
// or `~/` expandable.
func shellDir(dir string) string {
	if dir == "~" {
		return `"$HOME"`
	}
	if rest, ok := strings.CutPrefix(dir, "~/"); ok {
		if rest == "" {
			return `"$HOME"`
		}
		return `"$HOME"/` + remote.ShellQuote(rest)
	}
	return remote.ShellQuote(dir)
}

// ListDir lists the directories under path on a host with one sh script
// over the host's Shell (a local exec or an SSH exec channel).
func (b *Backend) ListDir(ctx context.Context, hostID, path string, hidden bool) (backend.DirListing, error) {
	if hostID == "" {
		hostID = backend.LocalHostID
	}
	sh, err := b.shell(ctx, hostID)
	if err != nil {
		return backend.DirListing{}, err
	}
	res, err := sh.Exec(ctx, dirlist.Script(path, hidden), nil)
	if err != nil {
		return backend.DirListing{}, err
	}
	l, err := dirlist.Parse(res.Stdout)
	if err != nil {
		if res.Exit != 0 {
			return backend.DirListing{}, fmt.Errorf("core: listing %s on %s failed (exit %d): %s", path, hostID, res.Exit, strings.TrimSpace(res.Stderr))
		}
		return backend.DirListing{}, err
	}
	return l, nil
}

// terminateWait is how long Terminate waits for a killed session to exit.
const terminateWait = 5 * time.Second

// Terminate force-closes and deletes a session.
func (b *Backend) Terminate(ctx context.Context, s backend.Session) error {
	switch s.Source {
	case backend.SourceAgentd:
		return b.terminateAgentd(ctx, s)
	case backend.SourceShell:
		// Closing is best effort: a shell that is already closed or gone is
		// what we wanted.
		_ = b.killShell(s)
		return nil
	default:
		// zellij: delete-session --force kills a running session and drops
		// the resurrectable one; tmux: kill-session.
		return b.Kill(ctx, s)
	}
}

func (b *Backend) terminateAgentd(ctx context.Context, s backend.Session) error {
	exited := s.Status == "exited" || s.Info != nil && s.Info.State == "exited"
	if !exited {
		err := b.withClient(ctx, s.HostID, func(c *client.Client) error { return c.Kill(s.Name, "KILL") })
		switch {
		case err == nil:
		case client.ErrorCode(err) == proto.CodeNotFound:
			return nil // already removed
		case client.ErrorCode(err) == proto.CodeExited:
		default:
			return err
		}
		if err := b.waitExited(ctx, s); err != nil {
			return err
		}
	}
	err := b.withClient(ctx, s.HostID, func(c *client.Client) error { return c.Rm(s.Name) })
	if err != nil && client.ErrorCode(err) != proto.CodeNotFound {
		return err
	}
	b.wakeMonitor(s.HostID)
	return nil
}

// waitExited polls ls until the session is exited or gone.
func (b *Backend) waitExited(ctx context.Context, s backend.Session) error {
	ctx, cancel := context.WithTimeout(ctx, terminateWait)
	defer cancel()
	tick := time.NewTicker(100 * time.Millisecond)
	defer tick.Stop()
	for {
		var list []proto.SessionInfo
		err := b.withClient(ctx, s.HostID, func(c *client.Client) error {
			var err error
			list, err = c.Ls()
			return err
		})
		if err == nil {
			state := ""
			for _, si := range list {
				if si.Name == s.Name {
					state = si.State
				}
			}
			if state == "" || state == "exited" {
				return nil
			}
		}
		select {
		case <-ctx.Done():
			return errors.New("the session did not exit in time after the kill")
		case <-tick.C:
		}
	}
}
