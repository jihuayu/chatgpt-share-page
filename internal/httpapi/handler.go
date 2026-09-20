// Package httpapi exposes the snapshot service over HTTP: the JSON API for
// import/preview/refresh/delete and the immutable public pages.
package httpapi

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/jihuayu/chatgpt-share-page/internal/config"
	"github.com/jihuayu/chatgpt-share-page/internal/conversation"
	"github.com/jihuayu/chatgpt-share-page/internal/extractor"
	"github.com/jihuayu/chatgpt-share-page/internal/fetcher"
	"github.com/jihuayu/chatgpt-share-page/internal/publish"
	"github.com/jihuayu/chatgpt-share-page/internal/renderer"
	"github.com/jihuayu/chatgpt-share-page/internal/security"
	"github.com/jihuayu/chatgpt-share-page/internal/storage"
	"github.com/jihuayu/chatgpt-share-page/web"
)

const maxTimeoutSeconds = 300

// Handler implements all service endpoints.
type Handler struct {
	svc      *publish.Service
	store    *storage.Store
	files    *storage.FileStore
	renderer *renderer.Renderer
	cfg      config.Config
	log      *slog.Logger

	fallbackMu  sync.Map // path -> *sync.Mutex
	fallbackSem chan struct{}
}

// New builds the HTTP handler around the publish service.
func New(
	cfg config.Config,
	svc *publish.Service,
	store *storage.Store,
	files *storage.FileStore,
	r *renderer.Renderer,
	log *slog.Logger,
) *Handler {
	if log == nil {
		log = slog.Default()
	}
	return &Handler{
		svc:         svc,
		store:       store,
		files:       files,
		renderer:    r,
		cfg:         cfg,
		log:         log,
		fallbackSem: make(chan struct{}, 2),
	}
}

// Routes returns the mux with all endpoints registered.
func (h *Handler) Routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", h.handleHealth)
	mux.HandleFunc("GET /{$}", h.handleIndex)
	mux.HandleFunc("GET /favicon.ico", h.handleFavicon)
	mux.HandleFunc("GET /assets/index.css", h.handleIndexCSS)
	mux.HandleFunc("GET /assets/index.js", h.handleIndexJS)
	mux.HandleFunc("GET /assets/logo.png", h.handleLogo)
	mux.HandleFunc("POST /api/v1/snapshots", h.handleImport)
	mux.HandleFunc("GET /api/v1/snapshots/{id}", h.handleGetSnapshot)
	mux.HandleFunc("POST /api/v1/snapshots/{id}/refresh", h.handleRefresh)
	mux.HandleFunc("DELETE /api/v1/snapshots/{id}", h.handleDelete)
	mux.HandleFunc("POST /api/v1/previews", h.handlePreview)
	mux.HandleFunc("GET /c/{slug}", h.handleStablePage)
	mux.HandleFunc("GET /c/{slug}/r/{revision}", h.handleRevisionPage)
	mux.HandleFunc("GET /e/{slug}", h.handleStableEmbed)
	mux.HandleFunc("GET /e/{slug}/r/{revision}", h.handleRevisionEmbed)
	return mux
}

func (h *Handler) handleHealth(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, r, http.StatusOK, map[string]string{"status": "ok"})
}

func (h *Handler) handleIndex(w http.ResponseWriter, r *http.Request) {
	data, err := web.Static.ReadFile("static/index.html")
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "internal_error", "interface unavailable")
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'self'; script-src 'self'; connect-src 'self'; img-src 'self'; base-uri 'none'; form-action 'self'; frame-ancestors 'none'")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	_, _ = w.Write(data)
}

func (h *Handler) handleFavicon(w http.ResponseWriter, r *http.Request) {
	h.serveStaticAsset(w, r, "static/logo.png", "image/png", "public, max-age=86400")
}

func (h *Handler) handleIndexCSS(w http.ResponseWriter, r *http.Request) {
	h.serveIndexAsset(w, r, "static/index.css", "text/css; charset=utf-8")
}

func (h *Handler) handleIndexJS(w http.ResponseWriter, r *http.Request) {
	h.serveIndexAsset(w, r, "static/index.js", "text/javascript; charset=utf-8")
}

func (h *Handler) handleLogo(w http.ResponseWriter, r *http.Request) {
	h.serveStaticAsset(w, r, "static/logo.png", "image/png", "public, max-age=86400")
}

func (h *Handler) serveIndexAsset(w http.ResponseWriter, r *http.Request, name, contentType string) {
	h.serveStaticAsset(w, r, name, contentType, "public, max-age=3600")
}

func (h *Handler) serveStaticAsset(w http.ResponseWriter, r *http.Request, name, contentType, cacheControl string) {
	data, err := web.Static.ReadFile(name)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Cache-Control", cacheControl)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	_, _ = w.Write(data)
}

type importRequest struct {
	URL            string  `json:"url"`
	Title          string  `json:"title"`
	IncludeHidden  *bool   `json:"include_hidden"`
	AllNodes       *bool   `json:"all_nodes"`
	Timezone       *string `json:"timezone"`
	TimeoutSeconds int     `json:"timeout_seconds"`
}

func (r *importRequest) toService() publish.ImportRequest {
	return publish.ImportRequest{
		URL:            strings.TrimSpace(r.URL),
		Title:          strings.TrimSpace(r.Title),
		IncludeHidden:  r.IncludeHidden,
		AllNodes:       r.AllNodes,
		Timezone:       trimOptionalString(r.Timezone),
		TimeoutSeconds: r.TimeoutSeconds,
	}
}

func trimOptionalString(value *string) *string {
	if value == nil {
		return nil
	}
	trimmed := strings.TrimSpace(*value)
	return &trimmed
}

func (h *Handler) handleImport(w http.ResponseWriter, r *http.Request) {
	var input importRequest
	if !decodeJSON(w, r, &input, h.cfg.MaxRequestBytes) {
		return
	}
	if strings.TrimSpace(input.URL) == "" {
		writeError(w, r, http.StatusBadRequest, "missing_url", "url is required")
		return
	}
	if input.TimeoutSeconds < 0 || input.TimeoutSeconds > maxTimeoutSeconds {
		writeError(w, r, http.StatusBadRequest, "invalid_option", "timeout_seconds must be between 0 and 300")
		return
	}
	result, err := h.svc.Import(r.Context(), input.toService())
	if err != nil {
		h.serviceError(w, r, err)
		return
	}
	status := http.StatusOK
	if !result.Existing {
		status = http.StatusCreated
	}
	writeJSON(w, r, status, result)
}

func (h *Handler) handleGetSnapshot(w http.ResponseWriter, r *http.Request) {
	snap, ok := h.authorized(w, r)
	if !ok {
		return
	}
	writeJSON(w, r, http.StatusOK, map[string]any{
		"id":              snap.ID,
		"slug":            snap.Slug,
		"title":           snap.Title,
		"source_url":      snap.SourceURL,
		"status":          snap.Status,
		"visibility":      snap.Visibility,
		"noindex":         snap.NoIndex,
		"active_revision": snap.ActiveRevision,
		"content_hash":    snap.ContentHash,
		"message_count":   snap.MessageCount,
		"created_at":      snap.CreatedAt.Format(time.RFC3339),
		"updated_at":      snap.UpdatedAt.Format(time.RFC3339),
		"page_url":        h.cfg.PublicBaseURL + "/c/" + snap.Slug,
		"embed_url":       h.cfg.PublicBaseURL + "/e/" + snap.Slug,
	})
}

func (h *Handler) handleRefresh(w http.ResponseWriter, r *http.Request) {
	snap, ok := h.authorized(w, r)
	if !ok {
		return
	}
	var input importRequest
	if r.ContentLength != 0 {
		if !decodeJSON(w, r, &input, h.cfg.MaxRequestBytes) {
			return
		}
		if input.TimeoutSeconds < 0 || input.TimeoutSeconds > maxTimeoutSeconds {
			writeError(w, r, http.StatusBadRequest, "invalid_option", "timeout_seconds must be between 0 and 300")
			return
		}
	}
	result, err := h.svc.Refresh(r.Context(), snap.ID, input.toService())
	if err != nil {
		h.serviceError(w, r, err)
		return
	}
	writeJSON(w, r, http.StatusOK, result)
}

func (h *Handler) handleDelete(w http.ResponseWriter, r *http.Request) {
	snap, ok := h.authorized(w, r)
	if !ok {
		return
	}
	if err := h.svc.Delete(r.Context(), snap.ID); err != nil {
		h.serviceError(w, r, err)
		return
	}
	writeJSON(w, r, http.StatusOK, map[string]string{"status": "deleted", "id": snap.ID})
}

type previewRequest struct {
	SnapshotID     string  `json:"snapshot_id"`
	Kind           string  `json:"kind"`
	URL            string  `json:"url"`
	Title          string  `json:"title"`
	IncludeHidden  *bool   `json:"include_hidden"`
	AllNodes       *bool   `json:"all_nodes"`
	Timezone       *string `json:"timezone"`
	TimeoutSeconds int     `json:"timeout_seconds"`
}

// handlePreview renders a stored snapshot revision on demand. The result is
// never persisted or published and is served with Cache-Control: no-store.
func (h *Handler) handlePreview(w http.ResponseWriter, r *http.Request) {
	var input previewRequest
	if !decodeJSON(w, r, &input, h.cfg.MaxRequestBytes) {
		return
	}
	snapshotID := strings.TrimSpace(input.SnapshotID)
	if snapshotID == "" {
		writeError(w, r, http.StatusBadRequest, "invalid_json", "snapshot_id is required")
		return
	}
	if input.TimeoutSeconds < 0 || input.TimeoutSeconds > maxTimeoutSeconds {
		writeError(w, r, http.StatusBadRequest, "invalid_option", "timeout_seconds must be between 0 and 300")
		return
	}
	snap, err := h.store.GetSnapshotByID(r.Context(), snapshotID)
	if err != nil {
		h.serviceError(w, r, &publish.NotFoundError{What: "snapshot"})
		return
	}
	if !h.checkToken(w, r, snapshotID) {
		return
	}
	kind := input.Kind
	if kind == "" {
		kind = "page"
	}
	if kind != "page" && kind != "embed" {
		writeError(w, r, http.StatusBadRequest, "invalid_option", "kind must be page or embed")
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	if strings.TrimSpace(input.URL) != "" {
		h.previewFromURL(w, r, snap, input)
		return
	}
	htmlBytes, _, err := h.svc.Preview(r.Context(), snap.ID, kind)
	if err != nil {
		h.serviceError(w, r, err)
		return
	}
	writeJSON(w, r, http.StatusOK, map[string]any{
		"snapshot_id": snap.ID,
		"kind":        kind,
		"html":        string(htmlBytes),
	})
}

// previewFromURL fetches a candidate replacement for the snapshot without
// persisting anything.
func (h *Handler) previewFromURL(w http.ResponseWriter, r *http.Request, snap *storage.Snapshot, input previewRequest) {
	result, err := h.svc.PreviewImport(r.Context(), snap.ID, publish.ImportRequest{
		URL:            strings.TrimSpace(input.URL),
		Title:          strings.TrimSpace(input.Title),
		IncludeHidden:  input.IncludeHidden,
		AllNodes:       input.AllNodes,
		Timezone:       trimOptionalString(input.Timezone),
		TimeoutSeconds: input.TimeoutSeconds,
	}, input.Kind)
	if err != nil {
		h.serviceError(w, r, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, r, http.StatusOK, result)
}

// authorized resolves {id} and checks the bearer token.
func (h *Handler) authorized(w http.ResponseWriter, r *http.Request) (*storage.Snapshot, bool) {
	id := r.PathValue("id")
	snap, err := h.store.GetSnapshotByID(r.Context(), id)
	if err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			writeError(w, r, http.StatusNotFound, "snapshot_not_found", "snapshot not found")
		} else {
			writeError(w, r, http.StatusInternalServerError, "storage_failed", "internal error")
		}
		return nil, false
	}
	if !h.checkToken(w, r, snap.ID) {
		return nil, false
	}
	return snap, true
}

// checkToken validates the Authorization bearer token against the snapshot's
// stored admin token hash.
func (h *Handler) checkToken(w http.ResponseWriter, r *http.Request, snapshotID string) bool {
	header := r.Header.Get("Authorization")
	if !strings.HasPrefix(header, "Bearer ") || strings.TrimSpace(header[7:]) == "" {
		writeError(w, r, http.StatusUnauthorized, "missing_token", "Authorization: Bearer <admin-token> required")
		return false
	}
	token := strings.TrimSpace(header[7:])
	hash, err := h.store.TokenHash(r.Context(), snapshotID)
	if err != nil || !security.TokenMatches(token, hash) {
		writeError(w, r, http.StatusForbidden, "invalid_token", "admin token is invalid")
		return false
	}
	return true
}

// --- public pages ---------------------------------------------------------

func (h *Handler) handleStablePage(w http.ResponseWriter, r *http.Request) {
	h.serveStable(w, r, "c")
}

func (h *Handler) handleStableEmbed(w http.ResponseWriter, r *http.Request) {
	h.serveStable(w, r, "e")
}

// serveStable redirects the stable address to the currently active revision.
func (h *Handler) serveStable(w http.ResponseWriter, r *http.Request, kind string) {
	snap, ok := h.lookupPublic(w, r)
	if !ok {
		return
	}
	target := "/" + kind + "/" + snap.Slug + "/r/" + snap.ActiveRevision
	w.Header().Set("Cache-Control", "public, max-age=0, must-revalidate")
	w.Header().Set("Cloudflare-CDN-Cache-Control", "public, max-age=60, stale-while-revalidate=86400")
	h.noindexHeader(w, snap)
	http.Redirect(w, r, target, http.StatusFound)
}

func (h *Handler) handleRevisionPage(w http.ResponseWriter, r *http.Request) {
	h.serveRevision(w, r, "c")
}

func (h *Handler) handleRevisionEmbed(w http.ResponseWriter, r *http.Request) {
	h.serveRevision(w, r, "e")
}

// serveRevision returns the immutable artifact for one revision, regenerating
// it from snapshot.json if the file was lost.
func (h *Handler) serveRevision(w http.ResponseWriter, r *http.Request, kind string) {
	snap, ok := h.lookupPublic(w, r)
	if !ok {
		return
	}
	revisionID := r.PathValue("revision")
	rev, err := h.store.GetRevision(r.Context(), snap.ID, revisionID)
	if err != nil {
		writeError(w, r, http.StatusNotFound, "snapshot_not_found", "revision not found")
		return
	}
	path := rev.PagePath
	if kind == "e" {
		path = rev.EmbedPath
	}
	// Preserve archived HTML and snapshot JSON; cache the upgraded
	// presentation separately so existing public URLs receive rendering fixes.
	if rev.RendererVersion == "r1" || rev.RendererVersion == "r2" {
		path += "." + renderer.Version
	}
	data, err := h.files.ReadFile(path)
	if err != nil {
		data, err = h.regenerateArtifact(r.Context(), rev, kind, path)
		if err != nil {
			h.log.Error("artifact missing and fallback failed",
				"snapshot_id", snap.ID, "revision", revisionID, "error", err)
			writeError(w, r, http.StatusInternalServerError, "render_failed", "artifact unavailable")
			return
		}
	}
	h.writeArtifact(w, r, snap, rev, data, kind)
}

func (h *Handler) lookupPublic(w http.ResponseWriter, r *http.Request) (*storage.Snapshot, bool) {
	slug := r.PathValue("slug")
	snap, err := h.store.GetSnapshotBySlug(r.Context(), slug)
	if err != nil {
		writeError(w, r, http.StatusNotFound, "snapshot_not_found", "snapshot not found")
		return nil, false
	}
	if snap.Status != "active" {
		writeError(w, r, http.StatusNotFound, "snapshot_deleted", "snapshot has been deleted")
		return nil, false
	}
	return snap, true
}

// regenerateArtifact rebuilds a missing page/embed file from snapshot.json.
// This fallback path is rare, serialized per path and globally bounded.
func (h *Handler) regenerateArtifact(ctx context.Context, rev *storage.Revision, kind, path string) ([]byte, error) {
	mu, _ := h.fallbackMu.LoadOrStore(path, &sync.Mutex{})
	lock := mu.(*sync.Mutex)
	lock.Lock()
	defer lock.Unlock()
	if data, err := h.files.ReadFile(path); err == nil {
		return data, nil // another request rebuilt it
	}
	if rev.RendererVersion != renderer.Version && rev.RendererVersion != "r1" && rev.RendererVersion != "r2" {
		return nil, errors.New("artifact renderer version is no longer available")
	}
	select {
	case h.fallbackSem <- struct{}{}:
		defer func() { <-h.fallbackSem }()
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	raw, err := h.files.ReadFile(rev.SnapshotPath)
	if err != nil {
		return nil, err
	}
	var snapshot conversation.ConversationSnapshot
	if err := json.Unmarshal(raw, &snapshot); err != nil {
		return nil, err
	}
	if rev.RendererVersion == "r1" {
		payload, readErr := h.files.ReadFile(h.files.RawRelPath(rev.SnapshotID))
		if readErr != nil {
			return nil, fmt.Errorf("read legacy message metadata: %w", readErr)
		}
		var original map[string]any
		if err := json.Unmarshal(payload, &original); err != nil {
			return nil, err
		}
		conversation.RestorePresentationMetadata(&snapshot, original)
	}
	var data []byte
	if kind == "e" {
		data, err = h.renderer.RenderEmbed(&snapshot)
	} else {
		data, err = h.renderer.RenderPage(&snapshot)
	}
	if err != nil {
		return nil, err
	}
	if err := h.files.WriteFileAtomic(path, data); err != nil {
		return nil, err
	}
	h.log.Warn("regenerated missing artifact",
		"snapshot_id", rev.SnapshotID, "revision", rev.Revision, "path", path)
	return data, nil
}

func (h *Handler) writeArtifact(w http.ResponseWriter, r *http.Request, snap *storage.Snapshot, rev *storage.Revision, data []byte, kind string) {
	sum := sha256.Sum256(data)
	etag := fmt.Sprintf(`"%x"`, sum)
	csp := h.renderer.PageCSP()
	if kind == "e" {
		csp = h.renderer.EmbedCSP()
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Content-Security-Policy", csp)
	w.Header().Set("Cache-Control", "public, no-cache")
	w.Header().Set("Cloudflare-CDN-Cache-Control", "public, max-age=300, stale-if-error=604800")
	w.Header().Set("ETag", etag)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Referrer-Policy", "no-referrer")
	h.noindexHeader(w, snap)
	if match := r.Header.Get("If-None-Match"); match == etag {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(data)
}

func (h *Handler) noindexHeader(w http.ResponseWriter, snap *storage.Snapshot) {
	if snap.NoIndex {
		w.Header().Set("X-Robots-Tag", "noindex, nofollow")
	}
}

// --- errors ---------------------------------------------------------------

func (h *Handler) serviceError(w http.ResponseWriter, r *http.Request, err error) {
	var invalidURL *security.InvalidURLError
	var invalidOption *conversation.InvalidOptionError
	var fetchErr *fetcher.FetchError
	var parseErr *extractor.ParseError
	var storageErr *publish.StorageError
	var conflictErr *publish.ConflictError
	var notFound *publish.NotFoundError
	var deleted *publish.DeletedError
	var renderErr *renderer.RenderError
	switch {
	case errors.As(err, &invalidURL):
		writeError(w, r, http.StatusBadRequest, "invalid_url", invalidURL.Error())
	case errors.As(err, &invalidOption):
		writeError(w, r, http.StatusBadRequest, "invalid_option", invalidOption.Error())
	case errors.As(err, &fetchErr):
		writeError(w, r, http.StatusBadGateway, "fetch_failed", fetchErr.Error())
	case errors.As(err, &parseErr):
		writeError(w, r, http.StatusUnprocessableEntity, "parse_failed", parseErr.Error())
	case errors.As(err, &notFound):
		writeError(w, r, http.StatusNotFound, "snapshot_not_found", notFound.Error())
	case errors.As(err, &deleted):
		writeError(w, r, http.StatusNotFound, "snapshot_deleted", deleted.Error())
	case errors.As(err, &conflictErr):
		writeError(w, r, http.StatusConflict, "revision_conflict", conflictErr.Error())
	case errors.As(err, &renderErr):
		writeError(w, r, http.StatusInternalServerError, "render_failed", "HTML generation failed")
	case errors.As(err, &storageErr):
		h.log.Error("storage failure", "error", storageErr.Err)
		writeError(w, r, http.StatusInternalServerError, "storage_failed", "internal storage error")
	case errors.Is(err, context.DeadlineExceeded):
		writeError(w, r, http.StatusBadGateway, "fetch_failed", "fetch timed out")
	default:
		h.log.Error("internal error", "error", err)
		writeError(w, r, http.StatusInternalServerError, "internal_error", "internal server error")
	}
}

// decodeJSON reads one JSON object; unknown fields and oversize bodies are
// rejected. Returns false after writing the error response.
func decodeJSON(w http.ResponseWriter, r *http.Request, dst any, maxBytes int64) bool {
	if maxBytes <= 0 {
		maxBytes = 1 << 20
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBytes))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(dst); err != nil {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			writeError(w, r, http.StatusRequestEntityTooLarge, "request_too_large", "request body exceeds limit")
		} else {
			writeError(w, r, http.StatusBadRequest, "invalid_json", "request body must be a valid JSON object")
		}
		return false
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		writeError(w, r, http.StatusBadRequest, "invalid_json", "request body must contain one JSON object")
		return false
	}
	return true
}

type errorResponse struct {
	Error apiError `json:"error"`
}

type apiError struct {
	Code      string `json:"code"`
	Message   string `json:"message"`
	RequestID string `json:"request_id,omitempty"`
}

func writeError(w http.ResponseWriter, r *http.Request, status int, code, message string) {
	writeJSON(w, r, status, errorResponse{Error: apiError{
		Code: code, Message: message, RequestID: requestID(r.Context()),
	}})
}

func writeJSON(w http.ResponseWriter, r *http.Request, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	if strings.HasPrefix(r.URL.Path, "/api/") || r.URL.Path == "/healthz" {
		w.Header().Set("Cache-Control", "no-store")
	}
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
