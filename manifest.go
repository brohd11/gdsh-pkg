// Package gdshpkg installs gdsh command packages into a command root (<root>/packages/<name>)
// from a reviewed index: godot-gdsh-lib-manifest. Each entry pins a tag and the commit it was
// reviewed at; installs download that commit, so a moved tag can't change what is served.
package gdshpkg

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

// GdshRepo is gdsh itself. Hosts bring their own copy, so it is never installed.
const GdshRepo = "brohd11/godot-gdsh"

// DefaultManifestURL is the published index.
const DefaultManifestURL = "https://raw.githubusercontent.com/brohd11/godot-gdsh-lib-manifest/main/manifest.json"

// Manifest is the index: packages approved for install.
type Manifest struct {
	Version  int       `json:"version"`
	Packages []Package `json:"packages"`
}

// Package is one entry. Name, Repo, Summary, Tags and the Tag/Commit pin are written by hand;
// Gdsh, Requires and ProjectOnly are derived from package.cfg at Commit by gdsh-manifest.
type Package struct {
	Name    string   `json:"name"`
	Repo    string   `json:"repo"`
	Summary string   `json:"summary"`
	Tags    []string `json:"tags,omitempty"`
	Tag     string   `json:"tag"`
	Commit  string   `json:"commit"`
	// Gdsh is the lowest gdsh version the package requires.
	Gdsh string `json:"gdsh,omitempty"`
	// Requires are other indexed gdsh packages (repos), installed as siblings.
	Requires []string `json:"requires"`
	// ProjectOnly are requires a command root can't hold (non-gdsh code such as util_r): the
	// package has to be installed into the project instead, with gdaddon.
	ProjectOnly []string `json:"project_only,omitempty"`
}

// ParseManifest decodes and sanity-checks an index.
func ParseManifest(data []byte) (*Manifest, error) {
	var m Manifest
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("manifest: %w", err)
	}
	if m.Version != 1 {
		return nil, fmt.Errorf("manifest: unsupported version %d (update this tool)", m.Version)
	}
	names, repos := map[string]bool{}, map[string]bool{}
	for _, p := range m.Packages {
		if p.Name == "" || p.Repo == "" {
			return nil, fmt.Errorf("manifest: an entry lacks name or repo: %+v", p)
		}
		if strings.ContainsAny(p.Name, `/\.`) {
			return nil, fmt.Errorf("manifest: invalid package name %q", p.Name)
		}
		if names[p.Name] || repos[strings.ToLower(p.Repo)] {
			return nil, fmt.Errorf("manifest: %s / %s listed twice", p.Name, p.Repo)
		}
		names[p.Name], repos[strings.ToLower(p.Repo)] = true, true
	}
	return &m, nil
}

// LoadManifest reads an index file.
func LoadManifest(path string) (*Manifest, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return ParseManifest(data)
}

// Marshal is the canonical file form: two-space indent and a trailing newline.
func (m *Manifest) Marshal() ([]byte, error) {
	data, err := json.MarshalIndent(m, "", "  ")
	return append(data, '\n'), err
}

// Find returns the entry named name, or by its repo ("owner/repo").
func (m *Manifest) Find(nameOrRepo string) (*Package, bool) {
	for i := range m.Packages {
		if m.Packages[i].Name == nameOrRepo || strings.EqualFold(m.Packages[i].Repo, nameOrRepo) {
			return &m.Packages[i], true
		}
	}
	return nil, false
}

// Search returns entries whose name, summary or tags contain term (all for "").
func (m *Manifest) Search(term string) []Package {
	term = strings.ToLower(term)
	var out []Package
	for _, p := range m.Packages {
		hay := strings.ToLower(p.Name + " " + p.Summary + " " + strings.Join(p.Tags, " "))
		if strings.Contains(hay, term) {
			out = append(out, p)
		}
	}
	return out
}
