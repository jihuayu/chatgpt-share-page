// Package storage persists snapshot metadata in SQLite and snapshot
// artifacts on the local filesystem.
package storage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite"
)

// Snapshot is one row of the snapshots table.
type Snapshot struct {
	ID             string
	Slug           string
	Title          string
	SourceProvider string
	SourceURL      string
	SourceShareID  string
	Visibility     string
	NoIndex        bool
	Status         string // active | deleted
	ActiveRevision string
	ContentHash    string
	MessageCount   int
	CreatedAt      time.Time
	UpdatedAt      time.Time
	DeletedAt      *time.Time
}

// Revision is one row of snapshot_revisions.
type Revision struct {
	SnapshotID       string
	Revision         string
	ContentHash      string
	ExtractorVersion string
	RendererVersion  string
	SnapshotPath     string
	PagePath         string
	EmbedPath        string
	CreatedAt        time.Time
	// MessageCount is written back to the snapshots table on activation;
	// it is not stored per revision.
	MessageCount int
}

// ErrNotFound is returned when a row does not exist.
var ErrNotFound = errors.New("not found")

// Store wraps the SQLite database.
type Store struct {
	db *sql.DB
}

// Open opens (and creates) the SQLite database, applying migrations.
func Open(path string) (*Store, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("create database dir: %w", err)
	}
	dsn := fmt.Sprintf("file:%s?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)", filepath.ToSlash(path))
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}
	// Single-writer SQLite: keep the pool small and serialized.
	db.SetMaxOpenConns(1)
	store := &Store{db: db}
	if err := store.migrate(); err != nil {
		db.Close()
		return nil, err
	}
	return store, nil
}

// Close closes the underlying database.
func (s *Store) Close() error { return s.db.Close() }

const timeFormat = time.RFC3339Nano

func formatTime(t time.Time) string { return t.UTC().Format(timeFormat) }

func parseTime(text string) time.Time {
	parsed, err := time.Parse(timeFormat, text)
	if err != nil {
		return time.Time{}
	}
	return parsed
}

func parseTimePtr(text sql.NullString) *time.Time {
	if !text.Valid || text.String == "" {
		return nil
	}
	parsed := parseTime(text.String)
	return &parsed
}

// SlugExists reports whether a slug is already taken.
func (s *Store) SlugExists(ctx context.Context, slug string) (bool, error) {
	var one int
	err := s.db.QueryRowContext(ctx, `SELECT 1 FROM snapshots WHERE slug = ?`, slug).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

// GetSnapshotByID loads a snapshot by primary key.
func (s *Store) GetSnapshotByID(ctx context.Context, id string) (*Snapshot, error) {
	return s.scanSnapshot(s.db.QueryRowContext(ctx,
		`SELECT id, slug, title, source_provider, source_url, source_share_id,
		        visibility, noindex, status, COALESCE(active_revision,''), content_hash, message_count,
		        created_at, updated_at, deleted_at
		 FROM snapshots WHERE id = ?`, id))
}

// GetSnapshotBySlug loads a snapshot by public slug.
func (s *Store) GetSnapshotBySlug(ctx context.Context, slug string) (*Snapshot, error) {
	return s.scanSnapshot(s.db.QueryRowContext(ctx,
		`SELECT id, slug, title, source_provider, source_url, source_share_id,
		        visibility, noindex, status, COALESCE(active_revision,''), content_hash, message_count,
		        created_at, updated_at, deleted_at
		 FROM snapshots WHERE slug = ?`, slug))
}

// GetSnapshotByShareID loads a snapshot by its source share identifier.
func (s *Store) GetSnapshotByShareID(ctx context.Context, provider, shareID string) (*Snapshot, error) {
	return s.scanSnapshot(s.db.QueryRowContext(ctx,
		`SELECT id, slug, title, source_provider, source_url, source_share_id,
		        visibility, noindex, status, COALESCE(active_revision,''), content_hash, message_count,
		        created_at, updated_at, deleted_at
		 FROM snapshots WHERE source_provider = ? AND source_share_id = ?`, provider, shareID))
}

func (s *Store) scanSnapshot(row *sql.Row) (*Snapshot, error) {
	var snap Snapshot
	var noindex int
	var createdAt, updatedAt string
	var deletedAt sql.NullString
	err := row.Scan(&snap.ID, &snap.Slug, &snap.Title, &snap.SourceProvider,
		&snap.SourceURL, &snap.SourceShareID, &snap.Visibility, &noindex,
		&snap.Status, &snap.ActiveRevision, &snap.ContentHash, &snap.MessageCount,
		&createdAt, &updatedAt, &deletedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	snap.NoIndex = noindex != 0
	snap.CreatedAt = parseTime(createdAt)
	snap.UpdatedAt = parseTime(updatedAt)
	snap.DeletedAt = parseTimePtr(deletedAt)
	return &snap, nil
}

// GetRevision loads one revision row.
func (s *Store) GetRevision(ctx context.Context, snapshotID, revision string) (*Revision, error) {
	var rev Revision
	var createdAt string
	err := s.db.QueryRowContext(ctx,
		`SELECT snapshot_id, revision, content_hash, extractor_version, renderer_version,
		        snapshot_path, page_path, embed_path, created_at
		 FROM snapshot_revisions WHERE snapshot_id = ? AND revision = ?`,
		snapshotID, revision).
		Scan(&rev.SnapshotID, &rev.Revision, &rev.ContentHash, &rev.ExtractorVersion,
			&rev.RendererVersion, &rev.SnapshotPath, &rev.PagePath, &rev.EmbedPath, &createdAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	rev.CreatedAt = parseTime(createdAt)
	return &rev, nil
}

// PublishRevision atomically records a revision and makes it active. When
// snap is non-nil the snapshot row is inserted first (new import).
func (s *Store) PublishRevision(ctx context.Context, snap *Snapshot, rev *Revision, tokenHash string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	now := formatTime(time.Now())
	if snap != nil {
		var deletedAt sql.NullString
		if snap.DeletedAt != nil {
			deletedAt = sql.NullString{String: formatTime(*snap.DeletedAt), Valid: true}
		}
		noindex := 0
		if snap.NoIndex {
			noindex = 1
		}
		_, err = tx.ExecContext(ctx,
			`INSERT INTO snapshots (id, slug, title, source_provider, source_url, source_share_id,
				visibility, noindex, status, active_revision, content_hash, message_count,
				created_at, updated_at, deleted_at)
			 VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
			snap.ID, snap.Slug, snap.Title, snap.SourceProvider, snap.SourceURL, snap.SourceShareID,
			snap.Visibility, noindex, snap.Status, rev.Revision, rev.ContentHash, snap.MessageCount,
			formatTime(snap.CreatedAt), now, deletedAt)
		if err != nil {
			return fmt.Errorf("insert snapshot: %w", err)
		}
		if tokenHash != "" {
			_, err = tx.ExecContext(ctx,
				`INSERT INTO snapshot_admin_tokens (snapshot_id, token_hash, created_at) VALUES (?,?,?)`,
				snap.ID, tokenHash, now)
			if err != nil {
				return fmt.Errorf("insert admin token: %w", err)
			}
		}
	}
	_, err = tx.ExecContext(ctx,
		`INSERT INTO snapshot_revisions (snapshot_id, revision, content_hash, extractor_version,
			renderer_version, snapshot_path, page_path, embed_path, created_at)
		 VALUES (?,?,?,?,?,?,?,?,?)`,
		rev.SnapshotID, rev.Revision, rev.ContentHash, rev.ExtractorVersion, rev.RendererVersion,
		rev.SnapshotPath, rev.PagePath, rev.EmbedPath, now)
	if err != nil {
		return fmt.Errorf("insert revision: %w", err)
	}
	res, err := tx.ExecContext(ctx,
		`UPDATE snapshots SET active_revision = ?, content_hash = ?, message_count = ?, updated_at = ?
		 WHERE id = ? AND status = 'active'`,
		rev.Revision, rev.ContentHash, rev.MessageCount, now, rev.SnapshotID)
	if err != nil {
		return fmt.Errorf("activate revision: %w", err)
	}
	if affected, _ := res.RowsAffected(); affected == 0 {
		return fmt.Errorf("activate revision: snapshot %s not active", rev.SnapshotID)
	}
	return tx.Commit()
}

// ActivateRevision points the snapshot at an already-recorded revision.
func (s *Store) ActivateRevision(ctx context.Context, snap *Snapshot, revision, contentHash string, messageCount int) error {
	res, err := s.db.ExecContext(ctx,
		`UPDATE snapshots SET title = ?, source_provider = ?, source_url = ?, source_share_id = ?,
			active_revision = ?, content_hash = ?, message_count = ?, updated_at = ?
		 WHERE id = ? AND status = 'active'`,
		snap.Title, snap.SourceProvider, snap.SourceURL, snap.SourceShareID,
		revision, contentHash, messageCount, formatTime(time.Now()), snap.ID)
	if err != nil {
		return err
	}
	if affected, _ := res.RowsAffected(); affected == 0 {
		return ErrNotFound
	}
	return nil
}

// PublishRefresh records a new revision and atomically updates the mutable
// snapshot metadata that was derived from the refreshed source.
func (s *Store) PublishRefresh(ctx context.Context, snap *Snapshot, rev *Revision) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	now := formatTime(time.Now())
	_, err = tx.ExecContext(ctx,
		`INSERT INTO snapshot_revisions (snapshot_id, revision, content_hash, extractor_version,
			renderer_version, snapshot_path, page_path, embed_path, created_at)
		 VALUES (?,?,?,?,?,?,?,?,?)`,
		rev.SnapshotID, rev.Revision, rev.ContentHash, rev.ExtractorVersion, rev.RendererVersion,
		rev.SnapshotPath, rev.PagePath, rev.EmbedPath, now)
	if err != nil {
		return fmt.Errorf("insert revision: %w", err)
	}
	res, err := tx.ExecContext(ctx,
		`UPDATE snapshots SET title = ?, source_provider = ?, source_url = ?, source_share_id = ?,
			active_revision = ?, content_hash = ?, message_count = ?, updated_at = ?
		 WHERE id = ? AND status = 'active'`,
		snap.Title, snap.SourceProvider, snap.SourceURL, snap.SourceShareID,
		rev.Revision, rev.ContentHash, rev.MessageCount, now, snap.ID)
	if err != nil {
		return fmt.Errorf("activate revision: %w", err)
	}
	if affected, _ := res.RowsAffected(); affected == 0 {
		return fmt.Errorf("activate revision: snapshot %s not active", snap.ID)
	}
	return tx.Commit()
}

// MarkDeleted flags a snapshot as deleted; revision rows remain for audit.
func (s *Store) MarkDeleted(ctx context.Context, snapshotID string) error {
	res, err := s.db.ExecContext(ctx,
		`UPDATE snapshots SET status = 'deleted', deleted_at = ?, updated_at = ?
		 WHERE id = ? AND status != 'deleted'`,
		formatTime(time.Now()), formatTime(time.Now()), snapshotID)
	if err != nil {
		return err
	}
	if affected, _ := res.RowsAffected(); affected == 0 {
		return ErrNotFound
	}
	return nil
}

// SaveTokenHash stores the admin token hash for a snapshot.
func (s *Store) SaveTokenHash(ctx context.Context, snapshotID, tokenHash string) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO snapshot_admin_tokens (snapshot_id, token_hash, created_at) VALUES (?,?,?)
		 ON CONFLICT(snapshot_id) DO UPDATE SET token_hash = excluded.token_hash, revoked_at = NULL`,
		snapshotID, tokenHash, formatTime(time.Now()))
	return err
}

// TokenHash returns the stored admin token hash for a snapshot.
func (s *Store) TokenHash(ctx context.Context, snapshotID string) (string, error) {
	var hash string
	err := s.db.QueryRowContext(ctx,
		`SELECT token_hash FROM snapshot_admin_tokens WHERE snapshot_id = ? AND revoked_at IS NULL`,
		snapshotID).Scan(&hash)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNotFound
	}
	if err != nil {
		return "", err
	}
	return hash, nil
}
