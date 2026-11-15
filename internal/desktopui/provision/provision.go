// Package provision detects what a host has (one POSIX sh script) and
// installs agentd, its service, the hc listener, the agentd MCP server and
// agent CLIs through a backend.Shell. It is a port of the agentd-app Flutter
// layer lib/src/provision: same scripts, same step graph.
package provision

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/digiogithub/agentd/internal/desktopui/backend"
)

// DefaultDetectTimeout bounds the detect script (ten tools, 5 s each at most).
const DefaultDetectTimeout = 90 * time.Second

// Service detects hosts and runs install plans.
type Service struct {
	// DetectTimeout bounds Detect (DefaultDetectTimeout when zero).
	DetectTimeout time.Duration
}

// New returns a Service with the default timeouts.
func New() *Service { return &Service{DetectTimeout: DefaultDetectTimeout} }

// Detect runs DetectScript once on the host and parses it.
func (s *Service) Detect(ctx context.Context, sh backend.Shell) (*backend.Capabilities, error) {
	to := s.DetectTimeout
	if to <= 0 {
		to = DefaultDetectTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, to)
	defer cancel()
	r, err := sh.Exec(ctx, DetectScript(), nil)
	if err != nil {
		return nil, fmt.Errorf("provision: host detection: %w", err)
	}
	c, err := ParseDetect(r.Stdout)
	if err != nil {
		msg := fmt.Sprintf("provision: host detection failed (exit %d)", r.Exit)
		if t := tail(r.Stderr, 5); t != "" {
			msg += ": " + t
		}
		return nil, fmt.Errorf("%s: %w", msg, err)
	}
	return c, nil
}

// Plan lists the steps Install would run, with their exact scripts.
func (s *Service) Plan(caps *backend.Capabilities, opts backend.ProvisionOptions) []backend.PlannedStep {
	steps := buildPlan(caps, opts)
	res := make([]backend.PlannedStep, len(steps))
	for i, st := range steps {
		res[i] = st.PlannedStep
	}
	return res
}

// Install runs the plan step by step and reports progress on the returned
// channel, which is closed at the end. Per step: one event when it starts
// (Message = title), then one with Done (Message = last output line, else
// the title) or one with Err ("exit N: <last stderr lines>" or the exec
// error). A failed step stops the rest of its group (download → verify →
// install) and every step that needs that group (service, hc listener and
// MCP need agentd; MCP for a CLI needs that CLI): those get a single event
// with Skipped set (Message names the failed step) and never run.
// Independent steps keep going. When ctx ends, the remaining steps are
// reported as skipped.
func (s *Service) Install(ctx context.Context, sh backend.Shell, caps *backend.Capabilities, opts backend.ProvisionOptions) <-chan backend.ProvisionEvent {
	steps := buildPlan(caps, opts)
	// Two events per step at most: sends never block.
	ch := make(chan backend.ProvisionEvent, 2*len(steps))
	go func() {
		defer close(ch)
		failed := map[string]string{} // group -> title of the failed step
		for _, st := range steps {
			if ctx.Err() != nil {
				ch <- backend.ProvisionEvent{Step: st.Step, Message: "cancelled", Skipped: true}
				continue
			}
			blocker := ""
			if _, ok := failed[st.group]; ok {
				blocker = st.group
			} else {
				for _, n := range st.needs {
					if _, ok := failed[n]; ok {
						blocker = n
						break
					}
				}
			}
			if blocker != "" {
				ch <- backend.ProvisionEvent{Step: st.Step, Message: fmt.Sprintf("%q failed", failed[blocker]), Skipped: true}
				if _, ok := failed[st.group]; !ok {
					failed[st.group] = st.Title
				}
				continue
			}
			ch <- backend.ProvisionEvent{Step: st.Step, Message: st.Title}
			sctx, cancel := context.WithTimeout(ctx, st.timeout)
			r, err := sh.Exec(sctx, st.Command, nil)
			cancel()
			switch {
			case err != nil:
				failed[st.group] = st.Title
				ch <- backend.ProvisionEvent{Step: st.Step, Message: st.Title, Err: err.Error()}
			case r.Exit == 0:
				msg := lastLine(r.Stdout)
				if msg == "" {
					msg = st.Title
				}
				ch <- backend.ProvisionEvent{Step: st.Step, Message: msg, Done: true}
			default:
				failed[st.group] = st.Title
				out := r.Stderr
				if strings.TrimSpace(out) == "" {
					out = r.Stdout
				}
				e := fmt.Sprintf("exit %d", r.Exit)
				if why := tail(out, 5); why != "" {
					e += ": " + why
				}
				ch <- backend.ProvisionEvent{Step: st.Step, Message: st.Title, Err: e}
			}
		}
	}()
	return ch
}

func lastLine(s string) string {
	lines := strings.Split(s, "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if l := strings.TrimSpace(lines[i]); l != "" {
			return l
		}
	}
	return ""
}

// tail returns the last n non-blank lines of s, for error messages.
func tail(s string, n int) string {
	var l []string
	for _, x := range strings.Split(s, "\n") {
		if strings.TrimSpace(x) != "" {
			l = append(l, strings.TrimRight(x, " \t\r"))
		}
	}
	if len(l) > n {
		l = l[len(l)-n:]
	}
	return strings.Join(l, "\n")
}
