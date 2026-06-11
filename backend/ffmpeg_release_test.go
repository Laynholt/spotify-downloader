package backend

import "testing"

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
