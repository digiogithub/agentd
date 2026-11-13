package daemon

import (
	"fmt"
	"testing"

	"github.com/digiogithub/agentd/internal/inject"
	"github.com/digiogithub/agentd/internal/proto"
	"github.com/digiogithub/agentd/internal/session"
)

func TestClassifyError(t *testing.T) {
	for _, tc := range []struct {
		err  error
		want string
	}{
		{session.ErrWriteTimeout, proto.CodeTimeout},
		{fmt.Errorf("type: %w", session.ErrWriteTimeout), proto.CodeTimeout},
		{inject.ErrPasteNotVisible, proto.CodeVerifyFailed},
		{inject.ErrDraft, proto.CodeBusy},
		{fmt.Errorf("plain"), ""},
		{proto.Errorf(proto.CodeForbidden, "x: %w", session.ErrWriteTimeout), proto.CodeForbidden},
	} {
		err := classifyError(tc.err)
		if got := proto.CodeOf(err); got != tc.want {
			t.Errorf("%v: code %q, want %q", tc.err, got, tc.want)
		}
		if err.Error() != tc.err.Error() {
			t.Errorf("message changed: %q -> %q", tc.err, err)
		}
	}
	if classifyError(nil) != nil {
		t.Fatal("nil")
	}
}
