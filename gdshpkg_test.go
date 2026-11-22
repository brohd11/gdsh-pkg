package gdshpkg

import (
	"archive/zip"
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseCfg(t *testing.T) {
	cfg, err := ParseCfg([]byte(`[package]

name="gdsh-lib-utils"
version="0.4.0"
path="addons/_lib/gdsh_lib/utils"

require=[
"brohd11/godot-gdsh@v0.4.5",
"brohd11/godot-util-r@v0.4.2",
]

[namespace]
path=""
`))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Version != "0.4.0" || cfg.InstallName() != "utils" || len(cfg.Require) != 2 || cfg.Require[1] != "brohd11/godot-util-r@v0.4.2" {
		t.Errorf("cfg: %+v", cfg)
	}
	if repo, tag := SplitRequire("https://github.com/brohd11/godot-gdsh.git@v0.4.5"); repo != GdshRepo || tag != "v0.4.5" {
		t.Errorf("SplitRequire: %s %s", repo, tag)
	}
}

func TestCompareVersions(t *testing.T) {
	for _, c := range []struct {
		a, b string
		want int
	}{{"v0.1.0", "0.1.0", 0}, {"0.2.0", "v0.10.0", -1}, {"1.0", "0.9.9", 1}, {"0.1", "0.1.0", 0}} {
		if got := CompareVersions(c.a, c.b); got != c.want {
			t.Errorf("%s vs %s: %d, want %d", c.a, c.b, got, c.want)
		}
	}
}

// pkgZip is a repo archive as GitHub serves it: everything under one wrapper folder.
func pkgZip(t *testing.T, name, version string, require ...string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	reqs := ""
	for _, r := range require {
		reqs += `"` + r + `",`
	}
	files := map[string]string{
		"package.cfg":                      "[package]\nversion=\"" + version + "\"\npath=\"addons/_lib/gdsh_lib/" + name + "\"\nrequire=[" + reqs + "]\n",
		"cmd/manifest.gd":                  "extends RefCounted\n",
		"cmd/" + name + "/" + name + ".gd": "# command\n",
		"src/util.gd":                      "# src\n",
		"_export_ignore/test/x_test.gd":    "# test\n",
		".github/workflows/ci.yml":         "on: push\n",
		".gitignore":                       "*.tmp\n",
	}
	zw.Create("godot-gdsh-lib-" + name + "-abc/")
	for p, body := range files {
		w, _ := zw.Create("godot-gdsh-lib-" + name + "-abc/" + p)
		w.Write([]byte(body))
	}
	zw.Close()
	return buf.Bytes()
}

const testIndex = `{"version": 1, "packages": [
  {"name": "level", "repo": "o/godot-gdsh-lib-level", "summary": "Level commands", "tags": ["3d"],
   "tag": "v0.2.0", "commit": "c1", "gdsh": "v0.4.5", "requires": []},
  {"name": "terrain3d", "repo": "o/godot-gdsh-lib-terrain3d", "summary": "Terrain", "tags": ["3d"],
   "tag": "v0.1.0", "commit": "c2", "requires": ["o/godot-gdsh-lib-level"]},
  {"name": "utils", "repo": "o/godot-gdsh-lib-utils", "summary": "cat, ls",
   "tag": "v0.4.0", "commit": "c3", "requires": [], "project_only": ["o/godot-util-r"]}
]}`

func fakeGitHub(t *testing.T) (*Client, *int) {
	t.Helper()
	zips := map[string][]byte{
		"/o/godot-gdsh-lib-level/archive/c1.zip":     pkgZip(t, "level", "0.2.0"),
		"/o/godot-gdsh-lib-terrain3d/archive/c2.zip": pkgZip(t, "terrain3d", "0.1.0", "brohd11/godot-gdsh@v0.4.5", "o/godot-gdsh-lib-level@v0.1.0"),
	}
	hits := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/manifest.json" {
			hits++
			w.Write([]byte(testIndex))
			return
		}
		if z, ok := zips[r.URL.Path]; ok {
			w.Write(z)
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(srv.Close)
	return &Client{
		ManifestURL: srv.URL + "/manifest.json",
		ArchiveURL:  func(repo, commit string) string { return srv.URL + "/" + repo + "/archive/" + commit + ".zip" },
		CacheDir:    t.TempDir(),
	}, &hits
}

func TestFetchIndexCache(t *testing.T) {
	c, hits := fakeGitHub(t)
	ctx := context.Background()
	if _, err := c.FetchIndex(ctx, false); err != nil {
		t.Fatal(err)
	}
	if _, err := c.FetchIndex(ctx, false); err != nil || *hits != 1 {
		t.Errorf("a fresh cache should be used: hits %d, %v", *hits, err)
	}
	if _, err := c.FetchIndex(ctx, true); err != nil || *hits != 2 {
		t.Errorf("refresh should download: hits %d, %v", *hits, err)
	}
	c.ManifestURL = "http://127.0.0.1:1/manifest.json"
	idx, err := c.FetchIndex(ctx, true)
	if err != nil || !idx.Stale || len(idx.Packages) != 3 {
		t.Errorf("offline should fall back to the cache: %+v %v", idx, err)
	}
	c.CacheDir = t.TempDir()
	if _, err := c.FetchIndex(ctx, true); err == nil {
		t.Error("offline without a cache should fail")
	}
}

func TestPlanAndInstall(t *testing.T) {
	c, _ := fakeGitHub(t)
	ctx := context.Background()
	idx, err := c.FetchIndex(ctx, false)
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()

	steps, err := idx.Plan(root, []string{"terrain3d"}, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(steps) != 2 || steps[0].Pkg.Name != "level" || steps[0].For != "terrain3d" || steps[1].Action != ActInstall {
		t.Fatalf("plan: %+v", steps)
	}
	var lines []string
	if err := c.Install(ctx, root, steps, func(s string) { lines = append(lines, s) }); err != nil {
		t.Fatal(err)
	}
	level := filepath.Join(root, "packages", "level")
	for _, p := range []string{"package.cfg", "cmd/level/level.gd", "src/util.gd"} {
		if _, err := os.Stat(filepath.Join(level, p)); err != nil {
			t.Error(err)
		}
	}
	for _, p := range []string{"_export_ignore", ".github", ".gitignore"} {
		if _, err := os.Stat(filepath.Join(level, p)); err == nil {
			t.Errorf("%s should be stripped", p)
		}
	}
	if _, err := os.Stat(filepath.Join(root, ".pkg-staging")); err == nil {
		t.Error("staging folder left behind")
	}
	if strings.Join(lines, "|") != "Installed level v0.2.0|Installed terrain3d v0.1.0" {
		t.Errorf("report: %q", lines)
	}

	// Installed and current: kept, unless forced.
	steps, _ = idx.Plan(root, []string{"terrain3d"}, false)
	if steps[0].Action != ActKeep || steps[1].Action != ActKeep || steps[1].Installed != "0.1.0" {
		t.Errorf("replan: %+v", steps)
	}
	if steps, _ = idx.Plan(root, []string{"level"}, true); steps[0].Action != ActUpdate {
		t.Errorf("force: %+v", steps)
	}

	// An older install is outdated and updates in place.
	os.WriteFile(filepath.Join(level, "package.cfg"), []byte("[package]\nversion=\"0.1.0\"\npath=\"x/level\"\n"), 0o644)
	installed, _ := ListInstalled(root)
	if up := idx.Outdated(installed); len(up) != 1 || up[0].Name != "level" || up[0].Pkg.Tag != "v0.2.0" {
		t.Errorf("outdated: %+v", up)
	}
	steps, _ = idx.Plan(root, []string{"level"}, false)
	if err := c.Install(ctx, root, steps, nil); err != nil || steps[0].Action != ActUpdate {
		t.Fatalf("update: %v %+v", err, steps)
	}
	if installed, _ = ListInstalled(root); installed[0].Cfg.Version != "0.2.0" {
		t.Errorf("after update: %+v", installed[0].Cfg)
	}

	// level is needed by terrain3d.
	if deps := Dependents(installed, "level", idx.Manifest); len(deps) != 1 || deps[0] != "terrain3d" {
		t.Errorf("dependents: %v", deps)
	}
	if err := Remove(root, "terrain3d"); err != nil {
		t.Fatal(err)
	}
	if err := Remove(root, "terrain3d"); err == nil {
		t.Error("removing twice should fail")
	}
	if err := Remove(root, "../x"); err == nil {
		t.Error("a path should not be removable")
	}
}

func TestPlanRefusals(t *testing.T) {
	c, _ := fakeGitHub(t)
	idx, _ := c.FetchIndex(context.Background(), false)
	_, err := idx.Plan(t.TempDir(), []string{"utils"}, false)
	var po ProjectOnlyError
	if !errors.As(err, &po) || !strings.Contains(err.Error(), "gdaddon install o/godot-gdsh-lib-utils") {
		t.Errorf("project_only: %v", err)
	}
	if _, err := idx.Plan(t.TempDir(), []string{"nope"}, false); err == nil || !strings.Contains(err.Error(), "not in the package index") {
		t.Errorf("unknown: %v", err)
	}
}

func TestInstallChecksArchive(t *testing.T) {
	c, _ := fakeGitHub(t)
	idx, _ := c.FetchIndex(context.Background(), false)
	// The index says "level" but the archive's path names another folder.
	bad := *idx.Manifest
	bad.Packages = append([]Package(nil), idx.Packages...)
	bad.Packages[0].Name = "lvl"
	steps, _ := bad.Plan(t.TempDir(), []string{"lvl"}, false)
	root := t.TempDir()
	if err := c.Install(context.Background(), root, steps, nil); err == nil || !strings.Contains(err.Error(), `names "level"`) {
		t.Errorf("name mismatch: %v", err)
	}
	if entries, _ := os.ReadDir(root); len(entries) != 0 {
		t.Errorf("a failed install left %v", entries)
	}
}

func TestExtractRefusesEscapes(t *testing.T) {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, _ := zw.Create("../evil.gd")
	w.Write([]byte("x"))
	zw.Close()
	if err := Extract(buf.Bytes(), t.TempDir()); err == nil {
		t.Error("an escaping path should be refused")
	}
}

func TestSearch(t *testing.T) {
	m, _ := ParseManifest([]byte(testIndex))
	if got := m.Search("3D"); len(got) != 2 {
		t.Errorf("search 3D: %v", got)
	}
	if _, err := ParseManifest([]byte(`{"version": 2, "packages": []}`)); err == nil {
		t.Error("an unknown version should be refused")
	}
}
