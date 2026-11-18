package shell

import (
	"testing"

	"github.com/egoist/mygo"
)

func TestWindowFrame(t *testing.T) {
	cases := []struct {
		goos     string
		frameles bool
		style    mygo.TitleBarStyle
		hide     bool
	}{
		{"linux", true, mygo.TitleBarDefault, false},
		{"windows", false, mygo.TitleBarHidden, true},
		{"darwin", false, mygo.TitleBarDefault, false},
	}
	for _, c := range cases {
		f, s, h := windowFrame(c.goos)
		if f != c.frameles || s != c.style || h != c.hide {
			t.Errorf("%s: got %v %v %v", c.goos, f, s, h)
		}
	}
}
