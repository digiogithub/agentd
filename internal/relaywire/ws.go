package relaywire

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/coder/websocket"
)

// ReadLimit is the largest WebSocket message accepted on any relay socket.
const ReadLimit = 70 << 10

// WS adapts a WebSocket to MsgConn (binary messages).
type WS struct {
	C *websocket.Conn
}

// NewWS wraps c and sets the read limit.
func NewWS(c *websocket.Conn) *WS {
	c.SetReadLimit(ReadLimit)
	return &WS{C: c}
}

// ReadMsg reads one binary message.
func (w *WS) ReadMsg(ctx context.Context) ([]byte, error) {
	typ, b, err := w.C.Read(ctx)
	if err != nil {
		return nil, err
	}
	if typ != websocket.MessageBinary {
		return nil, errors.New("relay: unexpected text message")
	}
	return b, nil
}

// WriteMsg writes one binary message.
func (w *WS) WriteMsg(ctx context.Context, b []byte) error {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	return w.C.Write(ctx, websocket.MessageBinary, b)
}

// Close closes the socket normally.
func (w *WS) Close() error {
	return w.C.Close(websocket.StatusNormalClosure, "")
}

// KeepAlive pings the peer every interval until ctx ends or a ping fails
// (then the socket is closed, which ends readers).
func KeepAlive(ctx context.Context, c *websocket.Conn, interval time.Duration) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			pctx, cancel := context.WithTimeout(ctx, interval)
			err := c.Ping(pctx)
			cancel()
			if err != nil {
				_ = c.CloseNow()
				return
			}
		}
	}
}

// Dial opens a WebSocket to a relay endpoint with the protocol subprotocol.
func Dial(ctx context.Context, url string, header http.Header) (*websocket.Conn, error) {
	c, _, err := websocket.Dial(ctx, url, &websocket.DialOptions{
		Subprotocols: []string{Subprotocol},
		HTTPHeader:   header,
	})
	if err != nil {
		return nil, err
	}
	c.SetReadLimit(ReadLimit)
	return c, nil
}

// Close codes the relay uses on client tunnels.
const (
	StatusHostOffline   websocket.StatusCode = 4404
	StatusHostNoAnswer  websocket.StatusCode = 4504
	StatusRateLimited   websocket.StatusCode = 4429
	StatusTooManyTunnel websocket.StatusCode = 4503
)

// ErrHostOffline is returned by DialTunnel when the relay has no link for the route.
var ErrHostOffline = errors.New("relay: host is not connected to the relay")

// DialTunnel opens a client tunnel to route through the relay at base and
// returns it as a MsgConn. It does not run the handshake.
func DialTunnel(ctx context.Context, base, route string) (*WS, error) {
	u, err := Endpoint(base, "/v1/connect", map[string][]string{"route": {route}})
	if err != nil {
		return nil, err
	}
	c, err := Dial(ctx, u, nil)
	if err != nil {
		return nil, err
	}
	return NewWS(c), nil
}

// TunnelError maps a close status of a client tunnel to a clear error.
func TunnelError(err error) error {
	switch websocket.CloseStatus(err) {
	case StatusHostOffline:
		return ErrHostOffline
	case StatusHostNoAnswer:
		return errors.New("relay: the host did not answer")
	case StatusRateLimited:
		return errors.New("relay: too many connection attempts, retry later")
	case StatusTooManyTunnel:
		return errors.New("relay: too many tunnels to this host")
	}
	return err
}
