package proto

import "time"

// Board (op board): everything a dashboard paints for one host in one call.

// BoardArgs is the argument of op board.
type BoardArgs struct {
	// SinceDigest: when it still equals the board's digest the answer is
	// {not_modified, digest} with no sessions.
	SinceDigest string `json:"since_digest,omitempty"`
}

// BoardQuestion is a question an agent asked the human and nobody answered.
type BoardQuestion struct {
	ID        int64     `json:"id"`
	Body      string    `json:"body"`
	Options   []string  `json:"options,omitempty"`
	Task      string    `json:"task,omitempty"`
	CreatedAt time.Time `json:"created_at"`
}

// BoardSession is one session of the board.
type BoardSession struct {
	Name      string          `json:"name"`
	Status    string          `json:"status"`
	Attention bool            `json:"attention"`
	Turn      uint64          `json:"turn"`
	Preview   string          `json:"preview,omitempty"`
	Tasks     []Task          `json:"tasks"`
	Questions []BoardQuestion `json:"questions"`
}

// BoardResult answers op board. UnreadHuman counts the unread messages of the
// caller's mailbox (0 for guest devices).
type BoardResult struct {
	Sessions    []BoardSession `json:"sessions"`
	UnreadHuman int            `json:"unread_human"`
	Digest      string         `json:"digest"`
	NotModified bool           `json:"not_modified,omitempty"`
}
