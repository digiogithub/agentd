package app

import "os"

// DeviceName is the default name this computer gives itself when pairing
// with a relay host: its hostname.
func (a *Context) DeviceName() string {
	if n, err := os.Hostname(); err == nil && n != "" {
		return n
	}
	return "agentd-desktop"
}
