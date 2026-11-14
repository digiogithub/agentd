package screen

import (
	"encoding/base64"
	"strconv"
	"strings"
)

// ProgramStatus is one OSC 7501 report (the Program Status Protocol used by
// herdr-core): the program's own word for what it is doing. Fields are
// sanitized; Title and Msg arrive base64-encoded on the wire.
//
//	ESC ] 7501 ; state=working:id=build:kind=…:progress=40:app=claude-code:title=<b64>:msg=<b64> ST
type ProgramStatus struct {
	State    string // idle, working, done, blocked, error (clear removes records)
	Kind     string // permission, question, auth (blocked only)
	Progress *int   // 0..100 (working or blocked)
	ID       string // empty for the root record
	App      string
	Title    string
	Msg      string
}

// MaxStatusRecords caps the records a program may keep (herdr-core: 256).
const MaxStatusRecords = 256

var statusStates = map[string]bool{"idle": true, "working": true, "done": true, "blocked": true, "error": true, "clear": true}
var statusKinds = map[string]bool{"permission": true, "question": true, "auth": true}

// ParseOSC7501 parses the payload of an OSC 7501 sequence (with or without
// the leading "7501;"). It reports false for anything without a known state.
// Unknown keys are ignored; a bad optional field is dropped, never fatal.
func ParseOSC7501(data string) (ProgramStatus, bool) {
	data = strings.TrimPrefix(data, "7501;")
	var st ProgramStatus
	for _, f := range strings.Split(data, ":") {
		k, v, ok := strings.Cut(f, "=")
		if !ok {
			continue
		}
		switch k {
		case "state":
			st.State = v
		case "kind":
			if statusKinds[v] {
				st.Kind = v
			}
		case "progress":
			if n, err := strconv.Atoi(v); err == nil && n >= 0 && n <= 100 {
				st.Progress = &n
			}
		case "id":
			st.ID = Sanitize(v, 64)
		case "app":
			st.App = Sanitize(v, 64)
		case "title":
			st.Title = decodeB64(v, MaxTitleLen)
		case "msg":
			st.Msg = decodeB64(v, MaxTitleLen)
		}
	}
	if !statusStates[st.State] {
		return ProgramStatus{}, false
	}
	if st.State != "blocked" {
		st.Kind = ""
	}
	if st.State != "working" && st.State != "blocked" {
		st.Progress = nil
	}
	return st, true
}

func decodeB64(v string, max int) string {
	b, err := base64.StdEncoding.DecodeString(v)
	if err != nil {
		if b, err = base64.RawStdEncoding.DecodeString(v); err != nil {
			return ""
		}
	}
	return Sanitize(string(b), max)
}

// ProgramStatuses is a program's current reports: the root record (nil when
// never reported or cleared) and the other records by id.
type ProgramStatuses struct {
	Root     *ProgramStatus
	Records  map[string]ProgramStatus
	Reported bool // ever sent a report
	Gen      uint64
}

// Apply records one report: it replaces its record; clear removes the record
// and its children (ids starting with "<id>."; an empty id clears everything).
func (p *ProgramStatuses) Apply(st ProgramStatus) {
	p.Reported = true
	p.Gen++
	if st.State == "clear" {
		if st.ID == "" {
			p.Root, p.Records = nil, nil
			return
		}
		for id := range p.Records {
			if id == st.ID || strings.HasPrefix(id, st.ID+".") {
				delete(p.Records, id)
			}
		}
		return
	}
	if st.ID == "" {
		s := st
		p.Root = &s
		return
	}
	if p.Records == nil {
		p.Records = map[string]ProgramStatus{}
	}
	if _, ok := p.Records[st.ID]; !ok && len(p.Records) >= MaxStatusRecords {
		return
	}
	p.Records[st.ID] = st
}

// Copy returns a deep copy (safe to hand out).
func (p ProgramStatuses) Copy() ProgramStatuses {
	out := ProgramStatuses{Reported: p.Reported, Gen: p.Gen}
	if p.Root != nil {
		r := *p.Root
		out.Root = &r
	}
	if len(p.Records) > 0 {
		out.Records = make(map[string]ProgramStatus, len(p.Records))
		for k, v := range p.Records {
			out.Records[k] = v
		}
	}
	return out
}

// StatusFromProgram maps a reported root state onto agentd's status
// vocabulary: working -> working, blocked -> waiting, idle/done/error -> idle.
func StatusFromProgram(state string) Status {
	switch state {
	case "working":
		return StatusWorking
	case "blocked":
		return StatusWaiting
	}
	return StatusIdle
}
