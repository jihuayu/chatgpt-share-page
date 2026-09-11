package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
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
	t.Helper()
	table := []any{
		map[string]any{
			"title":           title,
			"conversation_id": "conv-" + title,
			"current_node":    "n2",
			"mapping": map[string]any{
				"n1": map[string]any{
					"id": "n1",
					"message": map[string]any{
						"id":          "m1",
						"author":      map[string]any{"role": "user"},
						"create_time": 1700000000.0,
						"content":     map[string]any{"content_type": "text", "parts": []any{"What is Go?"}},
					},
				},
				"n2": map[string]any{
					"id":     "n2",
					"parent": "n1",
					"message": map[string]any{
						"id":          "m2",
						"author":      map[string]any{"role": "assistant"},
						"create_time": 1700000060.0,
						"content": map[string]any{
							"content_type": "text",
							"parts":        []any{"Go is a language.\n```go\nfmt.Println(\"hi\")\n```"},
						},
					},
				},
			},
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
	env.fetch.page = testPage(t, "Test Conversation V2")
	resp, refreshed := doJSON(t, http.MethodPost, env.server.URL+"/api/v1/snapshots/"+snapID+"/refresh", token, map[string]any{})
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || refreshed["changed"] != true {
		t.Fatalf("refresh = %d %v", resp.StatusCode, refreshed)
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

	// Refresh with identical content -> no change.
	resp, unchanged := doJSON(t, http.MethodPost, env.server.URL+"/api/v1/snapshots/"+snapID+"/refresh", token, map[string]any{})
	resp.Body.Close()
	if unchanged["changed"] != false {
		t.Errorf("unchanged refresh should be a no-op: %v", unchanged)
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
