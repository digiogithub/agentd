package main

import (
	"errors"
	"fmt"
	"io"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/digiogithub/agentd/internal/client"
	"github.com/digiogithub/agentd/internal/proto"
)

// pushAPI is what `agentd push` needs from the daemon (tests use a fake).
type pushAPI interface {
	PushRegister(a proto.PushRegisterArgs) (proto.PushRegisterResult, error)
	PushUnregister(a proto.PushUnregisterArgs) (proto.PushUnregisterResult, error)
	PushLs() (proto.PushLsResult, error)
	PushTest(id string) (proto.PushTestResult, error)
}

var _ pushAPI = (*client.Client)(nil)

// pushCmd is `agentd push`: the FCM push tokens of this host (plan
// docs/plans/agentd/push-fcm_plan.md).
func pushCmd() *cobra.Command {
	return newPushCmd(func() (pushAPI, func(), error) {
		c, err := connect()
		if err != nil {
			return nil, nil, err
		}
		return c, func() { _ = c.Close() }, nil
	})
}

func newPushCmd(open func() (pushAPI, func(), error)) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "push",
		Short: "Manage the push notification tokens of this host (Firebase Cloud Messaging)",
		Long: `Apps register their FCM token with the daemon; when [push] is enabled the daemon
sends a notification to every token when an agent asks you a question or needs
a permission. Only humans use these commands; agent sessions are refused.`,
	}
	with := func(fn func(api pushAPI) error) error {
		api, closeFn, err := open()
		if err != nil {
			return err
		}
		defer closeFn()
		return fn(api)
	}

	var asJSON bool
	ls := &cobra.Command{
		Use:   "ls",
		Short: "List the registered tokens and whether push is active",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return with(func(api pushAPI) error {
				res, err := api.PushLs()
				if err != nil {
					return err
				}
				if asJSON {
					return printJSON(cmd.OutOrStdout(), res)
				}
				return printPush(cmd.OutOrStdout(), res)
			})
		},
	}
	ls.Flags().BoolVar(&asJSON, "json", false, "print the raw result as JSON")

	test := &cobra.Command{
		Use:   "test [ID]",
		Short: "Send a test notification to one token, or to all of them",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id := ""
			if len(args) == 1 {
				id = args[0]
			}
			return with(func(api pushAPI) error {
				res, err := api.PushTest(id)
				if err != nil {
					return err
				}
				out := cmd.OutOrStdout()
				fmt.Fprintf(out, "sent %d\n", res.Sent)
				for _, e := range res.Errors {
					fmt.Fprintf(cmd.ErrOrStderr(), "error: %s\n", e)
				}
				if len(res.Errors) > 0 {
					return fmt.Errorf("%d notification(s) failed", len(res.Errors))
				}
				return nil
			})
		},
	}

	rm := &cobra.Command{
		Use:   "rm ID",
		Short: "Remove a registered token",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return with(func(api pushAPI) error {
				res, err := api.PushUnregister(proto.PushUnregisterArgs{ID: args[0]})
				if err != nil {
					return err
				}
				if res.Removed == 0 {
					return fmt.Errorf("no token %q", args[0])
				}
				fmt.Fprintf(cmd.OutOrStdout(), "token %s removed\n", args[0])
				return nil
			})
		},
	}

	var token, platform, name, hostRef string
	register := &cobra.Command{
		Use:    "register --token T --platform P --name N [--host-ref R]",
		Short:  "Register a token (apps do this themselves; for tests and scripts)",
		Hidden: true,
		Args:   cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if token == "" {
				return errors.New("push register: --token is required")
			}
			return with(func(api pushAPI) error {
				res, err := api.PushRegister(proto.PushRegisterArgs{Token: token, Platform: platform, Name: name, HostRef: hostRef})
				if err != nil {
					return err
				}
				fmt.Fprintf(cmd.OutOrStdout(), "token %s registered\n", res.ID)
				return nil
			})
		},
	}
	register.Flags().StringVar(&token, "token", "", "the FCM registration token")
	register.Flags().StringVar(&platform, "platform", proto.PushOther, "android|ios|macos|other")
	register.Flags().StringVar(&name, "name", "", "device name")
	register.Flags().StringVar(&hostRef, "host-ref", "", "the app's reference for this host")

	cmd.AddCommand(ls, test, rm, register)
	return cmd
}

func printPush(out io.Writer, res proto.PushLsResult) error {
	if res.Enabled {
		fmt.Fprintf(out, "push: on (project %s)\n", res.ProjectID)
	} else {
		fmt.Fprintf(out, "push: off (%s)\n", res.Reason)
	}
	if len(res.Tokens) == 0 {
		fmt.Fprintln(out, "no tokens registered")
		return nil
	}
	w := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
	fmt.Fprintln(w, "ID\tPLATFORM\tNAME\tDEVICE\tREGISTERED\tLAST OK\tLAST ERROR")
	for _, t := range res.Tokens {
		lastOK := "-"
		if t.LastOKAt != nil {
			lastOK = ago(*t.LastOKAt)
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n", t.ID, t.Platform, oneLine(t.Name, 30), dash(t.Device), ago(t.CreatedAt), lastOK, dash(oneLine(t.LastError, 60)))
	}
	return w.Flush()
}

func dash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}
