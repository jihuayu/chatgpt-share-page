package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jihuayu/chatgpt-share-page/internal/config"
	"github.com/jihuayu/chatgpt-share-page/internal/publish"
	"github.com/jihuayu/chatgpt-share-page/internal/renderer"
	"github.com/jihuayu/chatgpt-share-page/internal/storage"
)

// fakeFetcher returns a canned share page.
type fakeFetcher struct {
	page string
	err  error
}

func (f *fakeFetcher) FetchHTML(ctx context.Context, rawURL string) (string, error) {
	return f.page, f.err
}

func testPage(t *testing.T, title string) string {
	return testPageWithMessages(t, title, 2)
}

func testPageWithMessages(t *testing.T, title string, count int) string {
	t.Helper()
	if count < 1 {
		count = 1
	}
	mapping := make(map[string]any, count)
	for i := 1; i <= count; i++ {
		role := "assistant"
		content := fmt.Sprintf("Additional answer %d.", i)
		if i == 1 {
			role = "user"
			content = "What is Go?"
		} else if i == 2 {
			content = "Go is a language.\n```go\nfmt.Println(\"hi\")\n```"
		}
		node := map[string]any{
			"id": "n" + strconv.Itoa(i),
			"message": map[string]any{
				"id":          "m" + strconv.Itoa(i),
				"author":      map[string]any{"role": role},
				"create_time": 1700000000.0 + float64(i*60),
				"content":     map[string]any{"content_type": "text", "parts": []any{content}},
			},
		}
		if i > 1 {
			node["parent"] = "n" + strconv.Itoa(i-1)
		}
		mapping["n"+strconv.Itoa(i)] = node
	}
	table := []any{
		map[string]any{
			"title":           title,
			"conversation_id": "conv-" + title,
			"current_node":    "n" + strconv.Itoa(count),
			"mapping":         mapping,
		},
	}
	tableJSON, _ := json.Marshal(table)
	return `<!doctype html><html><script>streamController.enqueue(` +
		strconv.Quote(string(tableJSON)) + `)</script></html>`
}

type testEnv struct {
	server *httptest.Server
	store  *storage.Store
	files  *storage.FileStore
	fetch  *fakeFetcher
}

func newTestEnv(t *testing.T) *testEnv {
	t.Helper()
	dir := t.TempDir()
	cfg := config.Config{
		DataDir:              dir,
		DatabasePath:         dir + "/app.db",
		PublicBaseURL:        "http://share.test",
		MaxImportConcurrency: 2,
		FetchTimeout:         10 * time.Second,
		MaxFetchBytes:        20 << 20,
		MaxRequestBytes:      1 << 20,
		MaxRedirects:         3,
	}
	store, err := storage.Open(cfg.DatabasePath)
	if err != nil {
		t.Fatal(err)
	}
	files, err := storage.NewFileStore(cfg.DataDir)
	if err != nil {
		t.Fatal(err)
	}
	r, err := renderer.New()
	if err != nil {
		t.Fatal(err)
	}
	fetch := &fakeFetcher{page: testPage(t, "Test Conversation")}
	svc := publish.NewService(cfg, store, files, fetch, r, nil, slog.Default())
	h := New(cfg, svc, store, files, r, slog.Default())
	server := httptest.NewServer(Middleware(slog.Default(), h.Routes()))
	t.Cleanup(func() {
		server.Close()
		store.Close()
	})
	return &testEnv{server: server, store: store, files: files, fetch: fetch}
}

func doJSON(t *testing.T, method, url, token string, body any) (*http.Response, map[string]any) {
	t.Helper()
	var reader io.Reader
	if body != nil {
		payload, _ := json.Marshal(body)
		reader = strings.NewReader(string(payload))
	}
	req, err := http.NewRequest(method, url, reader)
	if err != nil {
		t.Fatal(err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	data, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	var parsed map[string]any
	_ = json.Unmarshal(data, &parsed)
	return resp, parsed
}

func TestHealthz(t *testing.T) {
	env := newTestEnv(t)
	resp, body := doJSON(t, http.MethodGet, env.server.URL+"/healthz", "", nil)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK || body["status"] != "ok" {
		t.Fatalf("healthz = %d %v", resp.StatusCode, body)
	}
}

func TestIndexAndAssets(t *testing.T) {
	env := newTestEnv(t)
	resp, err := http.Get(env.server.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), `id="import-form"`) {
		t.Fatalf("index = %d %q", resp.StatusCode, body)
	}
	if resp.Header.Get("Cache-Control") != "no-store" {
		t.Error("index must be no-store")
	}
	csp := resp.Header.Get("Content-Security-Policy")
	for _, directive := range []string{"script-src 'self'", "connect-src 'self'", "frame-ancestors 'none'"} {
		if !strings.Contains(csp, directive) {
			t.Errorf("index CSP missing %q: %q", directive, csp)
		}
	}

	for _, asset := range []struct {
		path        string
		contentType string
		marker      string
	}{
		{path: "/assets/index.css", contentType: "text/css", marker: "--accent"},
		{path: "/assets/index.js", contentType: "text/javascript", marker: "/api/v1/snapshots"},
	} {
		resp, err = http.Get(env.server.URL + asset.path)
		if err != nil {
			t.Fatal(err)
		}
		data, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK || !strings.HasPrefix(resp.Header.Get("Content-Type"), asset.contentType) {
			t.Errorf("asset %s = %d %q", asset.path, resp.StatusCode, resp.Header.Get("Content-Type"))
		}
		if resp.Header.Get("Cache-Control") != "public, max-age=3600" || resp.Header.Get("X-Content-Type-Options") != "nosniff" {
			t.Errorf("asset %s missing cache/security headers", asset.path)
		}
		if !strings.Contains(string(data), asset.marker) {
			t.Errorf("asset %s missing marker %q", asset.path, asset.marker)
		}
	}

	for _, path := range []string{"/assets/logo.png", "/favicon.ico"} {
		resp, err = http.Get(env.server.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		data, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK || resp.Header.Get("Content-Type") != "image/png" {
			t.Errorf("image asset %s = %d %q", path, resp.StatusCode, resp.Header.Get("Content-Type"))
		}
		if resp.Header.Get("Cache-Control") != "public, max-age=86400" || resp.Header.Get("X-Content-Type-Options") != "nosniff" {
			t.Errorf("image asset %s missing cache/security headers", path)
		}
		if len(data) < 8 || string(data[1:4]) != "PNG" {
			t.Errorf("image asset %s is not a PNG", path)
		}
	}
}

func TestImportAndServe(t *testing.T) {
	env := newTestEnv(t)
	shareURL := "https://chatgpt.com/share/test-share-1"

	resp, body := doJSON(t, http.MethodPost, env.server.URL+"/api/v1/snapshots", "", map[string]any{
		"url": shareURL,
	})
	resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("import status = %d body = %v", resp.StatusCode, body)
	}
	for _, field := range []string{"id", "slug", "revision", "admin_token", "page_url", "embed_url"} {
		if body[field] == nil || body[field] == "" {
			t.Fatalf("missing field %s in %v", field, body)
		}
	}
	token := body["admin_token"].(string)
	slug := body["slug"].(string)
	revision := body["revision"].(string)
	snapID := body["id"].(string)

	// Re-import same URL -> dedupes, no new token.
	resp, dup := doJSON(t, http.MethodPost, env.server.URL+"/api/v1/snapshots", "", map[string]any{"url": shareURL})
	resp.Body.Close()
	if dup["existing"] != true || dup["admin_token"] != nil {
		t.Fatalf("expected dedupe without token, got %v", dup)
	}

	// Stable URL redirects to revision.
	client := &http.Client{CheckRedirect: func(req *http.Request, via []*http.Request) error {
		return http.ErrUseLastResponse
	}}
	resp, err := client.Get(env.server.URL + "/c/" + slug)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusFound {
		t.Fatalf("stable page status = %d", resp.StatusCode)
	}
	if loc := resp.Header.Get("Location"); loc != "/c/"+slug+"/r/"+revision {
		t.Fatalf("redirect = %q", loc)
	}

	// Immutable page serves HTML with cache + CSP + noindex headers.
	resp, err = client.Get(env.server.URL + "/c/" + slug + "/r/" + revision)
	if err != nil {
		t.Fatal(err)
	}
	pageBytes, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("revision page status = %d", resp.StatusCode)
	}
	page := string(pageBytes)
	if !strings.Contains(page, "What is Go?") || !strings.Contains(page, "Test Conversation") {
		t.Error("page missing message content")
	}
	if !strings.Contains(page, "chroma") {
		t.Error("expected chroma highlighted code")
	}
	if resp.Header.Get("ETag") == "" {
		t.Error("missing ETag")
	}
	if !strings.Contains(resp.Header.Get("Content-Security-Policy"), "script-src 'sha256-") {
		t.Error("missing CSP script hash")
	}
	if resp.Header.Get("X-Robots-Tag") == "" {
		t.Error("missing noindex header")
	}
	// ETag revalidation.
	req, _ := http.NewRequest(http.MethodGet, env.server.URL+"/c/"+slug+"/r/"+revision, nil)
	req.Header.Set("If-None-Match", resp.Header.Get("ETag"))
	resp304, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp304.Body.Close()
	if resp304.StatusCode != http.StatusNotModified {
		t.Errorf("etag revalidate = %d", resp304.StatusCode)
	}
	for _, header := range []string{"Cache-Control", "Cloudflare-CDN-Cache-Control", "Content-Security-Policy", "ETag", "X-Robots-Tag"} {
		if resp304.Header.Get(header) == "" {
			t.Errorf("304 missing %s", header)
		}
	}

	// A current-version artifact can be rebuilt from its immutable snapshot.
	revRow, err := env.store.GetRevision(context.Background(), snapID, revision)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(env.files.Abs(revRow.PagePath)); err != nil {
		t.Fatal(err)
	}
	resp, err = client.Get(env.server.URL + "/c/" + slug + "/r/" + revision)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("fallback page status = %d", resp.StatusCode)
	}
	if _, err := os.Stat(env.files.Abs(revRow.PagePath)); err != nil {
		t.Fatalf("fallback did not restore page: %v", err)
	}

	// Embed page.
	resp, err = client.Get(env.server.URL + "/e/" + slug + "/r/" + revision)
	if err != nil {
		t.Fatal(err)
	}
	embedBytes, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if !strings.Contains(string(embedBytes), "postMessage") {
		t.Error("embed missing height sync script")
	}
	if !strings.Contains(resp.Header.Get("Content-Security-Policy"), "frame-ancestors https:") {
		t.Error("embed CSP should allow framing")
	}

	// Files exist on disk.
	for _, name := range []string{"snapshot.json", "page.html", "embed.html"} {
		if _, err := env.files.ReadFile(fmt.Sprintf("conversations/%s/revisions/%s/%s", snapID, revision, name)); err != nil {
			t.Errorf("artifact %s missing: %v", name, err)
		}
	}
	if _, err := env.files.ReadFile(fmt.Sprintf("conversations/%s/raw.json", snapID)); err != nil {
		t.Errorf("raw.json missing: %v", err)
	}

	// Auth required for admin endpoints.
	resp, _ = doJSON(t, http.MethodGet, env.server.URL+"/api/v1/snapshots/"+snapID, "", nil)
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", resp.StatusCode)
	}
	resp, _ = doJSON(t, http.MethodGet, env.server.URL+"/api/v1/snapshots/"+snapID, "wrong", nil)
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("expected 403, got %d", resp.StatusCode)
	}
	resp, meta := doJSON(t, http.MethodGet, env.server.URL+"/api/v1/snapshots/"+snapID, token, nil)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || meta["active_revision"] != revision {
		t.Errorf("meta = %d %v", resp.StatusCode, meta)
	}

	// Refresh with changed content -> new revision, old still served.
	env.fetch.page = testPageWithMessages(t, "Test Conversation V2", 3)
	resp, refreshed := doJSON(t, http.MethodPost, env.server.URL+"/api/v1/snapshots/"+snapID+"/refresh", token, map[string]any{})
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || refreshed["changed"] != true {
		t.Fatalf("refresh = %d %v", resp.StatusCode, refreshed)
	}
	if refreshed["title"] != "Test Conversation V2" || refreshed["message_count"] != float64(3) {
		t.Fatalf("refresh returned stale metadata: %v", refreshed)
	}
	newRev := refreshed["revision"].(string)
	if newRev == revision {
		t.Fatal("refresh must create a new revision")
	}
	resp, err = client.Get(env.server.URL + "/c/" + slug + "/r/" + revision)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Error("old revision must remain reachable")
	}
	resp, err = client.Get(env.server.URL + "/c/" + slug)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if loc := resp.Header.Get("Location"); !strings.HasSuffix(loc, newRev) {
		t.Errorf("stable URL should point to new revision, got %q", loc)
	}
	resp, meta = doJSON(t, http.MethodGet, env.server.URL+"/api/v1/snapshots/"+snapID, token, nil)
	resp.Body.Close()
	if meta["title"] != "Test Conversation V2" || meta["message_count"] != float64(3) {
		t.Fatalf("refresh metadata not persisted: %v", meta)
	}

	// Refresh with identical content -> no change.
	resp, unchanged := doJSON(t, http.MethodPost, env.server.URL+"/api/v1/snapshots/"+snapID+"/refresh", token, map[string]any{})
	resp.Body.Close()
	if unchanged["changed"] != false {
		t.Errorf("unchanged refresh should be a no-op: %v", unchanged)
	}

	// Returning to previous content reactivates the immutable old revision and
	// replaces raw.json with the payload that produced the active content.
	env.fetch.page = testPage(t, "Test Conversation")
	resp, reactivated := doJSON(t, http.MethodPost, env.server.URL+"/api/v1/snapshots/"+snapID+"/refresh", token, map[string]any{})
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || reactivated["changed"] != true || reactivated["revision"] != revision {
		t.Fatalf("reactivation = %d %v", resp.StatusCode, reactivated)
	}
	raw, err := env.files.ReadFile(env.files.RawRelPath(snapID))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "Test Conversation") || strings.Contains(string(raw), "Test Conversation V2") {
		t.Fatalf("raw.json was not refreshed: %s", raw)
	}

	// Preview returns HTML without touching state.
	resp, prev := doJSON(t, http.MethodPost, env.server.URL+"/api/v1/previews", token, map[string]any{
		"snapshot_id": snapID, "kind": "embed",
	})
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || !strings.Contains(prev["html"].(string), "postMessage") {
		t.Fatalf("preview = %d", resp.StatusCode)
	}
	if resp.Header.Get("Cache-Control") != "no-store" {
		t.Error("preview must be no-store")
	}
	for _, timeout := range []int{-1, 301} {
		resp, body := doJSON(t, http.MethodPost, env.server.URL+"/api/v1/previews", token, map[string]any{
			"snapshot_id": snapID, "timeout_seconds": timeout,
		})
		resp.Body.Close()
		if resp.StatusCode != http.StatusBadRequest || body["error"].(map[string]any)["code"] != "invalid_option" {
			t.Errorf("preview timeout %d = %d %v", timeout, resp.StatusCode, body)
		}
	}

	resp, prev = doJSON(t, http.MethodPost, env.server.URL+"/api/v1/previews", token, map[string]any{
		"snapshot_id": snapID,
		"url":         "https://chatgpt.com/share/preview-title",
		"title":       "  Trimmed preview title  ",
	})
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || prev["title"] != "Trimmed preview title" {
		t.Fatalf("URL preview title = %d %v", resp.StatusCode, prev)
	}

	// Delete -> stable address dead, admin API still sees deleted status.
	resp, _ = doJSON(t, http.MethodDelete, env.server.URL+"/api/v1/snapshots/"+snapID, token, nil)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatal("delete failed")
	}
	resp, err = client.Get(env.server.URL + "/c/" + slug)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("deleted stable address = %d", resp.StatusCode)
	}
	resp, err = client.Get(env.server.URL + "/c/" + slug + "/r/" + newRev)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("deleted revision address = %d", resp.StatusCode)
	}
}

func TestRefreshAcceptsChunkedJSON(t *testing.T) {
	env := newTestEnv(t)
	resp, imported := doJSON(t, http.MethodPost, env.server.URL+"/api/v1/snapshots", "", map[string]any{
		"url": "https://chatgpt.com/share/chunked",
	})
	resp.Body.Close()
	env.fetch.page = testPageWithMessages(t, "Chunked Refresh", 3)

	req, err := http.NewRequest(http.MethodPost,
		env.server.URL+"/api/v1/snapshots/"+imported["id"].(string)+"/refresh",
		io.NopCloser(strings.NewReader(`{"title":"Chunked title"}`)))
	if err != nil {
		t.Fatal(err)
	}
	req.ContentLength = -1
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+imported["admin_token"].(string))
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	data, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("chunked refresh = %d %s", resp.StatusCode, data)
	}
	var result map[string]any
	if err := json.Unmarshal(data, &result); err != nil {
		t.Fatal(err)
	}
	if result["title"] != "Chunked title" || result["message_count"] != float64(3) {
		t.Fatalf("chunked refresh result = %v", result)
	}
}

func TestRefreshInheritsOmittedSnapshotOptions(t *testing.T) {
	env := newTestEnv(t)
	resp, imported := doJSON(t, http.MethodPost, env.server.URL+"/api/v1/snapshots", "", map[string]any{
		"url":            "https://chatgpt.com/share/options",
		"include_hidden": true,
		"all_nodes":      true,
		"timezone":       "Asia/Shanghai",
	})
	resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("import = %d %v", resp.StatusCode, imported)
	}

	snapshotID := imported["id"].(string)
	token := imported["admin_token"].(string)
	revision := imported["revision"].(string)
	resp, unchanged := doJSON(t, http.MethodPost, env.server.URL+"/api/v1/snapshots/"+snapshotID+"/refresh", token, map[string]any{})
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || unchanged["changed"] != false || unchanged["revision"] != revision {
		t.Fatalf("omitted options changed revision: %d %v", resp.StatusCode, unchanged)
	}

	resp, changed := doJSON(t, http.MethodPost, env.server.URL+"/api/v1/snapshots/"+snapshotID+"/refresh", token, map[string]any{
		"include_hidden": false,
		"all_nodes":      false,
		"timezone":       "UTC",
	})
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || changed["changed"] != true || changed["revision"] == revision {
		t.Fatalf("explicit options were not applied: %d %v", resp.StatusCode, changed)
	}

	row, err := env.store.GetRevision(context.Background(), snapshotID, changed["revision"].(string))
	if err != nil {
		t.Fatal(err)
	}
	data, err := env.files.ReadFile(row.SnapshotPath)
	if err != nil {
		t.Fatal(err)
	}
	var snapshot struct {
		Metadata struct {
			IncludeHidden bool   `json:"include_hidden"`
			AllNodes      bool   `json:"all_nodes"`
			Timezone      string `json:"timezone"`
		} `json:"metadata"`
	}
	if err := json.Unmarshal(data, &snapshot); err != nil {
		t.Fatal(err)
	}
	if snapshot.Metadata.IncludeHidden || snapshot.Metadata.AllNodes || snapshot.Metadata.Timezone != "UTC" {
		t.Fatalf("stored options = %+v", snapshot.Metadata)
	}
}

func TestFallbackRefusesUnavailableRendererVersion(t *testing.T) {
	env := newTestEnv(t)
	resp, imported := doJSON(t, http.MethodPost, env.server.URL+"/api/v1/snapshots", "", map[string]any{
		"url": "https://chatgpt.com/share/legacy-renderer",
	})
	resp.Body.Close()
	snapshotID := imported["id"].(string)
	slug := imported["slug"].(string)
	revision := "legacy-renderer"
	relDir := env.files.RevisionRelDir(snapshotID, revision)
	rev := &storage.Revision{
		SnapshotID:       snapshotID,
		Revision:         revision,
		ContentHash:      "legacy-content-hash",
		ExtractorVersion: "e1",
		RendererVersion:  "r0",
		SnapshotPath:     filepath.Join(relDir, "snapshot.json"),
		PagePath:         filepath.Join(relDir, "page.html"),
		EmbedPath:        filepath.Join(relDir, "embed.html"),
		MessageCount:     2,
	}
	if err := env.store.PublishRevision(context.Background(), nil, rev, ""); err != nil {
		t.Fatal(err)
	}
	resp, err := http.Get(env.server.URL + "/c/" + slug + "/r/" + revision)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("legacy fallback = %d", resp.StatusCode)
	}
	if _, err := os.Stat(env.files.Abs(rev.PagePath)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("legacy artifact should not be regenerated: %v", err)
	}
}

func TestLegacyChatPresentationPreservesArchive(t *testing.T) {
	env := newTestEnv(t)
	env.fetch.page = testPageWithMessages(t, "Legacy chat", 3)
	resp, imported := doJSON(t, http.MethodPost, env.server.URL+"/api/v1/snapshots", "", map[string]any{"url": "https://chatgpt.com/share/legacy-chat"})
	resp.Body.Close()
	id, slug := imported["id"].(string), imported["slug"].(string)
	active, err := env.store.GetRevision(context.Background(), id, imported["revision"].(string))
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := env.files.ReadFile(active.SnapshotPath)
	if err != nil {
		t.Fatal(err)
	}
	rawBytes, err := env.files.ReadFile(env.files.RawRelPath(id))
	if err != nil {
		t.Fatal(err)
	}
	var raw map[string]any
	if err := json.Unmarshal(rawBytes, &raw); err != nil {
		t.Fatal(err)
	}
	raw["mapping"].(map[string]any)["n2"].(map[string]any)["message"].(map[string]any)["channel"] = "commentary"
	rawBytes, _ = json.Marshal(raw)
	if err := env.files.WriteRawJSON(id, rawBytes); err != nil {
		t.Fatal(err)
	}
	rev := *active
	rev.Revision = "legacy-chat"
	rev.RendererVersion = "r1"
	rel := env.files.RevisionRelDir(id, rev.Revision)
	rev.SnapshotPath = filepath.Join(rel, "snapshot.json")
	rev.PagePath = filepath.Join(rel, "page.html")
	rev.EmbedPath = filepath.Join(rel, "embed.html")
	if err := env.files.WriteRevision(id, rev.Revision, storage.RevisionFiles{SnapshotJSON: snapshot, PageHTML: []byte("old page"), EmbedHTML: []byte("old embed")}); err != nil {
		t.Fatal(err)
	}
	if err := env.store.PublishRevision(context.Background(), nil, &rev, ""); err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"c", "e"} {
		for attempt := 0; attempt < 2; attempt++ {
			request, _ := http.NewRequest(http.MethodGet, env.server.URL+"/"+kind+"/"+slug+"/r/legacy-chat", nil)
			request.Header.Set("If-None-Match", `"`+rev.ContentHash+`"`)
			result, err := http.DefaultClient.Do(request)
			if err != nil {
				t.Fatal(err)
			}
			body, _ := io.ReadAll(result.Body)
			result.Body.Close()
			if result.StatusCode != 200 || !strings.Contains(string(body), "msg-assistant") || strings.Contains(string(body), "Go is a language") || !strings.Contains(string(body), "Additional answer 3") {
				t.Fatalf("bad legacy response %d: %s", result.StatusCode, body)
			}
		}
	}
	archived, _ := env.files.ReadFile(rev.PagePath)
	if string(archived) != "old page" {
		t.Fatal("overwrote archive")
	}
	archivedSnapshot, _ := env.files.ReadFile(rev.SnapshotPath)
	if string(archivedSnapshot) != string(snapshot) {
		t.Fatal("overwrote snapshot")
	}
}

func TestImportValidation(t *testing.T) {
	env := newTestEnv(t)
	url := env.server.URL + "/api/v1/snapshots"

	// invalid JSON / unknown field
	resp, _ := doJSON(t, http.MethodPost, url, "", map[string]any{"url": "https://chatgpt.com/share/x", "bogus": 1})
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("unknown field = %d", resp.StatusCode)
	}
	// missing url
	resp, _ = doJSON(t, http.MethodPost, url, "", map[string]any{})
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("missing url = %d", resp.StatusCode)
	}
	// invalid url
	resp, body := doJSON(t, http.MethodPost, url, "", map[string]any{"url": "http://evil.com/share/x"})
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("invalid url = %d", resp.StatusCode)
	}
	errObj := body["error"].(map[string]any)
	if errObj["code"] != "invalid_url" {
		t.Errorf("code = %v", errObj["code"])
	}
	if errObj["request_id"] == "" {
		t.Error("request_id missing")
	}
	// bad timeout
	resp, _ = doJSON(t, http.MethodPost, url, "", map[string]any{"url": "https://chatgpt.com/share/x", "timeout_seconds": 999})
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("bad timeout = %d", resp.StatusCode)
	}
	// method check
	resp, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Errorf("GET on POST route = %d", resp.StatusCode)
	}
}

func TestImportParseFailure(t *testing.T) {
	env := newTestEnv(t)
	env.fetch.page = `<html><body>no payload</body></html>`
	resp, body := doJSON(t, http.MethodPost, env.server.URL+"/api/v1/snapshots", "", map[string]any{
		"url": "https://chatgpt.com/share/empty",
	})
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	if body["error"].(map[string]any)["code"] != "parse_failed" {
		t.Errorf("code = %v", body)
	}
}
