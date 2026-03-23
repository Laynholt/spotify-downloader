package backend

import (
	"os"
	"path/filepath"
	"testing"
)

func TestMoveFileWithFallbackMovesContents(t *testing.T) {
	dir := t.TempDir()
	srcPath := filepath.Join(dir, "source.mp3")
	dstPath := filepath.Join(dir, "renamed.mp3")

	if err := os.WriteFile(srcPath, []byte("content"), 0644); err != nil {
		t.Fatalf("write source file: %v", err)
	}

	if err := MoveFileWithFallback(srcPath, dstPath); err != nil {
		t.Fatalf("move file with fallback: %v", err)
	}

	if _, err := os.Stat(srcPath); !os.IsNotExist(err) {
		t.Fatalf("expected source file to be removed, got err=%v", err)
	}

	data, err := os.ReadFile(dstPath)
	if err != nil {
		t.Fatalf("read destination file: %v", err)
	}
	if string(data) != "content" {
		t.Fatalf("unexpected destination content: %q", string(data))
	}
}
