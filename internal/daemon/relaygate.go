package daemon

// The op gate for relay devices (design docs/design/relay-transport.md §5).
// Local callers are the same uid and many ops do not check identity, so every
// request from a relay stream goes through relayGate first: deny by default,
// an allow-list per scope, and the device's session filter.

import (
	"encoding/json"
	"slices"

	"github.com/digiogithub/agentd/internal/config"
	"github.com/digiogithub/agentd/internal/proto"
)

// relayAudited are the side-effecting ops recorded in relay/audit.log.
var relayAudited = []string{proto.OpRun, proto.OpAct, proto.OpKill, proto.OpRm, proto.OpSend, proto.OpReply,
	proto.OpViewAck, proto.OpLabel, proto.OpDocSet, proto.OpAttach}

// relayGate checks (and may rewrite) a request from device d. It returns the
// session the request names ("" if none) for the audit log.
func relayGate(d *relayDevice, req *proto.Request) (string, error) {
	forbid := func(why string) error {
		return proto.Errorf(proto.CodeForbidden, "%s: not allowed over the relay (%s)", req.Op, why)
	}
	need := func(scope string) error {
		if !d.atLeast(scope) {
			return proto.Errorf(proto.CodeForbidden, "%s: needs the %s scope (this device has %s)", req.Op, scope, d.Scope)
		}
		return nil
	}
	var named struct {
		Name string `json:"name"`
	}
	if len(req.Args) > 0 {
		_ = json.Unmarshal(req.Args, &named)
	}
	session := func() (string, error) {
		if named.Name == "" {
			return "", proto.Errorf(proto.CodeInvalid, "%s: name required", req.Op)
		}
		if !d.allows(named.Name) {
			return "", proto.Errorf(proto.CodeNotFound, "no session %q", named.Name)
		}
		return named.Name, nil
	}

	switch req.Op {
	case proto.OpPing, proto.OpWhoami, proto.OpInfo, proto.OpLs, proto.OpSubscribe, proto.OpBoard:
		return "", nil
	case proto.OpView, proto.OpTurns, proto.OpTasks, proto.OpDump:
		return session()
	case proto.OpAttach:
		name, err := session()
		if err != nil {
			return "", err
		}
		if !d.atLeast(config.RelayScopeOperate) {
			// A view device may watch, never type or resize.
			var a proto.AttachArgs
			if err := json.Unmarshal(req.Args, &a); err != nil {
				return "", proto.Errorf(proto.CodeInvalid, "attach: %v", err)
			}
			a.Observe, a.Passive = true, false
			req.Args, _ = json.Marshal(a)
		}
		return name, nil
	case proto.OpAct, proto.OpViewAck:
		if err := need(config.RelayScopeOperate); err != nil {
			return "", err
		}
		return session()
	case proto.OpSend, proto.OpInbox, proto.OpRead, proto.OpReply:
		if d.Kind != relayDeviceKindOwner {
			return "", forbid("guests have no mailbox")
		}
		return "", need(config.RelayScopeOperate)
	case proto.OpRun:
		if err := need(config.RelayScopeAdmin); err != nil {
			return "", err
		}
		var a proto.RunArgs
		if err := json.Unmarshal(req.Args, &a); err != nil {
			return "", proto.Errorf(proto.CodeInvalid, "run: %v", err)
		}
		if len(a.Cmd) > 0 || len(a.Env) > 0 || len(a.EnvBase) > 0 {
			return "", forbid("only a harness from a profile can be started remotely: no cmd, env or env_base")
		}
		if len(d.Sessions) > 0 && (a.Name == "" || !d.allows(a.Name)) {
			return "", proto.Errorf(proto.CodeForbidden, "run: this device may only start sessions matching %v (give --name)", d.Sessions)
		}
		return a.Name, nil
	case proto.OpKill, proto.OpRm, proto.OpLabel:
		if err := need(config.RelayScopeAdmin); err != nil {
			return "", err
		}
		return session()
	case proto.OpDocGet, proto.OpDocSet:
		if len(d.Sessions) > 0 {
			return "", forbid("documents are host-wide; this device is limited to some sessions")
		}
		return "", need(config.RelayScopeAdmin)
	}
	return "", forbid("op not exposed to remote devices")
}

// relayFilterResult trims answers for device d after dispatch.
func relayFilterResult(d *relayDevice, op string, data any) any {
	switch op {
	case proto.OpLs:
		res, ok := data.(proto.LsResult)
		if !ok || len(d.Sessions) == 0 {
			return data
		}
		res.Sessions = slices.DeleteFunc(slices.Clone(res.Sessions), func(si proto.SessionInfo) bool { return !d.allows(si.Name) })
		return res
	case proto.OpInfo:
		res, ok := data.(proto.InfoResult)
		if !ok || d.atLeast(config.RelayScopeAdmin) {
			return data
		}
		res.Aliases = []proto.AliasInfo{} // directories of the host: admin only
		return res
	case proto.OpWhoami:
		res, ok := data.(proto.WhoamiResult)
		if !ok {
			return data
		}
		info := d.info(true)
		info.LastSeen = nil
		res.Device = &info
		return res
	}
	return data
}

// relayAuditOp records side-effecting ops and refusals of relay devices.
func (s *Server) relayAuditOp(d *relayDevice, op, session string, err error) {
	if err == nil && !slices.Contains(relayAudited, op) {
		return
	}
	f := map[string]any{"device": d.ID, "op": op}
	if session != "" {
		f["session"] = session
	}
	if err != nil {
		f["code"] = proto.CodeOf(err)
		if f["code"] == "" {
			f["code"] = "error"
		}
	}
	s.relayAudit("op", f)
}
