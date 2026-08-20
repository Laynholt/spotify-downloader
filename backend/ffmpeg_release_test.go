package backend

import (
	"path/filepath"
	"testing"
)

// Production break caught: FFmpeg helper getters consult the host PATH or the
// legacy user profile instead of returning portable runtime locations.
func TestHelperPathsUsePortableFFmpegBin(t *testing.T) {
	paths, err := ResolveRuntimePaths(filepath.Join(t.TempDir(), "SpotiDownloader.exe"), "test")
	if err != nil {
		t.Fatal(err)
	}
	if err := paths.Prepare(); err != nil {
		t.Fatal(err)
	}
	installRuntimePathsForTest(t, paths)

	dir, err := GetFFmpegDir()
	if err != nil {
		t.Fatal(err)
	}
	if dir != paths.BinDir {
		t.Fatalf("ffmpeg dir: %s", dir)
	}
	for name, getPath := range map[string]func() (string, error){
		"ffmpeg":  GetFFmpegPath,
		"ffprobe": GetFFprobePath,
	} {
		path, err := getPath()
		if err != nil {
			t.Fatalf("%s path: %v", name, err)
		}
		if filepath.Dir(path) != paths.BinDir {
			t.Fatalf("%s path: %s", name, path)
		}
	}
}

func TestSelectFFmpegAssetForPlatform(t *testing.T) {
	assets := []GitHubReleaseAsset{
		{Name: "checksums.sha256", BrowserDownloadURL: "checksums"},
		{Name: "ffmpeg-n7.1.4-linux64-gpl-7.1.tar.xz", BrowserDownloadURL: "linux"},
		{Name: "ffmpeg-n7.1.4-win64-gpl-7.1.zip", BrowserDownloadURL: "windows"},
		{Name: "ffmpeg-n7.1.4-darwin64-gpl-7.1.zip", BrowserDownloadURL: "macos"},
	}

	got, ok := SelectFFmpegAsset("windows", "amd64", assets)
	if !ok || got.BrowserDownloadURL != "windows" {
		t.Fatalf("expected windows asset, got %#v ok=%v", got, ok)
	}

	got, ok = SelectFFmpegAsset("linux", "amd64", assets)
	if !ok || got.BrowserDownloadURL != "linux" {
		t.Fatalf("expected linux asset, got %#v ok=%v", got, ok)
	}
}
