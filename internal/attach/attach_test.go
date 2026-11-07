package attach

import (
	"bytes"
	"io"
	"net"
	"testing"
	"time"

	"github.com/digiogithub/agentd/internal/proto"
)

func TestKeyFilter(t *testing.T) {
	cases := []struct {
		name   string
		feeds  []string
		want   string
		detach bool
	}{
		{"plain", []string{"hello"}, "hello", false},
		{"detach", []string{"ab\x1dd"}, "ab", true},
		{"detach discards rest", []string{"a\x1ddxyz"}, "a", true},
		{"literal prefix", []string{"\x1d\x1d"}, "\x1d", false},
		{"prefix then other", []string{"\x1dx"}, "\x1dx", false},
		{"prefix split across feeds", []string{"a\x1d", "d"}, "a", true},
		{"literal split", []string{"\x1d", "\x1dq"}, "\x1dq", false},
		{"other split", []string{"\x1d", "e"}, "\x1de", false},
		{"double literal then detach", []string{"\x1d\x1d\x1dd"}, "\x1d", true},
		{"uppercase D is not detach", []string{"\x1dD"}, "\x1dD", false},
	}
	for _, tc := range cases {
		var k KeyFilter
		var got []byte
		detach := false
		for _, f := range tc.feeds {
			out, d := k.Feed([]byte(f))
			got = append(got, out...)
			if d {
				detach = true
				break
			}
		}
		if string(got) != tc.want || detach != tc.detach {
			t.Errorf("%s: got %q detach=%v, want %q detach=%v", tc.name, got, detach, tc.want, tc.detach)
		}
	}
	var k KeyFilter
	k.Feed([]byte("x\x1d"))
	if !k.Pending() {
		t.Error("trailing prefix should be pending")
	}
}

// fakeServer reads frames from the client side of a pipe.
func readFrames(t *testing.T, c net.Conn, n int) []proto.Frame {
	t.Helper()
	var out []proto.Frame
	for len(out) < n {
		_ = c.SetReadDeadline(time.Now().Add(5 * time.Second))
		f, err := proto.ReadFrame(c)
		if err != nil {
			t.Fatalf("read frame %d: %v", len(out), err)
		}
		out = append(out, f)
	}
	return out
}

func TestStreamDetach(t *testing.T) {
	cli, srv := net.Pipe()
	defer func() { _ = srv.Close() }()
	inR, inW := io.Pipe()
	var out bytes.Buffer
	resize := make(chan [2]uint16, 1)
	type ret struct {
		res Result
		err error
	}
	done := make(chan ret, 1)
	go func() {
		r, err := Stream(cli, Streams{In: inR, Out: &out, Resize: resize})
		done <- ret{r, err}
	}()

	sfw := proto.NewFrameWriter(srv)
	if err := sfw.Data([]byte("screen")); err != nil {
		t.Fatal(err)
	}
	resize <- [2]uint16{100, 30}
	f := readFrames(t, srv, 1)[0]
	if c, r, _ := proto.ParseResize(f.Payload); f.Type != proto.FrameResize || c != 100 || r != 30 {
		t.Fatalf("resize frame %+v", f)
	}
	go func() { _, _ = inW.Write([]byte("hi\x1d\x1d")) }()
	f = readFrames(t, srv, 1)[0]
	if f.Type != proto.FrameData || string(f.Payload) != "hi\x1d" {
		t.Fatalf("data frame %q", f.Payload)
	}
	go func() { _, _ = inW.Write([]byte("\x1dd")) }()
	f = readFrames(t, srv, 1)[0]
	if f.Type != proto.FrameDetach {
		t.Fatalf("want detach frame, got %+v", f)
	}
	r := <-done
	if r.err != nil || !r.res.Detached {
		t.Fatalf("result %+v %v", r.res, r.err)
	}
	if out.String() != "screen" {
		t.Fatalf("out = %q", out.String())
	}
}

func TestStreamExit(t *testing.T) {
	cli, srv := net.Pipe()
	defer func() { _ = srv.Close() }()
	inR, _ := io.Pipe()
	var out bytes.Buffer
	go func() {
		fw := proto.NewFrameWriter(srv)
		_ = fw.Data([]byte("bye"))
		_ = fw.Exit(42)
	}()
	res, err := Stream(cli, Streams{In: inR, Out: &out})
	if err != nil || !res.Exited || res.ExitCode != 42 {
		t.Fatalf("res %+v err %v", res, err)
	}
	if out.String() != "bye" {
		t.Fatalf("out = %q", out.String())
	}
}
