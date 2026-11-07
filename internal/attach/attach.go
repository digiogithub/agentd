// Package attach is the client side of `agentd attach`: it puts the local
// terminal in raw mode, forwards keystrokes and window size changes to the
// daemon as frames, writes the session output to the terminal, and handles
// the detach key (Ctrl-] then d).
package attach

import (
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/signal"
	"sync"
	"syscall"

	"golang.org/x/term"

	"github.com/digiogithub/agentd/internal/proto"
)

// DetachPrefix is the first key of the detach sequence (Ctrl-]).
const DetachPrefix byte = 0x1d

// DetachKey is the key that, after DetachPrefix, detaches.
const DetachKey byte = 'd'

// terminalReset undoes modes a session may have left enabled on the client
// terminal: alternate screen, bracketed paste, mouse tracking, focus events,
// hidden cursor, SGR attributes, scroll region.
const terminalReset = "\x1b[?1049l\x1b[?2004l\x1b[?1000l\x1b[?1002l\x1b[?1003l\x1b[?1006l\x1b[?1015l\x1b[?1004l\x1b[?1l\x1b[?25h\x1b[0m\x1b[r"

// KeyFilter is the detach-key state machine. Feed it stdin bytes; it returns
// the bytes to forward and whether the user asked to detach.
//
//   - Ctrl-] d detaches (bytes after it in the same chunk are discarded);
//   - Ctrl-] Ctrl-] forwards one literal Ctrl-];
//   - Ctrl-] followed by anything else forwards both bytes.
//
// A trailing Ctrl-] is held until the next Feed.
type KeyFilter struct {
	pending bool
}

// Feed processes p.
func (k *KeyFilter) Feed(p []byte) (out []byte, detach bool) {
	out = make([]byte, 0, len(p)+1)
	for _, b := range p {
		if k.pending {
			k.pending = false
			switch b {
			case DetachKey:
				return out, true
			case DetachPrefix:
				out = append(out, DetachPrefix)
			default:
				out = append(out, DetachPrefix, b)
			}
			continue
		}
		if b == DetachPrefix {
			k.pending = true
			continue
		}
		out = append(out, b)
	}
	return out, false
}

// Pending reports whether a Ctrl-] is held.
func (k *KeyFilter) Pending() bool { return k.pending }

// Result is how an attach ended.
type Result struct {
	Detached bool
	Exited   bool
	ExitCode int
}

// Streams are the local endpoints of an attach. Run uses the process's
// stdin/stdout; tests pass pipes.
type Streams struct {
	In  io.Reader
	Out io.Writer
	// Resize, if set, delivers window sizes to send as R frames.
	Resize <-chan [2]uint16
}

// Run attaches the current terminal to conn (from client.Attach) for session
// name. It restores the terminal on return, including on SIGTERM/SIGHUP/SIGQUIT,
// and prints a status line when the session exits or the user detaches.
func Run(conn net.Conn, name string) error {
	inFd, outFd := int(os.Stdin.Fd()), int(os.Stdout.Fd())
	if term.IsTerminal(inFd) {
		old, err := term.MakeRaw(inFd)
		if err != nil {
			return fmt.Errorf("raw mode: %w", err)
		}
		var once sync.Once
		restore := func() { once.Do(func() { _ = term.Restore(inFd, old) }) }
		defer restore()
	}

	resize := make(chan [2]uint16, 1)
	winch := make(chan os.Signal, 1)
	signal.Notify(winch, syscall.SIGWINCH)
	defer signal.Stop(winch)
	go func() {
		for range winch {
			if cols, rows, err := term.GetSize(outFd); err == nil && cols > 0 && rows > 0 {
				select {
				case resize <- [2]uint16{uint16(cols), uint16(rows)}:
				default:
				}
			}
		}
	}()

	// Terminating signals close the connection so Run returns and the
	// deferred restore runs.
	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, syscall.SIGTERM, syscall.SIGHUP, syscall.SIGQUIT, syscall.SIGINT)
	defer signal.Stop(sigs)
	go func() {
		if _, ok := <-sigs; ok {
			_ = conn.Close()
		}
	}()

	res, err := Stream(conn, Streams{In: os.Stdin, Out: os.Stdout, Resize: resize})
	_, _ = io.WriteString(os.Stdout, terminalReset)
	switch {
	case res.Exited:
		_, _ = fmt.Fprintf(os.Stdout, "\r\n[agentd: session exited with code %d]\r\n", res.ExitCode)
	case res.Detached:
		_, _ = fmt.Fprintf(os.Stdout, "\r\n[agentd: detached from %s]\r\n", name)
	case err != nil:
		_, _ = fmt.Fprintf(os.Stdout, "\r\n[agentd: connection lost: %v]\r\n", err)
	}
	return err
}

// Stream pumps frames between conn and s until the session exits, the user
// detaches, or the connection fails. It closes conn before returning. It does
// not touch terminal modes. Note that a goroutine may stay blocked reading
// s.In after Stream returns (stdin cannot be interrupted portably).
func Stream(conn net.Conn, s Streams) (Result, error) {
	defer func() { _ = conn.Close() }()
	fw := proto.NewFrameWriter(conn)

	type ended struct {
		res Result
		err error
	}
	done := make(chan ended, 3)
	quit := make(chan struct{})
	defer close(quit)

	// Server → local output.
	outDone := make(chan struct{})
	go func() {
		defer close(outDone)
		for {
			f, err := proto.ReadFrame(conn)
			if err != nil {
				if errors.Is(err, io.EOF) {
					err = errors.New("daemon closed the connection")
				}
				done <- ended{err: err}
				return
			}
			switch f.Type {
			case proto.FrameData:
				if _, err := s.Out.Write(f.Payload); err != nil {
					done <- ended{err: err}
					return
				}
			case proto.FrameExit:
				code, _ := proto.ParseExit(f.Payload)
				done <- ended{res: Result{Exited: true, ExitCode: code}}
				return
			case proto.FrameDetach:
				done <- ended{res: Result{Detached: true}}
				return
			}
		}
	}()

	// Local input → server.
	go func() {
		var kf KeyFilter
		buf := make([]byte, 4096)
		for {
			n, err := s.In.Read(buf)
			if n > 0 {
				out, detach := kf.Feed(buf[:n])
				if len(out) > 0 {
					if werr := fw.Data(out); werr != nil {
						done <- ended{err: werr}
						return
					}
				}
				if detach {
					_ = fw.Detach()
					done <- ended{res: Result{Detached: true}}
					return
				}
			}
			if err != nil {
				if errors.Is(err, io.EOF) {
					// Input closed (e.g. piped stdin): detach cleanly.
					_ = fw.Detach()
					done <- ended{res: Result{Detached: true}}
				} else {
					done <- ended{err: err}
				}
				return
			}
		}
	}()

	// Window size changes.
	if s.Resize != nil {
		go func() {
			for {
				select {
				case sz := <-s.Resize:
					if err := fw.Resize(sz[0], sz[1]); err != nil {
						return
					}
				case <-quit:
					return
				}
			}
		}()
	}

	e := <-done
	// Stop the output pump before returning so nothing is written to s.Out
	// after Stream returns.
	_ = conn.Close()
	<-outDone
	return e.res, e.err
}
