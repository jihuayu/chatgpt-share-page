package storage

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestWriteRevisionAtomic(t *testing.T) {
	dir := t.TempDir()
	fs, err := NewFileStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	files := RevisionFiles{
		SnapshotJSON: []byte(`{"version":1}`),
		PageHTML:     []byte("<html>page</html>"),
		EmbedHTML:    []byte("<html>embed</html>"),
	}
	if err := fs.WriteRevision("s1", "rev1", files); err != nil {
		t.Fatal(err)
	}
	rel := fs.RevisionRelDir("s1", "rev1")
	for _, name := range []string{"snapshot.json", "page.html", "embed.html"} {
		if _, err := os.Stat(filepath.Join(fs.Abs(rel), name)); err != nil {
			t.Errorf("missing %s", name)
		}
	}
	// Temp dir must be gone.
	matches, _ := filepath.Glob(filepath.Join(fs.Abs(rel), "..", ".tmp-*"))
	if len(matches) != 0 {
		t.Errorf("temp dirs left behind: %v", matches)
	}
	// Second write to same revision must not overwrite.
	files.PageHTML = []byte("<html>changed</html>")
	if err := fs.WriteRevision("s1", "rev1", files); !errors.Is(err, ErrRevisionExists) {
		t.Errorf("expected ErrRevisionExists, got %v", err)
	}
	data, err := fs.ReadFile(filepath.Join(rel, "page.html"))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "<html>page</html>" {
		t.Error("existing revision must stay immutable")
	}
}

func TestReadFileRejectsTraversal(t *testing.T) {
	fs, err := NewFileStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fs.ReadFile("../outside"); err == nil {
		t.Error("path traversal must be rejected")
	}
}
