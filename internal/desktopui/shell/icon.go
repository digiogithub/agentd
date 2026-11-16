package shell

import _ "embed"

// appIcon is the application icon, 512x512 PNG rendered from
// packaging/agentd-desktop.svg.
//
//go:embed icon.png
var appIcon []byte
