package client

import (
	"bufio"
	"net"
	"path/filepath"
	"testing"
	"time"

	"github.com/digiogithub/agentd/internal/proto"
)

// Review 18: the Subscription closes its connection when the daemon says bye,
// without waiting for the caller to Close it.
func TestSubscriptionClosesConnOnBye(t *testing.T) {
	sock := filepath.Join(t.TempDir(), "s.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	peerClosed := make(chan error, 1)
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			peerClosed <- err
			return
		}
		defer conn.Close()
		r := bufio.NewReader(conn)
		if _, err := r.ReadBytes('\n'); err != nil { // the subscribe request
			peerClosed <- err
			return
		}
		_, _ = conn.Write([]byte(`{"ok":true,"data":{"epoch":"e","seq":0,"sessions":[]}}` + "\n"))
		_, _ = conn.Write([]byte(`{"kind":"bye"}` + "\n"))
		_ = conn.SetReadDeadline(time.Now().Add(3 * time.Second))
		_, err = r.ReadByte() // returns only when the client closed its end
		peerClosed <- err
	}()
	c, err := Dial(sock)
	if err != nil {
		t.Fatal(err)
	}
	sub, err := c.Subscribe(proto.SubscribeArgs{}, 4)
	if err != nil {
		t.Fatal(err)
	}
	var sawBye bool
	for ev := range sub.Events {
		sawBye = sawBye || ev.Kind == proto.EvBye
	}
	if !sawBye {
		t.Fatal("no bye event")
	}
	select {
	case err := <-peerClosed:
		if ne, ok := err.(net.Error); ok && ne.Timeout() {
			t.Fatal("the client kept the connection open after bye")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("peer never saw the close")
	}
}
