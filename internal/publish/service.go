// Package publish orchestrates the import, refresh and deletion lifecycle:
// fetch -> extract -> normalize -> write artifacts -> activate revision in
// SQLite -> purge the stable-URL edge cache.
package publish

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"time"

	"github.com/jihuayu/chatgpt-share-page/internal/cache"
	"github.com/jihuayu/chatgpt-share-page/internal/config"
	"github.com/jihuayu/chatgpt-share-page/internal/conversation"
	"github.com/jihuayu/chatgpt-share-page/internal/extractor"
	"github.com/jihuayu/chatgpt-share-page/internal/renderer"
	"github.com/jihuayu/chatgpt-share-page/internal/security"
	"github.com/jihuayu/chatgpt-share-page/internal/storage"
)

// extractorVersion identifies the payload extraction/normalization pipeline.
const extractorVersion = "e1"

// StorageError indicates a SQLite or filesystem write failure.
type StorageError struct{ Err error }

func (e *StorageError) Error() string { return "storage failed: " + e.Err.Error() }
func (e *StorageError) Unwrap() error { return e.Err }

// ConflictError indicates a concurrent update problem.
type ConflictError struct{ Message string }

func (e *ConflictError) Error() string { return e.Message }

// NotFoundError indicates a missing snapshot or revision.
type NotFoundError struct{ What string }

func (e *NotFoundError) Error() string { return e.What + " not found" }

// DeletedError indicates the snapshot has been deleted.
type DeletedError struct{}

func (e *DeletedError) Error() string { return "snapshot deleted" }

// ImportRequest describes one import or refresh operation.
type ImportRequest struct {
	URL            string
	Title          string
	IncludeHidden  *bool
	AllNodes       *bool
	Timezone       *string
	TimeoutSeconds int
}

// ImportResult is returned after an import or refresh.
type ImportResult struct {
	ID           string `json:"id"`
	Slug         string `json:"slug"`
	Revision     string `json:"revision"`
	Title        string `json:"title"`
	MessageCount int    `json:"message_count"`
	PageURL      string `json:"page_url"`
	EmbedURL     string `json:"embed_url"`
	AdminToken   string `json:"admin_token,omitempty"`
	Existing     bool   `json:"existing,omitempty"`
	Changed      bool   `json:"changed"`
}

// Fetcher is the part of fetcher.Client the pipeline needs; an interface so
// tests can substitute a stub.
type Fetcher interface {
	FetchHTML(ctx context.Context, rawURL string) (string, error)
}

// Service wires together the pipeline dependencies.
type Service struct {
	cfg      config.Config
	store    *storage.Store
	files    *storage.FileStore
	fetcher  Fetcher
	renderer *renderer.Renderer
	purger   cache.Purger
	log      *slog.Logger
	sem      chan struct{}
}

// NewService builds the publish service.
func NewService(
	cfg config.Config,
	store *storage.Store,
	files *storage.FileStore,
	fetchClient Fetcher,
	r *renderer.Renderer,
	purger cache.Purger,
	log *slog.Logger,
) *Service {
	if log == nil {
		log = slog.Default()
	}
	if purger == nil {
		purger = &cache.CloudflarePurger{}
	}
	concurrency := cfg.MaxImportConcurrency
	if concurrency < 1 {
		concurrency = 1
	}
	return &Service{
		cfg:      cfg,
		store:    store,
		files:    files,
		fetcher:  fetchClient,
		renderer: r,
		purger:   purger,
		log:      log,
		sem:      make(chan struct{}, concurrency),
	}
}

// Import creates a snapshot (or returns the existing one for a share URL that
// was already imported).
func (s *Service) Import(ctx context.Context, req ImportRequest) (*ImportResult, error) {
	if err := security.ValidateShareURL(req.URL); err != nil {
		return nil, err
	}
	options := req.options(conversation.Options{})
	if _, err := conversation.ResolveLocation(options.Timezone); err != nil {
		return nil, err
	}
	if err := s.acquire(ctx); err != nil {
		return nil, err
	}
	defer func() { <-s.sem }()

	shareID := security.ExtractShareID(req.URL)
	if shareID == "" {
		return nil, &security.InvalidURLError{URL: req.URL, Reason: "path must match /share/<share-id>"}
	}
	if existing, err := s.store.GetSnapshotByShareID(ctx, "chatgpt", shareID); err == nil {
		if existing.Status == "active" {
			return s.resultFor(existing, existing.ActiveRevision, "", true, false), nil
		}
	} else if !errors.Is(err, storage.ErrNotFound) {
		return nil, &StorageError{Err: err}
	}

	snapshot, raw, err := s.buildSnapshot(ctx, req, "", options)
	if err != nil {
		return nil, err
	}
	if req.Title != "" {
		snapshot.Title = req.Title
	}

	slug, err := s.uniqueSlug(ctx, snapshot.Title)
	if err != nil {
		return nil, &StorageError{Err: err}
	}
	snapshot.ID = security.NewID("snp")
	token, err := security.NewAdminToken()
	if err != nil {
		return nil, &StorageError{Err: err}
	}

	contentHash := conversation.ContentHash(snapshot)
	revisionID := conversation.RevisionID(contentHash)
	relDir := s.files.RevisionRelDir(snapshot.ID, revisionID)
	row := &storage.Snapshot{
		ID:             snapshot.ID,
		Slug:           slug,
		Title:          snapshot.Title,
		SourceProvider: snapshot.Source.Provider,
		SourceURL:      snapshot.Source.URL,
		SourceShareID:  snapshot.Source.ShareID,
		Visibility:     "unlisted",
		NoIndex:        true,
		Status:         "active",
		ContentHash:    contentHash,
		MessageCount:   snapshot.Metadata.MessageCount,
		CreatedAt:      snapshot.ImportedAt,
	}
	rev := &storage.Revision{
		SnapshotID:       snapshot.ID,
		Revision:         revisionID,
		ContentHash:      contentHash,
		ExtractorVersion: extractorVersion,
		RendererVersion:  renderer.Version,
		SnapshotPath:     filepath.Join(relDir, "snapshot.json"),
		PagePath:         filepath.Join(relDir, "page.html"),
		EmbedPath:        filepath.Join(relDir, "embed.html"),
		MessageCount:     snapshot.Metadata.MessageCount,
	}
	if err := s.writeRevisionArtifacts(snapshot, rev, raw); err != nil {
		return nil, err
	}
	if err := s.store.PublishRevision(ctx, row, rev, security.HashToken(token)); err != nil {
		return nil, &StorageError{Err: err}
	}
	s.log.Info("snapshot imported",
		"snapshot_id", snapshot.ID, "slug", slug, "revision", revisionID,
		"source_host", "chatgpt.com", "messages", snapshot.Metadata.MessageCount)
	return s.resultFor(row, revisionID, token, false, true), nil
}

// Refresh re-imports the source and publishes a new revision when content
// changed. Identical content keeps the active revision; content matching an
// older revision re-activates that revision instead of duplicating files.
func (s *Service) Refresh(ctx context.Context, snapshotID string, req ImportRequest) (*ImportResult, error) {
	snap, err := s.store.GetSnapshotByID(ctx, snapshotID)
	if err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			return nil, &NotFoundError{What: "snapshot"}
		}
		return nil, &StorageError{Err: err}
	}
	if snap.Status != "active" {
		return nil, &DeletedError{}
	}
	if req.URL == "" {
		req.URL = snap.SourceURL
	}
	options, err := s.activeOptions(ctx, snap)
	if err != nil {
		return nil, err
	}
	options = req.options(options)
	if err := security.ValidateShareURL(req.URL); err != nil {
		return nil, err
	}
	if _, err := conversation.ResolveLocation(options.Timezone); err != nil {
		return nil, err
	}
	if err := s.acquire(ctx); err != nil {
		return nil, err
	}
	defer func() { <-s.sem }()

	snapshot, raw, err := s.buildSnapshot(ctx, req, snap.ID, options)
	if err != nil {
		return nil, err
	}
	if req.Title != "" {
		snapshot.Title = req.Title
	}
	contentHash := conversation.ContentHash(snapshot)
	updated := refreshedSnapshotRow(snap, snapshot, contentHash)
	if contentHash == snap.ContentHash {
		return s.resultFor(updated, snap.ActiveRevision, "", false, false), nil
	}
	revisionID := conversation.RevisionID(contentHash)
	if _, err := s.store.GetRevision(ctx, snap.ID, revisionID); err == nil {
		// Content identical to an older revision: reactivate it.
		if err := s.files.WriteRawJSON(snap.ID, raw); err != nil {
			return nil, &StorageError{Err: fmt.Errorf("write raw.json: %w", err)}
		}
		if err := s.store.ActivateRevision(ctx, updated, revisionID, contentHash, snapshot.Metadata.MessageCount); err != nil {
			return nil, &StorageError{Err: err}
		}
		s.purgeStable(context.Background(), snap)
		return s.resultFor(updated, revisionID, "", false, true), nil
	} else if !errors.Is(err, storage.ErrNotFound) {
		return nil, &StorageError{Err: err}
	}

	relDir := s.files.RevisionRelDir(snap.ID, revisionID)
	rev := &storage.Revision{
		SnapshotID:       snap.ID,
		Revision:         revisionID,
		ContentHash:      contentHash,
		ExtractorVersion: extractorVersion,
		RendererVersion:  renderer.Version,
		SnapshotPath:     filepath.Join(relDir, "snapshot.json"),
		PagePath:         filepath.Join(relDir, "page.html"),
		EmbedPath:        filepath.Join(relDir, "embed.html"),
		MessageCount:     snapshot.Metadata.MessageCount,
	}
	if err := s.writeRevisionArtifacts(snapshot, rev, raw); err != nil {
		return nil, err
	}
	if err := s.store.PublishRefresh(ctx, updated, rev); err != nil {
		return nil, &StorageError{Err: err}
	}
	s.purgeStable(context.Background(), snap)
	s.log.Info("snapshot refreshed",
		"snapshot_id", snap.ID, "revision", revisionID)
	return s.resultFor(updated, revisionID, "", false, true), nil
}

func refreshedSnapshotRow(current *storage.Snapshot, snapshot *conversation.ConversationSnapshot, contentHash string) *storage.Snapshot {
	return &storage.Snapshot{
		ID:             current.ID,
		Slug:           current.Slug,
		Title:          snapshot.Title,
		SourceProvider: snapshot.Source.Provider,
		SourceURL:      snapshot.Source.URL,
		SourceShareID:  snapshot.Source.ShareID,
		Visibility:     current.Visibility,
		NoIndex:        current.NoIndex,
		Status:         current.Status,
		ContentHash:    contentHash,
		MessageCount:   snapshot.Metadata.MessageCount,
		CreatedAt:      current.CreatedAt,
		UpdatedAt:      snapshot.UpdatedAt,
	}
}

// Delete marks the snapshot deleted; stable URLs stop serving immediately.
// Revision files remain on disk for the deferred cleanup policy.
func (s *Service) Delete(ctx context.Context, snapshotID string) error {
	snap, err := s.store.GetSnapshotByID(ctx, snapshotID)
	if err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			return &NotFoundError{What: "snapshot"}
		}
		return &StorageError{Err: err}
	}
	if snap.Status == "deleted" {
		return &DeletedError{}
	}
	if err := s.store.MarkDeleted(ctx, snapshotID); err != nil {
		return &StorageError{Err: err}
	}
	s.purgeStable(context.Background(), snap)
	s.log.Info("snapshot deleted", "snapshot_id", snapshotID, "slug", snap.Slug)
	return nil
}

// Preview renders a stored snapshot revision to HTML without changing state.
func (s *Service) Preview(ctx context.Context, snapshotID, kind string) ([]byte, *storage.Snapshot, error) {
	snap, err := s.store.GetSnapshotByID(ctx, snapshotID)
	if err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			return nil, nil, &NotFoundError{What: "snapshot"}
		}
		return nil, nil, &StorageError{Err: err}
	}
	rev, err := s.store.GetRevision(ctx, snap.ID, snap.ActiveRevision)
	if err != nil {
		return nil, nil, &NotFoundError{What: "revision"}
	}
	data, err := s.files.ReadFile(rev.SnapshotPath)
	if err != nil {
		return nil, nil, &StorageError{Err: err}
	}
	var snapshot conversation.ConversationSnapshot
	if err := json.Unmarshal(data, &snapshot); err != nil {
		return nil, nil, &StorageError{Err: err}
	}
	if kind == "embed" {
		htmlBytes, err := s.renderer.RenderEmbed(&snapshot)
		if err != nil {
			return nil, nil, err
		}
		return htmlBytes, snap, nil
	}
	htmlBytes, err := s.renderer.RenderPage(&snapshot)
	if err != nil {
		return nil, nil, err
	}
	return htmlBytes, snap, nil
}

// PreviewImport fetches a share URL and renders it to HTML in memory. Nothing
// is persisted and no revision is created. snapshotID is only used to bind the
// call to an existing snapshot for authorization context.
func (s *Service) PreviewImport(ctx context.Context, snapshotID string, req ImportRequest, kind string) (map[string]any, error) {
	if err := security.ValidateShareURL(req.URL); err != nil {
		return nil, err
	}
	options := req.options(conversation.Options{})
	if _, err := conversation.ResolveLocation(options.Timezone); err != nil {
		return nil, err
	}
	if err := s.acquire(ctx); err != nil {
		return nil, err
	}
	defer func() { <-s.sem }()
	snapshot, _, err := s.buildSnapshot(ctx, req, snapshotID, options)
	if err != nil {
		return nil, err
	}
	if req.Title != "" {
		snapshot.Title = req.Title
	}
	var html []byte
	if kind == "embed" {
		html, err = s.renderer.RenderEmbed(snapshot)
	} else {
		html, err = s.renderer.RenderPage(snapshot)
	}
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"snapshot_id":   snapshotID,
		"kind":          kind,
		"title":         snapshot.Title,
		"message_count": snapshot.Metadata.MessageCount,
		"html":          string(html),
	}, nil
}

func (req ImportRequest) options(defaults conversation.Options) conversation.Options {
	if req.IncludeHidden != nil {
		defaults.IncludeHidden = *req.IncludeHidden
	}
	if req.AllNodes != nil {
		defaults.AllNodes = *req.AllNodes
	}
	if req.Timezone != nil {
		defaults.Timezone = *req.Timezone
	}
	return defaults
}

func (s *Service) activeOptions(ctx context.Context, snap *storage.Snapshot) (conversation.Options, error) {
	rev, err := s.store.GetRevision(ctx, snap.ID, snap.ActiveRevision)
	if err != nil {
		return conversation.Options{}, &StorageError{Err: fmt.Errorf("load active revision: %w", err)}
	}
	data, err := s.files.ReadFile(rev.SnapshotPath)
	if err != nil {
		return conversation.Options{}, &StorageError{Err: fmt.Errorf("read active snapshot: %w", err)}
	}
	var active conversation.ConversationSnapshot
	if err := json.Unmarshal(data, &active); err != nil {
		return conversation.Options{}, &StorageError{Err: fmt.Errorf("decode active snapshot: %w", err)}
	}
	return conversation.Options{
		IncludeHidden: active.Metadata.IncludeHidden,
		AllNodes:      active.Metadata.AllNodes,
		Timezone:      active.Metadata.Timezone,
	}, nil
}

// buildSnapshot fetches, extracts and normalizes a share URL.
func (s *Service) buildSnapshot(ctx context.Context, req ImportRequest, snapshotID string, options conversation.Options) (*conversation.ConversationSnapshot, []byte, error) {
	timeout := s.cfg.FetchTimeout
	if req.TimeoutSeconds > 0 {
		timeout = time.Duration(req.TimeoutSeconds) * time.Second
	}
	if timeout > 5*time.Minute {
		return nil, nil, &conversation.InvalidOptionError{Message: "timeout_seconds must not exceed 300"}
	}
	fetchCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	page, err := s.fetcher.FetchHTML(fetchCtx, req.URL)
	if err != nil {
		return nil, nil, err
	}
	conversationMap, err := extractor.ExtractConversation(page)
	if err != nil {
		return nil, nil, err
	}
	raw, err := json.Marshal(conversationMap)
	if err != nil {
		return nil, nil, &StorageError{Err: fmt.Errorf("encode raw conversation: %w", err)}
	}
	snapshot, err := conversation.Normalize(conversationMap, req.URL, options, time.Now())
	if err != nil {
		return nil, nil, err
	}
	snapshot.ID = snapshotID
	return snapshot, raw, nil
}

// writeRevisionArtifacts renders and persists snapshot.json, page.html and
// embed.html, then the raw payload. Nothing is activated here.
func (s *Service) writeRevisionArtifacts(snapshot *conversation.ConversationSnapshot, rev *storage.Revision, raw []byte) error {
	snapshotJSON, err := json.MarshalIndent(snapshot, "", "  ")
	if err != nil {
		return &StorageError{Err: fmt.Errorf("encode snapshot: %w", err)}
	}
	page, err := s.renderer.RenderPage(snapshot)
	if err != nil {
		return err
	}
	embedHTML, err := s.renderer.RenderEmbed(snapshot)
	if err != nil {
		return err
	}
	err = s.files.WriteRevision(rev.SnapshotID, rev.Revision, storage.RevisionFiles{
		SnapshotJSON: snapshotJSON,
		PageHTML:     page,
		EmbedHTML:    embedHTML,
	})
	if err != nil {
		if errors.Is(err, storage.ErrRevisionExists) {
			return &ConflictError{Message: "revision already exists"}
		}
		return &StorageError{Err: err}
	}
	if err := s.files.WriteRawJSON(rev.SnapshotID, raw); err != nil {
		return &StorageError{Err: fmt.Errorf("write raw.json: %w", err)}
	}
	return nil
}

func (s *Service) uniqueSlug(ctx context.Context, title string) (string, error) {
	base := conversation.Slugify(title)
	slug := base
	for attempt := 0; attempt < 10; attempt++ {
		exists, err := s.store.SlugExists(ctx, slug)
		if err != nil {
			return "", err
		}
		if !exists {
			return slug, nil
		}
		slug = fmt.Sprintf("%s-%s", base, security.NewID("s")[2:8])
	}
	return "", fmt.Errorf("could not allocate unique slug")
}

func (s *Service) resultFor(snap *storage.Snapshot, revision, token string, existing, changed bool) *ImportResult {
	base := s.cfg.PublicBaseURL
	return &ImportResult{
		ID:           snap.ID,
		Slug:         snap.Slug,
		Revision:     revision,
		Title:        snap.Title,
		MessageCount: snap.MessageCount,
		PageURL:      fmt.Sprintf("%s/c/%s", base, snap.Slug),
		EmbedURL:     fmt.Sprintf("%s/e/%s", base, snap.Slug),
		AdminToken:   token,
		Existing:     existing,
		Changed:      changed,
	}
}

func (s *Service) purgeStable(ctx context.Context, snap *storage.Snapshot) {
	urls := []string{
		fmt.Sprintf("%s/c/%s", s.cfg.PublicBaseURL, snap.Slug),
		fmt.Sprintf("%s/e/%s", s.cfg.PublicBaseURL, snap.Slug),
	}
	if err := s.purger.PurgeURLs(ctx, urls); err != nil {
		if errors.Is(err, cache.ErrNotConfigured) {
			return
		}
		s.log.Warn("cache purge failed", "snapshot_id", snap.ID, "error", err)
	}
}

func (s *Service) acquire(ctx context.Context) error {
	select {
	case s.sem <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
