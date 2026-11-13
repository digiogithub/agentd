package main

import (
	"fmt"
	"io"
	"path/filepath"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/digiogithub/agentd/internal/apitoken"
	"github.com/digiogithub/agentd/internal/config"
	"github.com/digiogithub/agentd/internal/httpapi"
)

// devicesDir is <state>/api, where devices.json lives.
func devicesDir() (string, error) {
	d, err := config.StateDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(d, "api"), nil
}

// auditPath is <state>/api/audit.log.
func auditPath() (string, error) {
	d, err := devicesDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(d, "audit.log"), nil
}

func auditMaxBytes(a config.APIConfig) int64 {
	mb := a.AuditMaxMB
	if mb <= 0 {
		mb = config.DefaultAPIAuditMaxMB
	}
	return int64(mb) << 20
}

// auditCLI appends one event made through the CLI (device add / revoke) to
// the gateway's audit log. Failures are reported on stderr, never fatal.
func auditCLI(stderr io.Writer, ev httpapi.AuditEvent) {
	path, err := auditPath()
	if err == nil {
		limit := int64(config.DefaultAPIAuditMaxMB) << 20
		if cfg, cerr := config.LoadDefault(); cerr == nil {
			limit = auditMaxBytes(cfg.API)
		}
		var a *httpapi.AuditFile
		if a, err = httpapi.OpenAuditFile(path, limit); err == nil {
			ev.Device = "cli"
			a.Audit(ev)
			err = a.Close()
		}
	}
	if err != nil {
		fmt.Fprintf(stderr, "warning: audit log not written: %v\n", err)
	}
}

func openDevices() (*apitoken.Store, error) {
	dir, err := devicesDir()
	if err != nil {
		return nil, err
	}
	return apitoken.Open(dir, apitoken.Options{})
}

// devicesCmd is `agentd serve devices [list|revoke|add]`. It works on
// api/devices.json directly (flock), so it runs whether or not `agentd
// serve` is up; a running gateway notices within about a second.
func devicesCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "devices",
		Short: "Manage the device tokens of the HTTP API (list, revoke, add)",
		Args:  cobra.NoArgs,
		RunE:  func(cmd *cobra.Command, _ []string) error { return runDevicesList(cmd.OutOrStdout()) },
	}
	cmd.AddCommand(&cobra.Command{
		Use: "list", Short: "List devices (default)", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error { return runDevicesList(cmd.OutOrStdout()) },
	})
	cmd.AddCommand(&cobra.Command{
		Use: "revoke <id|name>", Short: "Revoke a device: its token stops working and its open streams close", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			s, err := openDevices()
			if err != nil {
				return err
			}
			if s.Err() != nil {
				return s.Err()
			}
			d, err := s.Revoke(args[0])
			if err != nil {
				return err
			}
			auditCLI(cmd.ErrOrStderr(), httpapi.AuditEvent{Action: "device_revoke", Result: "ok",
				Detail: map[string]any{"device_id": d.ID, "name": d.Name}})
			fmt.Fprintf(cmd.OutOrStdout(), "revoked %s (%s)\n", d.ID, d.Name)
			return nil
		},
	})
	var name, scope, ttl string
	add := &cobra.Command{
		Use:   "add --name NAME [--scope read|write|admin] [--ttl 30d]",
		Short: "Create a token without pairing (headless setups); printed once",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			var d time.Duration
			if ttl != "" && ttl != "0" {
				var err error
				if d, err = config.ParseDuration(ttl); err != nil {
					return fmt.Errorf("invalid --ttl: %w", err)
				}
			}
			s, err := openDevices()
			if err != nil {
				return err
			}
			if s.Err() != nil {
				return s.Err()
			}
			dev, tok, err := s.Create(apitoken.CreateArgs{Name: name, Scope: scope, TTL: d})
			if err != nil {
				return err
			}
			auditCLI(cmd.ErrOrStderr(), httpapi.AuditEvent{Action: "device_add", Result: "ok",
				Detail: map[string]any{"device_id": dev.ID, "name": dev.Name, "scope": dev.Scope}})
			// The clear token goes to stdout only, nowhere else.
			fmt.Fprintln(cmd.OutOrStdout(), tok)
			fmt.Fprintf(cmd.ErrOrStderr(), "device %s (%s, scope %s) created; this token is shown only once\n", dev.ID, dev.Name, dev.Scope)
			return nil
		},
	}
	add.Flags().StringVar(&name, "name", "", "device name (required)")
	add.Flags().StringVar(&scope, "scope", apitoken.ScopeRead, "scope: read, write or admin")
	add.Flags().StringVar(&ttl, "ttl", "30d", `lifetime, e.g. "30d" ("0" = no absolute expiry)`)
	_ = add.MarkFlagRequired("name")
	cmd.AddCommand(add)
	return cmd
}

func runDevicesList(out io.Writer) error {
	s, err := openDevices()
	if err != nil {
		return err
	}
	if s.Err() != nil {
		return s.Err()
	}
	devs, err := s.List()
	if err != nil {
		return err
	}
	if len(devs) == 0 {
		fmt.Fprintln(out, "no devices")
		return nil
	}
	tw := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "ID\tNAME\tSCOPE\tCREATED\tLAST SEEN\tEXPIRES\tSTATE")
	for _, d := range devs {
		seen := "never"
		if !d.LastSeenAt.IsZero() {
			seen = d.LastSeenAt.Local().Format("2006-01-02 15:04")
			if d.LastSeenAddr != "" {
				seen += " (" + d.LastSeenAddr + ")"
			}
		}
		exp := "-"
		if !d.ExpiresAt.IsZero() {
			exp = d.ExpiresAt.Local().Format("2006-01-02 15:04")
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n", d.ID, d.Name, d.Scope, d.CreatedAt.Local().Format("2006-01-02 15:04"), seen, exp, s.State(d))
	}
	return tw.Flush()
}
