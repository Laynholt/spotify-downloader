package backend

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Production break caught: runtime directories are derived from the current
// working directory or omit one of the portable cache/run path components.
func TestResolveRuntimePathsUsesExecutableDirectory(t *testing.T) {
	exe := filepath.Join(t.TempDir(), "portable", "SpotiDownloader.exe")
	paths, err := ResolveRuntimePaths(exe, "run-42")
	if err != nil {
		t.Fatal(err)
	}
	wantRoot := filepath.Join(filepath.Dir(exe), "SpotiDownloader.runtime")
	if paths.RootDir != wantRoot {
		t.Fatalf("RootDir = %q, want %q", paths.RootDir, wantRoot)
	}
	if paths.BinDir != filepath.Join(wantRoot, "bin") {
		t.Fatal(paths.BinDir)
	}
	if paths.MetadataCacheFile != filepath.Join(wantRoot, "cache", "metadata-cache.json") {
		t.Fatal(paths.MetadataCacheFile)
	}
	if paths.RunTempDir != filepath.Join(wantRoot, "temp", "run-42") {
		t.Fatal(paths.RunTempDir)
	}
}

// Production break caught: startup accepts a runtime root that is not a
// writable directory instead of surfacing the portable-directory failure.
func TestPrepareRuntimePathsReturnsPortableWriteError(t *testing.T) {
	paths, _ := ResolveRuntimePaths(filepath.Join(t.TempDir(), "SpotiDownloader.exe"), "run")
	if err := os.WriteFile(paths.RootDir, []byte("blocks-directory"), 0600); err != nil {
		t.Fatal(err)
	}
	err := paths.Prepare()
	if err == nil || !strings.Contains(err.Error(), "portable runtime directory is not writable") {
		t.Fatalf("unexpected error: %v", err)
	}
}
