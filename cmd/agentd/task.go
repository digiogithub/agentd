package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/digiogithub/agentd/internal/client"
	"github.com/digiogithub/agentd/internal/proto"
)

// taskCmd is `agentd task`: the calling agent session reports its plan
// (writes are refused outside a session); `task ls` reads any list.
func taskCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "task",
		Short: "Report this agent session's task list (plan) to agentd, or read one",
		Long: `An agent session reports its plan to the daemon, which keeps it in memory
for the session's lifetime and shows it to clients (ls, events task.changed).
Only the session itself writes its list (set, add, start, block, done); humans
and other sessions read it with "task ls NAME" (a session reads another one only
with [security] agent_view = true).`,
	}
	var asJSON bool
	var note string
	update := func(use, short, status string) *cobra.Command {
		c := &cobra.Command{
			Use:   use + " ID",
			Short: short,
			Args:  cobra.ExactArgs(1),
			RunE: func(cmd *cobra.Command, args []string) error {
				a := proto.TaskUpdateArgs{ID: args[0], Status: &status}
				if cmd.Flags().Changed("note") {
					a.Note = &note
				}
				return withTasks(cmd, asJSON, func(c *client.Client) (proto.TasksResult, error) { return c.TaskUpdate(a) })
			},
		}
		c.Flags().StringVar(&note, "note", "", "set the task's note (empty clears it)")
		return c
	}

	set := &cobra.Command{
		Use:   "set",
		Short: "Replace the list from stdin: a JSON array of tasks, or one title per line",
		Long: `Replace this session's task list with the one read from stdin: either a JSON
array of {"id"?, "title", "status"?, "note"?} (status pending, in_progress,
blocked, done or cancelled; default pending; missing ids become t1, t2...) or
plain text with one task title per line (all pending). At most 50 tasks.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			tasks, err := parseTaskInput(cmd.InOrStdin())
			if err != nil {
				return err
			}
			return withTasks(cmd, asJSON, func(c *client.Client) (proto.TasksResult, error) { return c.TasksSet(tasks) })
		},
	}
	add := &cobra.Command{
		Use:   "add TITLE",
		Short: "Append a pending task to this session's list",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			title := strings.Join(args, " ")
			return withTasks(cmd, asJSON, func(c *client.Client) (proto.TasksResult, error) {
				who, err := c.Whoami()
				if err != nil {
					return proto.TasksResult{}, err
				}
				if !who.Session {
					return proto.TasksResult{}, errors.New("task add: only an agent session writes its task list")
				}
				cur, err := c.Tasks(who.Identity)
				if err != nil {
					return proto.TasksResult{}, err
				}
				return c.TasksSet(append(cur.Tasks, proto.Task{Title: title}))
			})
		},
	}
	ls := &cobra.Command{
		Use:   "ls [NAME]",
		Short: "Show a session's task list (default: this session)",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return withTasks(cmd, asJSON, func(c *client.Client) (proto.TasksResult, error) {
				name := ""
				if len(args) == 1 {
					name = args[0]
				} else {
					who, err := c.Whoami()
					if err != nil {
						return proto.TasksResult{}, err
					}
					if !who.Session {
						return proto.TasksResult{}, errors.New("task ls: give a session NAME (not inside an agent session)")
					}
					name = who.Identity
				}
				return c.Tasks(name)
			})
		},
	}
	cmd.AddCommand(set, add, ls, taskSyncCmd(),
		update("start", "Mark a task in_progress", proto.TaskInProgress),
		update("block", "Mark a task blocked (optionally with --note)", proto.TaskBlocked),
		update("done", "Mark a task done", proto.TaskDone))
	cmd.PersistentFlags().BoolVar(&asJSON, "json", false, "print JSON")
	return cmd
}

// withTasks dials, runs fn and prints the list it returns.
func withTasks(cmd *cobra.Command, asJSON bool, fn func(c *client.Client) (proto.TasksResult, error)) error {
	c, err := connect()
	if err != nil {
		return err
	}
	defer c.Close()
	res, err := fn(c)
	if err != nil {
		return err
	}
	if asJSON {
		return printJSON(cmd.OutOrStdout(), res)
	}
	return printTasks(cmd.OutOrStdout(), res)
}

func printTasks(out io.Writer, res proto.TasksResult) error {
	if len(res.Tasks) == 0 {
		if res.Revision == 0 {
			fmt.Fprintln(out, "no task list reported")
		} else {
			fmt.Fprintf(out, "empty task list (revision %d)\n", res.Revision)
		}
		return nil
	}
	w := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
	fmt.Fprintln(w, "ID\tSTATUS\tUPDATED\tTITLE")
	for _, t := range res.Tasks {
		title := oneLine(t.Title, 80)
		if t.Note != "" {
			title += " (" + oneLine(t.Note, 60) + ")"
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", t.ID, t.Status, ago(t.UpdatedAt), title)
	}
	fmt.Fprintf(w, "revision %d\n", res.Revision)
	return w.Flush()
}

// parseTaskInput reads a tasks_set list: a JSON array of tasks, or one title
// per non-blank line.
func parseTaskInput(r io.Reader) ([]proto.Task, error) {
	data, err := io.ReadAll(io.LimitReader(r, 1<<20))
	if err != nil {
		return nil, err
	}
	trimmed := bytes.TrimSpace(data)
	if bytes.HasPrefix(trimmed, []byte("[")) {
		var tasks []proto.Task
		if err := json.Unmarshal(trimmed, &tasks); err != nil {
			return nil, fmt.Errorf("task set: stdin is not a JSON array of tasks: %w", err)
		}
		return tasks, nil
	}
	tasks := []proto.Task{}
	sc := bufio.NewScanner(bytes.NewReader(trimmed))
	sc.Buffer(make([]byte, 64*1024), 1<<20)
	for sc.Scan() {
		if line := strings.TrimSpace(sc.Text()); line != "" {
			tasks = append(tasks, proto.Task{Title: line})
		}
	}
	return tasks, sc.Err()
}
