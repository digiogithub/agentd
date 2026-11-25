// Package push sends push notifications through Firebase Cloud Messaging
// (HTTP v1) with a Google service account, and keeps the registered device
// tokens (plan docs/plans/agentd/push-fcm_plan.md). Stdlib only. Tokens,
// access tokens, the private key and payload text are never logged and never
// part of an error.
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
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"
)

// Scope is the OAuth scope FCM HTTP v1 needs.
const Scope = "https://www.googleapis.com/auth/firebase.messaging"

const (
	maxCredentialsBytes = 64 << 10
	maxResponseBytes    = 16 << 10
	tokenLifetime       = time.Hour
	tokenRefreshMargin  = 60 * time.Second
	requestTimeout      = 10 * time.Second
)

// Credentials is a parsed Google service account key.
type Credentials struct {
	ProjectID   string
	ClientEmail string
	TokenURI    string
	key         *rsa.PrivateKey
}

// ParseCredentials parses a service account JSON. Errors never contain its
// content.
func ParseCredentials(data []byte) (*Credentials, error) {
	var f struct {
		Type        string `json:"type"`
		ProjectID   string `json:"project_id"`
		ClientEmail string `json:"client_email"`
		PrivateKey  string `json:"private_key"`
		TokenURI    string `json:"token_uri"`
	}
	if err := json.Unmarshal(data, &f); err != nil {
		return nil, errors.New("push: credentials are not valid JSON")
	}
	switch {
	case f.Type != "service_account":
		return nil, errors.New(`push: credentials: type must be "service_account"`)
	case f.ProjectID == "" || f.ClientEmail == "" || f.TokenURI == "" || f.PrivateKey == "":
		return nil, errors.New("push: credentials: project_id, client_email, private_key and token_uri are required")
	}
	u, err := url.Parse(f.TokenURI)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
		return nil, errors.New("push: credentials: token_uri is not an http(s) URL")
	}
	block, _ := pem.Decode([]byte(f.PrivateKey))
	if block == nil {
		return nil, errors.New("push: credentials: private_key is not PEM")
	}
	k, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, errors.New("push: credentials: private_key is not PKCS#8")
	}
	rk, ok := k.(*rsa.PrivateKey)
	if !ok {
		return nil, errors.New("push: credentials: private_key is not an RSA key")
	}
	return &Credentials{ProjectID: f.ProjectID, ClientEmail: f.ClientEmail, TokenURI: f.TokenURI, key: rk}, nil
}

// LoadCredentials reads and parses a service account file (capped at 64 KiB).
// The file's permissions are the caller's business (config.CheckCredentialsFile).
func LoadCredentials(path string) (*Credentials, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("push: open credentials: %w", errors.Unwrap(err))
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, maxCredentialsBytes+1))
	if err != nil || len(data) > maxCredentialsBytes {
		return nil, errors.New("push: credentials file unreadable or too large")
	}
	return ParseCredentials(data)
}

func b64(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }

// assertion builds the signed RS256 JWT of the bearer grant.
func (c *Credentials) assertion(now time.Time) (string, error) {
	hdr, _ := json.Marshal(map[string]string{"alg": "RS256", "typ": "JWT"})
	claims, _ := json.Marshal(map[string]any{
		"iss": c.ClientEmail, "scope": Scope, "aud": c.TokenURI,
		"iat": now.Unix(), "exp": now.Add(tokenLifetime).Unix(),
	})
	signing := b64(hdr) + "." + b64(claims)
	sum := sha256.Sum256([]byte(signing))
	sig, err := rsa.SignPKCS1v15(rand.Reader, c.key, crypto.SHA256, sum[:])
	if err != nil {
		return "", errors.New("push: signing the JWT failed")
	}
	return signing + "." + b64(sig), nil
}

// newHTTPClient returns a client with a timeout and no redirects.
func newHTTPClient() *http.Client {
	return &http.Client{
		Timeout:       requestTimeout,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
}

// TokenSource obtains and caches OAuth access tokens (JWT bearer grant).
type TokenSource struct {
	creds *Credentials
	// HTTP is the client for the token endpoint (default: 10 s, no redirects).
	HTTP *http.Client
	// Now is the clock (default time.Now).
	Now func() time.Time

	mu  sync.Mutex
	tok string
	exp time.Time
}

// NewTokenSource returns a token source for creds.
func NewTokenSource(creds *Credentials) *TokenSource {
	return &TokenSource{creds: creds, HTTP: newHTTPClient(), Now: time.Now}
}

// Token returns a cached access token, refreshing it when it expires within
// 60 s.
func (t *TokenSource) Token(ctx context.Context) (string, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	now := t.Now()
	if t.tok != "" && now.Add(tokenRefreshMargin).Before(t.exp) {
		return t.tok, nil
	}
	jwt, err := t.creds.assertion(now)
	if err != nil {
		return "", err
	}
	form := url.Values{"grant_type": {"urn:ietf:params:oauth:grant-type:jwt-bearer"}, "assertion": {jwt}}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, t.creds.TokenURI, strings.NewReader(form.Encode()))
	if err != nil {
		return "", errors.New("push: token request failed")
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := t.HTTP.Do(req)
	if err != nil {
		return "", &netError{what: "token endpoint unreachable"}
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if resp.StatusCode != http.StatusOK {
		e := fmt.Errorf("push: token endpoint answered %d", resp.StatusCode)
		if resp.StatusCode == 429 || resp.StatusCode >= 500 {
			return "", &retryError{err: e}
		}
		return "", e
	}
	var r struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int64  `json:"expires_in"`
	}
	if json.Unmarshal(body, &r) != nil || r.AccessToken == "" {
		return "", errors.New("push: token endpoint answer has no access_token")
	}
	if r.ExpiresIn <= 0 {
		r.ExpiresIn = int64(tokenLifetime / time.Second)
	}
	t.tok, t.exp = r.AccessToken, now.Add(time.Duration(r.ExpiresIn)*time.Second)
	return t.tok, nil
}

// Invalidate drops the cached token if it is still old (after a 401).
func (t *TokenSource) Invalidate(old string) {
	t.mu.Lock()
	if t.tok == old {
		t.tok, t.exp = "", time.Time{}
	}
	t.mu.Unlock()
}
