package proto

import "time"

// OpRelay manages the relay transport (humans only, never from a session):
// status, invites and paired devices. See docs/design/relay-transport.md.
const OpRelay = "relay"

// Relay actions (RelayArgs.Action).
const (
	RelayStatus  = "status"
	RelayInvite  = "invite"
	RelayPending = "pending"
	RelayApprove = "approve"
	RelayCancel  = "cancel"
	RelayDevices = "devices"
	RelayRevoke  = "revoke"
)

// RelayArgs is the argument of op relay.
type RelayArgs struct {
	Action string `json:"action"`

	// invite
	Name      string   `json:"name,omitempty"` // device name hint
	Scope     string   `json:"scope,omitempty"`
	Sessions  []string `json:"sessions,omitempty"`
	Guest     bool     `json:"guest,omitempty"`
	TTLSec    int      `json:"ttl_s,omitempty"`     // invite lifetime
	Expires   int      `json:"expires_s,omitempty"` // device lifetime (guests)
	NoApprove bool     `json:"no_approve,omitempty"`

	// pending / approve / cancel
	Invite string `json:"invite,omitempty"`
	WaitMS int    `json:"wait_ms,omitempty"`
	Accept bool   `json:"accept,omitempty"`

	// revoke
	Device string `json:"device,omitempty"` // id or name
}

// RelayDeviceInfo describes a paired device.
type RelayDeviceInfo struct {
	ID        string     `json:"id"`
	Name      string     `json:"name"`
	Platform  string     `json:"platform,omitempty"`
	Scope     string     `json:"scope"`
	Sessions  []string   `json:"sessions,omitempty"`
	Kind      string     `json:"kind"` // owner | guest
	CreatedAt time.Time  `json:"created_at"`
	ExpiresAt *time.Time `json:"expires_at,omitempty"`
	LastSeen  *time.Time `json:"last_seen,omitempty"`
	Online    bool       `json:"online,omitempty"`
}

// RelayInviteInfo is a live invite.
type RelayInviteInfo struct {
	ID        string    `json:"id"`
	Link      string    `json:"link,omitempty"` // only in the answer to invite
	Scope     string    `json:"scope"`
	Sessions  []string  `json:"sessions,omitempty"`
	Kind      string    `json:"kind"`
	ExpiresAt time.Time `json:"expires_at"`
	// State: waiting (no device yet), request (a device asks for approval),
	// paired, denied, expired, cancelled.
	State   string           `json:"state"`
	Request *RelayRequest    `json:"request,omitempty"`
	Device  *RelayDeviceInfo `json:"device,omitempty"`
}

// RelayRequest is a device asking to pair through an invite.
type RelayRequest struct {
	DeviceID string `json:"device_id"`
	Name     string `json:"name"`
	Platform string `json:"platform,omitempty"`
}

// RelayResult answers op relay.
type RelayResult struct {
	// status
	Enabled bool       `json:"enabled"`
	URL     string     `json:"url,omitempty"`
	Label   string     `json:"label,omitempty"`
	Route   string     `json:"route,omitempty"`
	State   string     `json:"state,omitempty"` // off | connecting | online | offline
	Error   string     `json:"error,omitempty"`
	Since   *time.Time `json:"since,omitempty"`

	Invite  *RelayInviteInfo  `json:"invite,omitempty"`
	Invites []RelayInviteInfo `json:"invites,omitempty"`
	Devices []RelayDeviceInfo `json:"devices,omitempty"`
	Device  *RelayDeviceInfo  `json:"device,omitempty"`
}
