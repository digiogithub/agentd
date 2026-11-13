package main

import (
	"bufio"
	"bytes"
	"strings"
	"testing"
)

func keys(s string) <-chan byte {
	ch := make(chan byte, len(s)+1)
	for i := 0; i < len(s); i++ {
		ch <- s[i]
	}
	close(ch)
	return ch
}

func TestReadKey(t *testing.T) {
	for in, want := range map[string]string{
		"\r": "enter", "\x1b[A": "up", "\x1b[B": "down", "\x1bOA": "ss3-up", "\x1b": "esc",
		"7": "7", "é": "é", "\x1b[200~": "paste[", "\x7f": "bs",
	} {
		if got, _ := readKey(keys(in)); got != want {
			t.Errorf("readKey(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestWriteParagraphsWrapAt(t *testing.T) {
	var b bytes.Buffer
	w := bufio.NewWriter(&b)
	writeParagraphs(w, 40)
	for _, l := range strings.Split(b.String(), "\r\n") {
		if len([]rune(l)) > 40 {
			t.Fatalf("line longer than 40: %q", l)
		}
	}
}
