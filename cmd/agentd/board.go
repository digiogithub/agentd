package main

import (
	"fmt"
	"io"
	"strings"

	"github.com/spf13/cobra"

	"github.com/digiogithub/agentd/internal/proto"
)

// boardCmd is `agentd board`: the dashboard of this host in one call.
func boardCmd() *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "board",
		Short: "Show every session's status, tasks and open questions",
		Long: `One block per session: status, current task, task counts and the questions the
agent asked you that are still open. Answer a question with: agentd reply ID TEXT`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := connect()
			if err != nil {
				return err
			}
			defer c.Close()
			res, err := c.Board("")
			if err != nil {
				return err
			}
			if asJSON {
				return printJSON(cmd.OutOrStdout(), res)
			}
			return printBoard(cmd.OutOrStdout(), res)
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "print the raw result as JSON")
	return cmd
}

func printBoard(out io.Writer, res proto.BoardResult) error {
	if len(res.Sessions) == 0 {
		fmt.Fprintln(out, "no sessions")
	}
	for i, s := range res.Sessions {
		if i > 0 {
			fmt.Fprintln(out)
		}
		head := s.Name + "  " + s.Status
		if s.Attention {
			head += "  [attention]"
		}
		fmt.Fprintln(out, head)
		if s.Preview != "" {
			fmt.Fprintf(out, "  > %s\n", oneLine(s.Preview, 100))
		}
		if len(s.Tasks) > 0 {
			var c [5]int
			cur := ""
			for _, t := range s.Tasks {
				switch t.Status {
				case proto.TaskPending:
					c[0]++
				case proto.TaskInProgress:
					c[1]++
					if cur == "" {
						cur = t.Title
					}
				case proto.TaskBlocked:
					c[2]++
				case proto.TaskDone:
					c[3]++
				case proto.TaskCancelled:
					c[4]++
				}
			}
			fmt.Fprintf(out, "  tasks: %d done, %d in progress, %d blocked, %d pending, %d cancelled\n", c[3], c[1], c[2], c[0], c[4])
			if cur != "" {
				fmt.Fprintf(out, "  current: %s\n", oneLine(cur, 100))
			}
		}
		for _, q := range s.Questions {
			fmt.Fprintf(out, "  question #%d: %s\n", q.ID, oneLine(q.Body, 200))
			if len(q.Options) > 0 {
				fmt.Fprintf(out, "    options: %s\n", strings.Join(q.Options, " | "))
			}
			if q.Task != "" {
				fmt.Fprintf(out, "    task: %s\n", q.Task)
			}
			fmt.Fprintf(out, "    answer: agentd reply %d TEXT\n", q.ID)
		}
	}
	fmt.Fprintf(out, "\n%d unread message(s) in your mailbox\n", res.UnreadHuman)
	return nil
}
