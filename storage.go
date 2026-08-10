package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// assetStore is where produced files live. The pipeline records a URI on each
// Asset row; this is what those URIs point at.
//
// The local implementation keeps the service runnable with no configuration,
// the same reasoning that picked SQLite. Object storage replaces it by
// implementing this interface — the Asset model does not change, because the
// URI carries its own scheme.
type assetStore interface {
	Put(key string, r io.Reader) (int64, error)
	Get(key string) (io.ReadCloser, error)
	Delete(key string) error
}

// store is the process-wide asset store.
var store assetStore

// LocalScheme prefixes URIs held in the local store.
const LocalScheme = "local://"

// defaultMaxAssetMB caps an upload. A master video is the largest thing the
// pipeline handles, so the default is generous rather than tight.
const defaultMaxAssetMB = 512

// ErrAssetTooLarge is returned when an upload exceeds the configured cap.
var ErrAssetTooLarge = errors.New("asset exceeds the size limit")

// contentTypes maps the formats agents produce to the extension stored on
// disk. It doubles as the allowlist: anything absent is refused, so a worker
// cannot park arbitrary file types in the store.
var contentTypes = map[string]string{
	"video/mp4":        ".mp4",
	"video/quicktime":  ".mov",
	"video/webm":       ".webm",
	"audio/mpeg":       ".mp3",
	"audio/wav":        ".wav",
	"audio/x-wav":      ".wav",
	"audio/mp4":        ".m4a",
	"image/png":        ".png",
	"image/jpeg":       ".jpg",
	"image/webp":       ".webp",
	"text/plain":       ".txt",
	"text/vtt":         ".vtt",
	"application/json": ".json",
}

// extensionFor reports the stored extension for a content type.
func extensionFor(contentType string) (string, bool) {
	// A browser or SDK may append parameters, e.g. "text/plain; charset=utf-8".
	base := strings.TrimSpace(strings.SplitN(contentType, ";", 2)[0])
	ext, ok := contentTypes[strings.ToLower(base)]
	return ext, ok
}

// maxAssetBytes is the configured upload cap.
func maxAssetBytes() int64 {
	if raw := os.Getenv("ASSET_MAX_MB"); raw != "" {
		if mb, err := strconv.Atoi(raw); err == nil && mb > 0 {
			return int64(mb) << 20
		}
	}
	return int64(defaultMaxAssetMB) << 20
}

// bodyLimit is the largest request the app accepts. Fiber's default is 4MB,
// which is under a single master video — leaving it there would make the
// configured cap unreachable and refuse uploads before a handler ever sees
// them. The extra megabyte covers the multipart envelope around the file.
func bodyLimit() int {
	return int(maxAssetBytes()) + (1 << 20)
}

// assetKey builds the storage key for an asset. It is derived entirely from
// values the server controls — never from a filename a worker supplied — so a
// worker cannot choose where its bytes land.
func assetKey(episodeID, assetID uint, kind AssetKind, ext string) string {
	return fmt.Sprintf("episodes/%d/%s/%d%s", episodeID, strings.ToLower(string(kind)), assetID, ext)
}

/* ---------- local filesystem ---------- */

type localStore struct{ root string }

// newLocalStore prepares a directory to hold assets.
func newLocalStore(root string) (*localStore, error) {
	absolute, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(absolute, 0o755); err != nil {
		return nil, err
	}
	return &localStore{root: absolute}, nil
}

// resolve turns a key into a path inside the store, refusing anything that
// would escape it. Keys are server-generated today, so this guards against a
// future caller rather than a current one — which is exactly when a traversal
// bug would otherwise be introduced unnoticed.
func (s *localStore) resolve(key string) (string, error) {
	if key == "" {
		return "", fmt.Errorf("empty asset key")
	}
	if filepath.IsAbs(key) || strings.Contains(key, "\\") {
		return "", fmt.Errorf("asset key %q must be a relative slash-separated path", key)
	}

	path := filepath.Join(s.root, filepath.FromSlash(key))
	cleaned := filepath.Clean(path)

	// filepath.Join already cleans, but compare explicitly so the intent is
	// checked rather than assumed.
	if cleaned != path || !strings.HasPrefix(cleaned, s.root+string(os.PathSeparator)) {
		return "", fmt.Errorf("asset key %q escapes the store", key)
	}
	return cleaned, nil
}

func (s *localStore) Put(key string, r io.Reader) (int64, error) {
	path, err := s.resolve(key)
	if err != nil {
		return 0, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return 0, err
	}

	file, err := os.Create(path)
	if err != nil {
		return 0, err
	}
	defer file.Close()

	// Read one byte past the cap so an oversized upload is detected rather
	// than silently truncated to the limit.
	limit := maxAssetBytes()
	written, err := io.Copy(file, io.LimitReader(r, limit+1))
	if err != nil {
		os.Remove(path)
		return 0, err
	}
	if written > limit {
		os.Remove(path)
		return 0, ErrAssetTooLarge
	}
	return written, nil
}

func (s *localStore) Get(key string) (io.ReadCloser, error) {
	path, err := s.resolve(key)
	if err != nil {
		return nil, err
	}
	return os.Open(path)
}

func (s *localStore) Delete(key string) error {
	path, err := s.resolve(key)
	if err != nil {
		return err
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

// initStore opens the asset store. ASSET_DIR overrides the location.
func initStore() error {
	dir := os.Getenv("ASSET_DIR")
	if dir == "" {
		dir = "assets"
	}
	local, err := newLocalStore(dir)
	if err != nil {
		return err
	}
	store = local
	return nil
}
