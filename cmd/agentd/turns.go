package main

import (
	"fmt"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"
)

func turnsCmd() *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "turns NAME",
		Short: "List a session's finished turns (outcome, origin, last line)",
		Long: `List the turns a session finished (each time its status left working), oldest
first, at most 100: outcome (completed, aborted within 15 s of a stop key, exited),
origin (human, act, mail, program: who wrote last before it started) and the last
transcript line. An agent session may read only its own turns unless
[security] agent_view = true.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := connect()
			if err != nil {
				return err
			}
			defer c.Close()
			turns, err := c.Turns(args[0])
			if err != nil {
				return err
			}
			if asJSON {
				return printJSON(cmd.OutOrStdout(), turns)
			}
			w := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 4, 2, ' ', 0)
			fmt.Fprintln(w, "TURN\tOUTCOME\tORIGIN\tENDED\tTOOK\tLAST LINE")
			for _, t := range turns {
				took := "-"
				if !t.StartedAt.IsZero() {
					took = t.EndedAt.Sub(t.StartedAt).Round(100 * time.Millisecond).String()
				}
				fmt.Fprintf(w, "%d\t%s\t%s\t%s\t%s\t%s\n", t.Turn, t.Outcome, t.Origin, ago(t.EndedAt), took, oneLine(t.Message, 60))
			}
			return w.Flush()
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "print JSON")
	return cmd
}
