package backend

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestRenameNoReplaceKeepsExistingDestination(t *testing.T) {
	dir := t.TempDir()
	srcPath := filepath.Join(dir, "a.mp3")
	dstPath := filepath.Join(dir, "b.mp3")
	if err := os.WriteFile(srcPath, []byte("a"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dstPath, []byte("b"), 0644); err != nil {
		t.Fatal(err)
	}

	if err := RenameNoReplace(srcPath, dstPath); !errors.Is(err, ErrFileExists) {
		t.Fatalf("expected ErrFileExists, got %v", err)
	}
	if data, _ := os.ReadFile(dstPath); string(data) != "b" {
		t.Fatalf("destination was overwritten: %q", data)
	}
	if _, err := os.Stat(srcPath); err != nil {
		t.Fatalf("source must stay in place: %v", err)
	}

	newPath := filepath.Join(dir, "c.mp3")
	if err := RenameNoReplace(srcPath, newPath); err != nil {
		t.Fatalf("rename to a free name: %v", err)
	}
	if data, _ := os.ReadFile(newPath); string(data) != "a" {
		t.Fatalf("unexpected renamed content: %q", data)
	}
}

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
