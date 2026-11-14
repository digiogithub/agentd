//go:build linux

package daemon

import (
	"bytes"
	"os"
	"strconv"
)

// procEnvSet reports whether process pid started with a non-empty
// environment variable key (/proc/<pid>/environ: the environment at exec,
// readable for processes of the same user). Best effort: false when it
// cannot be read.
func procEnvSet(pid int, key string) bool {
	b, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/environ")
	if err != nil {
		return false
	}
	prefix := []byte(key + "=")
	for kv := range bytes.SplitSeq(b, []byte{0}) {
		if v, ok := bytes.CutPrefix(kv, prefix); ok && len(v) > 0 {
			return true
		}
	}
	return false
}
