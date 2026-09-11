package storage

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/jihuayu/chatgpt-share-page/internal/security"
)

// ErrRevisionExists is returned when a revision directory already exists.
var ErrRevisionExists = errors.New("revision already exists")

// FileStore owns the on-disk artifact layout under a data directory:
//
//	<root>/conversations/<snapshot-id>/raw.json
//	<root>/conversations/<snapshot-id>/revisions/<rev>/{snapshot,page,embed}.{json,html}
type FileStore struct {
	root string
}

// NewFileStore creates a file store rooted at dir.
func NewFileStore(root string) (*FileStore, error) {
	if err := os.MkdirAll(filepath.Join(root, "conversations"), 0o755); err != nil {
		return nil, fmt.Errorf("create data dir: %w", err)
	}
	return &FileStore{root: root}, nil
}

// Root returns the data directory root.
func (f *FileStore) Root() string { return f.root }

func (f *FileStore) conversationDir(snapshotID string) string {
	return filepath.Join(f.root, "conversations", snapshotID)
}

// RevisionDir returns the absolute directory for one revision.
func (f *FileStore) RevisionDir(snapshotID, revision string) string {
	return filepath.Join(f.conversationDir(snapshotID), "revisions", revision)
}

// RevisionRelDir returns the revision directory relative to root; this is the
// form stored in the database.
func (f *FileStore) RevisionRelDir(snapshotID, revision string) string {
	return filepath.Join("conversations", snapshotID, "revisions", revision)
}

// RawRelPath is the raw payload path relative to root.
func (f *FileStore) RawRelPath(snapshotID string) string {
	return filepath.Join("conversations", snapshotID, "raw.json")
}

// Abs resolves a root-relative path stored in the database.
func (f *FileStore) Abs(rel string) string {
	return filepath.Join(f.root, rel)
}

// WriteRawJSON atomically stores the raw extracted conversation payload.
func (f *FileStore) WriteRawJSON(snapshotID string, raw []byte) error {
	dir := f.conversationDir(snapshotID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	return writeFileAtomic(filepath.Join(dir, "raw.json"), raw, 0o644)
}

// RevisionFiles is the set of artifacts written for one revision.
type RevisionFiles struct {
	SnapshotJSON []byte
	PageHTML     []byte
	EmbedHTML    []byte
}

// WriteRevision writes all revision artifacts into a temporary directory and
// atomically renames it into place. A pre-existing revision directory is left
// untouched and reported as ErrRevisionExists.
func (f *FileStore) WriteRevision(snapshotID, revision string, files RevisionFiles) error {
	revisionsDir := filepath.Join(f.conversationDir(snapshotID), "revisions")
	finalDir := f.RevisionDir(snapshotID, revision)
	if _, err := os.Stat(finalDir); err == nil {
		return ErrRevisionExists
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := os.MkdirAll(revisionsDir, 0o755); err != nil {
		return err
	}
	tmpDir := filepath.Join(revisionsDir, ".tmp-"+revision+"-"+security.NewID("w")[2:10])
	if err := os.MkdirAll(tmpDir, 0o755); err != nil {
		return err
	}
	cleanup := true
	defer func() {
		if cleanup {
			os.RemoveAll(tmpDir)
		}
	}()
	artifacts := map[string][]byte{
		"snapshot.json": files.SnapshotJSON,
		"page.html":     files.PageHTML,
		"embed.html":    files.EmbedHTML,
	}
	for name, data := range artifacts {
		if len(data) == 0 {
			return fmt.Errorf("empty artifact %s", name)
		}
		if err := writeFileAtomic(filepath.Join(tmpDir, name), data, 0o644); err != nil {
			return fmt.Errorf("write %s: %w", name, err)
		}
	}
	if err := os.Rename(tmpDir, finalDir); err != nil {
		return fmt.Errorf("activate revision dir: %w", err)
	}
	cleanup = false
	return nil
}

// ReadFile reads a root-relative artifact path.
func (f *FileStore) ReadFile(rel string) ([]byte, error) {
	if filepath.IsAbs(rel) || strings.HasPrefix(rel, "..") {
		return nil, fmt.Errorf("invalid artifact path %q", rel)
	}
	return os.ReadFile(f.Abs(rel))
}

// WriteFileAtomic persists data at a root-relative path (fsync + rename).
func (f *FileStore) WriteFileAtomic(rel string, data []byte) error {
	if filepath.IsAbs(rel) || strings.HasPrefix(rel, "..") {
		return fmt.Errorf("invalid artifact path %q", rel)
	}
	return writeFileAtomic(f.Abs(rel), data, 0o644)
}

// writeFileAtomic writes data to a sibling temp file, fsyncs, then renames.
func writeFileAtomic(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".tmp-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmpName, perm); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
}
