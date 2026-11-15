package app

import (
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/digiogithub/agentd/internal/desktopui/backend"
	"github.com/digiogithub/agentd/internal/proto"
)

// RelativeTime is "ahora", "hace 3 min", "hace 2 h", "hace 2 días".
func RelativeTime(t, now time.Time) string {
	if t.IsZero() {
		return ""
	}
	d := now.Sub(t)
	switch {
	case d < 45*time.Second:
		return "ahora"
	case d < time.Hour:
		return fmt.Sprintf("hace %d min", max(1, int(d.Minutes())))
	case d < 24*time.Hour:
		return fmt.Sprintf("hace %d h", int(d.Hours()))
	}
	days := int(d.Hours() / 24)
	if days == 1 {
		return "hace 1 día"
	}
	return fmt.Sprintf("hace %d días", days)
}

// ShortDuration is "40 s", "2 min", "3 h": how long a session has been in
// its state.
func ShortDuration(t, now time.Time) string {
	if t.IsZero() {
		return ""
	}
	d := now.Sub(t)
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%d s", max(0, int(d.Seconds())))
	case d < time.Hour:
		return fmt.Sprintf("%d min", int(d.Minutes()))
	}
	return fmt.Sprintf("%d h", int(d.Hours()))
}

// Plural is "1 sesión" / "3 sesiones".
func Plural(n int, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return fmt.Sprintf("%d %s", n, many)
}

// SessionTitle is the name of an agentd session, "zellij · main" otherwise.
func SessionTitle(s backend.Session) string {
	switch s.Source {
	case backend.SourceAgentd, "":
		return s.Name
	case backend.SourceShell:
		return "SSH · " + s.Name
	}
	return string(s.Source) + " · " + s.Name
}

// SourceLabel is "agentd" or "SSH".
func SourceLabel(s backend.Source) string {
	if s == backend.SourceAgentd {
		return "agentd"
	}
	return "SSH"
}

// AvatarLetter is the letter of a session's avatar (codex is "X").
func AvatarLetter(s backend.Session) string {
	h := strings.ToLower(s.Harness)
	if h == "" {
		h = strings.ToLower(s.Name)
	}
	if strings.HasPrefix(h, "codex") {
		return "X"
	}
	if h == "" {
		return "?"
	}
	return strings.ToUpper(h[:1])
}

// Group is a section of the agents inbox.
type Group int

// Groups, in the order the inbox shows them.
const (
	GroupNeedsAnswer Group = iota
	GroupWorking
	GroupDone
)

// GroupOf puts a session in its inbox section. v may be nil.
func GroupOf(s backend.Session, v *proto.View) Group {
	status := s.Status
	if v != nil && v.Status != "" {
		status = v.Status
	}
	switch {
	case status == "waiting" || v != nil && v.Dialog != nil && len(v.Options) > 0 && status != "exited":
		return GroupNeedsAnswer
	case status == "working" || status == "starting":
		return GroupWorking
	}
	return GroupDone
}

var (
	allowRe = regexp.MustCompile(`(?i)^\s*(\d+[.)]\s*)?(yes|s[ií]|allow|permitir|approve|aprobar|proceed|continuar)\b`)
	denyRe  = regexp.MustCompile(`(?i)^\s*(\d+[.)]\s*)?(no|deny|denegar|reject|rechazar|cancel|cancelar)\b`)
)

// AllowOption returns the option of v that allows (the first "Yes"), or nil.
func AllowOption(v *proto.View) *proto.ViewOption {
	if v == nil {
		return nil
	}
	for i := range v.Options {
		if allowRe.MatchString(v.Options[i].Label) {
			return &v.Options[i]
		}
	}
	return nil
}

// DenyOption returns the option of v that denies (the last "No"), or nil.
func DenyOption(v *proto.View) *proto.ViewOption {
	if v == nil {
		return nil
	}
	for i := len(v.Options) - 1; i >= 0; i-- {
		if denyRe.MatchString(v.Options[i].Label) {
			return &v.Options[i]
		}
	}
	return nil
}

// IsPermission reports whether the dialog on v is a permission request: a
// dialog whose options include one that allows and one that denies (or a
// yes/no menu).
func IsPermission(v *proto.View) bool {
	if v == nil || v.Dialog == nil || len(v.Options) == 0 {
		return false
	}
	if v.Options[0].Kind == "yesno" {
		return true
	}
	return AllowOption(v) != nil && DenyOption(v) != nil
}

// Pending reports whether v waits on a question with options.
func Pending(v *proto.View) bool {
	return v != nil && v.Status != "exited" && len(v.Options) > 0
}

// SessionPath is the route of a session in the agents area.
func SessionPath(s backend.Session) string {
	return "/agents/s/" + url.PathEscape(s.HostID) + "/" + url.PathEscape(string(s.Source)) + "/" + url.PathEscape(s.Name)
}

// SessionKeyFromRoute rebuilds Session.Key() from the parts of SessionPath.
func SessionKeyFromRoute(host, source, name string) string {
	un := func(s string) string {
		if v, err := url.PathUnescape(s); err == nil {
			return v
		}
		return s
	}
	return un(host) + "/" + un(source) + "/" + un(name)
}

// ErrorText is a short Spanish reason for a backend error (act codes).
func ErrorText(err error) string {
	switch proto.CodeOf(err) {
	case proto.CodeConflict:
		return "El menú cambió: revisa la pregunta actualizada"
	case proto.CodeBusy:
		return "Hay texto a medio escribir en la sesión"
	case proto.CodeNotReady:
		return "La sesión no está lista"
	case proto.CodeVerifyFailed:
		return "El texto no apareció en la sesión"
	case proto.CodeExited:
		return "La sesión ha terminado"
	case proto.CodeTimeout:
		return "La sesión no respondió a tiempo"
	}
	if err == nil {
		return ""
	}
	var hk *backend.HostKeyError
	switch {
	case errors.Is(err, backend.ErrHostKeyChanged):
		return HostKeyChangedText
	case errors.As(err, &hk):
		return "La huella del host no está confirmada todavía"
	case errors.Is(err, backend.ErrPasswordRequired):
		return "Este host pide contraseña"
	}
	return err.Error()
}

// HostKeyChangedText explains a changed host key (a hard error).
const HostKeyChangedText = "La clave del host ha cambiado desde que la aceptaste. Puede ser un ataque de intermediario: no se conecta. " +
	"Si reinstalaste el host a propósito, quítalo de la app y añádelo de nuevo."

// HostErrText is the line a host card shows for its connection error.
func HostErrText(h backend.HostStatus) string {
	switch h.ErrCode {
	case backend.HostErrKeyChanged:
		return "La clave del host ha cambiado: no se conecta (¿intermediario?)"
	case backend.HostErrKeyUnknown:
		return "Huella del host sin confirmar"
	case backend.HostErrPassword:
		return "Pide contraseña"
	}
	return h.Err
}

// ConflictView returns the view an act conflict carries, if any.
func ConflictView(err error) *proto.View {
	var re *proto.RemoteError
	if ok := asRemote(err, &re); ok && len(re.Data) > 0 {
		var c proto.ActConflict
		if jsonUnmarshal(re.Data, &c) == nil && c.View.Revision != 0 {
			return &c.View
		}
	}
	var ce *proto.CodedError
	if asCoded(err, &ce) {
		if c, ok := ce.Data.(proto.ActConflict); ok {
			return &c.View
		}
	}
	return nil
}

// HostKeyFingerprint returns the fingerprint to confirm when err says the
// host key is not known yet (trust on first use), else "".
func HostKeyFingerprint(err error) string {
	var hk interface{ HostKeyFingerprint() string }
	if asAny(err, &hk) {
		return hk.HostKeyFingerprint()
	}
	return ""
}
