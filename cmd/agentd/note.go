package main

import (
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/digiogithub/agentd/internal/client"
	"github.com/digiogithub/agentd/internal/proto"
)

// noteAPI is what `agentd note` needs from the daemon (tests use a fake).
type noteAPI interface {
	Notes(states []string, sinceDigest string) (proto.NotesResult, error)
	NoteAdd(title, body string) (proto.Note, error)
	NoteUpdate(a proto.NoteUpdateArgs) (proto.Note, error)
	NoteRm(id int64) (proto.Note, error)
	NoteAssign(a proto.NoteAssignArgs) (proto.NoteAssignResult, error)
}

var _ noteAPI = (*client.Client)(nil)

// noteCmd is `agentd note`: the human's backlog of notes / future tasks on
// this host (plan docs/plans/agentd/backlog-notes_plan.md).
func noteCmd() *cobra.Command {
	return newNoteCmd(func() (noteAPI, func(), error) {
		c, err := connect()
		if err != nil {
			return nil, nil, err
		}
		return c, func() { _ = c.Close() }, nil
	})
}

func newNoteCmd(open func() (noteAPI, func(), error)) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "note",
		Short: "Keep a backlog of notes and future tasks, and assign them to sessions",
		Long: `The backlog is a list of notes (ideas, future tasks) the daemon keeps on this
host. A note can be assigned to a running session: it then appears in that
session's task list (id n<ID>) and the session gets a mailbox message about it.
Only humans use the backlog; agent sessions are refused.`,
	}
	with := func(fn func(api noteAPI) error) error {
		api, closeFn, err := open()
		if err != nil {
			return err
		}
		defer closeFn()
		return fn(api)
	}

	var body string
	add := &cobra.Command{
		Use:   "add TITLE",
		Short: "Add a note to the backlog (--body - reads the body from stdin)",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			b := body
			if b == "-" {
				data, err := io.ReadAll(io.LimitReader(cmd.InOrStdin(), 1<<20))
				if err != nil {
					return err
				}
				b = string(data)
			}
			return with(func(api noteAPI) error {
				n, err := api.NoteAdd(strings.Join(args, " "), b)
				if err != nil {
					return err
				}
				fmt.Fprintf(cmd.OutOrStdout(), "note #%d added\n", n.ID)
				return nil
			})
		},
	}
	add.Flags().StringVar(&body, "body", "", `the note's body ("-" reads stdin)`)

	var all, asJSON bool
	ls := &cobra.Command{
		Use:   "ls",
		Short: "List the open and assigned notes (--all adds the done ones)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			var states []string
			if !all {
				states = []string{proto.NoteOpen, proto.NoteAssigned}
			}
			return with(func(api noteAPI) error {
				res, err := api.Notes(states, "")
				if err != nil {
					return err
				}
				if asJSON {
					return printJSON(cmd.OutOrStdout(), res)
				}
				return printNotes(cmd.OutOrStdout(), res.Notes)
			})
		},
	}
	ls.Flags().BoolVar(&all, "all", false, "include done notes")
	ls.Flags().BoolVar(&asJSON, "json", false, "print the raw result as JSON")

	var title, ebody string
	var reopen bool
	edit := &cobra.Command{
		Use:   "edit ID",
		Short: "Change a note's title or body, or put it back in the backlog (--reopen)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := parseNoteID(args[0])
			if err != nil {
				return err
			}
			a := proto.NoteUpdateArgs{ID: id}
			if cmd.Flags().Changed("title") {
				a.Title = &title
			}
			if cmd.Flags().Changed("body") {
				a.Body = &ebody
			}
			if reopen {
				open := proto.NoteOpen
				a.State = &open
			}
			if a.Title == nil && a.Body == nil && a.State == nil {
				return errors.New("note edit: give --title, --body or --reopen")
			}
			return with(func(api noteAPI) error {
				n, err := api.NoteUpdate(a)
				if err != nil {
					return err
				}
				fmt.Fprintf(cmd.OutOrStdout(), "note #%d updated (%s)\n", n.ID, n.State)
				return nil
			})
		},
	}
	edit.Flags().StringVar(&title, "title", "", "new title")
	edit.Flags().StringVar(&ebody, "body", "", "new body")
	edit.Flags().BoolVar(&reopen, "reopen", false, "take an assigned or done note back to open")

	rm := &cobra.Command{
		Use:   "rm ID",
		Short: "Remove a note (a task already created from it stays)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := parseNoteID(args[0])
			if err != nil {
				return err
			}
			return with(func(api noteAPI) error {
				n, err := api.NoteRm(id)
				if err != nil {
					return err
				}
				fmt.Fprintf(cmd.OutOrStdout(), "note #%d removed\n", n.ID)
				return nil
			})
		},
	}

	var start, noNotify bool
	assign := &cobra.Command{
		Use:   "assign ID SESSION",
		Short: "Hand a note to a running session as a task (pending, or --start = in progress)",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := parseNoteID(args[0])
			if err != nil {
				return err
			}
			a := proto.NoteAssignArgs{ID: id, Session: args[1], Status: proto.TaskPending}
			if start {
				a.Status = proto.TaskInProgress
			}
			if noNotify {
				f := false
				a.Notify = &f
			}
			return with(func(api noteAPI) error {
				res, err := api.NoteAssign(a)
				if err != nil {
					return err
				}
				out := cmd.OutOrStdout()
				fmt.Fprintf(out, "note #%d assigned to %s as task %s (%s)\n", res.Note.ID, args[1], res.Task.ID, res.Task.Status)
				if res.MessageID != 0 {
					fmt.Fprintf(out, "notice sent: message #%d\n", res.MessageID)
				}
				if res.Warning != "" {
					fmt.Fprintf(cmd.ErrOrStderr(), "warning: %s\n", res.Warning)
				}
				return nil
			})
		},
	}
	assign.Flags().BoolVar(&start, "start", false, "create the task as in_progress instead of pending")
	assign.Flags().BoolVar(&noNotify, "no-notify", false, "do not send the session a mailbox message")

	cmd.AddCommand(add, ls, edit, rm, assign)
	return cmd
}

func parseNoteID(s string) (int64, error) {
	id, err := strconv.ParseInt(strings.TrimPrefix(s, "#"), 10, 64)
	if err != nil || id <= 0 {
		return 0, fmt.Errorf("invalid note id %q", s)
	}
	return id, nil
}

func printNotes(out io.Writer, notes []proto.Note) error {
	if len(notes) == 0 {
		fmt.Fprintln(out, "no notes")
		return nil
	}
	w := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
	fmt.Fprintln(w, "ID\tSTATE\tCREATED\tTITLE")
	for _, n := range notes {
		state := n.State
		if n.State == proto.NoteAssigned || (n.State == proto.NoteDone && n.AssignedTo != "") {
			state += " -> " + n.AssignedTo
		}
		fmt.Fprintf(w, "%d\t%s\t%s\t%s\n", n.ID, state, ago(n.CreatedAt), oneLine(n.Title, 80))
	}
	return w.Flush()
}
