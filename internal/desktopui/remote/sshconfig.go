package remote

import (
	"bufio"
	"io"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/digiogithub/agentd/internal/desktopui/backend"
)

// maxIncludeDepth bounds nested Include directives.
const maxIncludeDepth = 8

type sshBlock struct {
	patterns []string // Host patterns; nil = global (before any Host)
	match    bool     // a Match block: never applied
	settings [][2]string
}

func (b *sshBlock) matches(alias string) bool {
	if b.match {
		return false
	}
	if b.patterns == nil {
		return true
	}
	ok := false
	for _, p := range b.patterns {
		neg := strings.HasPrefix(p, "!")
		p = strings.TrimPrefix(p, "!")
		m, _ := path.Match(p, alias)
		if m && neg {
			return false
		}
		if m {
			ok = true
		}
	}
	return ok
}

func isWildcard(p string) bool { return strings.ContainsAny(p, "*?![]") }

// sshConfigParser accumulates blocks across Include files.
type sshConfigParser struct {
	blocks  []*sshBlock
	aliases []string
	seen    map[string]bool
	home    string
}

// splitDirective splits "Key value", "Key=value" and "Key = value".
func splitDirective(line string) (key, value string, ok bool) {
	line = strings.TrimSpace(line)
	if line == "" || strings.HasPrefix(line, "#") {
		return "", "", false
	}
	i := strings.IndexAny(line, " \t=")
	if i < 0 {
		return strings.ToLower(line), "", true
	}
	key = strings.ToLower(line[:i])
	value = strings.TrimSpace(line[i:])
	value = strings.TrimSpace(strings.TrimPrefix(value, "="))
	return key, value, true
}

// splitArgs splits a value into words, honouring double quotes.
func splitArgs(v string) []string {
	var out []string
	var cur strings.Builder
	inQ, has := false, false
	for _, r := range v {
		switch {
		case r == '"':
			inQ = !inQ
			has = true
		case (r == ' ' || r == '\t') && !inQ:
			if has {
				out = append(out, cur.String())
				cur.Reset()
				has = false
			}
		default:
			cur.WriteRune(r)
			has = true
		}
	}
	if has {
		out = append(out, cur.String())
	}
	return out
}

func (p *sshConfigParser) current() *sshBlock {
	if len(p.blocks) == 0 {
		p.blocks = append(p.blocks, &sshBlock{})
	}
	return p.blocks[len(p.blocks)-1]
}

func (p *sshConfigParser) parse(r io.Reader, baseDir string, depth int) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	for sc.Scan() {
		key, value, ok := splitDirective(sc.Text())
		if !ok {
			continue
		}
		switch key {
		case "host":
			b := &sshBlock{patterns: splitArgs(value)}
			p.blocks = append(p.blocks, b)
			for _, pat := range b.patterns {
				if !isWildcard(pat) && !p.seen[pat] {
					p.seen[pat] = true
					p.aliases = append(p.aliases, pat)
				}
			}
		case "match":
			p.blocks = append(p.blocks, &sshBlock{match: true})
		case "include":
			if depth >= maxIncludeDepth {
				continue
			}
			for _, inc := range splitArgs(value) {
				inc = p.expand(inc)
				if !filepath.IsAbs(inc) {
					inc = filepath.Join(baseDir, inc)
				}
				files, _ := filepath.Glob(inc)
				for _, f := range files {
					fh, err := os.Open(f)
					if err != nil {
						continue
					}
					p.parse(fh, baseDir, depth+1)
					_ = fh.Close()
				}
			}
		default:
			b := p.current()
			b.settings = append(b.settings, [2]string{key, value})
		}
	}
}

func (p *sshConfigParser) expand(s string) string {
	if s == "~" || strings.HasPrefix(s, "~/") {
		s = p.home + s[1:]
	}
	return strings.ReplaceAll(s, "%d", p.home)
}

// ParseSSHConfig reads an OpenSSH client config and returns one host per
// concrete Host alias (wildcard patterns are skipped, Match blocks ignored).
// Values follow ssh's first-match-wins rule across matching blocks, so a
// trailing `Host *` supplies defaults. Include is followed (relative to
// baseDir, globbed, best effort). home expands ~ and %d.
func ParseSSHConfig(r io.Reader, baseDir, home string) []backend.Host {
	p := &sshConfigParser{seen: map[string]bool{}, home: home}
	p.parse(r, baseDir, 0)
	var out []backend.Host
	for _, alias := range p.aliases {
		vals := map[string]string{}
		for _, b := range p.blocks {
			if !b.matches(alias) {
				continue
			}
			for _, kv := range b.settings {
				if _, set := vals[kv[0]]; !set {
					vals[kv[0]] = kv[1]
				}
			}
		}
		h := backend.Host{ID: Slug(alias), Name: alias, Addr: alias, SSHAlias: alias}
		if hn := firstArg(vals["hostname"]); hn != "" {
			h.Addr = strings.ReplaceAll(hn, "%h", alias)
		}
		h.User = firstArg(vals["user"])
		if port, err := strconv.Atoi(firstArg(vals["port"])); err == nil && port > 0 && port != 22 {
			h.Port = port
		}
		if id := firstArg(vals["identityfile"]); id != "" && !strings.EqualFold(id, "none") {
			h.KeyFile = p.expand(id)
			h.Auth = backend.AuthKeyFile
		}
		out = append(out, h)
	}
	return out
}

func firstArg(v string) string {
	a := splitArgs(v)
	if len(a) == 0 {
		return ""
	}
	return a[0]
}

// ImportSSHConfig parses ~/.ssh/config (a missing file = no hosts).
func ImportSSHConfig() ([]backend.Host, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}
	return ImportSSHConfigFile(filepath.Join(home, ".ssh", "config"), home)
}

// ImportSSHConfigFile parses one config file; Include paths are relative to
// its directory.
func ImportSSHConfigFile(file, home string) ([]backend.Host, error) {
	f, err := os.Open(file)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return ParseSSHConfig(f, filepath.Dir(file), home), nil
}
