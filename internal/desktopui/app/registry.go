package app

import (
	"sort"
	"sync"

	"github.com/egoist/mygo/ui"
)

// DefaultPath is the route shown at start-up.
const DefaultPath = "/agents"

// Section groups sidebar entries.
type Section string

// Sidebar sections.
const (
	SectionMain   Section = "main"   // panel, agents, hosts, mailbox
	SectionBottom Section = "bottom" // settings
	SectionHidden Section = "hidden" // routable pages (new session, add host, provision)
)

// View is what an area shows. Build runs on the UI thread every frame the
// area is visible; r is the matched route.
type View interface {
	Build(c *ui.Context, r *ui.Route)
}

// Closer is implemented by views owning goroutines or streams.
type Closer interface {
	Close()
}

// Area is a destination of the app: a sidebar entry or a hidden page.
type Area struct {
	// ID is unique and stable ("agents").
	ID string
	// Path is where the sidebar entry navigates.
	Path string
	// Patterns are the router patterns the area builds; Path when empty.
	Patterns []string
	// Title is the sidebar label (Spanish UI copy).
	Title string
	// Icon is a kit icon name.
	Icon    string
	Section Section
	// Order sorts entries within a section.
	Order int
	// Parent is the sidebar entry highlighted while a hidden page shows.
	Parent string
	// Shortcut is the digit of Ctrl/Cmd+digit (0 = none).
	Shortcut int
	// Badge is the count the sidebar entry shows (nil or 0 = none). It runs
	// on the UI thread every frame.
	Badge func(a *Context) int
	// New builds the area's view once, the first time it is shown.
	New func(a *Context) View
}

var (
	regMu sync.RWMutex
	areas = map[string]Area{}
)

// Register adds or replaces an area.
func Register(ar Area) {
	if ar.ID == "" || ar.New == nil {
		panic("app: Register needs an ID and New")
	}
	if ar.Path == "" {
		ar.Path = "/" + ar.ID
	}
	if len(ar.Patterns) == 0 {
		ar.Patterns = []string{ar.Path}
	}
	regMu.Lock()
	areas[ar.ID] = ar
	regMu.Unlock()
}

// Areas returns the registered areas sorted by section then order.
func Areas() []Area {
	regMu.RLock()
	out := make([]Area, 0, len(areas))
	for _, a := range areas {
		out = append(out, a)
	}
	regMu.RUnlock()
	rank := map[Section]int{SectionMain: 0, SectionBottom: 1, SectionHidden: 2}
	sort.SliceStable(out, func(i, j int) bool {
		if rank[out[i].Section] != rank[out[j].Section] {
			return rank[out[i].Section] < rank[out[j].Section]
		}
		if out[i].Order != out[j].Order {
			return out[i].Order < out[j].Order
		}
		return out[i].ID < out[j].ID
	})
	return out
}

// Lookup returns the area with id.
func Lookup(id string) (Area, bool) {
	regMu.RLock()
	defer regMu.RUnlock()
	a, ok := areas[id]
	return a, ok
}
