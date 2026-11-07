//go:build !linux && !darwin

package daemon

import "errors"

var errNoPeerCred = errors.New("peer credentials not supported on this platform")

// peerCred is not implemented on this platform: every connection is rejected.
func peerCred(uintptr) (pid, uid int, err error) {
	return 0, -1, errNoPeerCred
}

func parentPID(int) (int, error) { return 0, errNoPeerCred }
