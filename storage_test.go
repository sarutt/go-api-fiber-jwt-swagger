package main

import (
	"bytes"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/textproto"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gofiber/fiber/v2"
)

// useTempStore points the process-wide store at a directory for one test.
func useTempStore(t *testing.T) string {
	t.Helper()

	dir := t.TempDir()
	local, err := newLocalStore(dir)
	if err != nil {
		t.Fatalf("cannot open the test store: %v", err)
	}

	previous := store
	store = local
	t.Cleanup(func() { store = previous })
	return local.root
}

func TestStoreRoundTripsAFile(t *testing.T) {
	useTempStore(t)

	written, err := store.Put("episodes/1/master_video/9.mp4", strings.NewReader("hello"))
	if err != nil {
		t.Fatalf("put failed: %v", err)
	}
	if written != 5 {
		t.Errorf("stored %d bytes, want 5", written)
	}

	file, err := store.Get("episodes/1/master_video/9.mp4")
	if err != nil {
		t.Fatalf("get failed: %v", err)
	}
	defer file.Close()

	body, _ := io.ReadAll(file)
	if string(body) != "hello" {
		t.Errorf("read back %q", body)
	}

	if err := store.Delete("episodes/1/master_video/9.mp4"); err != nil {
		t.Fatalf("delete failed: %v", err)
	}
	if _, err := store.Get("episodes/1/master_video/9.mp4"); err == nil {
		t.Error("the file survived deletion")
	}
}

// Deleting something already gone is how a retry behaves, and must not error.
func TestDeletingAMissingFileIsNotAnError(t *testing.T) {
	useTempStore(t)

	if err := store.Delete("episodes/1/music/404.mp3"); err != nil {
		t.Errorf("deleting a missing file returned %v", err)
	}
}

// Keys are server-generated today. This guards the boundary anyway, because
// the moment a key comes from anywhere else a traversal bug is silent.
func TestStoreRefusesKeysThatEscapeIt(t *testing.T) {
	root := useTempStore(t)

	escapes := []string{
		"../outside.mp4",
		"episodes/../../outside.mp4",
		"/etc/passwd",
		"",
	}
	for _, key := range escapes {
		t.Run(key, func(t *testing.T) {
			if _, err := store.Put(key, strings.NewReader("x")); err == nil {
				t.Errorf("key %q was accepted", key)
			}
			if _, err := store.Get(key); err == nil {
				t.Errorf("key %q was readable", key)
			}
		})
	}

	// Nothing may have been written next to the store either.
	if _, err := os.Stat(filepath.Join(filepath.Dir(root), "outside.mp4")); !os.IsNotExist(err) {
		t.Error("a file was written outside the store")
	}
}

func TestStoreEnforcesTheSizeCap(t *testing.T) {
	root := useTempStore(t)
	t.Setenv("ASSET_MAX_MB", "1")

	oversized := bytes.Repeat([]byte("a"), (1<<20)+512)
	if _, err := store.Put("episodes/1/master_video/1.mp4", bytes.NewReader(oversized)); err != ErrAssetTooLarge {
		t.Fatalf("oversized upload returned %v, want ErrAssetTooLarge", err)
	}

	// A refused upload must not leave a truncated file behind.
	if _, err := os.Stat(filepath.Join(root, "episodes/1/master_video/1.mp4")); !os.IsNotExist(err) {
		t.Error("a partial file survived a refused upload")
	}
}

func TestContentTypeAllowlist(t *testing.T) {
	if ext, ok := extensionFor("video/mp4"); !ok || ext != ".mp4" {
		t.Errorf("video/mp4 mapped to %q, %v", ext, ok)
	}
	// Parameters are common on real uploads and must not defeat the lookup.
	if ext, ok := extensionFor("text/plain; charset=utf-8"); !ok || ext != ".txt" {
		t.Errorf("parameterised content type mapped to %q, %v", ext, ok)
	}
	if _, ok := extensionFor("application/x-msdownload"); ok {
		t.Error("an executable content type was allowed")
	}
}

func TestAssetKeyIgnoresCallerSuppliedNames(t *testing.T) {
	key := assetKey(4, 9, AssetMasterVideo, ".mp4")
	if key != "episodes/4/master_video/9.mp4" {
		t.Errorf("key is %q", key)
	}
}

/* ---------- upload and download over HTTP ---------- */

// upload posts a file the way an agent would.
func upload(t *testing.T, app *fiber.App, token string, episodeID uint, kind, filename, contentType string, content []byte) *http.Response {
	t.Helper()

	body := &bytes.Buffer{}
	form := multipart.NewWriter(body)
	form.WriteField("kind", kind)
	part, err := form.CreatePart(textproto.MIMEHeader{
		"Content-Disposition": []string{`form-data; name="file"; filename="` + filename + `"`},
		"Content-Type":        []string{contentType},
	})
	if err != nil {
		t.Fatalf("cannot build the upload: %v", err)
	}
	part.Write(content)
	form.Close()

	req := httptest.NewRequest(http.MethodPost, episodePath(episodeID, "assets/upload"), body)
	req.Header.Set("Content-Type", form.FormDataContentType())
	req.Header.Set("Authorization", "Bearer "+token)

	res, err := app.Test(req, 5000)
	if err != nil {
		t.Fatalf("upload failed: %v", err)
	}
	return res
}

func TestUploadedAssetCanBeDownloadedAgain(t *testing.T) {
	app := newTestApp(t)
	useTempStore(t)
	token := tokenFor(t, "editing-agent", RoleAgent)

	episode := newEpisode(t, app, token)
	content := []byte("not really an mp4, but bytes all the same")

	res := upload(t, app, token, episode.ID, string(AssetMasterVideo), "master.mp4", "video/mp4", content)
	if res.StatusCode != http.StatusCreated {
		t.Fatalf("upload returned %d, want 201", res.StatusCode)
	}

	var asset Asset
	decode(t, res, &asset)
	if asset.SizeBytes != int64(len(content)) {
		t.Errorf("recorded %d bytes, uploaded %d", asset.SizeBytes, len(content))
	}
	// The URI carries its own scheme so the model stays neutral about where
	// the bytes actually live.
	if !strings.HasPrefix(asset.URI, LocalScheme) {
		t.Errorf("uri is %q, want a %s uri", asset.URI, LocalScheme)
	}
	// The stored path comes from the episode and asset, not the filename.
	if asset.StorageKey != assetKey(episode.ID, asset.ID, AssetMasterVideo, ".mp4") {
		t.Errorf("storage key is %q", asset.StorageKey)
	}

	res = call(t, app, http.MethodGet, "/pipeline/assets/"+itoa(asset.ID)+"/content", token, nil)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("download returned %d, want 200", res.StatusCode)
	}
	body, _ := io.ReadAll(res.Body)
	if !bytes.Equal(body, content) {
		t.Errorf("downloaded %d bytes, uploaded %d", len(body), len(content))
	}
}

func TestUploadRefusesAFormatThePipelineDoesNotStore(t *testing.T) {
	app := newTestApp(t)
	useTempStore(t)
	token := tokenFor(t, "agent", RoleAgent)

	episode := newEpisode(t, app, token)
	res := upload(t, app, token, episode.ID, string(AssetMasterVideo), "payload.exe", "application/x-msdownload", []byte("MZ"))
	if res.StatusCode != http.StatusUnsupportedMediaType {
		t.Errorf("an executable upload returned %d, want 415", res.StatusCode)
	}

	// And no orphan row may be left behind by the refusal.
	var assets []Asset
	decode(t, call(t, app, http.MethodGet, episodePath(episode.ID, "assets"), token, nil), &assets)
	if len(assets) != 0 {
		t.Errorf("a refused upload left %d asset rows", len(assets))
	}
}

func TestDeletingAnAssetRemovesItsFile(t *testing.T) {
	app := newTestApp(t)
	useTempStore(t)
	token := tokenFor(t, "agent", RoleAgent)

	episode := newEpisode(t, app, token)
	var asset Asset
	decode(t, upload(t, app, token, episode.ID, string(AssetThumbnail), "thumb.png", "image/png", []byte("png")), &asset)

	res := call(t, app, http.MethodDelete, "/pipeline/assets/"+itoa(asset.ID), token, nil)
	if res.StatusCode != http.StatusNoContent {
		t.Fatalf("delete returned %d, want 204", res.StatusCode)
	}
	// Leaving the file behind would leak storage silently, one episode at a time.
	if _, err := store.Get(asset.StorageKey); err == nil {
		t.Error("the stored file survived deleting its asset")
	}
}

// An asset the agent hosts itself has no bytes here, and must say so rather
// than serving an empty body.
func TestRegisteredUriHasNoContentToServe(t *testing.T) {
	app := newTestApp(t)
	useTempStore(t)
	token := tokenFor(t, "agent", RoleAgent)

	episode := newEpisode(t, app, token)
	res := call(t, app, http.MethodPost, episodePath(episode.ID, "assets"), token, map[string]any{
		"kind": AssetMusic,
		"uri":  "s3://someone-elses-bucket/music.mp3",
	})
	var asset Asset
	decode(t, res, &asset)

	res = call(t, app, http.MethodGet, "/pipeline/assets/"+itoa(asset.ID)+"/content", token, nil)
	if res.StatusCode != http.StatusNotFound {
		t.Errorf("content for an externally hosted asset returned %d, want 404", res.StatusCode)
	}
}

// The configured cap is meaningless if the framework refuses the request
// first: Fiber's default body limit is 4MB, well under a master video.
func TestUploadsLargerThanTheFrameworkDefaultAreAccepted(t *testing.T) {
	app := newTestApp(t)
	useTempStore(t)
	token := tokenFor(t, "editing-agent", RoleAgent)

	episode := newEpisode(t, app, token)
	content := bytes.Repeat([]byte("a"), 5<<20)

	res := upload(t, app, token, episode.ID, string(AssetMasterVideo), "master.mp4", "video/mp4", content)
	if res.StatusCode != http.StatusCreated {
		t.Fatalf("a 5MB upload returned %d, want 201", res.StatusCode)
	}

	var asset Asset
	decode(t, res, &asset)
	if asset.SizeBytes != int64(len(content)) {
		t.Errorf("stored %d bytes of %d", asset.SizeBytes, len(content))
	}
}
