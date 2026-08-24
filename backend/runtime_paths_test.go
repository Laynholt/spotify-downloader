package backend

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func installRuntimePathsForTest(t *testing.T, paths RuntimePaths) {
	t.Helper()

	runtimePathsState.RLock()
	previousPaths := runtimePathsState.paths
	previousInitialized := runtimePathsState.initialized
	previousErr := runtimePathsState.initErr
	runtimePathsState.RUnlock()

	runtimePathsState.Lock()
	runtimePathsState.paths = paths
	runtimePathsState.initialized = true
	runtimePathsState.initErr = nil
	runtimePathsState.Unlock()

	t.Cleanup(func() {
		runtimePathsState.Lock()
		runtimePathsState.paths = previousPaths
		runtimePathsState.initialized = previousInitialized
		runtimePathsState.initErr = previousErr
		runtimePathsState.Unlock()
	})
}

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
	if paths.DenoCacheDir != filepath.Join(wantRoot, "cache", "deno") {
		t.Fatal(paths.DenoCacheDir)
	}
	if paths.RunTempDir != filepath.Join(wantRoot, "temp", "run-42") {
		t.Fatal(paths.RunTempDir)
	}
}

// Production break caught: startup leaves DENO_DIR pointing at a cache path
// that was never created inside the portable runtime.
func TestPrepareRuntimePathsCreatesDenoCacheDirectory(t *testing.T) {
	paths, err := ResolveRuntimePaths(filepath.Join(t.TempDir(), "SpotiDownloader.exe"), "run")
	if err != nil {
		t.Fatal(err)
	}
	if err := paths.Prepare(); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(paths.DenoCacheDir)
	if err != nil || !info.IsDir() {
		t.Fatalf("DenoCacheDir was not prepared: info=%v err=%v", info, err)
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

// Production break caught: runtime-owned temporary files fall back to the
// process/system temporary directory instead of the active portable run.
func TestRuntimeTempFilesStayBelowPortableRuntime(t *testing.T) {
	paths, err := ResolveRuntimePaths(filepath.Join(t.TempDir(), "SpotiDownloader.exe"), "run")
	if err != nil {
		t.Fatal(err)
	}
	if err := paths.Prepare(); err != nil {
		t.Fatal(err)
	}
	installRuntimePathsForTest(t, paths)

	runFile, err := CreateRunTemp("cover-*.jpg")
	if err != nil {
		t.Fatal(err)
	}
	defer runFile.Close()
	if filepath.Dir(runFile.Name()) != paths.RunTempDir {
		t.Fatalf("run temp path: %s", runFile.Name())
	}

	updatePath, err := CreateUpdateTempPath("spotidownloader-update-test.sh")
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Dir(updatePath) != paths.TempDir {
		t.Fatalf("update temp path: %s", updatePath)
	}
}

// Production break caught: startup leaves stale updater and abandoned-run
// artifacts behind, or removes the run directory selected for this process.
func TestRuntimeStaleCleanupKeepsCurrentRun(t *testing.T) {
	paths, err := ResolveRuntimePaths(filepath.Join(t.TempDir(), "SpotiDownloader.exe"), "current-run")
	if err != nil {
		t.Fatal(err)
	}
	if err := paths.Prepare(); err != nil {
		t.Fatal(err)
	}

	staleRun := filepath.Join(paths.TempDir, "abandoned-run")
	staleMount := filepath.Join(paths.TempDir, "spotidownloader-update-mount-old")
	staleScript := filepath.Join(paths.TempDir, "spotidownloader-update-old.sh")
	for _, dir := range []string{staleRun, staleMount} {
		if err := os.MkdirAll(dir, 0755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(staleScript, []byte("exit 0\n"), 0700); err != nil {
		t.Fatal(err)
	}
	currentFile := filepath.Join(paths.RunTempDir, "keep.txt")
	if err := os.WriteFile(currentFile, []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}

	staleAt := time.Now().Add(-48 * time.Hour)
	for _, target := range []string{staleRun, staleMount, staleScript} {
		if err := os.Chtimes(target, staleAt, staleAt); err != nil {
			t.Fatal(err)
		}
	}

	if err := paths.Prepare(); err != nil {
		t.Fatal(err)
	}
	for _, target := range []string{staleRun, staleMount, staleScript} {
		if _, err := os.Stat(target); !os.IsNotExist(err) {
			t.Fatalf("stale runtime artifact still exists (%s): %v", target, err)
		}
	}
	if _, err := os.Stat(currentFile); err != nil {
		t.Fatalf("current run was removed: %v", err)
	}
}
