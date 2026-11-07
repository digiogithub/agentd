// Package version holds the agentd build version.
package version

// Version is the agentd version. Release builds set it from the git tag (without the leading "v") with
//
//	-ldflags "-X github.com/digiogithub/agentd/internal/version.Version=1.2.3"
//
// Development builds report 0.0.0-dev.
var Version = "0.0.0-dev"
