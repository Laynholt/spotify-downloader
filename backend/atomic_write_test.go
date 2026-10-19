package backend

import (
	"os"
	"path/filepath"
	"testing"
)

func TestAtomicWriteFileReplacesAndCleansFailedWrite(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	if err := os.WriteFile(path, []byte("old"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := AtomicWriteFile(path, []byte("new"), 0600); err != nil {
		t.Fatal(err)
	}
	if data, err := os.ReadFile(path); err != nil || string(data) != "new" {
		t.Fatalf("config = %q, %v", data, err)
	}
	blocked := filepath.Join(dir, "directory")
	if err := os.Mkdir(blocked, 0700); err != nil {
		t.Fatal(err)
	}
	if err := AtomicWriteFile(blocked, []byte("bad"), 0600); err == nil {
		t.Fatal("replacement of directory succeeded")
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 2 {
		t.Fatalf("temporary file leaked: %v, %v", entries, err)
	}
}
