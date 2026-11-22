package gdshpkg

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// PackagesDir is where a root keeps its packages.
func PackagesDir(root string) string { return filepath.Join(root, "packages") }

// Installed is a package found in a root.
type Installed struct {
	Name string // its folder
	Dir  string
	Cfg  Cfg
}

// ListInstalled returns the packages in root, by name: folders under packages/ that have a
// package.cfg. A root without packages/ has none.
func ListInstalled(root string) ([]Installed, error) {
	entries, err := os.ReadDir(PackagesDir(root))
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []Installed
	for _, e := range entries {
		if !e.IsDir() || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		dir := filepath.Join(PackagesDir(root), e.Name())
		data, err := os.ReadFile(filepath.Join(dir, "package.cfg"))
		if err != nil {
			continue
		}
		cfg, err := ParseCfg(data)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", dir, err)
		}
		out = append(out, Installed{Name: e.Name(), Dir: dir, Cfg: cfg})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// Update is an installed package with a newer pin in the index.
type Update struct {
	Installed
	Pkg Package
}

// Outdated compares installed versions with the index pins. Packages not in the index are skipped.
func (m *Manifest) Outdated(installed []Installed) []Update {
	var out []Update
	for _, in := range installed {
		if p, ok := m.Find(in.Name); ok && CompareVersions(in.Cfg.Version, p.Tag) < 0 {
			out = append(out, Update{in, *p})
		}
	}
	return out
}

// Dependents returns the installed packages that require name. Requires are matched by repo
// through the index when it is given, else by the repo's last path part.
func Dependents(installed []Installed, name string, m *Manifest) []string {
	var out []string
	for _, in := range installed {
		if in.Name == name {
			continue
		}
		for _, spec := range in.Cfg.Require {
			if repoInstallName(spec, m) == name {
				out = append(out, in.Name)
				break
			}
		}
	}
	return out
}

func repoInstallName(spec string, m *Manifest) string {
	repo, _ := SplitRequire(spec)
	if m != nil {
		if p, ok := m.Find(repo); ok {
			return p.Name
		}
	}
	return strings.TrimPrefix(repo[strings.LastIndex(repo, "/")+1:], "godot-gdsh-lib-")
}

// Remove deletes an installed package's folder.
func Remove(root, name string) error {
	if name == "" || strings.ContainsAny(name, `/\`) || strings.HasPrefix(name, ".") {
		return fmt.Errorf("invalid package name %q", name)
	}
	dir := filepath.Join(PackagesDir(root), name)
	if _, err := os.Stat(filepath.Join(dir, "package.cfg")); err != nil {
		return fmt.Errorf("%s is not installed in %s", name, root)
	}
	return os.RemoveAll(dir)
}
