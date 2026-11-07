//go:build darwin

package daemon

import (
	"golang.org/x/sys/unix"
)

// peerCred returns the pid (LOCAL_PEERPID, best effort: 0 if unavailable)
// and uid (LOCAL_PEERCRED) of the process on the other end of a unix socket.
func peerCred(fd uintptr) (pid, uid int, err error) {
	cred, err := unix.GetsockoptXucred(int(fd), unix.SOL_LOCAL, unix.LOCAL_PEERCRED)
	if err != nil {
		return 0, -1, err
	}
	if p, perr := unix.GetsockoptInt(int(fd), unix.SOL_LOCAL, unix.LOCAL_PEERPID); perr == nil {
		pid = p
	}
	return pid, int(cred.Uid), nil
}

// parentPID returns the parent pid of pid (sysctl kern.proc.pid).
func parentPID(pid int) (int, error) {
	kp, err := unix.SysctlKinfoProc("kern.proc.pid", pid)
	if err != nil {
		return 0, err
	}
	return int(kp.Eproc.Ppid), nil
}
