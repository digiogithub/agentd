package screenfix

import (
	"bytes"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/creack/pty"

	"github.com/digiogithub/agentd/internal/testutil"
)

// drive runs fakeagent in a PTY, types each step and returns the output once
// the process exits (or after 5 s).
func drive(t *testing.T, args []string, steps ...string) string {
	t.Helper()
	cmd := exec.Command(testutil.BuildFakeAgent(t), args...)
	ptmx, err := pty.StartWithSize(cmd, &pty.Winsize{Cols: 100, Rows: 30})
	if err != nil {
		t.Fatal(err)
	}
	defer ptmx.Close()
	var (
		mu  sync.Mutex
		buf bytes.Buffer
	)
	done := make(chan struct{})
	go func() {
		defer close(done)
		b := make([]byte, 4096)
		for {
			n, err := ptmx.Read(b)
			mu.Lock()
			buf.Write(b[:n])
			mu.Unlock()
			if err != nil {
				return
			}
		}
	}()
	time.Sleep(300 * time.Millisecond)
	for _, s := range steps {
		_, _ = ptmx.WriteString(s)
		time.Sleep(250 * time.Millisecond)
	}
	select {
	case <-done:
	case <-time.After(1500 * time.Millisecond):
		_ = cmd.Process.Kill()
		<-done
	}
	_ = cmd.Wait()
	mu.Lock()
	defer mu.Unlock()
	return buf.String()
}

func TestFakeAgentWidgets(t *testing.T) {
	cases := []struct {
		name  string
		args  []string
		steps []string
		want  string
	}{
		{"menu", []string{"-menu", "A|B|C"}, []string{"\x1b[B", "\r"}, "chose: B"},
		{"numbered", []string{"-numbered", "-menu", "A|B|C"}, []string{"3"}, "chose: C"},
		{"digitmove", []string{"-numbered", "-digitmove", "-menu", "A|B|C"}, []string{"3", "\r"}, "chose: C"},
		{"yesno", []string{"-yesno", "Go?"}, []string{"y"}, "chose: yes"},
		{"dialog-esc", []string{"-dialog"}, []string{"\x1b"}, "cancelled"},
		{"appcursor-ss3", []string{"-menu", "A|B", "-appcursor"}, []string{"\x1bOB", "\r"}, "chose: B"},
		{"appcursor-csi-ignored", []string{"-menu", "A|B", "-appcursor"}, []string{"\x1b[B", "\r"}, "chose: A"},
		{"csi-only-without-appcursor", []string{"-menu", "A|B"}, []string{"\x1bOB", "\r"}, "chose: A"},
		{"bell", []string{"-menu", "A", "-bell", "-osc9", "done"}, []string{"\r"}, "\a\x1b]9;done\a"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if out := drive(t, c.args, c.steps...); !strings.Contains(out, c.want) {
				t.Fatalf("output lacks %q:\n%q", c.want, out)
			}
		})
	}
}

func TestFakeAgentBox(t *testing.T) {
	out := drive(t, []string{"-box", "-ghost", "hint"}, "hi\r", "\x04")
	if !strings.Contains(out, "GOT: hi") || !strings.Contains(out, "❯\u00a0") {
		t.Fatalf("box output: %q", out)
	}
}
