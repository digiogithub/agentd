package config

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeAliases(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "aliases.toml")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestAliasesPath(t *testing.T) {
	home := t.TempDir()
	tests := []struct {
		name string
		env  map[string]string
		want string
	}{
		{"override", map[string]string{EnvAliasesFile: "/a.toml", "XDG_CONFIG_HOME": "/y"}, "/a.toml"},
		{"xdg", map[string]string{"XDG_CONFIG_HOME": "/y"}, "/y/agentd/aliases.toml"},
		{"home fallback", map[string]string{}, filepath.Join(home, ".config/agentd/aliases.toml")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("HOME", home)
			for _, k := range []string{EnvAliasesFile, "XDG_CONFIG_HOME"} {
				t.Setenv(k, tt.env[k])
			}
			got, err := AliasesPath()
			if err != nil || got != tt.want {
				t.Fatalf("AliasesPath() = %q, %v; want %q", got, err, tt.want)
			}
		})
	}
}

func TestLoadAliases(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	tests := []struct {
		name    string
		file    string // "" = missing file
		env     string
		want    []Alias
		wantErr string
	}{
		{
			name: "missing file, no env",
			want: []Alias{},
		},
		{
			name: "file",
			file: "models = \"/home/user/src/webapp\"\n\"api-team\" = \"/srv/api-team/\"\n",
			want: []Alias{
				{"api-team", "/srv/api-team", AliasSourceFile},
				{"models", "/home/user/src/webapp", AliasSourceFile},
			},
		},
		{
			name: "env",
			env:  " models=/home/user/src/webapp , ,api = /home/user/src/api,",
			want: []Alias{
				{"api", "/home/user/src/api", AliasSourceEnv},
				{"models", "/home/user/src/webapp", AliasSourceEnv},
			},
		},
		{
			name: "env overrides file",
			file: "models = \"/old\"\ninfra = \"/home/user/work/infra\"\n",
			env:  "models=/new",
			want: []Alias{
				{"infra", "/home/user/work/infra", AliasSourceFile},
				{"models", "/new", AliasSourceEnv},
			},
		},
		{
			name: "tilde expansion",
			file: "h = \"~\"\nw = \"~/work/../work/x\"\n",
			env:  "e=~/e",
			want: []Alias{
				{"e", filepath.Join(home, "e"), AliasSourceEnv},
				{"h", home, AliasSourceFile},
				{"w", filepath.Join(home, "work/x"), AliasSourceFile},
			},
		},
		{name: "relative file path", file: "x = \"rel/dir\"\n", wantErr: "must be absolute"},
		{name: "relative env path", env: "x=./rel", wantErr: "must be absolute"},
		{name: "tilde user not expanded", env: "x=~bob/dir", wantErr: "must be absolute"},
		{name: "empty env path", env: "x=", wantErr: "empty path"},
		{name: "env missing equals", env: "models", wantErr: "want name=path"},
		{name: "env invalid name", env: "codex@models=/x", wantErr: "invalid session name"},
		{name: "env empty name", env: "=/x", wantErr: "invalid session name"},
		{name: "file invalid name", file: "\"a b\" = \"/x\"\n", wantErr: "invalid session name"},
		{name: "file name with at", file: "\"a@b\" = \"/x\"\n", wantErr: "invalid session name"},
		{name: "file dotdot name", file: "\"..\" = \"/x\"\n", wantErr: "invalid session name"},
		{name: "file non-string value", file: "x = 3\n", wantErr: "must be a string"},
		{name: "file nested table", file: "[x]\ny = \"/z\"\n", wantErr: "must be a string"},
		{name: "file bad toml", file: "x = \n", wantErr: "load aliases"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := filepath.Join(t.TempDir(), "missing.toml")
			if tt.file != "" {
				p = writeAliases(t, tt.file)
			}
			a, err := LoadAliases(p, tt.env)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("LoadAliases() err = %v, want containing %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("LoadAliases() err = %v", err)
			}
			got := a.List()
			if len(got) != len(tt.want) {
				t.Fatalf("List() = %v, want %v", got, tt.want)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Fatalf("List()[%d] = %+v, want %+v", i, got[i], tt.want[i])
				}
				if l, ok := a.Lookup(got[i].Name); !ok || l != got[i] {
					t.Fatalf("Lookup(%q) = %+v, %v", got[i].Name, l, ok)
				}
			}
		})
	}
}

func TestLoadDefaultAliases(t *testing.T) {
	p := writeAliases(t, "models = \"/m\"\n")
	t.Setenv(EnvAliasesFile, p)
	t.Setenv(EnvAliases, "api=/b")
	a, err := LoadDefaultAliases()
	if err != nil {
		t.Fatal(err)
	}
	if m, ok := a.Lookup("models"); !ok || m.Dir != "/m" || m.Source != AliasSourceFile {
		t.Fatalf("Lookup(models) = %+v, %v", m, ok)
	}
	if b, ok := a.Lookup("api"); !ok || b.Dir != "/b" || b.Source != AliasSourceEnv {
		t.Fatalf("Lookup(api) = %+v, %v", b, ok)
	}
}

func TestValidAliasName(t *testing.T) {
	for _, s := range []string{"models", "api-team", "a.b_c-1", "X"} {
		if err := ValidAliasName(s); err != nil {
			t.Errorf("ValidAliasName(%q) = %v, want nil", s, err)
		}
	}
	for _, s := range []string{"", ".", "..", "a@b", "a b", "a/b", "ñ"} {
		if err := ValidAliasName(s); !errors.Is(err, ErrInvalidName) {
			t.Errorf("ValidAliasName(%q) = %v, want ErrInvalidName", s, err)
		}
	}
}

func TestNilAliases(t *testing.T) {
	var a *Aliases
	if _, ok := a.Lookup("x"); ok {
		t.Fatal("nil Lookup found an alias")
	}
	if l := a.List(); len(l) != 0 {
		t.Fatalf("nil List() = %v", l)
	}
	dir := t.TempDir()
	got, label, err := a.ResolveTarget(dir)
	if err != nil || got != dir || label != filepath.Base(dir) {
		t.Fatalf("nil ResolveTarget(%q) = %q, %q, %v", dir, got, label, err)
	}
}

func TestResolveTarget(t *testing.T) {
	root := t.TempDir()
	mk := func(rel string) string {
		d := filepath.Join(root, rel)
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
		return d
	}
	models := mk("webapp")
	acme := mk("acme/devops")
	shop := mk("shop/devops")
	weird := mk("My Project+v2")
	plain := mk("plain")
	file := filepath.Join(root, "file.txt")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	a, err := LoadAliases("", "models="+models+",zz="+models+",shop="+shop+",gone="+filepath.Join(root, "gone"))
	if err != nil {
		t.Fatal(err)
	}
	t.Chdir(root)

	tests := []struct {
		name      string
		arg       string
		wantDir   string
		wantLabel string
		wantErr   string
	}{
		{name: "alias", arg: "models", wantDir: models, wantLabel: "models"},
		{name: "path matching alias", arg: models + "/", wantDir: models, wantLabel: "models"},
		{name: "relative path matching alias", arg: "shop/devops", wantDir: shop, wantLabel: "shop"},
		{name: "generic basename", arg: acme, wantDir: acme, wantLabel: "acme-devops"},
		{name: "relative plain", arg: "./plain", wantDir: plain, wantLabel: "plain"},
		{name: "sanitized label", arg: weird, wantDir: weird, wantLabel: "My-Project-v2"},
		{name: "nonexistent path", arg: "nope", wantErr: "not an alias or a directory"},
		{name: "file not dir", arg: "file.txt", wantErr: "is not a directory"},
		{name: "alias to missing dir", arg: "gone", wantErr: `alias "gone"`},
		{name: "empty", arg: "", wantErr: "empty target"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir, label, err := a.ResolveTarget(tt.arg)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("ResolveTarget(%q) err = %v, want containing %q", tt.arg, err, tt.wantErr)
				}
				return
			}
			if err != nil || dir != tt.wantDir || label != tt.wantLabel {
				t.Fatalf("ResolveTarget(%q) = %q, %q, %v; want %q, %q", tt.arg, dir, label, err, tt.wantDir, tt.wantLabel)
			}
		})
	}
}

func TestDirLabel(t *testing.T) {
	tests := []struct{ dir, want string }{
		{"/", "root"},
		{"/devops", "devops"},
		{"/srv/billing/devops", "billing-devops"},
		{"/x/proj/src", "proj-src"},
		{"/x/proj/app", "proj-app"},
		{"/x/proj/repo", "proj-repo"},
		{"/home/user/example.org", "example.org"},
		{"/x/a@b", "a-b"},
	}
	for _, tt := range tests {
		if got := dirLabel(tt.dir); got != tt.want {
			t.Errorf("dirLabel(%q) = %q, want %q", tt.dir, got, tt.want)
		}
	}
}

func TestLoadAliasesEnvUsesGivenEnvironment(t *testing.T) {
	home, dir := t.TempDir(), t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, "p"), 0o755); err != nil {
		t.Fatal(err)
	}
	xdg := t.TempDir()
	if err := os.MkdirAll(filepath.Join(xdg, "agentd"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(xdg, "agentd", "aliases.toml"), []byte("f = \"~/p\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	get := EnvLookup([]string{"HOME=" + home, "XDG_CONFIG_HOME=" + xdg, "AGENTD_ALIASES=e=" + dir, "BAD", "HOME=" + home})
	a, err := LoadAliasesEnv(get)
	if err != nil {
		t.Fatal(err)
	}
	if al, ok := a.Lookup("f"); !ok || al.Dir != filepath.Join(home, "p") || al.Source != AliasSourceFile {
		t.Fatalf("file alias with ~ against env HOME: %+v", al)
	}
	if al, ok := a.Lookup("e"); !ok || al.Dir != dir || al.Source != AliasSourceEnv {
		t.Fatalf("env alias: %+v", al)
	}
	if p, _ := AliasesPathEnv(EnvLookup([]string{"AGENTD_ALIASES_FILE=/x/y.toml", "XDG_CONFIG_HOME=" + xdg})); p != "/x/y.toml" {
		t.Fatalf("path = %q", p)
	}
}
