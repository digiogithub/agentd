//go:build !linux

package daemon

// procEnvSet is not implemented here (no /proc): the process ancestry check
// is the only session test of the hc listener on this platform.
func procEnvSet(int, string) bool { return false }
