package maint

import (
	"archive/zip"
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	gdshpkg "github.com/brohd11/gdsh-pkg"
)

func repoZip(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for p, body := range files {
		w, _ := zw.Create("wrap/" + p)
		w.Write([]byte(body))
	}
	zw.Close()
	return buf.Bytes()
}

func cfg(name, version string, require ...string) string {
	return "[package]\nversion=\"" + version + "\"\npath=\"addons/_lib/gdsh_lib/" + name + "\"\nrequire=[\"" + strings.Join(require, `","`) + "\"]\n"
}

const base = `extends "res://addons/_lib/gdsh/src/core/command_base.gd"` + "\n"

// fake serves archives by commit, raw files, and a release redirect for "o/terrain3d" only.
func fake(t *testing.T) (*Maint, map[string]map[string]string) {
	t.Helper()
	archives := map[string][]byte{
		"/o/level/archive/c1.zip": repoZip(t, map[string]string{
			"package.cfg": cfg("level", "0.1.0", "brohd11/godot-gdsh@v0.4.5"), "cmd/manifest.gd": `const X = preload("res://addons/_lib/gdsh_lib/level/cmd/level/level.gd")`,
			"cmd/level/level.gd": base,
			// A doc comment and another addon's file are not relocation problems.
			"src/ground.gd": "## see \"res://addons/_lib/gdsh_lib/level/src/ground.gd\"\nconst T = \"res://addons/terrain_3d/menu/template.txt\"\n"}),
		"/o/terrain3d/archive/c2.zip": repoZip(t, map[string]string{
			"package.cfg": cfg("terrain3d", "0.1.0", "brohd11/godot-gdsh@v0.4.6", "o/level@v0.1.0", "o/util-r@v1", "o/stray@v1"), "cmd/manifest.gd": "",
			"cmd/terrain3d/terrain3d.gd": base}),
		"/o/tree/archive/c3.zip": repoZip(t, map[string]string{
			"package.cfg": cfg("tree", "0.3.0"), "cmd/manifest.gd": "",
			"cmd/tree/tree.gd": base + `const U = preload("res://addons/_lib/gdsh_lib/tree/src/u.gd")`}),
		"/o/level/archive/c9.zip": repoZip(t, map[string]string{
			"package.cfg": cfg("level", "0.2.0", "brohd11/godot-gdsh@v0.4.5"), "cmd/manifest.gd": "", "cmd/level/level.gd": base}),
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case archives[r.URL.Path] != nil:
			w.Write(archives[r.URL.Path])
		case r.URL.Path == "/raw/o/stray/v1/cmd/manifest.gd":
			w.Write([]byte("extends RefCounted\n"))
		case r.URL.Path == "/o/terrain3d/releases/latest":
			http.Redirect(w, r, "/o/terrain3d/releases/tag/v0.3.0", http.StatusFound)
		case strings.HasSuffix(r.URL.Path, "/releases/latest"):
			http.Redirect(w, r, strings.TrimSuffix(r.URL.Path, "/latest"), http.StatusFound)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	tags := map[string]map[string]string{
		"o/level":     {"v0.1.0": "c1", "v0.2.0": "c9", "nightly": "zz"},
		"o/terrain3d": {"v0.1.0": "moved"},
		"o/tree":      {"v0.3.0": "c3"},
	}
	return &Maint{
		Client: &gdshpkg.Client{ArchiveURL: func(repo, commit string) string { return srv.URL + "/" + repo + "/archive/" + commit + ".zip" }},
		GitHub: srv.URL,
		RawURL: func(repo, ref, file string) string { return srv.URL + "/raw/" + repo + "/" + ref + "/" + file },
		LsRemote: func(_ context.Context, repo string) (map[string]string, error) {
			return tags[repo], nil
		},
	}, tags
}

func manifest(t *testing.T) *gdshpkg.Manifest {
	t.Helper()
	m, err := gdshpkg.ParseManifest([]byte(`{"version": 1, "packages": [
  {"name": "level", "repo": "o/level", "summary": "s", "tag": "v0.1.0", "commit": "c1", "requires": []},
  {"name": "terrain3d", "repo": "o/terrain3d", "summary": "s", "tag": "v0.1.0", "commit": "c2", "requires": []},
  {"name": "tree", "repo": "o/tree", "summary": "s", "tag": "v0.3.0", "commit": "c3", "requires": []}
]}`))
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func has(problems []Problem, isErr bool, pkg, substr string) bool {
	for _, p := range problems {
		if p.Error == isErr && p.Pkg == pkg && strings.Contains(p.Msg, substr) {
			return true
		}
	}
	return false
}

func TestDerive(t *testing.T) {
	m, _ := fake(t)
	man := manifest(t)
	problems, err := m.Derive(context.Background(), man, "")
	if err != nil {
		t.Fatal(err)
	}
	level, _ := man.Find("level")
	if level.Gdsh != "v0.4.5" || len(level.Requires) != 0 || level.ProjectOnly != nil {
		t.Errorf("level: %+v", level)
	}
	t3d, _ := man.Find("terrain3d")
	if t3d.Gdsh != "v0.4.6" || strings.Join(t3d.Requires, ",") != "o/level" || strings.Join(t3d.ProjectOnly, ",") != "o/util-r" {
		t.Errorf("terrain3d: %+v", t3d)
	}
	for _, want := range []struct {
		isErr     bool
		pkg, text string
	}{
		{false, "terrain3d", "moved from the reviewed commit c2"},
		{true, "terrain3d", "o/stray, a command package missing from the index"},
		{true, "tree", `not relocatable (a command root is outside res://addons): cmd/tree/tree.gd:2 "res://addons/_lib/gdsh_lib/tree/src/u.gd"`},
	} {
		if !has(problems, want.isErr, want.pkg, want.text) {
			t.Errorf("missing problem %+v in %v", want, problems)
		}
	}
	if has(problems, true, "level", "") {
		t.Errorf("level should be clean (manifest.gd's absolute preloads are allowed): %v", problems)
	}
}

func TestPinAndPropose(t *testing.T) {
	m, _ := fake(t)
	man := manifest(t)
	ctx := context.Background()
	if _, err := m.Pin(ctx, man, "level", "v9"); err == nil {
		t.Error("an unknown tag should fail")
	}
	problems, err := m.Pin(ctx, man, "level", "v0.2.0")
	if err != nil || len(problems) != 0 {
		t.Fatalf("pin: %v %v", err, problems)
	}
	if level, _ := man.Find("level"); level.Tag != "v0.2.0" || level.Commit != "c9" {
		t.Errorf("pinned: %+v", level)
	}

	// terrain3d has a release; level has none, so its highest version tag counts (not "nightly").
	man.Packages[0].Tag = "v0.1.0"
	props, err := m.Propose(ctx, man)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, p := range props {
		got[p.Name] = p.From + ">" + p.To
	}
	if len(got) != 2 || got["level"] != "v0.1.0>v0.2.0" || got["terrain3d"] != "v0.1.0>v0.3.0" {
		t.Errorf("propose: %v", props)
	}
	if props[0].CompareURL() == "" || !strings.Contains(props[0].CompareURL(), "/compare/v0.1.0...") {
		t.Errorf("compare: %s", props[0].CompareURL())
	}
}
