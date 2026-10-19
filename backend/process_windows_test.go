//go:build windows

package backend

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// A failing or stalled taskkill must still stop the actual helper tree.
func TestWindowsCancellationFallbackKillsDescendants(t *testing.T) {
	for _, mode := range []string{"missing", "failed", "timeout"} {
		t.Run(mode, func(t *testing.T) {
			dir := t.TempDir()
			marker, release, survived := filepath.Join(dir, "started"), filepath.Join(dir, "release"), filepath.Join(dir, "survived")
			cmd := exec.Command(os.Args[0], "-test.run=TestManagedCommandHelperProcess")
			cmd.Env = helperEnvironment(map[string]string{testHelperModeEnv: "managed-parent", testHelperMarker: marker, testHelperRelease: release, "SPOTI_DOWNLOAD_TEST_HELPER_SURVIVED": survived})
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = cmd.Process.Kill(); _ = os.WriteFile(release, []byte("release"), 0600) })
			if err := waitForFile(marker, 5*time.Second); err != nil {
				t.Fatal(err)
			}
			killer := exec.Command(filepath.Join(dir, "missing-taskkill.exe"))
			if mode == "failed" {
				killer = exec.Command("cmd.exe", "/c", "exit", "1")
			}
			if mode == "timeout" {
				ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
				defer cancel()
				killer = exec.CommandContext(ctx, os.Args[0], "-version")
				killer.Env = helperEnvironment(map[string]string{testHelperModeEnv: "fake-ffmpeg", testHelperVersionMarker: filepath.Join(dir, "killer")})
			}
			if err := terminateWindowsProcessTree(cmd.Process, killer); err != nil {
				t.Fatal(err)
			}
			if err := cmd.Wait(); err == nil {
				t.Fatal("canceled parent exited successfully")
			}
			if err := os.WriteFile(release, []byte("release"), 0600); err != nil {
				t.Fatal(err)
			}
			if err := waitForFile(survived, 300*time.Millisecond); err == nil {
				t.Fatal("fallback left descendant alive")
			}
		})
	}
}
