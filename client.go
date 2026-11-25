package gdshpkg

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// maxDownload bounds a manifest or package archive.
const maxDownload = 64 << 20

// Client fetches the index and package archives. The zero value uses the published index and
// GitHub, caching the index under the user cache folder for a day.
type Client struct {
	HTTP        *http.Client
	ManifestURL string
	// ArchiveURL is a zip of repo at commit; default GitHub's codeload archive.
	ArchiveURL func(repo, commit string) string
	// AssetURL is a file of repo's release at tag; default GitHub's release download.
	AssetURL func(repo, tag, asset string) string
	// CacheDir holds manifest.json; default <user cache>/gdsh.
	CacheDir string
	TTL      time.Duration
}

// HTTPClient is the client requests go through.
func (c *Client) HTTPClient() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	return &http.Client{Timeout: 60 * time.Second}
}

func (c *Client) archiveURL(repo, commit string) string {
	if c.ArchiveURL != nil {
		return c.ArchiveURL(repo, commit)
	}
	return fmt.Sprintf("https://github.com/%s/archive/%s.zip", repo, commit)
}

func (c *Client) assetURL(repo, tag, asset string) string {
	if c.AssetURL != nil {
		return c.AssetURL(repo, tag, asset)
	}
	return fmt.Sprintf("https://github.com/%s/releases/download/%s/%s", repo, tag, asset)
}

func (c *Client) cachePath() string {
	dir := c.CacheDir
	if dir == "" {
		base, err := os.UserCacheDir()
		if err != nil {
			return ""
		}
		dir = filepath.Join(base, "gdsh")
	}
	return filepath.Join(dir, "manifest.json")
}

// Index is a fetched manifest. Stale is set when the network failed and the cached copy was used.
type Index struct {
	*Manifest
	Stale   bool
	Fetched time.Time
}

// IndexEnv overrides the index location: a URL, or a local manifest.json (read as is, not
// cached), for trying an index before publishing it.
const IndexEnv = "GDSH_PACKAGE_INDEX"

// IndexURL is where the index comes from: ManifestURL, else $GDSH_PACKAGE_INDEX, else the
// published one.
func (c *Client) IndexURL() string {
	if c.ManifestURL != "" {
		return c.ManifestURL
	}
	if env := os.Getenv(IndexEnv); env != "" {
		return env
	}
	return DefaultManifestURL
}

// FetchIndex returns the index: the cached copy while it is younger than TTL (unless refresh),
// else a fresh download, else the cached copy marked Stale. A local file is always read fresh.
func (c *Client) FetchIndex(ctx context.Context, refresh bool) (*Index, error) {
	url := c.IndexURL()
	if !strings.HasPrefix(url, "http://") && !strings.HasPrefix(url, "https://") {
		m, err := LoadManifest(url)
		if err != nil {
			return nil, fmt.Errorf("package index: %w", err)
		}
		return &Index{Manifest: m, Fetched: time.Now()}, nil
	}
	ttl := c.TTL
	if ttl == 0 {
		ttl = 24 * time.Hour
	}
	cache := c.cachePath()
	cached, cachedAt := readCache(cache)
	if cached != nil && !refresh && time.Since(cachedAt) < ttl {
		return &Index{Manifest: cached, Fetched: cachedAt}, nil
	}
	data, err := c.Get(ctx, url)
	if err == nil {
		var m *Manifest
		if m, err = ParseManifest(data); err == nil {
			if cache != "" && os.MkdirAll(filepath.Dir(cache), 0o755) == nil {
				os.WriteFile(cache, data, 0o644)
			}
			return &Index{Manifest: m, Fetched: time.Now()}, nil
		}
	}
	if cached != nil {
		return &Index{Manifest: cached, Stale: true, Fetched: cachedAt}, nil
	}
	return nil, fmt.Errorf("package index: %w", err)
}

func readCache(path string) (*Manifest, time.Time) {
	if path == "" {
		return nil, time.Time{}
	}
	info, err := os.Stat(path)
	if err != nil {
		return nil, time.Time{}
	}
	m, err := LoadManifest(path)
	if err != nil {
		return nil, time.Time{}
	}
	return m, info.ModTime()
}

// Get downloads url (at most 64 MiB), failing on any status but 200.
func (c *Client) Get(ctx context.Context, url string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.HTTPClient().Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GET %s: %s", url, resp.Status)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxDownload+1))
	if err == nil && len(data) > maxDownload {
		err = errors.New("GET " + url + ": larger than 64 MiB")
	}
	return data, err
}

// Archive downloads repo at commit as a zip.
func (c *Client) Archive(ctx context.Context, repo, commit string) ([]byte, error) {
	return c.Get(ctx, c.archiveURL(repo, commit))
}

// Asset downloads a file of repo's release at tag.
func (c *Client) Asset(ctx context.Context, repo, tag, asset string) ([]byte, error) {
	return c.Get(ctx, c.assetURL(repo, tag, asset))
}

// SHA256 is data's hex digest, as an index pins a release asset.
func SHA256(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// AssetChangedError is a release asset that no longer matches its pinned digest.
type AssetChangedError struct {
	Pkg Package
	Got string
}

func (e AssetChangedError) Error() string {
	return fmt.Sprintf("release asset %s of %s %s changed since it was reviewed (sha256 %s, pinned %s)",
		e.Pkg.Asset, e.Pkg.Repo, e.Pkg.Tag, short(e.Got), short(e.Pkg.SHA256))
}

func short(s string) string {
	if len(s) > 12 {
		return s[:12]
	}
	return s
}

// Fetch downloads what p installs: its release asset, checked against the pinned sha256, or
// else its repo at the pinned commit.
func (c *Client) Fetch(ctx context.Context, p Package) ([]byte, error) {
	if p.Asset == "" {
		return c.Archive(ctx, p.Repo, p.Commit)
	}
	if p.SHA256 == "" {
		return nil, fmt.Errorf("%s has no pinned sha256 for its release asset", p.Name)
	}
	data, err := c.Asset(ctx, p.Repo, p.Tag, p.Asset)
	if err != nil {
		return nil, err
	}
	if got := SHA256(data); !strings.EqualFold(got, p.SHA256) {
		return nil, AssetChangedError{p, got}
	}
	return data, nil
}
