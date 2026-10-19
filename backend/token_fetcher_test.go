package backend

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

// Production break caught: token fetching accepts a missing, empty, or
// non-regular portable helper and proceeds toward browser/helper execution.
func TestPortableTokenHelperPathRejectsMissingOrInvalidHelpers(t *testing.T) {
	tests := []struct {
		name  string
		setup func(t *testing.T, path string)
	}{
		{name: "missing"},
		{
			name: "empty",
			setup: func(t *testing.T, path string) {
				t.Helper()
				if err := os.WriteFile(path, nil, 0755); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "directory",
			setup: func(t *testing.T, path string) {
				t.Helper()
				if err := os.Mkdir(path, 0755); err != nil {
					t.Fatal(err)
				}
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			paths, err := ResolveRuntimePaths(filepath.Join(t.TempDir(), "SpotiDownloader.exe"), test.name)
			if err != nil {
				t.Fatal(err)
			}
			if err := paths.Prepare(); err != nil {
				t.Fatal(err)
			}
			installRuntimePathsForTest(t, paths)

			helperPath := filepath.Join(paths.BinDir, tokenHelperName())
			if test.setup != nil {
				test.setup(t, helperPath)
			}

			_, err = portableTokenHelperPath(paths)
			want := fmt.Sprintf("portable helper is missing: %s", helperPath)
			if err == nil || err.Error() != want {
				t.Fatalf("portable helper error = %v, want %q", err, want)
			}
		})
	}
}

func TestFetchSessionTokenWithParamsContextKillsHelperTree(t *testing.T) {
	paths, err := ResolveRuntimePaths(filepath.Join(t.TempDir(), "SpotiDownloader.exe"), "token-tree")
	if err != nil {
		t.Fatal(err)
	}
	if err := paths.Prepare(); err != nil {
		t.Fatal(err)
	}
	installRuntimePathsForTest(t, paths)

	helper, err := os.ReadFile(os.Args[0])
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(paths.BinDir, tokenHelperName()), helper, 0755); err != nil {
		t.Fatal(err)
	}

	chromeDir := t.TempDir()
	chromeName := "google-chrome"
	if runtime.GOOS == "windows" {
		chromeName = "chrome.exe"
	}
	if err := os.WriteFile(filepath.Join(chromeDir, chromeName), helper, 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", chromeDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	marker := filepath.Join(t.TempDir(), "token-descendant-started")
	release := filepath.Join(t.TempDir(), "token-descendant-release")
	survived := filepath.Join(t.TempDir(), "token-descendant-survived")
	t.Setenv(testHelperModeEnv, "managed-parent")
	t.Setenv(testHelperMarker, marker)
	t.Setenv(testHelperRelease, release)
	t.Setenv("SPOTI_DOWNLOAD_TEST_HELPER_SURVIVED", survived)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, fetchErr := FetchSessionTokenWithParamsContext(ctx, 15, 0)
		done <- fetchErr
	}()
	if err := waitForFile(marker, helperProcessTimeout); err != nil {
		cancel()
		t.Fatal("token helper descendant did not start:", err)
	}
	cancel()

	select {
	case fetchErr := <-done:
		if !errors.Is(fetchErr, context.Canceled) {
			t.Fatalf("token fetch cancellation error = %v, want context.Canceled", fetchErr)
		}
	case <-time.After(helperProcessTimeout):
		t.Fatal("token helper cancellation exceeded bounded wait")
	}
	if err := os.WriteFile(release, []byte("release"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := waitForFile(survived, 300*time.Millisecond); err == nil {
		t.Fatal("token helper descendant survived cancellation")
	}
}
