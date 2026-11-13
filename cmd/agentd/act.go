package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/digiogithub/agentd/internal/proto"
)

// actCmd is `agentd act NAME ACTION [ARGS...]` (op act).
func actCmd() *cobra.Command {
	return newActCmd(func(a proto.ActArgs) (proto.ActResult, error) {
		c, err := connect()
		if err != nil {
			return proto.ActResult{}, err
		}
		defer c.Close()
		return c.Act(a)
	})
}

// newActCmd builds the act command around do (the daemon call).
func newActCmd(do func(proto.ActArgs) (proto.ActResult, error)) *cobra.Command {
	var (
		force, noSubmit, asJSON bool
		label                   string
		revision                uint64
		epoch                   string
	)
	cmd := &cobra.Command{
		Use:   "act NAME ACTION [ARGS...]",
		Short: "Act on a session: prompt TEXT | choose OPTION_ID | keys KEY... | interrupt | cancel",
		Long: `Act on a session's terminal through verified paths.

  agentd act NAME prompt TEXT...      paste TEXT into the input line and submit
  agentd act NAME choose OPTION_ID    select an option of the session's view (needs --revision and --epoch
                                      from "agentd view --json")
  agentd act NAME keys KEY...         send abstract keys (enter esc tab shift-tab up down left right
                                      home end pgup pgdown backspace delete ctrl-c ctrl-d ctrl-l space y n 0-9)
  agentd act NAME interrupt           send the profile's interrupt key (working or waiting sessions)
  agentd act NAME cancel              send the profile's cancel key (esc)

Refused with a coded error (busy, not_ready, conflict...) rather than guessed.
--force skips the status preconditions, never a pending human draft.`,
		Args: cobra.MinimumNArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			a := proto.ActArgs{Name: args[0], Action: args[1], Force: force}
			rest := args[2:]
			switch a.Action {
			case proto.ActPrompt:
				if len(rest) == 0 {
					return errors.New("act prompt: missing TEXT")
				}
				a.Text = strings.Join(rest, " ")
				if a.Text == "-" {
					b, err := io.ReadAll(os.Stdin)
					if err != nil {
						return err
					}
					a.Text = string(b)
				}
				if noSubmit {
					f := false
					a.Submit = &f
				}
			case proto.ActChoose:
				if len(rest) != 1 {
					return errors.New("act choose: want exactly one OPTION_ID")
				}
				a.OptionID, a.Label, a.Revision, a.Epoch = rest[0], label, revision, epoch
			case proto.ActKeys:
				a.Keys = rest
			case proto.ActInterrupt, proto.ActCancel:
				if len(rest) != 0 {
					return fmt.Errorf("act %s takes no arguments", a.Action)
				}
			default:
				return fmt.Errorf("unknown action %q (want prompt, choose, keys, interrupt or cancel)", a.Action)
			}
			res, err := do(a)
			if err != nil {
				var re *proto.RemoteError
				if asJSON && errors.As(err, &re) {
					_ = printJSON(os.Stdout, map[string]any{"ok": false, "code": re.Code, "error": re.Msg, "data": re.Data})
				}
				return err
			}
			if asJSON {
				return printJSON(os.Stdout, res)
			}
			fmt.Printf("%s: status %s, revision %d\n", res.Result, res.Status, res.Revision)
			if res.Warning != "" {
				fmt.Fprintf(os.Stderr, "warning: %s\n", res.Warning)
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&force, "force", false, "skip status preconditions (never a pending human draft)")
	cmd.Flags().BoolVar(&noSubmit, "no-submit", false, "prompt: paste without submitting")
	cmd.Flags().StringVar(&label, "label", "", "choose: expected label of the option")
	cmd.Flags().Uint64Var(&revision, "revision", 0, "choose: view revision the option id came from (required, with --epoch)")
	cmd.Flags().StringVar(&epoch, "epoch", "", "choose: daemon epoch of the view (required with --revision)")
	cmd.Flags().BoolVar(&asJSON, "json", false, "print the result (or error data) as JSON")
	return cmd
}
