package proto

// Op act (docs/design/api/daemon-ops.md §3): act on a session's terminal
// through safe, verified paths.

// OpAct is the op name of act.
const OpAct = "act"

// Act actions.
const (
	ActPrompt    = "prompt"
	ActChoose    = "choose"
	ActKeys      = "keys"
	ActInterrupt = "interrupt"
	ActCancel    = "cancel"
)

// MaxActText bounds ActArgs.Text (bytes).
const MaxActText = 64 << 10

// MaxActKeys bounds ActArgs.Keys.
const MaxActKeys = 32

// ActArgs are the arguments of op act.
type ActArgs struct {
	Name   string `json:"name"`
	Action string `json:"action"` // prompt | choose | keys | interrupt | cancel
	// Text is the prompt text (action prompt).
	Text string `json:"text,omitempty"`
	// Submit (prompt): write the submit sequence after the paste; nil = true.
	Submit *bool `json:"submit,omitempty"`
	// OptionID is the option to select (action choose), as in View.Options.
	OptionID string `json:"option_id,omitempty"`
	// Label is the label the client saw for OptionID (recommended).
	Label string `json:"label,omitempty"`
	// Revision is the view revision the client acted on. REQUIRED for choose
	// (missing = invalid); accepted when it is the current revision or the
	// same menu instance has been on screen since it. Needs Epoch.
	Revision uint64 `json:"revision,omitempty"`
	// Epoch is the daemon epoch the revision belongs to (View.Epoch).
	// Required whenever Revision is set; a mismatch is a conflict.
	Epoch string `json:"epoch,omitempty"`
	// Keys are abstract key names (action keys), at most MaxActKeys.
	Keys []string `json:"keys,omitempty"`
	// Client is the caller's self-declared client name ("hc", "cli"...), for
	// the audit log only: never trusted for permissions.
	Client string `json:"client,omitempty"`
	// Force skips the status preconditions (and the recent-keystroke check).
	// It never skips a pending human draft (except for interrupt/cancel),
	// paste verification, option re-verification or permissions.
	Force bool `json:"force,omitempty"`
}

// ActResult answers op act.
type ActResult struct {
	// Revision is the view revision after the action settled (best effort).
	Revision uint64 `json:"revision"`
	// Status is the session status after the action settled.
	Status string `json:"status"`
	// Result: submitted | pasted | selected | keys_sent | interrupted | cancelled.
	Result    string `json:"result"`
	Submitted bool   `json:"submitted,omitempty"` // prompt: submit sequence written
	Warning   string `json:"warning,omitempty"`   // e.g. "human typed during submit delay"
}

// ActConflict is the Data of a conflict error: the fresh state so the client
// can redraw without a second round trip.
type ActConflict struct {
	Revision uint64 `json:"revision"`
	View     View   `json:"view"`
}
