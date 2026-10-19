package backend

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// Catches ignoring a working PATH pair, accepting broken executables, and
// combining a partial portable installation with a different PATH pair.
func TestFFmpegResolverChoosesWorkingPair(t *testing.T) {
	for _, portable := range []string{"missing", "partial", "broken", "working"} {
		t.Run(portable, func(t *testing.T) {
			paths := installFakeFFmpegRuntime(t)
			pathDir := t.TempDir()
			for _, name := range []string{"ffmpeg", "ffprobe"} {
				if runtime.GOOS == "windows" {
					name += ".exe"
				}
				data, err := os.ReadFile(filepath.Join(paths.BinDir, name))
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(pathDir, name), data, 0755); err != nil {
					t.Fatal(err)
				}
				if portable == "missing" || portable == "partial" && (name == "ffprobe" || name == "ffprobe.exe") {
					if err := os.Remove(filepath.Join(paths.BinDir, name)); err != nil {
						t.Fatal(err)
					}
				} else if portable == "broken" {
					if err := os.WriteFile(filepath.Join(paths.BinDir, name), []byte("broken"), 0755); err != nil {
						t.Fatal(err)
					}
				}
			}
			t.Setenv("PATH", pathDir)
			wantDir := pathDir
			wantSource := "path"
			if portable == "working" {
				wantDir = paths.BinDir
				wantSource = "portable"
			}
			status := GetFFmpegStatus()
			if !status.Installed || status.Source != wantSource || status.Error != "" {
				t.Fatalf("status = %+v, want working %s pair", status, wantSource)
			}
			for _, get := range []func() (string, error){GetFFmpegPath, GetFFprobePath} {
				got, err := get()
				if err != nil || filepath.Dir(got) != wantDir {
					t.Fatalf("resolved %q, %v; want pair in %s", got, err, wantDir)
				}
			}
		})
	}
}

// yt-dlp receives FFmpeg's directory, so independently found PATH binaries
// must not qualify as a usable pair when FFprobe is absent from that directory.
func TestFFmpegResolverRequiresPATHSibling(t *testing.T) {
	for _, sibling := range []bool{false, true} {
		t.Run(fmt.Sprintf("sibling-%t", sibling), func(t *testing.T) {
			paths := installFakeFFmpegRuntime(t)
			ffmpegDir, probeDir := t.TempDir(), t.TempDir()
			for _, name := range []string{"ffmpeg", "ffprobe"} {
				if runtime.GOOS == "windows" {
					name += ".exe"
				}
				data, err := os.ReadFile(filepath.Join(paths.BinDir, name))
				if err != nil {
					t.Fatal(err)
				}
				dir := ffmpegDir
				if strings.HasPrefix(name, "ffprobe") {
					dir = probeDir
				}
				if err := os.WriteFile(filepath.Join(dir, name), data, 0755); err != nil {
					t.Fatal(err)
				}
				if sibling && strings.HasPrefix(name, "ffprobe") {
					if err := os.WriteFile(filepath.Join(ffmpegDir, name), data, 0755); err != nil {
						t.Fatal(err)
					}
				}
				if err := os.Remove(filepath.Join(paths.BinDir, name)); err != nil {
					t.Fatal(err)
				}
			}
			t.Setenv("PATH", probeDir+string(os.PathListSeparator)+ffmpegDir)
			status := GetFFmpegStatus()
			if sibling {
				if !status.Installed || filepath.Dir(status.FFprobePath) != ffmpegDir {
					t.Fatalf("sibling not selected: %+v", status)
				}
			} else if status.Installed || !strings.Contains(status.Error, "beside FFmpeg") {
				t.Fatalf("split PATH pair accepted or unexplained: %+v", status)
			}
		})
	}
}

func TestFFmpegResolverRejectsMissingPair(t *testing.T) {
	installRuntimePathsForCancellationTest(t)
	t.Setenv("PATH", t.TempDir())
	if got, err := GetFFmpegPath(); err == nil {
		t.Fatalf("missing pair returned %q without error", got)
	}
}

func TestFFmpegVersionCheckHasFiniteDeadline(t *testing.T) {
	installFakeFFmpegRuntime(t)
	t.Setenv("PATH", t.TempDir())
	t.Setenv(testHelperVersionMarker, filepath.Join(t.TempDir(), "version-started"))
	done := make(chan error, 1)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _, err := IsFFmpegInstalledContext(ctx); done <- err }()
	select {
	case <-done:
	case <-time.After(8 * time.Second):
		cancel()
		<-done
		t.Fatal("version check has no finite deadline")
	}
}
