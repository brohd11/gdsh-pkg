package gdshpkg

import (
	"bufio"
	"bytes"
	"fmt"
	"strings"
)

// Cfg is the [package] section of a package's package.cfg.
type Cfg struct {
	Name    string
	Version string
	Path    string   // project path, e.g. addons/_lib/gdsh_lib/level; its basename is the install name
	Require []string // "owner/repo@tag"
}

// InstallName is the folder a package installs to: the basename of its path. Packages find
// their dependencies as sibling folders by this name.
func (c Cfg) InstallName() string {
	p := strings.TrimRight(c.Path, "/")
	return p[strings.LastIndex(p, "/")+1:]
}

// ParseCfg reads the [package] section of a Godot ConfigFile: quoted strings and string arrays,
// which may span lines. Other sections and value types are ignored.
func ParseCfg(data []byte) (Cfg, error) {
	var cfg Cfg
	section := ""
	sc := bufio.NewScanner(bytes.NewReader(data))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, ";") || strings.HasPrefix(line, "#") {
			continue
		}
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") && !strings.Contains(line, "=") {
			section = line[1 : len(line)-1]
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok || section != "package" {
			continue
		}
		key, value = strings.TrimSpace(key), strings.TrimSpace(value)
		// An array runs until its closing bracket, possibly lines later.
		if strings.HasPrefix(value, "[") {
			for !strings.Contains(value, "]") && sc.Scan() {
				value += strings.TrimSpace(sc.Text())
			}
		}
		switch key {
		case "name":
			cfg.Name = unquote(value)
		case "version":
			cfg.Version = unquote(value)
		case "path":
			cfg.Path = unquote(value)
		case "require":
			list, err := parseStringArray(value)
			if err != nil {
				return cfg, fmt.Errorf("package.cfg require: %w", err)
			}
			cfg.Require = list
		}
	}
	return cfg, sc.Err()
}

func unquote(s string) string {
	if len(s) >= 2 && s[0] == '"' && s[len(s)-1] == '"' {
		return s[1 : len(s)-1]
	}
	return s
}

func parseStringArray(s string) ([]string, error) {
	if !strings.HasPrefix(s, "[") || !strings.HasSuffix(s, "]") {
		return nil, fmt.Errorf("not an array: %s", s)
	}
	var out []string
	for _, item := range strings.Split(s[1:len(s)-1], ",") {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		if len(item) < 2 || item[0] != '"' || item[len(item)-1] != '"' {
			return nil, fmt.Errorf("not a string: %s", item)
		}
		out = append(out, item[1:len(item)-1])
	}
	return out, nil
}

// SplitRequire splits "owner/repo@tag" (a github.com/ prefix or .git suffix is allowed).
func SplitRequire(spec string) (repo, tag string) {
	repo, tag, _ = strings.Cut(spec, "@")
	repo = strings.TrimPrefix(strings.TrimPrefix(repo, "https://"), "github.com/")
	return strings.TrimSuffix(repo, ".git"), tag
}
