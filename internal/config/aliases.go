package config

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/BurntSushi/toml"
)

// Environment variables for project aliases (AGENTS.md, "Project aliases").
const (
	// EnvAliasesFile overrides the aliases file path.
	EnvAliasesFile = "AGENTD_ALIASES_FILE"
	// EnvAliases holds extra aliases as comma-separated name=path pairs;
	// they override the file.
	EnvAliases = "AGENTD_ALIASES"
)

const aliasesName = "aliases.toml"

// Alias sources.
const (
	AliasSourceFile = "file"
	AliasSourceEnv  = "env"
)

// genericBasenames are directory names too vague to label a project alone;
// they are prefixed with their parent directory name.
var genericBasenames = map[string]bool{"devops": true, "app": true, "src": true, "repo": true}

// AliasesPath returns the aliases file path: $AGENTD_ALIASES_FILE if set,
// else $XDG_CONFIG_HOME/agentd/aliases.toml, else ~/.config/agentd/aliases.toml.
func AliasesPath() (string, error) { return AliasesPathEnv(os.Getenv) }

// AliasesPathEnv is AliasesPath reading the variables through getenv
// (AGENTD_ALIASES_FILE, XDG_CONFIG_HOME, HOME) instead of the process
// environment. A missing HOME falls back to the OS user's home.
func AliasesPathEnv(getenv func(string) string) (string, error) {
	if p := getenv(EnvAliasesFile); p != "" {
		return p, nil
	}
	if d := getenv("XDG_CONFIG_HOME"); d != "" {
		return filepath.Join(d, appDirName, aliasesName), nil
	}
	home, err := homeEnv(getenv)
	if err != nil {
		return "", fmt.Errorf("config: resolve aliases path: %w", err)
	}
	return filepath.Join(home, ".config", appDirName, aliasesName), nil
}

func homeEnv(getenv func(string) string) (string, error) {
	if h := getenv("HOME"); h != "" {
		return h, nil
	}
	return os.UserHomeDir()
}

// EnvLookup returns a getenv function over a "KEY=VALUE" list (as in
// os.Environ; the last duplicate wins).
func EnvLookup(env []string) func(string) string {
	m := make(map[string]string, len(env))
	for _, kv := range env {
		if k, v, ok := strings.Cut(kv, "="); ok {
			m[k] = v
		}
	}
	return func(k string) string { return m[k] }
}

// Alias maps a short project name to a working directory.
type Alias struct {
	Name   string
	Dir    string // absolute, cleaned
	Source string // AliasSourceFile or AliasSourceEnv
}

// Aliases is a set of project aliases. A nil *Aliases is valid and empty.
type Aliases struct {
	byName map[string]Alias
	getenv func(string) string // for "~" expansion; nil = process environment
}

// ValidAliasName reports whether s is a valid alias name: the session-name
// charset without '@' ([a-zA-Z0-9._-]+, not "." or "..").
func ValidAliasName(s string) error {
	if err := ValidName(s); err != nil || strings.Contains(s, "@") {
		return fmt.Errorf("%w %q: alias names must match [a-zA-Z0-9._-]+", ErrInvalidName, s)
	}
	return nil
}

// LoadAliases loads aliases from the TOML file at path (a missing file means
// no file aliases) and then from env (the AGENTD_ALIASES syntax), whose
// entries override the file. The file is a flat table of name = "path";
// non-string values, invalid names and non-absolute paths are errors.
func LoadAliases(path string, env string) (*Aliases, error) {
	a := &Aliases{byName: map[string]Alias{}}
	if path != "" {
		if err := a.loadFile(path); err != nil {
			return nil, err
		}
	}
	if err := a.loadEnv(env); err != nil {
		return nil, err
	}
	return a, nil
}

// LoadDefaultAliases loads aliases from AliasesPath() and $AGENTD_ALIASES.
func LoadDefaultAliases() (*Aliases, error) { return LoadAliasesEnv(os.Getenv) }

// LoadAliasesEnv is LoadDefaultAliases reading AGENTD_ALIASES_FILE,
// AGENTD_ALIASES, XDG_CONFIG_HOME and HOME (also for "~" expansion) through
// getenv. The daemon uses it with the caller's env_base so a run resolves
// aliases like the CLI that sent it.
func LoadAliasesEnv(getenv func(string) string) (*Aliases, error) {
	p, err := AliasesPathEnv(getenv)
	if err != nil {
		return nil, err
	}
	a := &Aliases{byName: map[string]Alias{}, getenv: getenv}
	if err := a.loadFile(p); err != nil {
		return nil, err
	}
	if err := a.loadEnv(getenv(EnvAliases)); err != nil {
		return nil, err
	}
	return a, nil
}

func (a *Aliases) loadFile(path string) error {
	var raw map[string]any
	if _, err := toml.DecodeFile(path, &raw); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("config: load aliases %s: %w", path, err)
	}
	for name, v := range raw {
		s, ok := v.(string)
		if !ok {
			return fmt.Errorf("config: load aliases %s: %q: value must be a string path, got %T", path, name, v)
		}
		if err := a.add(name, s, AliasSourceFile); err != nil {
			return fmt.Errorf("config: load aliases %s: %w", path, err)
		}
	}
	return nil
}

func (a *Aliases) loadEnv(env string) error {
	for _, item := range strings.Split(env, ",") {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		name, dir, ok := strings.Cut(item, "=")
		if !ok {
			return fmt.Errorf("config: %s: %q: want name=path", EnvAliases, item)
		}
		if err := a.add(strings.TrimSpace(name), strings.TrimSpace(dir), AliasSourceEnv); err != nil {
			return fmt.Errorf("config: %s: %w", EnvAliases, err)
		}
	}
	return nil
}

func (a *Aliases) add(name, dir, source string) error {
	if err := ValidAliasName(name); err != nil {
		return err
	}
	d, err := expandAliasDir(dir, a.getenv)
	if err != nil {
		return fmt.Errorf("alias %q: %w", name, err)
	}
	a.byName[name] = Alias{Name: name, Dir: d, Source: source}
	return nil
}

// expandAliasDir expands a leading "~" or "~/" to the home directory and
// requires the result to be absolute. The result is cleaned.
func expandAliasDir(p string, getenv func(string) string) (string, error) {
	if getenv == nil {
		getenv = os.Getenv
	}
	if p == "" {
		return "", errors.New("empty path")
	}
	if p == "~" || strings.HasPrefix(p, "~/") {
		home, err := homeEnv(getenv)
		if err != nil {
			return "", fmt.Errorf("expand %q: %w", p, err)
		}
		p = filepath.Join(home, p[1:])
	}
	if !filepath.IsAbs(p) {
		return "", fmt.Errorf("path %q must be absolute", p)
	}
	return filepath.Clean(p), nil
}

// Lookup returns the alias called name.
func (a *Aliases) Lookup(name string) (Alias, bool) {
	if a == nil {
		return Alias{}, false
	}
	al, ok := a.byName[name]
	return al, ok
}

// List returns all aliases sorted by name.
func (a *Aliases) List() []Alias {
	if a == nil {
		return nil
	}
	out := make([]Alias, 0, len(a.byName))
	for _, al := range a.byName {
		out = append(out, al)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// ResolveTarget resolves arg (an alias name or a directory path) to a working
// directory and a project label for session names. An alias match wins:
// dir = its Dir, label = its name. Otherwise arg is a path (relative paths
// are resolved against the current directory) that must exist and be a
// directory; label = the first alias (by name) whose Dir equals it, else its
// basename, with generic basenames (devops, app, src, repo) turned into
// "<parent>-<base>". Labels are sanitized to the alias-name charset.
func (a *Aliases) ResolveTarget(arg string) (dir, label string, err error) {
	return a.ResolveTargetIn(arg, "")
}

// ResolveTargetIn is ResolveTarget with an explicit base directory: a
// relative path (not an alias) is resolved against base instead of the
// process's current directory. An empty base means the current directory.
// The daemon uses it to resolve a client's relative target against the
// client's working directory.
func (a *Aliases) ResolveTargetIn(arg, base string) (dir, label string, err error) {
	if arg == "" {
		return "", "", errors.New("config: empty target: want an alias or a directory")
	}
	if al, ok := a.Lookup(arg); ok {
		if err := checkDir(al.Dir); err != nil {
			return "", "", fmt.Errorf("config: alias %q: %w", arg, err)
		}
		return al.Dir, al.Name, nil
	}
	if base != "" && !filepath.IsAbs(arg) {
		if !filepath.IsAbs(base) {
			return "", "", fmt.Errorf("config: base directory %q must be absolute", base)
		}
		arg = filepath.Join(base, arg)
	}
	dir, err = filepath.Abs(arg)
	if err != nil {
		return "", "", fmt.Errorf("config: resolve %q: %w", arg, err)
	}
	if err := checkDir(dir); err != nil {
		return "", "", fmt.Errorf("config: target %q is not an alias or a directory: %w", arg, err)
	}
	for _, al := range a.List() {
		if al.Dir == dir {
			return dir, al.Name, nil
		}
	}
	return dir, dirLabel(dir), nil
}

func checkDir(dir string) error {
	fi, err := os.Stat(dir)
	if err != nil {
		return err
	}
	if !fi.IsDir() {
		return fmt.Errorf("%s is not a directory", dir)
	}
	return nil
}

// dirLabel derives a project label from an absolute, cleaned directory.
func dirLabel(dir string) string {
	base := filepath.Base(dir)
	if base == string(filepath.Separator) {
		return "root"
	}
	if genericBasenames[base] {
		if parent := filepath.Base(filepath.Dir(dir)); parent != string(filepath.Separator) {
			base = parent + "-" + base
		}
	}
	return sanitizeLabel(base)
}

// sanitizeLabel replaces every character outside [a-zA-Z0-9._-] with '-'.
func sanitizeLabel(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '.', r == '_', r == '-':
			b.WriteRune(r)
		default:
			b.WriteByte('-')
		}
	}
	out := b.String()
	if out == "." || out == ".." {
		return strings.Repeat("-", len(out))
	}
	return out
}
