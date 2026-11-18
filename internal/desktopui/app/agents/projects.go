package agents

import (
	"path"
	"sort"
	"strings"
	"time"

	"github.com/digiogithub/agentd/internal/desktopui/app"
	"github.com/digiogithub/agentd/internal/proto"
)

// Project grouping of the agents list (Proyecto | Estado toggle).
//
// Group key = host + project directory. agentd sessions carry their cwd;
// zellij/tmux/shell sessions only a project label, which is resolved to the
// directory of the alias of that name when there is one. Anything else is
// grouped by its label, and sessions with neither go to "Sin proyecto".

// NoProject is the label of the group without a project.
const NoProject = "Sin proyecto"

// projGroup is one project of one host.
type projGroup struct {
	key     string
	host    string
	label   string
	path    string // absolute directory, "" when only a label is known
	newArg  string // value of ?project= for the + button ("" = none)
	entries []entry
	waiting int
	updated time.Time
}

// hostInfo is what the grouping needs of a host.
type hostInfo struct {
	home    string
	aliases []proto.AliasInfo
}

// expandHome turns a leading `~` into home (when known) and cleans p.
func expandHome(p, home string) string {
	switch {
	case p == "~" && home != "":
		p = home
	case strings.HasPrefix(p, "~/") && home != "":
		p = home + p[1:]
	}
	if !strings.HasPrefix(p, "/") {
		return p // still `~...` (home unknown) or relative: keep
	}
	return path.Clean(p)
}

// tilde shows p with the home directory as `~`.
func tilde(p, home string) string {
	if home == "" || home == "/" {
		return p
	}
	if p == home {
		return "~"
	}
	if rest, ok := strings.CutPrefix(p, home+"/"); ok {
		return "~/" + rest
	}
	return p
}

// groupFor returns the key, label, directory and + argument of a session.
func groupFor(e entry, hi hostInfo) (key, label, dir, arg string) {
	s := e.s
	dir = ""
	if s.Info != nil && s.Info.Cwd != "" {
		dir = expandHome(s.Info.Cwd, hi.home)
	} else if s.Project != "" {
		for _, al := range hi.aliases {
			if al.Name == s.Project {
				dir = expandHome(al.Dir, hi.home)
			}
		}
	}
	if dir != "" {
		for _, al := range hi.aliases {
			if expandHome(al.Dir, hi.home) == dir {
				return "dir:" + dir, al.Name, dir, al.Name
			}
		}
		return "dir:" + dir, tilde(dir, hi.home), dir, dir
	}
	if s.Project != "" {
		return "label:" + s.Project, s.Project, "", s.Project
	}
	return "none", NoProject, "", ""
}

// buildGroups groups entries per host + project. The order never depends on
// live data (status, attention, last update), so the list does not jump on
// every refresh: groups alphabetically by label (then host), sessions inside a
// group alphabetically by title (then key). The status shows in each card.
func buildGroups(entries []entry, hosts map[string]hostInfo) []*projGroup {
	byKey := map[string]*projGroup{}
	var order []*projGroup
	for _, e := range entries {
		key, label, dir, arg := groupFor(e, hosts[e.s.HostID])
		full := e.s.HostID + "|" + key
		g := byKey[full]
		if g == nil {
			g = &projGroup{key: full, host: e.s.HostID, label: label, path: dir, newArg: arg}
			byKey[full] = g
			order = append(order, g)
		}
		g.entries = append(g.entries, e)
		if e.g == app.GroupNeedsAnswer {
			g.waiting++
		}
		if e.s.Updated.After(g.updated) {
			g.updated = e.s.Updated
		}
	}
	for _, g := range order {
		sortEntries(g.entries)
	}
	sort.SliceStable(order, func(i, j int) bool {
		a, b := order[i], order[j]
		if la, lb := strings.ToLower(a.label), strings.ToLower(b.label); la != lb {
			return la < lb
		}
		if a.label != b.label {
			return a.label < b.label
		}
		return a.host < b.host
	})
	return order
}

// sortEntries orders sessions alphabetically by title, then by key: a stable
// order that live updates (status, activity) never change.
func sortEntries(es []entry) {
	sort.SliceStable(es, func(i, j int) bool {
		a, b := app.SessionTitle(es[i].s), app.SessionTitle(es[j].s)
		if la, lb := strings.ToLower(a), strings.ToLower(b); la != lb {
			return la < lb
		}
		if a != b {
			return a < b
		}
		return es[i].s.Key() < es[j].s.Key()
	})
}
