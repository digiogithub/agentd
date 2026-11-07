//go:build linux

package daemon

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"strconv"

	"golang.org/x/sys/unix"
)

// peerCred returns the pid and uid of the process on the other end of a unix
// socket (SO_PEERCRED: the process that called connect).
func peerCred(fd uintptr) (pid, uid int, err error) {
	cred, err := unix.GetsockoptUcred(int(fd), unix.SOL_SOCKET, unix.SO_PEERCRED)
	if err != nil {
		return 0, -1, err
	}
	return int(cred.Pid), int(cred.Uid), nil
}

// parentPID returns the parent pid of pid from /proc/<pid>/stat.
func parentPID(pid int) (int, error) {
	b, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return 0, err
	}
	// Format: "pid (comm) state ppid ...": comm may contain spaces and
	// parentheses, so split after the last ')'.
	i := bytes.LastIndexByte(b, ')')
	if i < 0 {
		return 0, errors.New("malformed stat")
	}
	f := bytes.Fields(b[i+1:])
	if len(f) < 2 {
		return 0, errors.New("malformed stat")
	}
	ppid, err := strconv.Atoi(string(f[1]))
	if err != nil {
		return 0, fmt.Errorf("malformed stat ppid: %w", err)
	}
	return ppid, nil
}
