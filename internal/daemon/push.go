package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"net/url"
	"os"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/digiogithub/agentd/internal/config"
	"github.com/digiogithub/agentd/internal/notify"
	"github.com/digiogithub/agentd/internal/proto"
	"github.com/digiogithub/agentd/internal/push"
	"github.com/digiogithub/agentd/internal/screen"
)

// Push notifications through FCM (plan docs/plans/agentd/push-fcm_plan.md):
// ops push_register / push_unregister / push_ls / push_test over
// internal/push.Store, and an in-process bus tap ([push]) that sends blocking
// events (a question, a permission dialog, optionally more) to every
// registered token. Sessions never use the ops. Tokens, access tokens and
// payload text are never logged.

const (
	pushQueueSize = 256
	pushChannel   = "agentd_attention"

	// Push reasons beyond internal/notify's.
	pushPermission = "permission"
)

// daemonPush is the tap's state. client is nil while push is off; reason says
// why (shown by push_ls).
type daemonPush struct {
	mu      sync.Mutex
	cancel  context.CancelFunc
	done    chan struct{}
	client  *push.Client
	project string
	reason  string
	host    string
	cfg     config.PushConfig

	qmu      sync.Mutex
	queue    []pushJob
	wake     chan struct{}
	lastSent map[string]time.Time
}

type pushJob struct {
	session, reason, text string
	messageID             int64
	at                    time.Time
}

var pushKinds = []string{proto.EvAttentionChanged, proto.EvSessionExited, proto.EvMailQueued, "task.changed"}

// startPush starts the tap when [push] is enabled and the credentials load.
// Any problem leaves it off, logs the cause (never the key) and is reported by
// push_ls.
func (s *Server) startPush() {
	d := &s.pushd
	d.mu.Lock()
	defer d.mu.Unlock()
	if s.Cfg == nil || !s.Cfg.Push.Enabled {
		d.reason = "push.enabled is false"
		return
	}
	if d.cancel != nil {
		return
	}
	cfg := s.Cfg.Push
	fail := func(reason string, err error) {
		d.reason = reason
		s.Log.Error("push disabled", "reason", reason, "err", err)
	}
	path, err := cfg.CredentialsPath()
	if err != nil {
		fail("credentials_file is not a usable path", err)
		return
	}
	if _, err := os.Stat(path); errors.Is(err, fs.ErrNotExist) {
		fail("credentials_file does not exist", err)
		return
	}
	if err := config.CheckCredentialsFile(path); err != nil {
		fail("credentials_file is not a private regular file (chmod 600, owned by you)", err)
		return
	}
	creds, err := push.LoadCredentials(path)
	if err != nil {
		fail("credentials_file is not a valid service account key", err)
		return
	}
	cl := push.NewClient(creds)
	if s.PushEndpoint != "" {
		cl.Endpoint = s.PushEndpoint
	}
	if s.PushBackoff > 0 {
		cl.Backoff = s.PushBackoff
	}
	sub, _ := s.bus.subscribe(eventFilter{kinds: pushKinds}, 0, "")
	if sub == nil {
		return // bus already closed
	}
	host := cfg.HostLabel
	if host == "" {
		host, _ = os.Hostname()
	}
	d.client, d.project, d.reason, d.host, d.cfg = cl, creds.ProjectID, "", host, cfg
	d.wake = make(chan struct{}, 1)
	d.lastSent = map[string]time.Time{}
	ctx, cancel := context.WithCancel(context.Background())
	d.cancel = cancel
	d.done = make(chan struct{})
	wg := sync.WaitGroup{}
	wg.Add(2)
	go func() { // tap
		defer wg.Done()
		defer s.bus.unsubscribe(sub)
		for {
			select {
			case ev := <-sub.ch:
				if job, ok := s.pushEventJob(ev); ok {
					d.offer(job)
				}
			case <-sub.quit:
				cancel()
				return
			case <-ctx.Done():
				return
			}
		}
	}()
	go func() { // worker
		defer wg.Done()
		for {
			job, ok := d.pop()
			if !ok {
				select {
				case <-d.wake:
					continue
				case <-ctx.Done():
					return
				}
			}
			s.pushDeliver(ctx, job)
		}
	}()
	go func() { wg.Wait(); close(d.done) }()
	s.Log.Info("push enabled", "project", creds.ProjectID, "on", cfg.On)
}

// stopPush stops the tap and its worker; Shutdown calls it first, with
// stopNotify. Safe when never started.
func (s *Server) stopPush() {
	d := &s.pushd
	d.mu.Lock()
	cancel, done := d.cancel, d.done
	d.cancel = nil
	d.client = nil
	d.mu.Unlock()
	if cancel == nil {
		return
	}
	cancel()
	<-done
}

// offer queues a job unless its reason is not wanted or it is debounced; a
// full queue drops the oldest job.
func (d *daemonPush) offer(j pushJob) {
	if !slices.Contains(d.cfg.On, j.reason) {
		return
	}
	d.qmu.Lock()
	defer d.qmu.Unlock()
	key := j.session + "\x00" + j.reason
	if db := time.Duration(d.cfg.Debounce); db > 0 {
		if last, ok := d.lastSent[key]; ok && j.at.Sub(last) < db {
			return
		}
	}
	d.lastSent[key] = j.at
	if len(d.queue) >= pushQueueSize {
		d.queue = d.queue[1:]
	}
	d.queue = append(d.queue, j)
	select {
	case d.wake <- struct{}{}:
	default:
	}
}

func (d *daemonPush) pop() (pushJob, bool) {
	d.qmu.Lock()
	defer d.qmu.Unlock()
	if len(d.queue) == 0 {
		return pushJob{}, false
	}
	j := d.queue[0]
	d.queue = d.queue[1:]
	return j, true
}

// pushEventJob maps a bus event onto a push job; ok=false = no push.
func (s *Server) pushEventJob(ev proto.Event) (pushJob, bool) {
	d := &s.pushd
	j := pushJob{session: ev.Session, at: time.Now()}
	switch ev.Kind {
	case proto.EvAttentionChanged:
		var a proto.AttentionChangedData
		if ev.Session == "" || json.Unmarshal(ev.Data, &a) != nil || !a.Attention {
			return j, false
		}
		switch a.Reason {
		case notify.TurnDone:
			j.reason = notify.TurnDone
		case notify.Waiting:
			j.reason = notify.Waiting
			if opts, q := s.pushDialog(ev.Session); opts > 0 {
				j.reason, j.text = pushPermission, q
			}
		default: // bell, notify
			j.reason = notify.Waiting
		}
		return j, true
	case proto.EvSessionExited:
		if ev.Session == "" {
			return j, false
		}
		j.reason = notify.Exited
		return j, true
	case proto.EvMailQueued:
		var m struct {
			ID   int64  `json:"id"`
			From string `json:"from"`
			To   string `json:"to"`
			Kind string `json:"kind"`
		}
		if json.Unmarshal(ev.Data, &m) != nil || m.Kind != "question" || !strings.HasPrefix(m.To, "human:") {
			return j, false
		}
		j.session, j.reason, j.messageID = m.From, notify.Question, m.ID
		if d.cfg.IncludePreview && s.Store != nil {
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			if msg, err := s.Store.Get(ctx, m.ID); err == nil {
				j.text = msg.Body
			}
			cancel()
		}
		return j, true
	case "task.changed":
		var t struct {
			Changed []struct {
				Title  string `json:"title"`
				Status string `json:"status"`
				From   string `json:"from"`
			} `json:"changed"`
		}
		if json.Unmarshal(ev.Data, &t) != nil {
			return j, false
		}
		for _, c := range t.Changed {
			if c.Status == "blocked" && c.From != "blocked" {
				j.reason, j.text = notify.Blocked, c.Title
				return j, true
			}
		}
	}
	return j, false
}

// pushDialog returns how many options the session's current view offers and
// its dialog question.
func (s *Server) pushDialog(session string) (int, string) {
	e, err := s.lookup(session)
	if err != nil || e.view == nil {
		return 0, ""
	}
	v := e.view
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.rev == 0 {
		v.refreshLocked(time.Now())
	}
	q := ""
	if v.cur.Dialog != nil {
		q = v.cur.Dialog.Question
	}
	return len(v.cur.Options), q
}

func pushTitle(session, reason string) string {
	switch reason {
	case notify.Question:
		return session + " te pregunta"
	case pushPermission:
		return session + " pide permiso"
	case notify.Blocked:
		return session + " tiene una tarea bloqueada"
	case notify.Exited:
		return session + " terminó"
	}
	return session + " espera tu respuesta"
}

// pushMessage builds the FCM message of a job for one registered token.
func (d *daemonPush) pushMessage(j pushJob, e push.Entry) push.Message {
	body := d.host
	if d.cfg.IncludePreview {
		if t := notify.CleanText(j.text); t != "" {
			body += " · " + t
		}
	}
	link := "agentd://open?h=" + url.QueryEscape(d.host) + "&s=" + url.QueryEscape(j.session)
	data := map[string]string{"v": "1", "host": d.host, "host_ref": e.HostRef, "session": j.session, "reason": j.reason, "link": link}
	if j.reason == notify.Question && j.messageID > 0 {
		data["message_id"] = itoa(j.messageID)
	}
	return push.Message{
		Token: e.Token, Title: pushTitle(j.session, j.reason), Body: body, Data: data,
		CollapseKey: j.session, ChannelID: pushChannel, Tag: d.host + "/" + j.session + "/" + j.reason,
	}
}

func itoa(n int64) string {
	b, _ := json.Marshal(n)
	return string(b)
}

// pushSend sends m to entry e and records the outcome: an unregistered token
// is removed. Logs name the token id only.
func (s *Server) pushSend(ctx context.Context, cl *push.Client, e push.Entry, m push.Message) error {
	cctx, cancel := context.WithTimeout(ctx, 40*time.Second)
	defer cancel()
	err := cl.Send(cctx, m)
	switch {
	case err == nil:
		s.PushStore.MarkOK(e.ID)
	case errors.Is(err, push.ErrUnregistered):
		_, _ = s.PushStore.Remove(e.ID)
		s.Log.Info("push: token dropped (unregistered)", "token", e.ID)
	default:
		s.PushStore.MarkError(e.ID, err.Error())
		s.Log.Warn("push: send failed", "token", e.ID, "err", err)
	}
	return err
}

func (s *Server) pushDeliver(ctx context.Context, j pushJob) {
	d := &s.pushd
	d.mu.Lock()
	cl := d.client
	d.mu.Unlock()
	if cl == nil || s.PushStore == nil {
		return
	}
	for _, e := range s.PushStore.List() {
		if ctx.Err() != nil {
			return
		}
		_ = s.pushSend(ctx, cl, e, d.pushMessage(j, e))
	}
}

// ---- ops ----

func pushGate(op string, who caller) error {
	if who.session {
		return proto.Errorf(proto.CodeForbidden, "%s: push notifications are for humans, not for agent sessions", op)
	}
	return nil
}

func (s *Server) pushStore(op string) (*push.Store, error) {
	if s.PushStore == nil {
		return nil, proto.Errorf(proto.CodeNotReady, "%s: the push token store is not available", op)
	}
	return s.PushStore, nil
}

func printableASCII(s string, max int) bool {
	if s == "" || len(s) > max {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < 0x21 || s[i] > 0x7e {
			return false
		}
	}
	return true
}

// pushRegister serves op push_register.
func (s *Server) pushRegister(who caller, a proto.PushRegisterArgs) (proto.PushRegisterResult, error) {
	const op = proto.OpPushRegister
	if err := pushGate(op, who); err != nil {
		return proto.PushRegisterResult{}, err
	}
	if who.dev != nil && who.dev.Kind != relayDeviceKindOwner {
		return proto.PushRegisterResult{}, proto.Errorf(proto.CodeForbidden, "%s: guest devices cannot register push tokens", op)
	}
	if !printableASCII(a.Token, proto.MaxPushToken) {
		return proto.PushRegisterResult{}, proto.Errorf(proto.CodeInvalid, "%s: token must be 1..%d printable ASCII characters", op, proto.MaxPushToken)
	}
	if !slices.Contains(proto.PushPlatforms, a.Platform) {
		return proto.PushRegisterResult{}, proto.Errorf(proto.CodeInvalid, "%s: platform must be one of %s", op, strings.Join(proto.PushPlatforms, ", "))
	}
	if a.HostRef != "" && !printableASCII(a.HostRef, proto.MaxPushHostRef) {
		return proto.PushRegisterResult{}, proto.Errorf(proto.CodeInvalid, "%s: host_ref must be at most %d printable ASCII characters", op, proto.MaxPushHostRef)
	}
	st, err := s.pushStore(op)
	if err != nil {
		return proto.PushRegisterResult{}, err
	}
	e := push.Entry{Token: a.Token, Platform: a.Platform, HostRef: a.HostRef,
		Name: strings.TrimSpace(screen.Sanitize(a.Name, proto.MaxPushName))}
	if who.dev != nil {
		e.Device = who.dev.ID
	}
	got, err := st.Upsert(e)
	if err != nil {
		s.Log.Error("push: saving tokens failed", "err", err)
		return proto.PushRegisterResult{ID: got.ID}, proto.Errorf(proto.CodeInternal, "%s: saving the token failed", op)
	}
	s.Log.Info("push: token registered", "token", got.ID, "platform", got.Platform, "device", got.Device)
	return proto.PushRegisterResult{ID: got.ID}, nil
}

// pushUnregister serves op push_unregister.
func (s *Server) pushUnregister(who caller, a proto.PushUnregisterArgs) (proto.PushUnregisterResult, error) {
	const op = proto.OpPushUnregister
	if err := pushGate(op, who); err != nil {
		return proto.PushUnregisterResult{}, err
	}
	id := a.ID
	if a.Token != "" {
		id = push.TokenID(a.Token)
	}
	if id == "" {
		return proto.PushUnregisterResult{}, proto.Errorf(proto.CodeInvalid, "%s: token or id required", op)
	}
	st, err := s.pushStore(op)
	if err != nil {
		return proto.PushUnregisterResult{}, err
	}
	if who.dev != nil {
		if e, ok := st.Get(id); !ok || e.Device != who.dev.ID {
			return proto.PushUnregisterResult{}, nil // not its own: indistinguishable from absent
		}
	}
	n, err := st.Remove(id)
	if err != nil {
		return proto.PushUnregisterResult{Removed: n}, proto.Errorf(proto.CodeInternal, "%s: saving the tokens failed", op)
	}
	return proto.PushUnregisterResult{Removed: n}, nil
}

// pushLs serves op push_ls.
func (s *Server) pushLs(who caller) (proto.PushLsResult, error) {
	const op = proto.OpPushLs
	if err := pushGate(op, who); err != nil {
		return proto.PushLsResult{}, err
	}
	st, err := s.pushStore(op)
	if err != nil {
		return proto.PushLsResult{}, err
	}
	res := proto.PushLsResult{Tokens: []proto.PushToken{}}
	for _, e := range st.List() {
		if who.dev != nil && e.Device != who.dev.ID {
			continue
		}
		res.Tokens = append(res.Tokens, proto.PushToken{ID: e.ID, Platform: e.Platform, Name: e.Name, HostRef: e.HostRef,
			Device: e.Device, CreatedAt: e.CreatedAt, LastOKAt: e.LastOKAt, LastError: e.LastError})
	}
	d := &s.pushd
	d.mu.Lock()
	res.Enabled, res.ProjectID, res.Reason = d.client != nil, d.project, d.reason
	d.mu.Unlock()
	if res.Enabled {
		res.Reason = ""
	} else {
		res.ProjectID = ""
	}
	return res, nil
}

// pushTest serves op push_test: a test notification, sent now, to one or every token.
func (s *Server) pushTest(ctx context.Context, who caller, a proto.PushTestArgs) (proto.PushTestResult, error) {
	const op = proto.OpPushTest
	if err := pushGate(op, who); err != nil {
		return proto.PushTestResult{}, err
	}
	if who.dev != nil {
		return proto.PushTestResult{}, proto.Errorf(proto.CodeForbidden, "%s: not allowed over the relay", op)
	}
	st, err := s.pushStore(op)
	if err != nil {
		return proto.PushTestResult{}, err
	}
	d := &s.pushd
	d.mu.Lock()
	cl, host, reason := d.client, d.host, d.reason
	d.mu.Unlock()
	if cl == nil {
		return proto.PushTestResult{}, proto.Errorf(proto.CodeNotReady, "%s: push is off (%s)", op, reason)
	}
	var targets []push.Entry
	for _, e := range st.List() {
		if a.ID == "" || e.ID == a.ID {
			targets = append(targets, e)
		}
	}
	if a.ID != "" && len(targets) == 0 {
		return proto.PushTestResult{}, proto.Errorf(proto.CodeNotFound, "%s: no token %q", op, a.ID)
	}
	res := proto.PushTestResult{Errors: []string{}}
	for _, e := range targets {
		m := push.Message{Token: e.Token, Title: "agentd", Body: host + " · notificación de prueba",
			Data:      map[string]string{"v": "1", "host": host, "host_ref": e.HostRef, "reason": "test"},
			ChannelID: pushChannel, Tag: host + "/test"}
		if err := s.pushSend(ctx, cl, e, m); err != nil {
			res.Errors = append(res.Errors, e.ID+": "+err.Error())
		} else {
			res.Sent++
		}
	}
	return res, nil
}

// pushRemoveDevice drops the tokens a revoked relay device registered.
func (s *Server) pushRemoveDevice(device string) {
	if s.PushStore == nil {
		return
	}
	if n, err := s.PushStore.RemoveDevice(device); err != nil {
		s.Log.Warn("push: removing a revoked device's tokens failed", "device", device, "err", err)
	} else if n > 0 {
		s.Log.Info("push: tokens of a revoked device removed", "device", device, "count", n)
	}
}
