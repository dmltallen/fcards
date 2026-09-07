// Package media handles fcasset: image references inside card markdown:
// extraction, download through signed workspace media URLs, on-disk caching,
// and terminal rendering via chafa (works in every terminal, including foot).
package media

import (
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"fcards/internal/api"
)

var (
	// imageMarkdownRe matches ![alt](fcasset:id) markdown image references.
	imageMarkdownRe = regexp.MustCompile(`!\[[^\]]*\]\(fcasset:([^\s)]+)\)`)
	// bareRefRe matches any fcasset: occurrence (backend contract: fcasset:([^\s)]+)).
	bareRefRe = regexp.MustCompile(`fcasset:([^\s)]+)`)
)

// AssetIDs returns the media asset ids referenced by card text, in order.
func AssetIDs(text string) []string {
	seen := map[string]bool{}
	var out []string
	for _, m := range imageMarkdownRe.FindAllStringSubmatch(text, -1) {
		if !seen[m[1]] {
			seen[m[1]] = true
			out = append(out, m[1])
		}
	}
	for _, m := range bareRefRe.FindAllStringSubmatch(text, -1) {
		if !seen[m[1]] {
			seen[m[1]] = true
			out = append(out, m[1])
		}
	}
	return out
}

// StripImages removes markdown image references so text renders cleanly.
func StripImages(text string) string {
	return strings.TrimSpace(imageMarkdownRe.ReplaceAllString(text, ""))
}

// Renderer downloads and renders card images.
type Renderer struct {
	client      *api.Client
	workspaceID string
	cacheDir    string
	chafa       string // chafa binary path, "" when unavailable
}

func CacheDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	dir := filepath.Join(home, ".cache", "fcards", "media")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	return dir, nil
}

// NewRenderer builds a renderer; Available() reports whether chafa exists.
func NewRenderer(client *api.Client, workspaceID string) *Renderer {
	chafa, _ := lookPath("chafa")
	cacheDir, _ := CacheDir()
	return &Renderer{client: client, workspaceID: workspaceID, cacheDir: cacheDir, chafa: chafa}
}

// Available reports whether images can be rendered (chafa installed).
func (r *Renderer) Available() bool { return r != nil && r.chafa != "" }

// FetchCached ensures the asset's bytes are on disk and returns the path.
func (r *Renderer) FetchCached(assetID string) (string, error) {
	if r == nil || r.client == nil {
		return "", fmt.Errorf("renderer unavailable")
	}
	dl, err := r.client.MediaDownloadURL(r.workspaceID, assetID)
	if err != nil {
		return "", err
	}
	// Cache key: asset id + content sha prefix, so updates re-fetch.
	sha := dl.MediaAsset.SHA256
	if len(sha) > 12 {
		sha = sha[:12]
	}
	ext := "img"
	switch {
	case strings.Contains(dl.MediaAsset.MimeType, "png"):
		ext = "png"
	case strings.Contains(dl.MediaAsset.MimeType, "jpeg"), strings.Contains(dl.MediaAsset.MimeType, "jpg"):
		ext = "jpg"
	case strings.Contains(dl.MediaAsset.MimeType, "webp"):
		ext = "webp"
	}
	path := filepath.Join(r.cacheDir, fmt.Sprintf("%s-%s.%s", assetID, sha, ext))
	if _, err := os.Stat(path); err == nil {
		return path, nil
	}
	data, err := r.client.FetchBytes(dl.Download.URL)
	if err != nil {
		return "", err
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return "", err
	}
	return path, nil
}

// RenderBlock renders an image file as terminal block-art sized to fit.
func (r *Renderer) RenderBlock(path string, maxCols, maxRows int) (string, error) {
	if !r.Available() {
		return "", fmt.Errorf("chafa not installed")
	}
	if maxCols < 8 {
		maxCols = 8
	}
	if maxRows < 3 {
		maxRows = 3
	}
	out, err := runChafa(r.chafa, maxCols, maxRows, path)
	if err != nil {
		return "", err
	}
	return strings.TrimRight(out, "\n"), nil
}

var _ = sha256.Size
