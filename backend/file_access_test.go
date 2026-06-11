package backend

import (
	"os"
	"path/filepath"
	"testing"
)

func TestValidateReadableTextFileAllowsLyricsOnly(t *testing.T) {
	dir := t.TempDir()
	lrcPath := filepath.Join(dir, "song.lrc")
	if err := os.WriteFile(lrcPath, []byte("[00:01] lyric"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := ValidateReadableTextFile(lrcPath); err != nil {
		t.Fatalf("expected .lrc to be readable: %v", err)
	}

	secretPath := filepath.Join(dir, "secret.env")
	if err := os.WriteFile(secretPath, []byte("TOKEN=secret"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := ValidateReadableTextFile(secretPath); err == nil {
		t.Fatalf("expected unsupported text extension to be rejected")
	}
}

func TestBuildSiblingRenamePathRejectsPathTraversal(t *testing.T) {
	oldPath := filepath.Join(t.TempDir(), "track.lrc")
	if _, err := BuildSiblingRenamePath(oldPath, ".."+string(filepath.Separator)+"escape"); err == nil {
		t.Fatalf("expected path separators in new name to be rejected")
	}
}
