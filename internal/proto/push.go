package proto

import "time"

// Push notifications through Firebase Cloud Messaging (plan
// docs/plans/agentd/push-fcm_plan.md). Apps register their FCM token with the
// host's daemon; the daemon sends blocking events (a question, a permission
// dialog) straight to FCM. Sessions never use these ops.

// Ops of push notifications.
const (
	OpPushRegister   = "push_register"   // PushRegisterArgs → PushRegisterResult
	OpPushUnregister = "push_unregister" // PushUnregisterArgs → PushUnregisterResult
	OpPushLs         = "push_ls"         // no args → PushLsResult
	OpPushTest       = "push_test"       // PushTestArgs → PushTestResult
)

// Push platforms.
const (
	PushAndroid = "android"
	PushIOS     = "ios"
	PushMacOS   = "macos"
	PushOther   = "other"
)

// PushPlatforms lists every valid platform.
var PushPlatforms = []string{PushAndroid, PushIOS, PushMacOS, PushOther}

// Push delivery modes of an Android token. notification = FCM notification
// message drawn by the system (default); data = data-only message, the app
// draws its own notification (title, body and tag are in the data) and gets
// a "resolved" data push when the question is answered or the dialog closes.
const (
	PushModeNotification = "notification"
	PushModeData         = "data"
)

// PushModes lists the modes this daemon accepts (advertised by push_register).
var PushModes = []string{PushModeNotification, PushModeData}

// PushResolved is the data reason of a push that withdraws an earlier one
// (data mode only): data "resolves" names the earlier reason and "tag" its tag.
const PushResolved = "resolved"

// Push limits.
const (
	MaxPushToken   = 4096 // printable ASCII bytes
	MaxPushName    = 64   // runes
	MaxPushHostRef = 128  // printable ASCII bytes
	MaxPushTokens  = 32   // registered tokens per host
)

// PushRegisterArgs registers (or refreshes) one FCM token.
type PushRegisterArgs struct {
	Token    string `json:"token"`
	Platform string `json:"platform"`
	Name     string `json:"name"`
	HostRef  string `json:"host_ref,omitempty"`
	// Mode is the token's delivery mode (PushModes; android only). Empty =
	// [push] android_mode.
	Mode string `json:"mode,omitempty"`
}

// PushRegisterResult is the answer of push_register: the token's id, the
// mode it gets (the effective one) and every mode this daemon accepts.
type PushRegisterResult struct {
	ID    string   `json:"id"`
	Mode  string   `json:"mode,omitempty"`
	Modes []string `json:"modes,omitempty"`
}

// PushUnregisterArgs names the token to drop by its value or by its id.
type PushUnregisterArgs struct {
	Token string `json:"token,omitempty"`
	ID    string `json:"id,omitempty"`
}

// PushUnregisterResult is the number of tokens removed.
type PushUnregisterResult struct {
	Removed int `json:"removed"`
}

// PushToken is a registered token as listed: never the token itself.
type PushToken struct {
	ID        string     `json:"id"`
	Platform  string     `json:"platform"`
	Name      string     `json:"name"`
	HostRef   string     `json:"host_ref,omitempty"`
	Device    string     `json:"device,omitempty"`
	Mode      string     `json:"mode,omitempty"` // as registered; empty = [push] android_mode
	CreatedAt time.Time  `json:"created_at"`
	LastOKAt  *time.Time `json:"last_ok_at,omitempty"`
	LastError string     `json:"last_error,omitempty"`
}

// PushLsResult lists the registered tokens and whether sending is active.
type PushLsResult struct {
	Tokens    []PushToken `json:"tokens"`
	Enabled   bool        `json:"enabled"`
	ProjectID string      `json:"project_id,omitempty"`
	Reason    string      `json:"reason,omitempty"` // why push is off
}

// PushTestArgs selects one token (empty = every token).
type PushTestArgs struct {
	ID string `json:"id,omitempty"`
}

// PushTestResult counts the test notifications FCM accepted.
type PushTestResult struct {
	Sent   int      `json:"sent"`
	Errors []string `json:"errors"`
}
