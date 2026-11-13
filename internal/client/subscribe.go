package client

import (
	"errors"
	"io"
	"net"
	"sync"

	"github.com/digiogithub/agentd/internal/proto"
)

// Subscription is a live event stream from op subscribe. Events arrive on
// Events (closed when the stream ends: daemon bye/shutdown, error or Close);
// Err then says why (nil after Close or a clean bye).
type Subscription struct {
	// Info is the OK response: epoch, last seq and the session snapshot.
	Info   proto.SubscribeResult
	Events <-chan proto.Event

	conn net.Conn
	once sync.Once
	mu   sync.Mutex
	err  error
	done chan struct{}
}

// Subscribe opens an event stream on the connection. The Client must not be
// used for other calls afterwards (it is consumed by the stream); Close the
// Subscription when done. buffer is the capacity of the Events channel
// (<= 0 means 256); a consumer that does not drain it stalls only its own
// reader, and the daemon then resyncs it.
func (c *Client) Subscribe(a proto.SubscribeArgs, buffer int) (*Subscription, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	var info proto.SubscribeResult
	if err := c.callLocked(proto.OpSubscribe, a, &info); err != nil {
		return nil, err
	}
	if buffer <= 0 {
		buffer = 256
	}
	ch := make(chan proto.Event, buffer)
	s := &Subscription{Info: info, Events: ch, conn: c.conn, done: make(chan struct{})}
	go func() {
		defer close(s.done)
		defer close(ch)
		for {
			var ev proto.Event
			if err := c.codec.ReadJSON(&ev); err != nil {
				if !errors.Is(err, io.EOF) && !errors.Is(err, net.ErrClosed) {
					s.setErr(err)
				}
				return
			}
			ch <- ev
			if ev.Kind == proto.EvBye {
				// The daemon ends the stream after bye: release the connection
				// now instead of waiting for Close.
				s.once.Do(func() { _ = s.conn.Close() })
				return
			}
		}
	}()
	return s, nil
}

func (s *Subscription) setErr(err error) {
	s.mu.Lock()
	s.err = err
	s.mu.Unlock()
}

// Err returns the error that ended the stream, if any; valid once Events is
// closed.
func (s *Subscription) Err() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.err
}

// Close ends the stream and waits for the reader to finish. Events still
// buffered are discarded.
func (s *Subscription) Close() error {
	var err error
	s.once.Do(func() { err = s.conn.Close() })
	for range s.Events { // unblock a reader parked on a full channel
	}
	<-s.done
	return err
}
