package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"text/tabwriter"
	"time"

	"github.com/skip2/go-qrcode"
	"github.com/spf13/cobra"

	"github.com/digiogithub/agentd/internal/client"
	"github.com/digiogithub/agentd/internal/proto"
)

func relayCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "relay",
		Short: "Reach this host from agentd-desktop / agentd-app through an agentd-relay",
		Long: `The daemon keeps an outbound link to the agentd-relay set in config.toml:

  [relay]
  enabled = true
  url = "https://relay.example.com"
  join_token_env = "AGENTD_RELAY_JOIN_TOKEN"   # or join_token = "..."
  max_scope = "operate"                       # view | operate | admin

Devices pair with a one-time invite (QR or link) and then connect end to end
encrypted; the relay never sees what they do. Scopes: view (list, read, watch),
operate (+ prompts, menus, interactive terminal, mailbox), admin (+ start, kill,
remove sessions). operate can type into agents that run shell commands: it is
as powerful as a shell on this host.`,
	}
	cmd.AddCommand(relayStatusCmd(), relayInviteCmd(), relayDevicesCmd(), relayRevokeCmd())
	return cmd
}

func relayStatusCmd() *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "status",
		Short: "Show the relay link (url, route, state) and connected devices",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := connect()
			if err != nil {
				return err
			}
			defer c.Close()
			st, err := c.Relay(proto.RelayArgs{Action: proto.RelayStatus})
			if err != nil {
				return err
			}
			devs, err := c.Relay(proto.RelayArgs{Action: proto.RelayDevices})
			if err != nil {
				return err
			}
			if asJSON {
				st.Devices, st.Invites = devs.Devices, devs.Invites
				return printJSON(cmd.OutOrStdout(), st)
			}
			out := cmd.OutOrStdout()
			fmt.Fprintf(out, "state:   %s\n", st.State)
			if st.Error != "" {
				fmt.Fprintf(out, "error:   %s\n", st.Error)
			}
			fmt.Fprintf(out, "url:     %s\nlabel:   %s\nroute:   %s\n", orDash(st.URL), st.Label, st.Route)
			online := 0
			for _, d := range devs.Devices {
				if d.Online {
					online++
				}
			}
			fmt.Fprintf(out, "devices: %d paired, %d connected, %d pending invites\n", len(devs.Devices), online, len(devs.Invites))
			return nil
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "print JSON")
	return cmd
}

type relayInviteOptions struct {
	args proto.RelayArgs
	ttl  time.Duration
	exp  time.Duration
}

func relayInviteCmd() *cobra.Command {
	var o relayInviteOptions
	cmd := &cobra.Command{
		Use:   "invite",
		Short: "Print a one-time invite (QR + link) and wait for a device to pair",
		Long: `Print a one-time invite as a terminal QR and an agentd://relay link, then wait
until a device redeems it and ask for approval ([y/N], default no). The code is
single use and expires (--ttl, default 10m). A guest invite (--guest) is for
someone else: it needs --sessions, never gets admin and expires (--expires,
default 24h). Ctrl-C cancels the invite.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			o.args.TTLSec = int(o.ttl / time.Second)
			o.args.Expires = int(o.exp / time.Second)
			ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
			defer stop()
			return runRelayInvite(ctx, o.args, cmd.InOrStdin(), cmd.OutOrStdout(), cmd.ErrOrStderr(), connect)
		},
	}
	f := cmd.Flags()
	f.StringVar(&o.args.Name, "name", "", "device name (default: the name the device sends)")
	f.StringVar(&o.args.Scope, "scope", "", "view, operate or admin (default operate, capped by [relay] max_scope)")
	f.StringSliceVar(&o.args.Sessions, "sessions", nil, "session globs the device may see (default all), e.g. 'codex@web*'")
	f.BoolVar(&o.args.Guest, "guest", false, "invite someone else to some sessions (expires, no mailbox, no admin)")
	f.DurationVar(&o.ttl, "ttl", 10*time.Minute, "how long the invite stays valid (max 1h)")
	f.DurationVar(&o.exp, "expires", 0, "device access lifetime (guests: default 24h, max 30 days; owners: none)")
	f.BoolVar(&o.args.NoApprove, "no-approve", false, "pair the first device that redeems the invite without asking")
	return cmd
}

func runRelayInvite(ctx context.Context, a proto.RelayArgs, in io.Reader, out, errw io.Writer, dial func() (*client.Client, error)) error {
	c, err := dial()
	if err != nil {
		return err
	}
	defer c.Close()
	a.Action = proto.RelayInvite
	res, err := c.Relay(a)
	if err != nil {
		return err
	}
	inv := res.Invite
	// An invite can be created while the host is not linked to the relay;
	// no device can redeem it then, so say it before printing the QR.
	if st, serr := c.Relay(proto.RelayArgs{Action: proto.RelayStatus}); serr == nil && st.State != "online" {
		why := st.Error
		if why == "" {
			why = "no error reported yet"
		}
		fmt.Fprintf(errw, "warning: this host is not linked to the relay (state %s: %s).\n"+
			"         Devices cannot use the invite until the link is up; see 'agentd relay status'.\n", orDash(st.State), why)
	}
	if a.NoApprove {
		fmt.Fprintln(errw, "warning: --no-approve: the first device to redeem the invite is paired without confirmation")
	}
	if q, qerr := qrcode.New(inv.Link, qrcode.Low); qerr == nil {
		fmt.Fprintln(out, q.ToSmallString(false))
	} else {
		fmt.Fprintf(errw, "(QR not rendered: %v)\n", qerr)
	}
	scope := inv.Scope
	if len(inv.Sessions) > 0 {
		scope += " on " + strings.Join(inv.Sessions, ", ")
	}
	fmt.Fprintf(out, "Link:  %s\n", inv.Link)
	fmt.Fprintf(out, "Grant: %s (%s); valid until %s. Waiting for a device (Ctrl-C cancels)...\n",
		scope, inv.Kind, inv.ExpiresAt.Local().Format("15:04:05"))

	answers := make(chan bool, 1)
	asked := false
	for {
		select {
		case <-ctx.Done():
			_, _ = c.Relay(proto.RelayArgs{Action: proto.RelayCancel, Invite: inv.ID})
			fmt.Fprintln(out, "\nCancelled.")
			return errors.New("invite cancelled")
		case yes := <-answers:
			if _, err := c.Relay(proto.RelayArgs{Action: proto.RelayApprove, Invite: inv.ID, Accept: yes}); err != nil {
				return err
			}
		default:
		}
		p, err := c.Relay(proto.RelayArgs{Action: proto.RelayPending, Invite: inv.ID, WaitMS: 1000})
		if err != nil {
			return err
		}
		cur := p.Invite
		switch cur.State {
		case "request":
			if !asked && cur.Request != nil {
				asked = true
				fmt.Fprintf(out, "\nDevice %q (%s, id %s) wants %s access. Approve? [y/N] ", cur.Request.Name, orDash(cur.Request.Platform), cur.Request.DeviceID, scope)
				go func() { answers <- readYes(in) }()
			}
		case "paired":
			name, id := "", ""
			if cur.Device != nil {
				name, id = cur.Device.Name, cur.Device.ID
			}
			fmt.Fprintf(out, "Paired: device %s (%s) now has %s access.\n", id, name, scope)
			return nil
		case "denied":
			fmt.Fprintln(out, "Pairing denied.")
			return errors.New("pairing denied")
		case "expired":
			return errors.New("invite expired (or burned after too many wrong attempts)")
		case "cancelled":
			return errors.New("invite cancelled (replaced by newer invites)")
		}
	}
}

func relayDevicesCmd() *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "devices",
		Short: "List paired devices",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := connect()
			if err != nil {
				return err
			}
			defer c.Close()
			res, err := c.Relay(proto.RelayArgs{Action: proto.RelayDevices})
			if err != nil {
				return err
			}
			if asJSON {
				return printJSON(cmd.OutOrStdout(), res.Devices)
			}
			w := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 4, 2, ' ', 0)
			fmt.Fprintln(w, "ID\tNAME\tKIND\tSCOPE\tSESSIONS\tLAST SEEN\tEXPIRES")
			for _, d := range res.Devices {
				seen, exp := "never", "-"
				if d.Online {
					seen = "connected"
				} else if d.LastSeen != nil {
					seen = ago(*d.LastSeen)
				}
				if d.ExpiresAt != nil {
					exp = d.ExpiresAt.Local().Format("2006-01-02 15:04")
				}
				sess := "all"
				if len(d.Sessions) > 0 {
					sess = strings.Join(d.Sessions, ",")
				}
				fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n", d.ID, d.Name, d.Kind, d.Scope, sess, seen, exp)
			}
			return w.Flush()
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "print JSON")
	return cmd
}

func relayRevokeCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "revoke ID|NAME",
		Short: "Revoke a paired device (its open connections are closed at once)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := connect()
			if err != nil {
				return err
			}
			defer c.Close()
			res, err := c.Relay(proto.RelayArgs{Action: proto.RelayRevoke, Device: args[0]})
			if err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Revoked %s (%s).\n", res.Device.ID, res.Device.Name)
			return nil
		},
	}
}
