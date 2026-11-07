package daemon

import (
	"errors"
	"fmt"
	"net"
	"os"
)

// peerInfo is what the kernel tells about the process that connected.
type peerInfo struct {
	pid int // 0 when unknown
	uid int
}

// peerOf returns the peer credentials of a unix connection.
func peerOf(conn net.Conn) (peerInfo, error) {
	uc, ok := conn.(*net.UnixConn)
	if !ok {
		return peerInfo{uid: -1}, errors.New("not a unix connection")
	}
	raw, err := uc.SyscallConn()
	if err != nil {
		return peerInfo{uid: -1}, err
	}
	p := peerInfo{uid: -1}
	var credErr error
	if err := raw.Control(func(fd uintptr) { p.pid, p.uid, credErr = peerCred(fd) }); err != nil {
		return peerInfo{uid: -1}, err
	}
	if credErr != nil {
		return peerInfo{uid: -1}, fmt.Errorf("peer credentials: %w", credErr)
	}
	return p, nil
}

// checkPeer rejects connections from other users.
func checkPeer(conn net.Conn) error {
	p, err := peerOf(conn)
	if err != nil {
		return err
	}
	if p.uid != os.Getuid() {
		return fmt.Errorf("peer uid %d is not %d", p.uid, os.Getuid())
	}
	return nil
}
