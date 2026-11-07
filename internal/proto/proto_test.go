package proto

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"strings"
	"testing"
)

func TestRequestRoundTrip(t *testing.T) {
	var buf bytes.Buffer
	c := NewCodec(&buf)
	req, err := NewRequest(OpRun, RunArgs{Name: "a", Cmd: []string{"sh"}, Cols: 80})
	if err != nil {
		t.Fatal(err)
	}
	if err := c.WriteRequest(req); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(buf.String(), `{"op":"run","args":{"name":"a"`) || !strings.HasSuffix(buf.String(), "\n") {
		t.Fatalf("wire = %q", buf.String())
	}
	got, err := c.ReadRequest()
	if err != nil {
		t.Fatal(err)
	}
	var args RunArgs
	if err := got.DecodeArgs(&args); err != nil {
		t.Fatal(err)
	}
	if got.Op != OpRun || args.Name != "a" || args.Cols != 80 || args.Cmd[0] != "sh" {
		t.Fatalf("got %+v %+v", got, args)
	}
}

func TestDecodeArgsUnknownField(t *testing.T) {
	r := Request{Op: OpRm, Args: []byte(`{"name":"x","bogus":1}`)}
	var a NameArgs
	if err := r.DecodeArgs(&a); err == nil {
		t.Fatal("unknown field accepted")
	}
	if err := (Request{Op: OpLs}).DecodeArgs(&a); err != nil {
		t.Fatalf("empty args: %v", err)
	}
}

func TestResponse(t *testing.T) {
	var buf bytes.Buffer
	c := NewCodec(&buf)
	ok, err := OKResponse(DumpResult{Text: "hi"})
	if err != nil {
		t.Fatal(err)
	}
	_ = c.WriteResponse(ok)
	_ = c.WriteResponse(ErrorResponse(errors.New("boom")))
	if want := "{\"ok\":true,\"data\":{\"text\":\"hi\"}}\n{\"ok\":false,\"error\":\"boom\"}\n"; buf.String() != want {
		t.Fatalf("wire = %q", buf.String())
	}
	r1, _ := c.ReadResponse()
	var d DumpResult
	if err := r1.Decode(&d); err != nil || d.Text != "hi" {
		t.Fatalf("decode: %v %+v", err, d)
	}
	r2, _ := c.ReadResponse()
	var re *RemoteError
	if err := r2.Decode(nil); !errors.As(err, &re) || re.Msg != "boom" {
		t.Fatalf("err = %v", err)
	}
	if _, err := c.ReadResponse(); !errors.Is(err, io.EOF) {
		t.Fatalf("eof: %v", err)
	}
}

func TestCodecLongLineAndTruncated(t *testing.T) {
	long := strings.Repeat("x", MaxLineSize+10) + "\n"
	c := NewCodec(&rw{Reader: strings.NewReader(long)})
	if _, err := c.ReadRequest(); !errors.Is(err, ErrLineTooLong) {
		t.Fatalf("err = %v", err)
	}
	c = NewCodec(&rw{Reader: strings.NewReader(`{"op":"ping"`)})
	if _, err := c.ReadRequest(); !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("err = %v", err)
	}
}

type rw struct {
	io.Reader
	io.Writer
}

func TestFrameRoundTrip(t *testing.T) {
	var buf bytes.Buffer
	fw := NewFrameWriter(&buf)
	_ = fw.Data([]byte("hello"))
	_ = fw.Resize(100, 30)
	_ = fw.Exit(-3)
	_ = fw.Detach()
	if !bytes.HasPrefix(buf.Bytes(), []byte{'D', 0, 0, 0, 5, 'h'}) {
		t.Fatalf("wire = % x", buf.Bytes()[:8])
	}
	f, err := ReadFrame(&buf)
	if err != nil || f.Type != FrameData || string(f.Payload) != "hello" {
		t.Fatalf("D: %v %+v", err, f)
	}
	f, _ = ReadFrame(&buf)
	if c, r, err := ParseResize(f.Payload); err != nil || c != 100 || r != 30 || f.Type != FrameResize {
		t.Fatalf("R: %v %d %d", err, c, r)
	}
	f, _ = ReadFrame(&buf)
	if code, err := ParseExit(f.Payload); err != nil || code != -3 {
		t.Fatalf("E: %v %d", err, code)
	}
	f, _ = ReadFrame(&buf)
	if f.Type != FrameDetach || len(f.Payload) != 0 {
		t.Fatalf("X: %+v", f)
	}
	if _, err := ReadFrame(&buf); !errors.Is(err, io.EOF) {
		t.Fatalf("eof: %v", err)
	}
}

func TestFrameDataSplit(t *testing.T) {
	var buf bytes.Buffer
	big := bytes.Repeat([]byte{'a'}, MaxFrameSize+7)
	if err := NewFrameWriter(&buf).Data(big); err != nil {
		t.Fatal(err)
	}
	f1, err := ReadFrame(&buf)
	if err != nil || len(f1.Payload) != MaxFrameSize {
		t.Fatalf("f1: %v %d", err, len(f1.Payload))
	}
	f2, err := ReadFrame(&buf)
	if err != nil || len(f2.Payload) != 7 {
		t.Fatalf("f2: %v %d", err, len(f2.Payload))
	}
	if err := WriteFrame(&buf, FrameData, big); !errors.Is(err, ErrFrameTooLarge) {
		t.Fatalf("oversized write: %v", err)
	}
}

func TestFrameRejects(t *testing.T) {
	hdr := func(typ byte, n uint32, payload ...byte) []byte {
		b := []byte{typ, 0, 0, 0, 0}
		binary.BigEndian.PutUint32(b[1:], n)
		return append(b, payload...)
	}
	cases := map[string]struct {
		in   []byte
		want error
	}{
		"too large":    {hdr('D', MaxFrameSize+1), ErrFrameTooLarge},
		"unknown":      {hdr('Z', 0), ErrUnknownFrame},
		"short resize": {hdr('R', 2, 0, 1), ErrInvalidResize},
		"zero resize":  {hdr('R', 4, 0, 0, 0, 5), ErrInvalidResize},
		"bad exit":     {hdr('E', 2, 'x', 'y'), ErrInvalidExitCode},
		"truncated":    {hdr('D', 10, 'a'), io.ErrUnexpectedEOF},
		"short header": {[]byte{'D', 0}, io.ErrUnexpectedEOF},
		"empty exit":   {hdr('E', 0), ErrInvalidExitCode},
		"huge length":  {hdr('D', 0xffffffff), ErrFrameTooLarge},
	}
	for name, tc := range cases {
		if _, err := ReadFrame(bytes.NewReader(tc.in)); !errors.Is(err, tc.want) {
			t.Errorf("%s: err = %v, want %v", name, err, tc.want)
		}
	}
	if err := WriteFrame(io.Discard, 'Z', nil); !errors.Is(err, ErrUnknownFrame) {
		t.Errorf("unknown write: %v", err)
	}
}

// TestCodecThenFrames checks that frames sent right after a JSON line are not
// lost in the codec's buffer (attach upgrade).
func TestCodecThenFrames(t *testing.T) {
	a, b := net.Pipe()
	defer a.Close()
	defer b.Close()
	go func() {
		c := NewCodec(a)
		ok, _ := OKResponse(nil)
		_ = c.WriteResponse(ok)
		_ = NewFrameWriter(a).Data([]byte("redraw"))
	}()
	c := NewCodec(b)
	if r, err := c.ReadResponse(); err != nil || !r.OK {
		t.Fatalf("resp: %v %+v", err, r)
	}
	f, err := ReadFrame(c.Reader())
	if err != nil || string(f.Payload) != "redraw" {
		t.Fatalf("frame: %v %+v", err, f)
	}
}

func FuzzReadFrame(f *testing.F) {
	var buf bytes.Buffer
	fw := NewFrameWriter(&buf)
	_ = fw.Data([]byte("hello"))
	f.Add(buf.Bytes())
	buf.Reset()
	_ = fw.Resize(80, 24)
	f.Add(buf.Bytes())
	buf.Reset()
	_ = fw.Exit(130)
	f.Add(buf.Bytes())
	f.Add([]byte{'X', 0, 0, 0, 0})
	f.Add([]byte{'D', 0xff, 0xff, 0xff, 0xff})
	f.Fuzz(func(t *testing.T, in []byte) {
		r := bytes.NewReader(in)
		for {
			fr, err := ReadFrame(r)
			if err != nil {
				return
			}
			if len(fr.Payload) > MaxFrameSize {
				t.Fatalf("payload %d > max", len(fr.Payload))
			}
			// A decoded frame must re-encode to the same bytes.
			var out bytes.Buffer
			if err := WriteFrame(&out, fr.Type, fr.Payload); err != nil {
				t.Fatalf("re-encode: %v", err)
			}
			back, err := ReadFrame(&out)
			if err != nil || back.Type != fr.Type || !bytes.Equal(back.Payload, fr.Payload) {
				t.Fatalf("round trip mismatch: %v", err)
			}
		}
	})
}
