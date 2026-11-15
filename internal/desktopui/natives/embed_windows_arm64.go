package natives

import "embed"

// files holds libghostty-vt for windows/arm64 when the build fetched it
// (make desktop-natives-windows-arm64); otherwise only the .keep placeholder.
//
//go:embed all:windows-arm64
var files embed.FS

const embedDir = "windows-arm64"
