package daemon

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"strconv"

	"github.com/digiogithub/agentd/internal/proto"
)

// board serves op board: every session's status, attention, task list and the
// open questions it asked the caller, plus the caller's unread count, with a
// digest for conditional fetch. Humans only (the board is the human's view); a
// relay device gets its session filter applied, and only owner devices get
// questions and the unread count.
func (s *Server) board(ctx context.Context, who caller, a proto.BoardArgs) (proto.BoardResult, error) {
	if who.session {
		return proto.BoardResult{}, proto.Errorf(proto.CodeForbidden, "board: the board is the human's view (sessions use peers/inbox)")
	}
	mail := who.dev == nil || who.dev.Kind == relayDeviceKindOwner
	questions := map[string][]proto.BoardQuestion{}
	unread := 0
	if mail {
		qs, err := s.Store.OpenQuestions(ctx, "", who.id)
		if err != nil {
			return proto.BoardResult{}, err
		}
		for _, q := range qs {
			questions[q.Sender] = append(questions[q.Sender], proto.BoardQuestion{
				ID: q.ID, Body: q.Body, Options: q.Options, Task: q.Task, CreatedAt: q.CreatedAt})
		}
		if unread, err = s.Store.UnreadCount(ctx, who.id); err != nil {
			return proto.BoardResult{}, err
		}
	}
	h := sha256.New()
	res := proto.BoardResult{Sessions: []proto.BoardSession{}, UnreadHuman: unread}
	for _, e := range s.sortedEntries() {
		name := e.sess.Name()
		if who.dev != nil && !who.dev.allows(name) {
			continue
		}
		bs := proto.BoardSession{Name: name, Tasks: []proto.Task{}, Questions: []proto.BoardQuestion{}}
		var viewRev uint64
		if e.view != nil {
			bs.Status, bs.Attention, viewRev, bs.Preview, bs.Turn = e.view.summary()
		}
		e.mu.Lock()
		tr := e.tasks.rev
		bs.Tasks = append(bs.Tasks, e.tasks.tasks...)
		e.mu.Unlock()
		if qs := questions[name]; qs != nil {
			bs.Questions = qs
		}
		h.Write([]byte(name + "\x00" + strconv.FormatUint(viewRev, 10) + "\x00" + bs.Status + "\x00" + strconv.FormatUint(tr, 10) + "\x00"))
		for _, q := range bs.Questions {
			h.Write(strconv.AppendInt(nil, q.ID, 10))
			h.Write([]byte{','})
		}
		h.Write([]byte{';'})
		res.Sessions = append(res.Sessions, bs)
	}
	h.Write([]byte("unread:" + strconv.Itoa(unread)))
	res.Digest = "d" + hex.EncodeToString(h.Sum(nil)[:8])
	if a.SinceDigest != "" && a.SinceDigest == res.Digest {
		return proto.BoardResult{Sessions: []proto.BoardSession{}, Digest: res.Digest, NotModified: true}, nil
	}
	return res, nil
}
