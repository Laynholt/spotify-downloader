package backend

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

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

func TestShouldSkipYtDlpReleaseCheckUses24HourTTL(t *testing.T) {
	markerPath := filepath.Join(t.TempDir(), ytDlpUpdateCheckFilename)
	if err := os.WriteFile(markerPath, []byte("checked"), 0600); err != nil {
		t.Fatal(err)
	}
	now := time.Unix(1_700_000_000, 0)
	if err := os.Chtimes(markerPath, now.Add(-ytDlpUpdateCheckTTL+time.Minute), now.Add(-ytDlpUpdateCheckTTL+time.Minute)); err != nil {
		t.Fatal(err)
	}
	if !shouldSkipYtDlpReleaseCheck(YtDlpStatus{Installed: true}, markerPath, now) {
		t.Fatal("expected a fresh marker to skip release check")
	}
}

func TestShouldSkipYtDlpReleaseCheckAtBoundary(t *testing.T) {
	markerPath := filepath.Join(t.TempDir(), ytDlpUpdateCheckFilename)
	if err := os.WriteFile(markerPath, []byte("checked"), 0600); err != nil {
		t.Fatal(err)
	}
	now := time.Unix(1_700_000_000, 0)
	if err := os.Chtimes(markerPath, now.Add(-ytDlpUpdateCheckTTL), now.Add(-ytDlpUpdateCheckTTL)); err != nil {
		t.Fatal(err)
	}
	if shouldSkipYtDlpReleaseCheck(YtDlpStatus{Installed: true}, markerPath, now) {
		t.Fatal("expected the exact TTL boundary to trigger a release check")
	}
}

func TestShouldSkipYtDlpReleaseCheckRequiresInstalledBinary(t *testing.T) {
	markerPath := filepath.Join(t.TempDir(), ytDlpUpdateCheckFilename)
	if err := os.WriteFile(markerPath, []byte("checked"), 0600); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	if shouldSkipYtDlpReleaseCheck(YtDlpStatus{Installed: false}, markerPath, now) {
		t.Fatal("an absent yt-dlp binary must not skip the release check")
	}
}

func TestShouldSkipYtDlpReleaseCheckRequiresMarker(t *testing.T) {
	markerPath := filepath.Join(t.TempDir(), ytDlpUpdateCheckFilename)
	if shouldSkipYtDlpReleaseCheck(YtDlpStatus{Installed: true}, markerPath, time.Now()) {
		t.Fatal("a missing marker must trigger a release check")
	}
}

func TestRecordYtDlpReleaseCheckCreatesFreshMarker(t *testing.T) {
	markerPath := filepath.Join(t.TempDir(), ytDlpUpdateCheckFilename)
	checkedAt := time.Unix(1_700_000_000, 123).UTC()
	if err := recordYtDlpReleaseCheck(markerPath, checkedAt); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(markerPath)
	if err != nil {
		t.Fatal(err)
	}
	if info.Size() == 0 {
		t.Fatal("recorded marker is empty")
	}
	if !shouldSkipYtDlpReleaseCheck(YtDlpStatus{Installed: true}, markerPath, checkedAt.Add(time.Hour)) {
		t.Fatal("newly recorded marker should be fresh")
	}
}
