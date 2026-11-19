package relaywire

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/flynn/noise"
)

// pipeConn is an in-memory MsgConn pair.
type pipeConn struct {
	in     chan []byte
	out    chan []byte
	closed chan struct{}
	once   *sync.Once
}

func newPipe() (*pipeConn, *pipeConn) {
	a, b := make(chan []byte, 1024), make(chan []byte, 1024)
	closed := make(chan struct{})
	once := &sync.Once{}
	return &pipeConn{in: a, out: b, closed: closed, once: once}, &pipeConn{in: b, out: a, closed: closed, once: once}
}

func (p *pipeConn) ReadMsg(ctx context.Context) ([]byte, error) {
	select {
	case b := <-p.in:
		return b, nil
	case <-p.closed:
		return nil, io.EOF
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (p *pipeConn) WriteMsg(ctx context.Context, b []byte) error {
	select {
	case <-p.closed:
		return io.ErrClosedPipe
	default:
	}
	select {
	case p.out <- append([]byte(nil), b...):
		return nil
	case <-p.closed:
		return io.ErrClosedPipe
	}
}

func (p *pipeConn) Close() error { p.once.Do(func() { close(p.closed) }); return nil }

func TestRouteAndDeviceID(t *testing.T) {
	k, err := GenerateHostKey()
	if err != nil {
		t.Fatal(err)
	}
	r := k.Route()
	if !ValidRoute(r) {
		t.Fatalf("route %q not valid", r)
	}
	if id := DeviceID(k.DH.Public); len(id) != 13 || id[0] != 'd' {
		t.Fatalf("device id %q", id)
	}
	b, _ := json.Marshal(k)
	var k2 HostKey
	if err := json.Unmarshal(b, &k2); err != nil {
		t.Fatal(err)
	}
	if k2.Route() != r || !bytes.Equal(k2.DH.Public, k.DH.Public) {
		t.Fatal("host key round trip changed the key")
	}
}

func TestInviteRoundTrip(t *testing.T) {
	inv := Invite{RelayURL: "https://relay.example.com", Route: strings.Repeat("a", 26), HostKey: bytes.Repeat([]byte{1}, 32),
		ID: "0123456789abcdef", Code: bytes.Repeat([]byte{2}, 32), Label: "box one"}
	got, err := ParseInvite(inv.String())
	if err != nil {
		t.Fatal(err)
	}
	if got.RelayURL != inv.RelayURL || got.Route != inv.Route || got.ID != inv.ID || got.Label != inv.Label ||
		!bytes.Equal(got.Code, inv.Code) || !bytes.Equal(got.HostKey, inv.HostKey) {
		t.Fatalf("round trip: %+v", got)
	}
	for _, bad := range []string{
		"https://relay?v=1",
		strings.Replace(inv.String(), "https%3A%2F%2Frelay.example.com", "http%3A%2F%2Frelay.example.com", 1),
		strings.Replace(inv.String(), "v=1", "v=2", 1),
		strings.Replace(inv.String(), "i=0123456789abcdef", "i=xyz", 1),
	} {
		if _, err := ParseInvite(bad); err == nil {
			t.Errorf("accepted %q", bad)
		}
	}
	if err := CheckRelayURL("http://127.0.0.1:8080"); err != nil {
		t.Errorf("loopback http refused: %v", err)
	}
}

func handshakePair(t *testing.T, psk []byte, hello Hello, decide func([]byte, Hello) Decision) (*Secure, *Secure, Welcome, error, error) {
	t.Helper()
	host, _ := GenerateHostKey()
	dev, _ := GenerateDHKey()
	c, s := newPipe()
	type res struct {
		sec *Secure
		err error
	}
	ch := make(chan res, 1)
	go func() {
		sec, _, _, err := ServerHandshake(context.Background(), s, host.Route(), host.DH, nil, decide)
		ch <- res{sec, err}
	}()
	csec, w, cerr := ClientHandshake(context.Background(), c, host.Route(), host.DH.Public, dev, hello, psk, nil)
	r := <-ch
	return csec, r.sec, w, cerr, r.err
}

func TestHandshakeConnect(t *testing.T) {
	csec, ssec, w, cerr, serr := handshakePair(t, nil, Hello{Mode: ModeConnect, Name: "pixel"}, func(pub []byte, h Hello) Decision {
		if h.Name != "pixel" || h.Mode != ModeConnect {
			t.Errorf("hello %+v", h)
		}
		return Decision{Welcome: Welcome{OK: true, Host: "box", Device: &Device{ID: DeviceID(pub), Scope: "view"}}}
	})
	if cerr != nil || serr != nil {
		t.Fatal(cerr, serr)
	}
	if w.Host != "box" || w.Device.Scope != "view" {
		t.Fatalf("welcome %+v", w)
	}
	if err := csec.Send(context.Background(), []byte("ping")); err != nil {
		t.Fatal(err)
	}
	b, err := ssec.Recv(context.Background())
	if err != nil || string(b) != "ping" {
		t.Fatal(string(b), err)
	}
}

func TestHandshakeRefused(t *testing.T) {
	_, _, _, cerr, serr := handshakePair(t, nil, Hello{Mode: ModeConnect}, func([]byte, Hello) Decision {
		return Decision{Welcome: Welcome{Code: "forbidden", Error: "unknown device"}}
	})
	var re *RemoteError
	if !errors.As(cerr, &re) || re.Code != "forbidden" || serr == nil {
		t.Fatalf("client %v server %v", cerr, serr)
	}
}

func TestHandshakePair(t *testing.T) {
	code := make([]byte, 32)
	_, _ = rand.Read(code)
	decide := func(code []byte) func([]byte, Hello) Decision {
		return func(_ []byte, h Hello) Decision {
			if h.Mode != ModePair || h.Invite != "0123456789abcdef" {
				t.Errorf("hello %+v", h)
			}
			return Decision{PSK: code, Welcome: Welcome{OK: true, Pending: true, Host: "box"}}
		}
	}
	csec, ssec, w, cerr, serr := handshakePair(t, code, Hello{Mode: ModePair, Invite: "0123456789abcdef"}, decide(code))
	if cerr != nil || serr != nil || !w.Pending {
		t.Fatal(cerr, serr, w)
	}
	if err := csec.SendJSON(context.Background(), PairMsg{T: "confirm"}); err != nil {
		t.Fatal(err)
	}
	var m PairMsg
	if err := ssec.RecvJSON(context.Background(), &m); err != nil || m.T != "confirm" {
		t.Fatal(m, err)
	}

	// Wrong code: the client cannot read msg2.
	other := bytes.Repeat([]byte{9}, 32)
	_, _, _, cerr, _ = handshakePair(t, other, Hello{Mode: ModePair, Invite: "0123456789abcdef"}, decide(code))
	if cerr == nil {
		t.Fatal("pairing with a wrong code succeeded")
	}

	// A connect hello inside a psk handshake is refused.
	_, _, _, cerr, _ = handshakePair(t, code, Hello{Mode: ModeConnect}, decide(code))
	if cerr == nil {
		t.Fatal("mode mismatch accepted")
	}
}

func muxPair(t *testing.T) (*Mux, *Mux) {
	t.Helper()
	csec, ssec, _, cerr, serr := handshakePair(t, nil, Hello{Mode: ModeConnect}, func([]byte, Hello) Decision {
		return Decision{Welcome: Welcome{OK: true}}
	})
	if cerr != nil || serr != nil {
		t.Fatal(cerr, serr)
	}
	cm, sm := NewClientMux(csec), NewServerMux(ssec)
	t.Cleanup(func() { _ = cm.Close(); _ = sm.Close() })
	return cm, sm
}

func TestMuxEchoLarge(t *testing.T) {
	cm, sm := muxPair(t)
	go func() {
		for {
			s, err := sm.Accept(context.Background())
			if err != nil {
				return
			}
			go func() { _, _ = io.Copy(s, s); _ = s.Close() }()
		}
	}()
	var wg sync.WaitGroup
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			s, err := cm.Open()
			if err != nil {
				t.Error(err)
				return
			}
			data := make([]byte, 3<<20) // larger than the window: exercises credit
			_, _ = rand.Read(data)
			go func() { _, _ = s.Write(data) }()
			got := make([]byte, len(data))
			if _, err := io.ReadFull(s, got); err != nil {
				t.Error(err)
				return
			}
			if !bytes.Equal(got, data) {
				t.Error("echo mismatch")
			}
			_ = s.Close()
		}()
	}
	wg.Wait()
}

func TestMuxCloseAndDeadline(t *testing.T) {
	cm, sm := muxPair(t)
	s, _ := cm.Open()
	hs, err := sm.Accept(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	_ = s.SetReadDeadline(time.Now().Add(50 * time.Millisecond))
	if _, err := s.Read(make([]byte, 1)); !errors.Is(err, os.ErrDeadlineExceeded) {
		t.Fatalf("deadline: %v", err)
	}
	_ = s.SetReadDeadline(time.Time{})
	_, _ = hs.Write([]byte("bye"))
	_ = hs.Close()
	b, err := io.ReadAll(s)
	if err != nil || string(b) != "bye" {
		t.Fatalf("read after close: %q %v", b, err)
	}
	if _, err := s.Write([]byte("x")); err == nil {
		t.Fatal("write after peer close succeeded")
	}
	_ = cm.Close()
	if _, err := cm.Open(); err == nil {
		t.Fatal("open on a closed mux")
	}
	select {
	case <-sm.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("server mux did not notice the tunnel closing")
	}
}

// TestVectors checks (and with AGENTD_WRITE_VECTORS=1 rewrites) the Noise
// vectors that the Dart client of agentd-app is tested against.
func TestVectors(t *testing.T) {
	type vec struct {
		Name            string `json:"name"`
		Protocol        string `json:"protocol"`
		Prologue        string `json:"prologue_hex"`
		InitStatic      string `json:"init_static_private_hex"`
		InitEphemeral   string `json:"init_ephemeral_private_hex"`
		RespStatic      string `json:"resp_static_private_hex"`
		RespEphemeral   string `json:"resp_ephemeral_private_hex"`
		PSK             string `json:"psk_hex,omitempty"`
		Payload1        string `json:"payload1_hex"`
		Msg1            string `json:"msg1_hex"`
		Payload2        string `json:"payload2_hex"`
		Msg2            string `json:"msg2_hex"`
		TransportC2S    string `json:"transport_c2s_plain_hex"`
		TransportC2SMsg string `json:"transport_c2s_msg_hex"`
		TransportS2C    string `json:"transport_s2c_plain_hex"`
		TransportS2CMsg string `json:"transport_s2c_msg_hex"`
	}
	key := func(seed byte) noise.DHKey {
		k, _ := DHKeyFromPrivate(bytes.Repeat([]byte{seed}, 32))
		return k
	}
	route := strings.Repeat("b", 26)
	var vecs []vec
	for _, withPSK := range []bool{false, true} {
		is, ie, rs, re := key(1), key(2), key(3), key(4)
		v := vec{Name: "IK", Protocol: "Noise_IK_25519_ChaChaPoly_SHA256", Prologue: hex.EncodeToString(Prologue(route)),
			InitStatic: hex.EncodeToString(is.Private), InitEphemeral: hex.EncodeToString(ie.Private),
			RespStatic: hex.EncodeToString(rs.Private), RespEphemeral: hex.EncodeToString(re.Private)}
		p1 := []byte(`{"v":1,"mode":"connect","name":"pixel"}`)
		ci := config(route, true, withPSK)
		cr := config(route, false, withPSK)
		if withPSK {
			psk := bytes.Repeat([]byte{7}, 32)
			ci.PresharedKey, cr.PresharedKey = psk, psk
			v.Name, v.Protocol, v.PSK = "IKpsk2", "Noise_IKpsk2_25519_ChaChaPoly_SHA256", hex.EncodeToString(psk)
			p1 = []byte(`{"v":1,"mode":"pair","invite":"0123456789abcdef","name":"pixel","platform":"android"}`)
		}
		// The ephemeral keys come from the Random source (32 bytes = private key).
		ci.StaticKeypair, ci.PeerStatic, ci.Random = is, rs.Public, bytes.NewReader(ie.Private)
		cr.StaticKeypair, cr.Random = rs, bytes.NewReader(re.Private)
		hi, err := noise.NewHandshakeState(ci)
		if err != nil {
			t.Fatal(err)
		}
		hr, err := noise.NewHandshakeState(cr)
		if err != nil {
			t.Fatal(err)
		}
		m1, _, _, err := hi.WriteMessage(nil, p1)
		if err != nil {
			t.Fatal(err)
		}
		if out, _, _, err := hr.ReadMessage(nil, m1); err != nil || !bytes.Equal(out, p1) {
			t.Fatal("msg1", err)
		}
		p2 := []byte(`{"v":1,"ok":true,"host":"box"}`)
		m2, rcs1, rcs2, err := hr.WriteMessage(nil, p2)
		if err != nil {
			t.Fatal(err)
		}
		out, ics1, ics2, err := hi.ReadMessage(nil, m2)
		if err != nil || !bytes.Equal(out, p2) {
			t.Fatal("msg2", err)
		}
		c2s := []byte{FrameOpen, 0, 0, 0, 1}
		s2c := []byte("{\"t\":\"paired\"}")
		tc, _ := ics1.Encrypt(nil, nil, c2s)
		if got, err := rcs1.Decrypt(nil, nil, tc); err != nil || !bytes.Equal(got, c2s) {
			t.Fatal("transport c2s", err)
		}
		ts, _ := rcs2.Encrypt(nil, nil, s2c)
		if got, err := ics2.Decrypt(nil, nil, ts); err != nil || !bytes.Equal(got, s2c) {
			t.Fatal("transport s2c", err)
		}
		v.Payload1, v.Msg1 = hex.EncodeToString(p1), hex.EncodeToString(m1)
		v.Payload2, v.Msg2 = hex.EncodeToString(p2), hex.EncodeToString(m2)
		v.TransportC2S, v.TransportC2SMsg = hex.EncodeToString(c2s), hex.EncodeToString(tc)
		v.TransportS2C, v.TransportS2CMsg = hex.EncodeToString(s2c), hex.EncodeToString(ts)
		vecs = append(vecs, v)
	}
	got, _ := json.MarshalIndent(vecs, "", "  ")
	got = append(got, '\n')
	const path = "testdata/noise_vectors.json"
	if os.Getenv("AGENTD_WRITE_VECTORS") == "1" {
		_ = os.MkdirAll("testdata", 0o755)
		if err := os.WriteFile(path, got, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%v (run with AGENTD_WRITE_VECTORS=1)", err)
	}
	if !bytes.Equal(want, got) {
		t.Fatal("vectors changed; the Dart client must be re-checked (AGENTD_WRITE_VECTORS=1 rewrites them)")
	}
}
