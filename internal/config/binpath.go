package config

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// UserBinSubdirs are the places (relative to $HOME) where installers and
// package managers put user-level binaries (npm/bun/pnpm globals, cargo, go,
// volta, version-manager shims). They are often missing from the PATH of a
// service or of a non-interactive SSH shell, which is where agentd runs.
var UserBinSubdirs = []string{
	".local/bin", "bin", ".bun/bin", ".cargo/bin", ".npm-global/bin", ".volta/bin",
	".local/share/pnpm", "go/bin", ".deno/bin", ".local/share/mise/shims", ".asdf/shims",
}

// SystemBinDirs are well-known absolute directories outside the default PATH
// of many services (Homebrew on macOS and Linux, snap).
var SystemBinDirs = []string{"/usr/local/bin", "/opt/homebrew/bin", "/home/linuxbrew/.linuxbrew/bin", "/snap/bin"}

// NodeVersionGlobs are $HOME-relative patterns of per-version Node installs
// (nvm, fnm); each match's `bin` directory is searched, newest version first.
var NodeVersionGlobs = []string{".nvm/versions/node/*/bin", ".local/share/fnm/node-versions/*/installation/bin"}

// ExtraBinDirs returns the directories searched, after PATH, for a binary
// (existing ones only). home may be "" (then only the system dirs).
func ExtraBinDirs(home string) []string {
	var out []string
	if home != "" {
		for _, d := range UserBinSubdirs {
			out = append(out, filepath.Join(home, d))
		}
		for _, g := range NodeVersionGlobs {
			m, _ := filepath.Glob(filepath.Join(home, g))
			sort.Sort(sort.Reverse(sort.StringSlice(m)))
			out = append(out, m...)
		}
	}
	out = append(out, SystemBinDirs...)
	var existing []string
	for _, d := range out {
		if st, err := os.Stat(d); err == nil && st.IsDir() {
			existing = append(existing, d)
		}
	}
	return existing
}

func isExecutable(p string) bool {
	st, err := os.Stat(p)
	return err == nil && st.Mode().IsRegular() && st.Mode().Perm()&0o111 != 0
}

// FindBinary resolves a bare command name to an absolute path: the
// directories of pathEnv first (a PATH string), then ExtraBinDirs(home). A
// name with a slash is returned unchanged when it is executable. ok is false
// when nothing was found.
func FindBinary(name, pathEnv, home string) (string, bool) {
	if name == "" {
		return "", false
	}
	if strings.ContainsRune(name, '/') {
		return name, isExecutable(name)
	}
	dirs := filepath.SplitList(pathEnv)
	dirs = append(dirs, ExtraBinDirs(home)...)
	for _, d := range dirs {
		if d == "" || !filepath.IsAbs(d) {
			continue
		}
		if p := filepath.Join(d, name); isExecutable(p) {
			return p, true
		}
	}
	return "", false
}
