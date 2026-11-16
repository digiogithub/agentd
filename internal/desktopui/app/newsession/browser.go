package newsession

import (
	"context"
	"errors"
	"strings"

	"github.com/digiogithub/agentd/internal/desktopui/app"
	"github.com/digiogithub/agentd/internal/desktopui/backend"
	"github.com/digiogithub/agentd/internal/desktopui/kit"
	"github.com/egoist/mygo/ui"
)

// browser is the folder browser dialog: it lists the directories of a host
// through Backend.ListDir, starting at the project (or $HOME), with every
// ancestor of the path clickable so the user can leave the home.
type browser struct {
	a      *app.Context
	open   bool
	host   string
	hidden bool
	cur    backend.DirListing
	loaded bool
	errMsg string
	busy   bool
	seq    int
	// use receives the chosen absolute path.
	use func(path string)
}

func newBrowser(a *app.Context, use func(string)) *browser { return &browser{a: a, use: use} }

// Open shows the dialog for host starting at start ("" = home).
func (b *browser) Open(host, start string) {
	b.open, b.host, b.cur, b.loaded, b.errMsg = true, host, backend.DirListing{}, false, ""
	b.load(start)
}

// load lists path; a failure keeps the previous listing and shows why.
func (b *browser) load(path string) {
	b.seq++
	seq, host, hidden := b.seq, b.host, b.hidden
	b.busy = true
	a := b.a
	a.Go(func(ctx context.Context) {
		l, err := a.Backend.ListDir(ctx, host, path, hidden)
		a.Update(func() {
			if seq != b.seq {
				return // a newer request superseded this one
			}
			b.busy = false
			switch {
			case err == nil:
				b.cur, b.loaded, b.errMsg = l, true, ""
			case errors.Is(err, backend.ErrDirDenied):
				b.errMsg = "Sin permiso: " + path
			case errors.Is(err, backend.ErrDirNotFound):
				b.errMsg = "No existe: " + path
			default:
				b.errMsg = "No se pudo listar la carpeta: " + app.ErrorText(err)
			}
		})
	})
}

func join(dir, name string) string {
	if dir == "/" {
		return "/" + name
	}
	return dir + "/" + name
}

func parent(p string) string {
	if i := strings.LastIndexByte(p, '/'); i > 0 {
		return p[:i]
	}
	return "/"
}

// crumbs are the ancestors of p from "/" to p itself.
func crumbs(p string) (names, paths []string) {
	names, paths = []string{"/"}, []string{"/"}
	acc := ""
	for _, part := range strings.Split(strings.Trim(p, "/"), "/") {
		if part == "" {
			continue
		}
		acc += "/" + part
		names, paths = append(names, part), append(paths, acc)
	}
	return names, paths
}

// Build draws the dialog.
func (b *browser) Build(c *ui.Context) {
	if !b.open {
		return
	}
	t := kit.Current()
	open := b.open
	kit.Dialog(c.Key("browser"), &open, kit.DialogOpts{Title: "Elegir carpeta", Width: 560,
		Body: func() {
			if b.loaded {
				names, paths := crumbs(b.cur.Path)
				ui.Row(c).Gap(2).GapY(4).Wrap().AlignItems(ui.Center).Children(func() {
					for i, n := range names {
						if kit.Button(c.Key("crumb:"+paths[i]), kit.ButtonOpts{Variant: kit.Ghost, Label: n, Height: 28, FontSize: 13,
							Aria: "Ir a " + paths[i]}).Clicked() {
							b.load(paths[i])
						}
						if i > 0 && i < len(names)-1 || i == 0 && len(names) > 1 {
							ui.Text(c, "›").TextColor(t.Faint).FontSize(13)
						}
					}
				})
			}
			ui.Row(c).Gap(8).GapY(8).Wrap().AlignItems(ui.Center).Children(func() {
				if kit.Button(c.Key("home"), kit.ButtonOpts{Variant: kit.Outline, Label: "Inicio (~)", Height: 32, FontSize: 13}).Clicked() {
					b.load("")
				}
				up := b.loaded && b.cur.Path != "/"
				if kit.Button(c.Key("up"), kit.ButtonOpts{Variant: kit.Outline, Label: "Subir", Height: 32, FontSize: 13, Disabled: !up}).Clicked() && up {
					b.load(parent(b.cur.Path))
				}
				ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
					on := b.hidden
					if kit.Toggle(c.Key("hidden"), &on, "Mostrar ocultas") {
						b.hidden = on
						if b.loaded {
							b.load(b.cur.Path)
						}
					}
					ui.Text(c, "Mostrar ocultas").FontSize(13).TextColor(t.Muted)
				})
			})
			if b.errMsg != "" {
				ui.Text(c, b.errMsg).FontSize(13).TextColor(t.Danger).LineHeight(1.4)
			}
			ui.Scroll(c.Key("dirs")).Height(300).Padding(4).Radius(12).Background(t.Bg).Border(1, t.Border).Gap(2).Children(func() {
				switch {
				case !b.loaded && b.busy:
					ui.Text(c, "Cargando…").FontSize(13).TextColor(t.Muted).Padding(8)
				case b.loaded && len(b.cur.Entries) == 0:
					ui.Text(c, "No hay carpetas aquí").FontSize(13).TextColor(t.Muted).Padding(8)
				}
				for _, e := range b.cur.Entries {
					b.entry(c, t, e)
				}
				if b.cur.Truncated {
					ui.Text(c, "Solo se muestran las primeras "+itoa(backend.MaxDirEntries)+" carpetas").FontSize(12).TextColor(t.Muted).Padding(8)
				}
			})
			if b.loaded {
				ui.Text(c, b.cur.Path).Font(kit.MonoFamily()).FontSize(12).TextColor(t.Muted).SingleLine().Ellipsis("…")
			}
		},
		Footer: func() {
			if kit.Button(c.Key("bcancel"), kit.ButtonOpts{Variant: kit.Outline, Label: "Cancelar", Height: 36}).Clicked() {
				open = false
			}
			if kit.Button(c.Key("buse"), kit.ButtonOpts{Variant: kit.Primary, Label: "Usar esta carpeta", Height: 36, Disabled: !b.loaded}).Clicked() && b.loaded {
				b.use(b.cur.Path)
				open = false
			}
		}})
	b.open = open
}

func (b *browser) entry(c *ui.Context, t *kit.Tokens, e backend.DirEntry) {
	ec := c.Key("dir:" + e.Name)
	row := ui.ButtonBase(ec).Label(entryLabel(e)).Height(34).Padding(0, 10).Gap(8).Radius(8).AlignItems(ui.Center).Disabled(!e.Enterable)
	if e.Enterable {
		row.Cursor(ui.CursorPointer)
		if row.Hovered() {
			row.Background(t.Raised)
		}
	} else {
		row.Opacity(0.5).Cursor(ui.CursorNotAllowed)
	}
	row.Children(func() {
		kit.Icon(ec, kit.IconFolder).FontSize(15).TextColor(t.Muted)
		ui.Text(ec, e.Name).FontSize(14).Grow(1).SingleLine().Ellipsis("…")
		if !e.Enterable {
			kit.Icon(ec, kit.IconLock).FontSize(14).TextColor(t.Faint)
		}
	})
	if row.Clicked() && e.Enterable {
		b.load(join(b.cur.Path, e.Name))
	}
}

// entryLabel names a folder row for assistive technology (and tests).
func entryLabel(e backend.DirEntry) string {
	if !e.Enterable {
		return "Carpeta sin permiso " + e.Name
	}
	return "Abrir carpeta " + e.Name
}
