package backend

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestDownloadAndExtractVerifiedContextCancellationRemovesTemp(t *testing.T) {
	paths := installRuntimePathsForCancellationTest(t)
	started := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(started)
		_, _ = io.WriteString(w, "partial ffmpeg archive")
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
		<-r.Context().Done()
	}))
	defer server.Close()

	previousFactory := ffmpegHTTPClientFactory
	t.Cleanup(func() { ffmpegHTTPClientFactory = previousFactory })
	ffmpegHTTPClientFactory = func(timeout time.Duration) *http.Client {
		return &http.Client{Timeout: timeout, Transport: rewriteRoundTripper{base: server.URL}}
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		err := downloadAndExtractVerifiedContext(ctx, "https://github.com/example/ffmpeg.tar.xz", "", paths.BinDir, nil, 0, 100)
		done <- err
	}()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("ffmpeg archive request did not start")
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("ffmpeg cancellation error = %v, want context.Canceled", err)
		}
	case <-time.After(time.Second):
		t.Fatal("ffmpeg archive request ignored cancellation")
	}
	entries, err := os.ReadDir(paths.RunTempDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("partial ffmpeg temp files remain: %v", entries)
	}
}

func TestDownloadFFmpegContextFirstInstallCancellation(t *testing.T) {
	installRuntimePathsForCancellationTest(t)
	started := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(started)
		<-r.Context().Done()
	}))
	defer server.Close()

	previousFactory := ffmpegHTTPClientFactory
	t.Cleanup(func() { ffmpegHTTPClientFactory = previousFactory })
	ffmpegHTTPClientFactory = func(timeout time.Duration) *http.Client {
		return &http.Client{Timeout: timeout, Transport: rewriteRoundTripper{base: server.URL}}
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- DownloadFFmpegContext(ctx, nil) }()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("ffmpeg first-install release request did not start")
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("ffmpeg cancellation error = %v, want context.Canceled", err)
		}
	case <-time.After(time.Second):
		t.Fatal("ffmpeg first-install cancellation ignored")
	}
}

// Production break caught: a working portable pair loses precedence to PATH
// or installation targets leave the portable runtime directory.
func TestHelperPathsUsePortableFFmpegBin(t *testing.T) {
	paths := installFakeFFmpegRuntime(t)

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
