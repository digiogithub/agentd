package main

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/digiogithub/agentd/internal/mailbox"
	"github.com/digiogithub/agentd/internal/proto"
)

// `agentd ask` posts a question to a human as a mailbox message (kind
// question); the human's answer is an ordinary reply, injected into the
// session by the delivery loop.

func askCmd() *cobra.Command {
	return newAskCmd(func(a proto.SendArgs) (proto.Message, error) {
		c, err := connect()
		if err != nil {
			return proto.Message{}, err
		}
		defer c.Close()
		return c.Send(a)
	})
}

// newAskCmd builds the ask command around do (the daemon call), so argument
// handling can be tested without a daemon.
func newAskCmd(do func(proto.SendArgs) (proto.Message, error)) *cobra.Command {
	var options []string
	var task, to string
	cmd := &cobra.Command{
		Use:   "ask TEXT...",
		Short: "Ask the human a question, with optional answer options (sessions only)",
		Long: `Ask the human a question as a mailbox message. The answer arrives as an
ordinary reply, typed into this session when it is idle (the reply body is the
chosen option or free text). Only sessions may ask; --to defaults to the human
the daemon runs as (human:<daemon user>). At most 8 --option values of 1..80
characters. --task ties the question to one of your task ids.`,
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			m, err := do(proto.SendArgs{
				To: to, Body: strings.Join(args, " "), Kind: mailbox.KindQuestion, Options: options, Task: task,
			})
			if err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "asked %s in message #%d; the answer will be delivered as a reply\n", m.Recipient, m.ID)
			return nil
		},
	}
	cmd.Flags().StringArrayVar(&options, "option", nil, "an answer option (repeatable, at most 8)")
	cmd.Flags().StringVar(&task, "task", "", "task id this question relates to")
	cmd.Flags().StringVar(&to, "to", "", `recipient "human:<name>" (default: the daemon's own user)`)
	return cmd
}

// questionSuffix describes a question's options for listings ("" otherwise).
func questionSuffix(m proto.Message) string {
	if m.Kind != mailbox.KindQuestion {
		return ""
	}
	s := " [question"
	if m.Task != "" {
		s += " task=" + m.Task
	}
	s += "]"
	if len(m.Options) > 0 {
		s += " options: " + strings.Join(m.Options, " | ")
	}
	return s
}
