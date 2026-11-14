package main

import (
	"fmt"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/digiogithub/agentd/internal/proto"
)

// Shared work queues: `agentd send queue:NAME ...` posts, `agentd claim`
// takes an item, `agentd queue` lists.

func claimCmd() *cobra.Command {
	return newClaimCmd(func(a proto.ClaimArgs) (proto.Message, error) {
		c, err := connect()
		if err != nil {
			return proto.Message{}, err
		}
		defer c.Close()
		return c.Claim(a)
	})
}

// newClaimCmd builds the claim command around do (the daemon call), so the
// argument handling can be tested without a daemon.
func newClaimCmd(do func(proto.ClaimArgs) (proto.Message, error)) *cobra.Command {
	var asJSON, inject bool
	cmd := &cobra.Command{
		Use:   "claim QUEUE [ID]",
		Short: "Take the oldest (or the given) unclaimed item of a work queue (sessions only)",
		Long: `Claim an item of the work queue QUEUE (posted with 'agentd send queue:QUEUE ...')
for the session this command runs in. The first claim wins; the item becomes
a normal message to the session (read, reply to the original sender). It is
printed and counts as fetched; --inject instead leaves it queued, so agentd
types its notice into the session when it is idle. [claim] in config.toml
decides which sessions may claim from a queue (default: any session).`,
		Args: cobra.RangeArgs(1, 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			a := proto.ClaimArgs{Queue: strings.TrimPrefix(args[0], "queue:"), Inject: inject}
			if len(args) == 2 {
				id, err := parseID(args[1])
				if err != nil {
					return err
				}
				a.ID = id
			}
			m, err := do(a)
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			if asJSON {
				return printJSON(out, m)
			}
			fmt.Fprintf(out, "claimed #%d from queue:%s (posted by %s)\n", m.ID, m.Queue, m.Sender)
			if inject {
				fmt.Fprintln(out, "queued for delivery to this session")
				return nil
			}
			if m.Subject != "" {
				fmt.Fprintf(out, "Subject: %s\n", m.Subject)
			}
			fmt.Fprintf(out, "\n%s\n", strings.TrimRight(m.Body, "\n"))
			return nil
		},
	}
	cmd.Flags().BoolVar(&inject, "inject", false, "leave the item queued so it is typed into the session like any message")
	cmd.Flags().BoolVar(&asJSON, "json", false, "JSON output")
	return cmd
}

func queueCmd() *cobra.Command {
	return newQueueCmd(func(a proto.QueueArgs) (proto.QueueResult, error) {
		c, err := connect()
		if err != nil {
			return proto.QueueResult{}, err
		}
		defer c.Close()
		return c.Queue(a)
	})
}

// newQueueCmd builds the queue command around do (the daemon call).
func newQueueCmd(do func(proto.QueueArgs) (proto.QueueResult, error)) *cobra.Command {
	var asJSON bool
	var a proto.QueueArgs
	cmd := &cobra.Command{
		Use:   "queue [NAME]",
		Short: "List the unclaimed items of a work queue (or every non-empty queue)",
		Long: `List the unclaimed items of the work queue NAME, oldest first, or without NAME
every queue that has unclaimed items. Bodies are shown only with --bodies.
Humans may list any queue; a session only the queues it may claim from.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 1 {
				a.Name = strings.TrimPrefix(args[0], "queue:")
			}
			res, err := do(a)
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			if asJSON {
				return printJSON(out, res)
			}
			tw := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
			if a.Name == "" {
				if len(res.Queues) == 0 {
					fmt.Fprintln(out, "no queue has unclaimed items")
					return nil
				}
				fmt.Fprintln(tw, "QUEUE\tUNCLAIMED")
				for _, q := range res.Queues {
					fmt.Fprintf(tw, "queue:%s\t%d\n", q.Name, q.Unclaimed)
				}
				return tw.Flush()
			}
			if res.NotModified {
				fmt.Fprintln(out, "not modified")
				return nil
			}
			if len(res.Items) == 0 {
				fmt.Fprintf(out, "queue:%s has no unclaimed items\n", a.Name)
				return nil
			}
			fmt.Fprintln(tw, "ID\tFROM\tAGE\tSUBJECT")
			for _, m := range res.Items {
				text := m.Subject
				if a.Bodies {
					text = strings.TrimSpace(strings.Join([]string{m.Subject, m.Body}, " "))
				}
				fmt.Fprintf(tw, "%d\t%s\t%s\t%s\n", m.ID, m.Sender, ago(m.CreatedAt), oneLine(text, 60))
			}
			return tw.Flush()
		},
	}
	cmd.Flags().BoolVar(&a.Bodies, "bodies", false, "include the message bodies")
	cmd.Flags().StringVar(&a.SinceDigest, "since-digest", "", "print \"not modified\" if the listing still has this digest (see --json)")
	cmd.Flags().BoolVar(&asJSON, "json", false, "JSON output (includes the digest)")
	return cmd
}
