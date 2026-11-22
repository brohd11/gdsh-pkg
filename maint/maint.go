// Package maint keeps godot-gdsh-lib-manifest honest: it derives each entry's gdsh, requires
// and project_only from package.cfg at the pinned commit, checks the pin and the package shape,
// moves pins (Pin) and lists newer tags for review (Propose). It never changes a pin on its own.
package maint

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	gdshpkg "github.com/brohd11/gdsh-pkg"
)

// Maint talks to GitHub. Zero-value fields use github.com, raw.githubusercontent.com and the git
// binary; tests replace them.
type Maint struct {
	Client *gdshpkg.Client
	// GitHub is the web base, for releases/latest redirects.
	GitHub string
	// RawURL is one file of repo at ref.
	RawURL func(repo, ref, file string) string
	// LsRemote lists a repo's tags as tag → commit (annotated tags peeled).
	LsRemote func(ctx context.Context, repo string) (map[string]string, error)
}

func (m *Maint) client() *gdshpkg.Client {
	if m.Client == nil {
		m.Client = &gdshpkg.Client{}
	}
	return m.Client
}

func (m *Maint) rawURL(repo, ref, file string) string {
	if m.RawURL != nil {
		return m.RawURL(repo, ref, file)
	}
	return fmt.Sprintf("https://raw.githubusercontent.com/%s/%s/%s", repo, ref, file)
}

// Tags returns repo's tags as tag → commit.
func (m *Maint) Tags(ctx context.Context, repo string) (map[string]string, error) {
	if m.LsRemote != nil {
		return m.LsRemote(ctx, repo)
	}
	out, err := exec.CommandContext(ctx, "git", "ls-remote", "--tags", "https://github.com/"+repo).Output()
	if err != nil {
		return nil, fmt.Errorf("git ls-remote %s: %w", repo, err)
	}
	tags := map[string]string{}
	sc := bufio.NewScanner(bytes.NewReader(out))
	for sc.Scan() {
		sha, ref, ok := strings.Cut(sc.Text(), "\t")
		if !ok || !strings.HasPrefix(ref, "refs/tags/") {
			continue
		}
		tag := strings.TrimPrefix(ref, "refs/tags/")
		if peeled, ok := strings.CutSuffix(tag, "^{}"); ok {
			tags[peeled] = sha // the commit an annotated tag points at
		} else if _, seen := tags[tag]; !seen {
			tags[tag] = sha
		}
	}
	return tags, nil
}

// LatestTag is repo's latest release (from the releases/latest redirect, which uses no API
// quota), or its highest version tag when it has no release.
func (m *Maint) LatestTag(ctx context.Context, repo string) (string, error) {
	base := m.GitHub
	if base == "" {
		base = "https://github.com"
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodHead, base+"/"+repo+"/releases/latest", nil)
	if err != nil {
		return "", err
	}
	hc := *m.client().HTTPClient()
	hc.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	if resp, err := hc.Do(req); err == nil {
		resp.Body.Close()
		if _, tag, ok := strings.Cut(resp.Header.Get("Location"), "/releases/tag/"); ok && tag != "" {
			return tag, nil
		}
	}
	tags, err := m.Tags(ctx, repo)
	if err != nil {
		return "", err
	}
	var best string
	for tag := range tags {
		if versionTag.MatchString(tag) && (best == "" || gdshpkg.CompareVersions(tag, best) > 0) {
			best = tag
		}
	}
	if best == "" {
		return "", fmt.Errorf("%s has no release or version tag", repo)
	}
	return best, nil
}

var versionTag = regexp.MustCompile(`^v?\d+(\.\d+)*$`)

// Problem is one finding. Errors fail a check; warnings are printed.
type Problem struct {
	Pkg   string
	Msg   string
	Error bool
}

func (p Problem) String() string {
	level := "warning"
	if p.Error {
		level = "error"
	}
	return fmt.Sprintf("%s: %s: %s", level, p.Pkg, p.Msg)
}

// Derive checks each entry (only the one named only, when set) and rewrites its derived fields
// from package.cfg at its pinned commit. A failure to reach GitHub is returned as an error.
func (m *Maint) Derive(ctx context.Context, man *gdshpkg.Manifest, only string) ([]Problem, error) {
	var problems []Problem
	for i := range man.Packages {
		p := &man.Packages[i]
		if only != "" && p.Name != only {
			continue
		}
		found, err := m.derive(ctx, man, p)
		if err != nil {
			return problems, fmt.Errorf("%s: %w", p.Name, err)
		}
		problems = append(problems, found...)
	}
	return problems, nil
}

func (m *Maint) derive(ctx context.Context, man *gdshpkg.Manifest, p *gdshpkg.Package) ([]Problem, error) {
	var problems []Problem
	add := func(isErr bool, format string, args ...any) {
		problems = append(problems, Problem{p.Name, fmt.Sprintf(format, args...), isErr})
	}
	if p.Tag == "" || p.Commit == "" {
		add(true, "no tag/commit pin; set one with: gdsh-manifest pin %s TAG", p.Name)
		return problems, nil
	}
	// Installs use the commit, so a moved tag is only worth a review, not a failure.
	tags, err := m.Tags(ctx, p.Repo)
	if err != nil {
		return nil, err
	}
	switch sha, ok := tags[p.Tag]; {
	case !ok:
		add(false, "tag %s no longer exists (installs still use commit %s)", p.Tag, short(p.Commit))
	case sha != p.Commit:
		add(false, "tag %s moved from the reviewed commit %s to %s; review before re-pinning", p.Tag, short(p.Commit), short(sha))
	}

	data, err := m.client().Archive(ctx, p.Repo, p.Commit)
	if err != nil {
		return nil, err
	}
	dir, err := os.MkdirTemp("", "gdsh-manifest-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)
	if err := gdshpkg.Extract(data, dir); err != nil {
		return nil, err
	}
	cfgData, err := os.ReadFile(filepath.Join(dir, "package.cfg"))
	if err != nil {
		add(true, "no package.cfg at %s", short(p.Commit))
		return problems, nil
	}
	cfg, err := gdshpkg.ParseCfg(cfgData)
	if err != nil {
		add(true, "%v", err)
		return problems, nil
	}
	if cfg.InstallName() != p.Name {
		add(true, "package.cfg path %q installs as %q, not %q", cfg.Path, cfg.InstallName(), p.Name)
	}
	if gdshpkg.CompareVersions(cfg.Version, p.Tag) != 0 {
		add(false, "package.cfg version %s doesn't match tag %s", cfg.Version, p.Tag)
	}
	if _, err := os.Stat(filepath.Join(dir, "cmd", "manifest.gd")); err != nil {
		add(true, "no cmd/manifest.gd: not a command package")
	}
	for _, hit := range absolutePaths(dir) {
		add(true, "not relocatable (a command root is outside res://addons): %s", hit)
	}

	p.Gdsh, p.Requires, p.ProjectOnly = "", []string{}, nil
	for _, spec := range cfg.Require {
		repo, tag := gdshpkg.SplitRequire(spec)
		if strings.EqualFold(repo, gdshpkg.GdshRepo) {
			p.Gdsh = tag
			continue
		}
		if dep, ok := man.Find(repo); ok {
			p.Requires = append(p.Requires, dep.Repo)
			continue
		}
		if m.exists(ctx, repo, tag, "cmd/manifest.gd") {
			add(true, "requires %s, a command package missing from the index", repo)
			continue
		}
		p.ProjectOnly = append(p.ProjectOnly, repo)
	}
	sort.Strings(p.Requires)
	sort.Strings(p.ProjectOnly)
	return problems, nil
}

func (m *Maint) exists(ctx context.Context, repo, ref, file string) bool {
	if ref == "" {
		ref = "HEAD"
	}
	_, err := m.client().Get(ctx, m.rawURL(repo, ref, file))
	return err == nil
}

// absolutePath matches a gdsh or gdsh_lib path in a string literal: in a command root those
// folders aren't there. Only command_base.gd's is rewritten for user paths, and manifest.gd is
// never loaded as a command. Other addons' paths (the project's own dependencies) are fine.
var absolutePath = regexp.MustCompile(`"res://addons/_lib/gdsh(?:_lib)?/[^"]*"`)

const commandBase = `"res://addons/_lib/gdsh/src/core/command_base.gd"`

// absolutePaths lists up to five "file:line" hits of gdsh/gdsh_lib paths in the package's .gd
// files, outside cmd/manifest.gd and comment lines.
func absolutePaths(dir string) []string {
	var hits []string
	filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".gd") || len(hits) >= 5 {
			return nil
		}
		rel, _ := filepath.Rel(dir, path)
		if filepath.ToSlash(rel) == "cmd/manifest.gd" {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return nil
		}
		for n, line := range strings.Split(string(data), "\n") {
			if strings.HasPrefix(strings.TrimSpace(line), "#") {
				continue
			}
			for _, lit := range absolutePath.FindAllString(line, -1) {
				if lit != commandBase && len(hits) < 5 {
					hits = append(hits, fmt.Sprintf("%s:%d %s", filepath.ToSlash(rel), n+1, lit))
				}
			}
		}
		return nil
	})
	return hits
}

// Pin points name at tag (resolved to its commit) and re-derives that entry.
func (m *Maint) Pin(ctx context.Context, man *gdshpkg.Manifest, name, tag string) ([]Problem, error) {
	p, ok := man.Find(name)
	if !ok {
		return nil, fmt.Errorf("%s is not in the manifest", name)
	}
	tags, err := m.Tags(ctx, p.Repo)
	if err != nil {
		return nil, err
	}
	sha, ok := tags[tag]
	if !ok {
		return nil, fmt.Errorf("%s has no tag %s", p.Repo, tag)
	}
	p.Tag, p.Commit = tag, sha
	return m.Derive(ctx, man, p.Name)
}

// Proposal is a newer tag than an entry's pin, for review.
type Proposal struct {
	Name, Repo, From, To string
}

// CompareURL shows what changed between the pin and the proposal.
func (p Proposal) CompareURL() string {
	return fmt.Sprintf("https://github.com/%s/compare/%s...%s", p.Repo, p.From, p.To)
}

// Propose lists entries whose latest tag is newer than their pin.
func (m *Maint) Propose(ctx context.Context, man *gdshpkg.Manifest) ([]Proposal, error) {
	var out []Proposal
	for _, p := range man.Packages {
		latest, err := m.LatestTag(ctx, p.Repo)
		if err != nil {
			return out, fmt.Errorf("%s: %w", p.Name, err)
		}
		if gdshpkg.CompareVersions(latest, p.Tag) > 0 {
			out = append(out, Proposal{p.Name, p.Repo, p.Tag, latest})
		}
	}
	return out, nil
}

func short(sha string) string {
	if len(sha) > 12 {
		return sha[:12]
	}
	return sha
}
