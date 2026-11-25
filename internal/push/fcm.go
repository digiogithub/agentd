package push

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// ErrUnregistered means FCM no longer knows the token: drop it.
var ErrUnregistered = errors.New("push: token is not registered")

// retryError marks a failure worth retrying (429, 5xx, network).
type retryError struct{ err error }

func (e *retryError) Error() string { return e.err.Error() }
func (e *retryError) Unwrap() error { return e.err }

type netError struct{ what string }

func (e *netError) Error() string { return "push: " + e.what }

// Retryable reports whether err is a transient failure (429, 5xx, network).
func Retryable(err error) bool {
	var r *retryError
	var n *netError
	return errors.As(err, &r) || errors.As(err, &n)
}

// DefaultEndpoint is the FCM API base.
const DefaultEndpoint = "https://fcm.googleapis.com"

// Message is one notification for one device token.
type Message struct {
	Token string
	Title string
	Body  string
	// Data is the string map the app receives.
	Data map[string]string
	// CollapseKey, ChannelID and Tag are the Android options.
	CollapseKey string
	ChannelID   string
	Tag         string
	// DataOnly sends a data message: no notification block (nor
	// android.notification); Title, Body and Tag go into the data as "title",
	// "body" and "tag" (when not empty) and the app draws the notification.
	DataOnly bool
	// Normal lowers the Android priority from HIGH to NORMAL.
	Normal bool
}

// Client sends messages through FCM HTTP v1.
type Client struct {
	creds  *Credentials
	Tokens *TokenSource
	// Endpoint is the API base URL (default DefaultEndpoint; tests inject).
	Endpoint string
	// HTTP is the client for FCM (default: 10 s, no redirects).
	HTTP *http.Client
	// Tries is the number of attempts for retryable failures (default 3);
	// Backoff the first wait, doubled each time (default 1 s).
	Tries   int
	Backoff time.Duration
}

// NewClient returns a client for creds.
func NewClient(creds *Credentials) *Client {
	return &Client{creds: creds, Tokens: NewTokenSource(creds), Endpoint: DefaultEndpoint,
		HTTP: newHTTPClient(), Tries: 3, Backoff: time.Second}
}

// ProjectID is the FCM project of the credentials.
func (c *Client) ProjectID() string { return c.creds.ProjectID }

func (m Message) payload() ([]byte, error) {
	android := map[string]any{"priority": "HIGH"}
	if m.Normal {
		android["priority"] = "NORMAL"
	}
	if m.CollapseKey != "" {
		android["collapse_key"] = m.CollapseKey
	}
	if m.DataOnly {
		data := make(map[string]string, len(m.Data)+3)
		for k, v := range m.Data {
			data[k] = v
		}
		for k, v := range map[string]string{"title": m.Title, "body": m.Body, "tag": m.Tag} {
			if v != "" {
				data[k] = v
			}
		}
		msg := map[string]any{"token": m.Token, "android": android}
		if len(data) > 0 {
			msg["data"] = data
		}
		return json.Marshal(map[string]any{"message": msg})
	}
	n := map[string]string{}
	if m.ChannelID != "" {
		n["channel_id"] = m.ChannelID
	}
	if m.Tag != "" {
		n["tag"] = m.Tag
	}
	if len(n) > 0 {
		android["notification"] = n
	}
	msg := map[string]any{
		"token":        m.Token,
		"notification": map[string]string{"title": m.Title, "body": m.Body},
		"android":      android,
	}
	if len(m.Data) > 0 {
		msg["data"] = m.Data
	}
	return json.Marshal(map[string]any{"message": msg})
}

// Send delivers m. It retries transient failures (Tries attempts, doubling
// Backoff) and refreshes the access token once on a 401. ErrUnregistered
// (wrapped) tells the caller to drop the token.
func (c *Client) Send(ctx context.Context, m Message) error {
	body, err := m.payload()
	if err != nil {
		return err
	}
	tries := c.Tries
	if tries < 1 {
		tries = 1
	}
	wait := c.Backoff
	var last error
	for i := 0; i < tries; i++ {
		if i > 0 {
			select {
			case <-time.After(wait):
			case <-ctx.Done():
				return last
			}
			wait *= 2
		}
		last = c.sendOnce(ctx, body)
		if last == nil || !Retryable(last) {
			return last
		}
	}
	return last
}

func (c *Client) sendOnce(ctx context.Context, body []byte) error {
	for refreshed := false; ; refreshed = true {
		tok, err := c.Tokens.Token(ctx)
		if err != nil {
			return err
		}
		status, data, err := c.post(ctx, tok, body)
		if err != nil {
			return err
		}
		if status == http.StatusUnauthorized && !refreshed {
			c.Tokens.Invalidate(tok)
			continue
		}
		return classify(status, data)
	}
}

func (c *Client) post(ctx context.Context, access string, body []byte) (int, []byte, error) {
	u := strings.TrimRight(c.Endpoint, "/") + "/v1/projects/" + url.PathEscape(c.creds.ProjectID) + "/messages:send"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, bytes.NewReader(body))
	if err != nil {
		return 0, nil, errors.New("push: request failed")
	}
	req.Header.Set("Authorization", "Bearer "+access)
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.HTTP.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return 0, nil, ctx.Err()
		}
		return 0, nil, &netError{what: "FCM unreachable"}
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	return resp.StatusCode, data, nil
}

// classify maps an FCM answer onto nil, ErrUnregistered, a retryable error or
// a plain one. Response bodies are parsed for the error status only and never
// quoted.
func classify(status int, data []byte) error {
	if status >= 200 && status < 300 {
		return nil
	}
	var r struct {
		Error struct {
			Status  string `json:"status"`
			Message string `json:"message"`
			Details []struct {
				ErrorCode string `json:"errorCode"`
			} `json:"details"`
		} `json:"error"`
	}
	_ = json.Unmarshal(data, &r)
	code := r.Error.Status
	for _, d := range r.Error.Details {
		if d.ErrorCode == "UNREGISTERED" {
			code = "UNREGISTERED"
		}
	}
	switch {
	case status == http.StatusNotFound && (code == "UNREGISTERED" || code == "NOT_FOUND"),
		code == "UNREGISTERED":
		return ErrUnregistered
	case status == http.StatusBadRequest && code == "INVALID_ARGUMENT" && strings.Contains(strings.ToLower(r.Error.Message), "token"):
		return ErrUnregistered
	}
	e := fmt.Errorf("push: FCM answered %d %s", status, safeCode(code))
	if status == http.StatusTooManyRequests || status >= 500 {
		return &retryError{err: e}
	}
	return e
}

// safeCode keeps only an upper-case status word.
func safeCode(s string) string {
	if len(s) > 40 {
		return ""
	}
	for _, r := range s {
		if (r < 'A' || r > 'Z') && r != '_' {
			return ""
		}
	}
	return s
}
