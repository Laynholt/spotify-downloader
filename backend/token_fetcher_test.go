package backend

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
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
