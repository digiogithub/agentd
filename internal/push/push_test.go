package push

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

var testKey = func() *rsa.PrivateKey {
	k, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		panic(err)
	}
	return k
}()

func credsJSON(t *testing.T, tokenURI string) []byte {
	t.Helper()
	der, err := x509.MarshalPKCS8PrivateKey(testKey)
	if err != nil {
		t.Fatal(err)
	}
	pk := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})
	b, _ := json.Marshal(map[string]string{"type": "service_account", "project_id": "proj-1",
		"client_email": "svc@proj-1.iam.gserviceaccount.com", "private_key": string(pk), "token_uri": tokenURI})
	return b
}

func TestParseCredentials(t *testing.T) {
	c, err := ParseCredentials(credsJSON(t, "https://oauth2.example/token"))
	if err != nil || c.ProjectID != "proj-1" || c.ClientEmail == "" {
		t.Fatalf("got %+v, %v", c, err)
	}
	for name, in := range map[string]string{
		"not json": "x", "wrong type": `{"type":"authorized_user"}`,
		"missing": `{"type":"service_account","project_id":"p"}`,
		"bad pem": `{"type":"service_account","project_id":"p","client_email":"e","private_key":"zzz","token_uri":"https://x"}`,
		"bad uri": `{"type":"service_account","project_id":"p","client_email":"e","private_key":"zzz","token_uri":"ftp://x"}`,
	} {
		if _, err := ParseCredentials([]byte(in)); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

// fakeGoogle serves the OAuth endpoint and FCM.
type fakeGoogle struct {
	t        *testing.T
	srv      *httptest.Server
	mu       sync.Mutex
	tokens   int32
	sends    []string // bodies
	auths    []string
	fcmCodes []int // status per send call, then 200
	fcmBody  string
	assert   []string
}

func newFakeGoogle(t *testing.T) *fakeGoogle {
	f := &fakeGoogle{t: t}
	mux := http.NewServeMux()
	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		f.mu.Lock()
		f.assert = append(f.assert, r.PostForm.Get("assertion"))
		f.mu.Unlock()
		if r.PostForm.Get("grant_type") != "urn:ietf:params:oauth:grant-type:jwt-bearer" {
			w.WriteHeader(400)
			return
		}
		n := atomic.AddInt32(&f.tokens, 1)
		_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "at" + string(rune('0'+n)), "expires_in": 3600})
	})
	mux.HandleFunc("/v1/projects/proj-1/messages:send", func(w http.ResponseWriter, r *http.Request) {
		b := make([]byte, 8192)
		n, _ := r.Body.Read(b)
		f.mu.Lock()
		f.sends = append(f.sends, string(b[:n]))
		f.auths = append(f.auths, r.Header.Get("Authorization"))
		code := 200
		if len(f.fcmCodes) > 0 {
			code, f.fcmCodes = f.fcmCodes[0], f.fcmCodes[1:]
		}
		body := f.fcmBody
		f.mu.Unlock()
		w.WriteHeader(code)
		if code != 200 {
			_, _ = w.Write([]byte(body))
		}
	})
	f.srv = httptest.NewServer(mux)
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeGoogle) client(t *testing.T) *Client {
	c, err := ParseCredentials(credsJSON(t, f.srv.URL+"/token"))
	if err != nil {
		t.Fatal(err)
	}
	cl := NewClient(c)
	cl.Endpoint = f.srv.URL
	cl.Backoff = time.Millisecond
	return cl
}

func TestJWTIsValidRS256(t *testing.T) {
	f := newFakeGoogle(t)
	cl := f.client(t)
	if _, err := cl.Tokens.Token(context.Background()); err != nil {
		t.Fatal(err)
	}
	parts := strings.Split(f.assert[0], ".")
	if len(parts) != 3 {
		t.Fatalf("jwt = %q", f.assert[0])
	}
	dec := func(s string) []byte { b, err := base64.RawURLEncoding.DecodeString(s); must(t, err); return b }
	var hdr map[string]string
	must(t, json.Unmarshal(dec(parts[0]), &hdr))
	if hdr["alg"] != "RS256" || hdr["typ"] != "JWT" {
		t.Fatalf("header %v", hdr)
	}
	var cl2 struct {
		Iss, Scope, Aud string
		Iat, Exp        int64
	}
	var raw map[string]any
	must(t, json.Unmarshal(dec(parts[1]), &raw))
	cl2.Iss, cl2.Scope, cl2.Aud = raw["iss"].(string), raw["scope"].(string), raw["aud"].(string)
	cl2.Iat, cl2.Exp = int64(raw["iat"].(float64)), int64(raw["exp"].(float64))
	if cl2.Iss != "svc@proj-1.iam.gserviceaccount.com" || cl2.Scope != Scope || cl2.Aud != f.srv.URL+"/token" || cl2.Exp-cl2.Iat != 3600 {
		t.Fatalf("claims %+v", cl2)
	}
	sum := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if err := rsa.VerifyPKCS1v15(&testKey.PublicKey, crypto.SHA256, sum[:], dec(parts[2])); err != nil {
		t.Fatalf("signature: %v", err)
	}
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func TestTokenCachedAndRefreshed(t *testing.T) {
	f := newFakeGoogle(t)
	ts := f.client(t).Tokens
	now := time.Now()
	ts.Now = func() time.Time { return now }
	a, _ := ts.Token(context.Background())
	b, _ := ts.Token(context.Background())
	if a != b || f.tokens != 1 {
		t.Fatalf("not cached: %s %s %d", a, b, f.tokens)
	}
	now = now.Add(3600*time.Second - 30*time.Second) // inside the 60 s margin
	c, _ := ts.Token(context.Background())
	if c == a || f.tokens != 2 {
		t.Fatalf("not refreshed: %s %d", c, f.tokens)
	}
}

func TestSendMessageShape(t *testing.T) {
	f := newFakeGoogle(t)
	cl := f.client(t)
	err := cl.Send(context.Background(), Message{Token: "tok", Title: "a@x te pregunta", Body: "h",
		Data: map[string]string{"v": "1", "session": "a@x"}, CollapseKey: "a@x", ChannelID: "agentd_attention", Tag: "h/a@x/question"})
	must(t, err)
	var got struct {
		Message struct {
			Token        string
			Notification map[string]string
			Data         map[string]string
			Android      struct {
				Priority     string
				CollapseKey  string `json:"collapse_key"`
				Notification map[string]string
			}
		}
	}
	must(t, json.Unmarshal([]byte(f.sends[0]), &got))
	m := got.Message
	if m.Token != "tok" || m.Notification["title"] != "a@x te pregunta" || m.Data["v"] != "1" ||
		m.Android.Priority != "HIGH" || m.Android.CollapseKey != "a@x" ||
		m.Android.Notification["channel_id"] != "agentd_attention" || m.Android.Notification["tag"] != "h/a@x/question" {
		t.Fatalf("payload %s", f.sends[0])
	}
	if f.auths[0] != "Bearer at1" {
		t.Fatalf("auth %q", f.auths[0])
	}
}

func TestSendClassification(t *testing.T) {
	cases := []struct {
		name  string
		codes []int
		body  string
		unreg bool
		fail  bool
		sends int
		retry bool
	}{
		{name: "ok", sends: 1},
		{name: "unregistered", codes: []int{404}, body: `{"error":{"status":"NOT_FOUND","details":[{"errorCode":"UNREGISTERED"}]}}`, unreg: true, fail: true, sends: 1},
		{name: "bad token arg", codes: []int{400}, body: `{"error":{"status":"INVALID_ARGUMENT","message":"The registration token is not a valid FCM registration token"}}`, unreg: true, fail: true, sends: 1},
		{name: "other 400", codes: []int{400}, body: `{"error":{"status":"INVALID_ARGUMENT","message":"bad payload"}}`, fail: true, sends: 1},
		{name: "403", codes: []int{403}, body: `{"error":{"status":"PERMISSION_DENIED"}}`, fail: true, sends: 1},
		{name: "retry then ok", codes: []int{503, 429}, sends: 3},
		{name: "retry exhausted", codes: []int{500, 500, 500}, fail: true, sends: 3, retry: true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newFakeGoogle(t)
			f.fcmCodes, f.fcmBody = c.codes, c.body
			err := f.client(t).Send(context.Background(), Message{Token: "tok", Title: "t"})
			if (err != nil) != c.fail || errors.Is(err, ErrUnregistered) != c.unreg || (err != nil && Retryable(err) != c.retry) {
				t.Fatalf("err = %v", err)
			}
			if len(f.sends) != c.sends {
				t.Fatalf("sends = %d, want %d", len(f.sends), c.sends)
			}
			if err != nil && strings.Contains(err.Error(), "tok") && !errors.Is(err, ErrUnregistered) {
				t.Fatalf("error mentions the token: %v", err)
			}
		})
	}
}

func TestSendRefreshesOn401(t *testing.T) {
	f := newFakeGoogle(t)
	f.fcmCodes = []int{401}
	must(t, f.client(t).Send(context.Background(), Message{Token: "tok", Title: "t"}))
	if f.tokens != 2 || len(f.sends) != 2 || f.auths[0] != "Bearer at1" || f.auths[1] != "Bearer at2" {
		t.Fatalf("tokens=%d auths=%v", f.tokens, f.auths)
	}
	// A second 401 in a row is an error, not a loop.
	f2 := newFakeGoogle(t)
	f2.fcmCodes = []int{401, 401}
	if err := f2.client(t).Send(context.Background(), Message{Token: "tok", Title: "t"}); err == nil {
		t.Fatal("double 401 accepted")
	}
}

func TestNoRedirects(t *testing.T) {
	var hit int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { atomic.AddInt32(&hit, 1) }))
	defer target.Close()
	f := newFakeGoogle(t)
	redir := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusFound)
	}))
	defer redir.Close()
	cl := f.client(t)
	cl.Endpoint = redir.URL
	if err := cl.Send(context.Background(), Message{Token: "tok", Title: "t"}); err == nil {
		t.Fatal("redirect followed as success")
	}
	if hit != 0 {
		t.Fatal("redirect target was contacted")
	}
}

func TestLoadCredentialsFile(t *testing.T) {
	p := filepath.Join(t.TempDir(), "k.json")
	must(t, os.WriteFile(p, credsJSON(t, "https://x/token"), 0o600))
	if _, err := LoadCredentials(p); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadCredentials(p + "x"); err == nil {
		t.Fatal("missing accepted")
	}
	_ = url.URL{}
}

func TestStore(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "push")
	p := filepath.Join(dir, "tokens.json")
	s, err := OpenStore(p)
	must(t, err)
	e, err := s.Upsert(Entry{Token: "T1", Platform: "android", Name: "phone", HostRef: "r"})
	must(t, err)
	if e.ID != TokenID("T1") || len(e.ID) != 13 || e.ID[0] != 'p' {
		t.Fatalf("id %q", e.ID)
	}
	if fi, _ := os.Stat(p); fi.Mode().Perm() != 0o600 {
		t.Fatalf("file mode %v", fi.Mode())
	}
	if fi, _ := os.Stat(dir); fi.Mode().Perm() != 0o700 {
		t.Fatalf("dir mode %v", fi.Mode())
	}
	e2, _ := s.Upsert(Entry{Token: "T1", Platform: "android", Name: "renamed"})
	if len(s.List()) != 1 || e2.Name != "renamed" || !e2.CreatedAt.Equal(e.CreatedAt) {
		t.Fatalf("duplicate: %+v", s.List())
	}
	s.MarkError(e.ID, "boom")
	s.MarkOK(e.ID)
	got, _ := s.Get(e.ID)
	if got.LastOKAt == nil || got.LastError != "" {
		t.Fatalf("mark: %+v", got)
	}
	// Persisted.
	s2, err := OpenStore(p)
	must(t, err)
	if len(s2.List()) != 1 {
		t.Fatal("not persisted")
	}
	// Device removal.
	_, _ = s.Upsert(Entry{Token: "T2", Platform: "ios", Name: "d", Device: "dev1"})
	if n, _ := s.RemoveDevice("dev1"); n != 1 || len(s.List()) != 1 {
		t.Fatalf("removeDevice %d %v", n, s.List())
	}
	if n, _ := s.Remove(e.ID); n != 1 || len(s.List()) != 0 {
		t.Fatal("remove")
	}
}

func TestStoreEvictsOldestAndHandlesCorruption(t *testing.T) {
	p := filepath.Join(t.TempDir(), "tokens.json")
	s, _ := OpenStore(p)
	base := time.Now()
	i := 0
	s.now = func() time.Time { i++; return base.Add(time.Duration(i) * time.Second) }
	for n := 0; n < MaxTokens+3; n++ {
		_, err := s.Upsert(Entry{Token: "tok" + string(rune('A'+n)), Platform: "other", Name: "n"})
		must(t, err)
	}
	l := s.List()
	if len(l) != MaxTokens {
		t.Fatalf("len %d", len(l))
	}
	for _, e := range l {
		if e.Token == "tokA" || e.Token == "tokB" || e.Token == "tokC" {
			t.Fatalf("oldest kept: %s", e.Token)
		}
	}
	must(t, os.WriteFile(p, []byte("{broken"), 0o600))
	s3, err := OpenStore(p)
	must(t, err)
	if len(s3.List()) != 0 {
		t.Fatal("corrupt file not reset")
	}
	if _, err := os.Stat(p + ".bad"); err != nil {
		t.Fatal(err)
	}
}
