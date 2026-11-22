package gdshpkg

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Action is what a plan does with one package.
type Action string

const (
	ActInstall Action = "install"
	ActUpdate  Action = "update"
	ActKeep    Action = "keep" // installed at the pin or newer
)

// Step is one package of a plan, dependencies before the packages that need them.
type Step struct {
	Pkg       Package
	Action    Action
	Installed string // the installed version, if any
	For       string // the package that pulled it in; "" when asked for
}

// ProjectOnlyError is a package a command root can't hold.
type ProjectOnlyError struct{ Pkg Package }

func (e ProjectOnlyError) Error() string {
	return fmt.Sprintf("%s needs %s, which a command root can't hold; install it into the project instead: gdaddon install %s",
		e.Pkg.Name, strings.Join(e.Pkg.ProjectOnly, ", "), e.Pkg.Repo)
}

// Plan resolves names (index names or repos) and their requires against what root has. With
// force, installed packages are reinstalled at their pin even when current. No network.
func (m *Manifest) Plan(root string, names []string, force bool) ([]Step, error) {
	installed, err := ListInstalled(root)
	if err != nil {
		return nil, err
	}
	have := map[string]string{}
	for _, in := range installed {
		have[in.Name] = in.Cfg.Version
	}
	var steps []Step
	done, visiting := map[string]bool{}, map[string]bool{}
	var visit func(p *Package, forName string) error
	visit = func(p *Package, forName string) error {
		if done[p.Name] {
			return nil
		}
		if visiting[p.Name] {
			return fmt.Errorf("dependency cycle through %s", p.Name)
		}
		if len(p.ProjectOnly) > 0 {
			return ProjectOnlyError{*p}
		}
		if p.Commit == "" {
			return fmt.Errorf("%s has no pinned commit in the index", p.Name)
		}
		visiting[p.Name] = true
		for _, repo := range p.Requires {
			dep, ok := m.Find(repo)
			if !ok {
				return fmt.Errorf("%s requires %s, which is not in the index", p.Name, repo)
			}
			if err := visit(dep, p.Name); err != nil {
				return err
			}
		}
		visiting[p.Name], done[p.Name] = false, true
		step := Step{Pkg: *p, Installed: have[p.Name], For: forName}
		switch v, ok := have[p.Name]; {
		case !ok:
			step.Action = ActInstall
		case force || CompareVersions(v, p.Tag) < 0:
			step.Action = ActUpdate
		default:
			step.Action = ActKeep
		}
		steps = append(steps, step)
		return nil
	}
	for _, name := range names {
		p, ok := m.Find(name)
		if !ok {
			return nil, fmt.Errorf("%s is not in the package index (see: gdsh search)", name)
		}
		if err := visit(p, ""); err != nil {
			return nil, err
		}
	}
	return steps, nil
}

// Install downloads and places every step that isn't ActKeep. report gets one line per package
// and any warnings. Each package is unpacked beside the root and swapped in with a rename, so a
// failure leaves the installed copy as it was.
func (c *Client) Install(ctx context.Context, root string, steps []Step, report func(string)) error {
	if report == nil {
		report = func(string) {}
	}
	for _, s := range steps {
		if s.Action == ActKeep {
			continue
		}
		if err := c.installOne(ctx, root, s.Pkg, report); err != nil {
			return fmt.Errorf("%s: %w", s.Pkg.Name, err)
		}
		verb := "Installed"
		if s.Action == ActUpdate {
			verb = "Updated"
		}
		report(fmt.Sprintf("%s %s %s", verb, s.Pkg.Name, s.Pkg.Tag))
	}
	return nil
}

func (c *Client) installOne(ctx context.Context, root string, p Package, report func(string)) error {
	data, err := c.Archive(ctx, p.Repo, p.Commit)
	if err != nil {
		return err
	}
	staging := filepath.Join(root, ".pkg-staging")
	if err := os.MkdirAll(staging, 0o755); err != nil {
		return err
	}
	defer os.Remove(staging) // last, and only once empty
	tmp, err := os.MkdirTemp(staging, p.Name+"-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)
	if err := Extract(data, tmp); err != nil {
		return err
	}
	cfgData, err := os.ReadFile(filepath.Join(tmp, "package.cfg"))
	if err != nil {
		return errors.New("archive has no package.cfg")
	}
	cfg, err := ParseCfg(cfgData)
	if err != nil {
		return err
	}
	if got := cfg.InstallName(); got != p.Name {
		return fmt.Errorf("package.cfg path names %q, the index %q", got, p.Name)
	}
	if _, err := os.Stat(filepath.Join(tmp, "cmd")); err != nil {
		return errors.New("archive has no cmd/ folder")
	}
	if cfg.Version != "" && CompareVersions(cfg.Version, p.Tag) != 0 {
		report(fmt.Sprintf("warning: %s %s says version %s", p.Name, p.Tag, cfg.Version))
	}
	return swapIn(tmp, filepath.Join(PackagesDir(root), p.Name))
}

// swapIn moves src to dest, replacing any dest; on failure the old dest is put back.
func swapIn(src, dest string) error {
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return err
	}
	old := ""
	if _, err := os.Stat(dest); err == nil {
		old = filepath.Join(filepath.Dir(src), ".old-"+filepath.Base(dest))
		if err := os.Rename(dest, old); err != nil {
			return err
		}
	}
	if err := os.Rename(src, dest); err != nil {
		if old != "" {
			os.Rename(old, dest)
		}
		return err
	}
	if old != "" {
		os.RemoveAll(old)
	}
	return nil
}
