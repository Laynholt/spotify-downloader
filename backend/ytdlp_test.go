package backend

import "testing"

func TestSelectYtDlpAssetForWindows(t *testing.T) {
	assets := []GitHubReleaseAsset{
		{Name: "yt-dlp", BrowserDownloadURL: "linux"},
		{Name: "yt-dlp.exe", BrowserDownloadURL: "windows"},
	}

	got, ok := SelectYtDlpAsset("windows", "amd64", assets)
	if !ok || got.BrowserDownloadURL != "windows" {
		t.Fatalf("got %#v, %v", got, ok)
	}
}

func TestSelectYtDlpAssetForUnix(t *testing.T) {
	assets := []GitHubReleaseAsset{
		{Name: "yt-dlp.exe", BrowserDownloadURL: "windows"},
		{Name: "yt-dlp", BrowserDownloadURL: "unix"},
	}

	got, ok := SelectYtDlpAsset("linux", "amd64", assets)
	if !ok || got.BrowserDownloadURL != "unix" {
		t.Fatalf("got %#v, %v", got, ok)
	}
}

func TestSelectYtDlpAssetPrefersPlatformBinaries(t *testing.T) {
	assets := []GitHubReleaseAsset{
		{Name: "yt-dlp", BrowserDownloadURL: "generic"},
		{Name: "yt-dlp_linux", BrowserDownloadURL: "linux"},
		{Name: "yt-dlp_macos", BrowserDownloadURL: "macos"},
	}

	got, ok := SelectYtDlpAsset("linux", "amd64", assets)
	if !ok || got.BrowserDownloadURL != "linux" {
		t.Fatalf("expected linux binary, got %#v ok=%v", got, ok)
	}

	got, ok = SelectYtDlpAsset("darwin", "arm64", assets)
	if !ok || got.BrowserDownloadURL != "macos" {
		t.Fatalf("expected macos binary, got %#v ok=%v", got, ok)
	}
}

func TestParseYtDlpSHA256Sums(t *testing.T) {
	checksums := []byte("abcd1234  yt-dlp_linux\nffff  yt-dlp.exe\n")
	hashes := parseSHA256Sums(checksums)
	if hashes["yt-dlp_linux"] != "abcd1234" {
		t.Fatalf("expected hash for yt-dlp_linux, got %q", hashes["yt-dlp_linux"])
	}
}
