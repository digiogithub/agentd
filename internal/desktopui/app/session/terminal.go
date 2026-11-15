package session

import (
	"context"
	"fmt"
	"io"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/digiogithub/agentd/internal/desktopui/app"
	"github.com/digiogithub/agentd/internal/desktopui/backend"
	"github.com/digiogithub/agentd/internal/desktopui/kit"
	"github.com/digiogithub/agentd/internal/proto"
	"github.com/egoist/mygo/plugins/terminal"
	"github.com/egoist/mygo/ui"
)

// TermKey is a key of the key bar.
type TermKey struct {
	Label, Name, Aria string
}

// KeyBar is the key bar of the mockup: esc tab ctrl arrows enter. Names are
// agentd act key names; "ctrl" arms Control for the next letter.
var KeyBar = []TermKey{
	{"esc", "esc", "Escape"}, {"tab", "tab", "Tabulador"}, {"ctrl", "ctrl", "Control"},
	{"↑", "up", "Arriba"}, {"↓", "down", "Abajo"}, {"←", "left", "Izquierda"}, {"→", "right", "Derecha"},
	{"⏎", "enter", "Intro"},
}

// keyBytes are the bytes of a key for terminals reached without agentd.
var keyBytes = map[string]string{
	"esc": "\x1b", "tab": "\t", "up": "\x1b[A", "down": "\x1b[B", "right": "\x1b[C", "left": "\x1b[D", "enter": "\r",
}

var ctrlLetter = regexp.MustCompile(`^[a-yA-Y]$`)

// termState is the terminal of the open session.
type termState struct {
	term    *terminal.Terminal
	conn    *ptyConn
	observe bool
	err     string
	libErr  string
	ctrl    bool
	line    string
	themeOf *kit.Tokens
}

func (ts *termState) close() {
	if ts.term != nil {
		_ = ts.term.Close()
	} else if ts.conn != nil {
		_ = ts.conn.Close()
	}
}

func (ts *termState) send(b []byte) {
	if ts.conn != nil && !ts.observe {
		_, _ = ts.conn.Write(b)
	}
}

// Terminal returns the open terminal, nil before the tab was shown (tests).
func (v *View) Terminal() *terminal.Terminal {
	if v.term == nil {
		return nil
	}
	return v.term.term
}

// TerminalError is why the terminal is not shown ("" when it is).
func (v *View) TerminalError() string {
	if v.term == nil {
		return ""
	}
	if v.term.libErr != "" {
		return v.term.libErr
	}
	return v.term.err
}

func (v *View) setObserve(on bool) {
	if v.term != nil {
		v.term.close()
	}
	v.term = &termState{observe: on}
}

// themes derives the terminal colours from the app tokens.
func themes() (light, dark *terminal.Theme) {
	mk := func(isDark bool, base *terminal.Theme) *terminal.Theme {
		tk := kit.Resolve(isDark)
		th := *base
		th.Foreground, th.Background = tk.Secondary, tk.TerminalBg
		th.Cursor = tk.Accent
		th.Selection = tk.Accent.Alpha(0.33)
		if isDark {
			th.Palette[0] = tk.Bg
			th.Palette[2], th.Palette[4], th.Palette[7] = tk.Success, tk.Working, tk.Secondary
			th.Palette[8], th.Palette[9], th.Palette[11], th.Palette[12], th.Palette[15] = tk.Faint, tk.Danger, tk.Waiting, tk.Accent, tk.Text
		}
		return &th
	}
	return mk(false, terminal.LightTheme()), mk(true, terminal.DarkTheme())
}

func (v *View) openTerminal(s backend.Session) {
	ts := v.term
	if ts.term != nil || ts.libErr != "" {
		return
	}
	if err := terminal.Load(); err != nil {
		ts.libErr = err.Error()
		return
	}
	pc := newPtyConn(v.a, s, ts)
	light, dark := themes()
	opts := terminal.Options{Conn: pc, Theme: light, DarkTheme: dark, Scrollback: 4 << 20,
		Font: terminal.Font{Family: kit.MonoFamily(), Size: 12.5}}
	// agentd sessions keep their PTY size (profile size, shared with other
	// clients): the screen follows it, the window only shows it.
	fixed := s.Source == backend.SourceAgentd && s.Info != nil && s.Info.Cols > 0
	if fixed {
		opts.ResizeByConn = true
		pc.fixedCols, pc.fixedRows = int(s.Info.Cols), int(s.Info.Rows)
	}
	term, err := terminal.New(opts)
	if err != nil {
		ts.libErr = err.Error()
		return
	}
	if fixed {
		term.Resize(pc.fixedCols, pc.fixedRows)
	}
	ts.term, ts.conn, ts.themeOf = term, pc, kit.Current()
}

func (v *View) terminalTab(c *ui.Context, t *kit.Tokens, s backend.Session, view *proto.View) {
	if v.term == nil {
		v.term = &termState{}
	}
	ts := v.term
	v.openTerminal(s)
	if ts.term != nil && ts.themeOf != t {
		ts.themeOf = t
		light, dark := themes()
		ts.term.SetTheme(light, dark)
	}
	control := !ts.observe
	ui.Column(c).Grow(1).MinHeight(0).Background(t.TerminalBg).Padding(10, 12).AlignItems(ui.Stretch).Children(func() {
		switch {
		case ts.libErr != "":
			ui.Column(c).Grow(1).AlignItems(ui.Center).Justify(ui.Center).Gap(10).Children(func() {
				ui.Text(c, "No se pudo cargar el motor de la terminal").FontSize(16).FontWeight(600)
				ui.Text(c, ts.libErr).FontSize(13).TextColor(t.Muted).MaxWidth(560).TextAlign(ui.Center)
				ui.Text(c, "La terminal usa libghostty-vt: las versiones publicadas la incluyen; si no, se descarga al usarla o se toma de MYGO_GHOSTTY_VT.").
					FontSize(13).TextColor(t.Muted).MaxWidth(560).TextAlign(ui.Center)
				if kit.Button(c.Key("lib-retry"), kit.ButtonOpts{Variant: kit.Outline, Label: "Reintentar"}).Clicked() {
					ts.libErr = ""
				}
			})
		case ts.err != "":
			ui.Column(c).Grow(1).AlignItems(ui.Center).Justify(ui.Center).Gap(12).Children(func() {
				ui.Text(c, ts.err).FontSize(14).TextColor(t.Danger)
				if kit.Button(c.Key("term-retry"), kit.ButtonOpts{Variant: kit.Outline, Label: "Reintentar"}).Clicked() {
					v.setObserve(ts.observe)
				}
			})
		default:
			terminal.View(c.Key(fmt.Sprint("term:", s.Key(), ts.observe)), ts.term).Grow(1).FillWidth().MinHeight(0).AutoFocus().
				Label("Terminal de la sesión")
		}
	})
	ui.Column(c).Shrink(0).Padding(8, 12).Gap(8).Background(t.NavBg).BorderWidth(1, 0, 0, 0).BorderColor(t.Divider).Children(func() {
		ui.Row(c).Gap(6).Children(func() {
			for _, k := range KeyBar {
				cc := c.Key("key:" + k.Name)
				armed := k.Name == "ctrl" && ts.ctrl
				b := ui.ButtonBase(cc).Label(k.Aria).Grow(1).Basis(0).Height(40).Radius(10).AlignItems(ui.Center).Justify(ui.Center).
					Cursor(ui.CursorPointer).Disabled(!control)
				if armed {
					b.Background(t.AccentBg).Border(1, t.Accent).TextColor(t.AccentLight)
				} else {
					b.Background(t.Surface).Border(1, t.Border).TextColor(t.Text)
				}
				if !control {
					b.Opacity(0.4)
				}
				b.Children(func() { ui.Text(cc, k.Label).Font(kit.MonoFamily()).FontSize(12.5).FontWeight(500) })
				if b.Clicked() && control {
					v.onKey(s, k)
				}
			}
		})
		ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
			ph := "Escribir en la terminal…"
			if ts.ctrl {
				ph = "ctrl + letra…"
			}
			in := ui.TextInput(c.Key("term-line"), &ts.line).Placeholder(ph).Label("Escribir en la terminal").Height(40).
				Padding(0, 12).Background(t.Surface).Border(1, t.Border).Radius(12).Font(kit.MonoFamily()).FontSize(13).
				Grow(1).Disabled(!control)
			if in.Submitted() && control {
				v.submitLine(s, ts.line)
				ts.line = ""
			}
			if kit.Button(c.Key("interrupt"), kit.ButtonOpts{Variant: kit.Danger, Label: "Interrumpir", Height: 40, FontSize: 13,
				Disabled: !control}).Clicked() && control {
				v.interrupt(s)
			}
		})
		hint := "Control: tus teclas llegan al agente. El tamaño del PTY no cambia."
		if !control {
			hint = "Observar: solo lectura, nadie ve tus pulsaciones."
		}
		ui.Text(c, hint).FontSize(11.5).TextColor(t.Muted)
	})
}

func (v *View) keys(s backend.Session, names ...string) {
	if s.Source != backend.SourceAgentd {
		var b strings.Builder
		for _, n := range names {
			if seq, ok := keyBytes[n]; ok {
				b.WriteString(seq)
			} else if l, ok := strings.CutPrefix(n, "ctrl-"); ok && len(l) == 1 {
				b.WriteByte(l[0] - 'a' + 1)
			}
		}
		v.term.send([]byte(b.String()))
		return
	}
	a := v.a
	a.Go(func(ctx context.Context) {
		if _, err := a.Backend.Act(ctx, s, proto.ActArgs{Name: s.Name, Action: "keys", Keys: names, Client: "agentd-desktop"}); err != nil {
			a.Toasts.Error("No se pudieron enviar las teclas", app.ErrorText(err))
		}
	})
}

func (v *View) onKey(s backend.Session, k TermKey) {
	if k.Name == "ctrl" {
		v.term.ctrl = !v.term.ctrl
		return
	}
	v.term.ctrl = false
	v.keys(s, k.Name)
}

func (v *View) submitLine(s backend.Session, text string) {
	ts := v.term
	switch {
	case ts.ctrl && ctrlLetter.MatchString(text):
		ts.ctrl = false
		v.keys(s, "ctrl-"+strings.ToLower(text))
	case text != "":
		ts.send([]byte(text + "\r"))
	default:
		v.keys(s, "enter")
	}
}

// ptyConn adapts backend.Terminal to the plugin's Conn: the PTY opens at
// the first size the view reports (or 80x24 after 700 ms), output goes to
// the terminal through a pipe, writes go to the PTY.
type ptyConn struct {
	a  *app.Context
	s  backend.Session
	ts *termState

	pr *io.PipeReader
	pw *io.PipeWriter

	fixedCols, fixedRows int

	mu         sync.Mutex
	pty        backend.PTY
	connecting bool
	closed     bool
	cols, rows int
	cancel     context.CancelFunc
}

func newPtyConn(a *app.Context, s backend.Session, ts *termState) *ptyConn {
	pr, pw := io.Pipe()
	p := &ptyConn{a: a, s: s, ts: ts, pr: pr, pw: pw}
	time.AfterFunc(700*time.Millisecond, func() { p.connect(80, 24) })
	return p
}

func (p *ptyConn) Read(b []byte) (int, error) { return p.pr.Read(b) }

func (p *ptyConn) Write(b []byte) (int, error) {
	p.mu.Lock()
	pt, observe := p.pty, p.ts.observe
	p.mu.Unlock()
	if pt == nil || observe {
		return len(b), nil
	}
	return pt.Write(b)
}

// Resize implements the plugin's size interface: it opens the PTY, then
// follows the view, except for agentd sessions whose size is fixed.
func (p *ptyConn) Resize(cols, rows int) error {
	if cols <= 0 || rows <= 0 {
		return nil
	}
	p.mu.Lock()
	pt := p.pty
	p.cols, p.rows = cols, rows
	p.mu.Unlock()
	if pt == nil {
		p.connect(cols, rows)
		return nil
	}
	if p.fixedCols > 0 {
		return nil
	}
	return pt.Resize(cols, rows)
}

func (p *ptyConn) connect(cols, rows int) {
	p.mu.Lock()
	if p.pty != nil || p.connecting || p.closed {
		p.mu.Unlock()
		return
	}
	if p.fixedCols > 0 {
		cols, rows = p.fixedCols, p.fixedRows
	}
	p.connecting = true
	observe := p.ts.observe
	ctx, cancel := context.WithCancel(p.a.Ctx)
	p.cancel = cancel
	p.mu.Unlock()
	p.a.Go(func(context.Context) {
		pt, err := p.a.Backend.Terminal(ctx, p.s, cols, rows, observe)
		p.mu.Lock()
		p.connecting = false
		if err == nil && p.closed {
			_ = pt.Close()
			err = io.ErrClosedPipe
		}
		if err == nil {
			p.pty = pt
		}
		p.mu.Unlock()
		if err != nil {
			if err != io.ErrClosedPipe {
				p.a.Update(func() { p.ts.err = "No se pudo abrir la terminal: " + app.ErrorText(err) })
			}
			return
		}
		go p.pump(pt)
	})
}

func (p *ptyConn) pump(pt backend.PTY) {
	buf := make([]byte, 32<<10)
	for {
		n, err := pt.Read(buf)
		if n > 0 {
			if _, werr := p.pw.Write(buf[:n]); werr != nil {
				return
			}
		}
		if err != nil {
			p.mu.Lock()
			closed := p.closed
			p.mu.Unlock()
			if !closed {
				code, _ := pt.Wait()
				msg := "\r\n\x1b[90m[desconectado]\x1b[0m\r\n"
				if code >= 0 {
					msg = fmt.Sprintf("\r\n\x1b[90m[terminó con código %d]\x1b[0m\r\n", code)
				}
				_, _ = p.pw.Write([]byte(msg))
			}
			return
		}
	}
}

// Close ends the attach (the program keeps running on the host).
func (p *ptyConn) Close() error {
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return nil
	}
	p.closed = true
	pt, cancel := p.pty, p.cancel
	p.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	_ = p.pw.Close()
	_ = p.pr.Close()
	if pt != nil {
		return pt.Close()
	}
	return nil
}
