package gdshpkg

import (
	"archive/zip"
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"
)

// skipped are folders a package keeps out of installs, besides any dot file or folder.
var skipped = map[string]bool{"_export_ignore": true, "export_ignore": true}

// Extract unpacks a repo zip into dir: the single wrapper folder GitHub adds is dropped, and so
// are dot files/folders and export_ignore folders at any depth. Links and paths escaping dir
// are refused.
func Extract(data []byte, dir string) error {
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return fmt.Errorf("archive: %w", err)
	}
	// Checked on the raw names, before the wrapper folder is stripped.
	for _, f := range zr.File {
		if clean := path.Clean(f.Name); path.IsAbs(f.Name) || clean == ".." || strings.HasPrefix(clean, "../") || strings.Contains(f.Name, `\`) {
			return fmt.Errorf("archive: unsafe path %q", f.Name)
		}
	}
	prefix := wrapperPrefix(zr.File)
	var total int64
	for _, f := range zr.File {
		name := strings.TrimPrefix(f.Name, prefix)
		if name == "" || name == "/" {
			continue
		}
		clean := path.Clean(name)
		if skip(clean) {
			continue
		}
		mode := f.Mode()
		if mode&os.ModeSymlink != 0 || (!mode.IsRegular() && !mode.IsDir()) {
			return fmt.Errorf("archive: %s is not a regular file", f.Name)
		}
		dest := filepath.Join(dir, filepath.FromSlash(clean))
		if mode.IsDir() {
			if err := os.MkdirAll(dest, 0o755); err != nil {
				return err
			}
			continue
		}
		total += int64(f.UncompressedSize64)
		if total > 4*maxDownload {
			return fmt.Errorf("archive: unpacks to more than %d MiB", 4*maxDownload>>20)
		}
		if err := writeZipFile(f, dest); err != nil {
			return err
		}
	}
	return nil
}

// wrapperPrefix is "name/" when every entry sits under that one folder, else "".
func wrapperPrefix(files []*zip.File) string {
	prefix := ""
	for _, f := range files {
		first, _, ok := strings.Cut(f.Name, "/")
		if !ok && !f.FileInfo().IsDir() {
			return ""
		}
		if prefix == "" {
			prefix = first + "/"
		} else if prefix != first+"/" {
			return ""
		}
	}
	return prefix
}

func skip(clean string) bool {
	for _, part := range strings.Split(clean, "/") {
		if strings.HasPrefix(part, ".") || skipped[part] {
			return true
		}
	}
	return false
}

func writeZipFile(f *zip.File, dest string) error {
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return err
	}
	r, err := f.Open()
	if err != nil {
		return err
	}
	defer r.Close()
	w, err := os.OpenFile(dest, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}
	_, err = io.Copy(w, io.LimitReader(r, int64(f.UncompressedSize64)+1))
	if cerr := w.Close(); err == nil {
		err = cerr
	}
	return err
}

// PackageRoot is the folder of an unpacked archive that holds the package: the shallowest one
// with a package.cfg, outside vendor/ folders (an export's bundled libraries have their own).
// That is dir for a repo archive and addons/.../<name> for an export laid out by project path.
func PackageRoot(dir string) (string, error) {
	level := []string{dir}
	for len(level) > 0 {
		var found, next []string
		for _, d := range level {
			if _, err := os.Stat(filepath.Join(d, "package.cfg")); err == nil {
				found = append(found, d)
				continue
			}
			entries, err := os.ReadDir(d)
			if err != nil {
				return "", err
			}
			for _, e := range entries {
				if e.IsDir() && e.Name() != "vendor" {
					next = append(next, filepath.Join(d, e.Name()))
				}
			}
		}
		switch len(found) {
		case 0:
			level = next
		case 1:
			return found[0], nil
		default:
			return "", fmt.Errorf("archive: more than one package.cfg at the same depth: %s", strings.Join(relAll(dir, found), ", "))
		}
	}
	return "", errors.New("archive has no package.cfg")
}

func relAll(base string, paths []string) []string {
	out := make([]string, len(paths))
	for i, p := range paths {
		r, _ := filepath.Rel(base, p)
		out[i] = filepath.ToSlash(r)
	}
	return out
}
