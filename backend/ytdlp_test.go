package backend

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestDownloadYtDlpAssetContextCancellationRemovesTemp(t *testing.T) {
	paths := installRuntimePathsForCancellationTest(t)
	started := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(started)
		_, _ = io.WriteString(w, "partial yt-dlp")
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
		<-r.Context().Done()
	}))
	defer server.Close()

	previousFactory := ytDlpHTTPClientFactory
	t.Cleanup(func() { ytDlpHTTPClientFactory = previousFactory })
	ytDlpHTTPClientFactory = func(timeout time.Duration) *http.Client {
		return &http.Client{Timeout: timeout, Transport: rewriteRoundTripper{base: server.URL}}
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := downloadYtDlpAssetContext(ctx, GitHubReleaseAsset{
			Name:               "yt-dlp",
			BrowserDownloadURL: "https://github.com/example/yt-dlp",
		}, nil)
		done <- err
	}()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("yt-dlp asset request did not start")
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("yt-dlp cancellation error = %v, want context.Canceled", err)
		}
	case <-time.After(time.Second):
		t.Fatal("yt-dlp asset request ignored cancellation")
	}
	if _, err := os.Stat(filepath.Join(paths.BinDir, "yt-dlp.tmp")); !os.IsNotExist(err) {
		t.Fatalf("partial yt-dlp temp file remains: %v", err)
	}
}

func TestDownloadWithYtDlpContextFirstInstallCancellation(t *testing.T) {
	installRuntimePathsForCancellationTest(t)
	started := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(started)
		<-r.Context().Done()
	}))
	defer server.Close()

	previousFactory := ytDlpHTTPClientFactory
	t.Cleanup(func() { ytDlpHTTPClientFactory = previousFactory })
	ytDlpHTTPClientFactory = func(timeout time.Duration) *http.Client {
		return &http.Client{Timeout: timeout, Transport: rewriteRoundTripper{base: server.URL}}
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan YtDlpDownloadResult, 1)
	go func() {
		done <- DownloadWithYtDlpContext(ctx, YtDlpDownloadRequest{
			Query:       "https://example.invalid/track",
			OutputBase:  filepath.Join(t.TempDir(), "track"),
			AudioFormat: "mp3",
		})
	}()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("yt-dlp first-install release request did not start")
	}
	cancel()
	select {
	case result := <-done:
		if result.Error != context.Canceled.Error() {
			t.Fatalf("yt-dlp cancellation result = %#v, want context.Canceled", result)
		}
	case <-time.After(time.Second):
		t.Fatal("yt-dlp first-install cancellation ignored")
	}
}

type rewriteRoundTripper struct{ base string }

func (rt rewriteRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	clone := req.Clone(req.Context())
	base, err := url.Parse(rt.base)
	if err != nil {
		return nil, err
	}
	clone.URL.Scheme = base.Scheme
	clone.URL.Host = base.Host
	return http.DefaultTransport.RoundTrip(clone)
}

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
