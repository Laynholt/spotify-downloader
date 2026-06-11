package backend

import (
	"os"
	"path/filepath"
	"testing"
)

func TestListDirectoryIsShallow(t *testing.T) {
	root := t.TempDir()
	childDir := filepath.Join(root, "album")
	if err := os.Mkdir(childDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(childDir, "track.mp3"), []byte("audio"), 0644); err != nil {
		t.Fatal(err)
	}

	files, err := ListDirectory(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 1 {
		t.Fatalf("expected one top-level entry, got %d", len(files))
	}
	if !files[0].IsDir {
		t.Fatalf("expected top-level entry to be a directory")
	}
	if len(files[0].Children) != 0 {
		t.Fatalf("expected shallow listing without eager children, got %d children", len(files[0].Children))
	}
	if files[0].TrackCount != 1 {
		t.Fatalf("expected track count to include nested audio files, got %d", files[0].TrackCount)
	}
}

func TestListDirectoryCountsMediaByType(t *testing.T) {
	root := t.TempDir()
	childDir := filepath.Join(root, "album")
	nestedDir := filepath.Join(childDir, "disc1")
	if err := os.MkdirAll(nestedDir, 0755); err != nil {
		t.Fatal(err)
	}
	for name := range map[string]bool{
		"track.flac": true,
		"cover.png":  true,
		"lyrics.lrc": true,
		"notes.md":   true,
	} {
		if err := os.WriteFile(filepath.Join(nestedDir, name), []byte("data"), 0644); err != nil {
			t.Fatal(err)
		}
	}

	files, err := ListDirectory(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 1 {
		t.Fatalf("expected one top-level entry, got %d", len(files))
	}
	if files[0].TrackCount != 1 || files[0].LyricCount != 1 || files[0].CoverCount != 1 {
		t.Fatalf("unexpected counts: track=%d lyric=%d cover=%d", files[0].TrackCount, files[0].LyricCount, files[0].CoverCount)
	}
}
