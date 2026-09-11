package storage

import "fmt"

// migrate applies the phase-1 schema. Migrations are tracked via
// PRAGMA user_version and run inside a transaction.
func (s *Store) migrate() error {
	var version int
	if err := s.db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil {
		return fmt.Errorf("read user_version: %w", err)
	}
	if version >= 1 {
		return nil
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	statements := []string{
		`CREATE TABLE IF NOT EXISTS snapshots (
			id              TEXT PRIMARY KEY,
			slug            TEXT NOT NULL UNIQUE,
			title           TEXT NOT NULL,
			source_provider TEXT NOT NULL,
			source_url      TEXT NOT NULL,
			source_share_id TEXT NOT NULL,
			visibility      TEXT NOT NULL DEFAULT 'unlisted',
			noindex         INTEGER NOT NULL DEFAULT 1,
			status          TEXT NOT NULL DEFAULT 'active',
			active_revision TEXT,
			content_hash    TEXT NOT NULL,
			message_count   INTEGER NOT NULL DEFAULT 0,
			created_at      TEXT NOT NULL,
			updated_at      TEXT NOT NULL,
			deleted_at      TEXT
		)`,
		`CREATE TABLE IF NOT EXISTS snapshot_revisions (
			snapshot_id       TEXT NOT NULL,
			revision          TEXT NOT NULL,
			content_hash      TEXT NOT NULL,
			extractor_version TEXT NOT NULL,
			renderer_version  TEXT NOT NULL,
			snapshot_path     TEXT NOT NULL,
			page_path         TEXT NOT NULL,
			embed_path        TEXT NOT NULL,
			created_at        TEXT NOT NULL,
			PRIMARY KEY (snapshot_id, revision),
			FOREIGN KEY (snapshot_id) REFERENCES snapshots(id)
		)`,
		`CREATE TABLE IF NOT EXISTS snapshot_admin_tokens (
			snapshot_id TEXT PRIMARY KEY,
			token_hash  TEXT NOT NULL,
			created_at  TEXT NOT NULL,
			revoked_at  TEXT,
			FOREIGN KEY (snapshot_id) REFERENCES snapshots(id)
		)`,
		`CREATE INDEX IF NOT EXISTS idx_snapshots_share ON snapshots(source_provider, source_share_id)`,
		`PRAGMA user_version = 1`,
	}
	for _, statement := range statements {
		if _, err := tx.Exec(statement); err != nil {
			return fmt.Errorf("migration failed: %w", err)
		}
	}
	return tx.Commit()
}
