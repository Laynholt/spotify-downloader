package backend

import "testing"

func TestSelectYtDlpAssetForWindows(t *testing.T) {
	assets := []GitHubReleaseAsset{
		{Name: "yt-dlp", BrowserDownloadURL: "linux"},
		{Name: "yt-dlp.exe", BrowserDownloadURL: "windows"},
	}

	got, ok := SelectYtDlpAsset("windows", assets)
	if !ok || got.BrowserDownloadURL != "windows" {
		t.Fatalf("got %#v, %v", got, ok)
	}
}

func TestSelectYtDlpAssetForUnix(t *testing.T) {
	assets := []GitHubReleaseAsset{
		{Name: "yt-dlp.exe", BrowserDownloadURL: "windows"},
		{Name: "yt-dlp", BrowserDownloadURL: "unix"},
	}

	got, ok := SelectYtDlpAsset("linux", assets)
	if !ok || got.BrowserDownloadURL != "unix" {
		t.Fatalf("got %#v, %v", got, ok)
	}
}
