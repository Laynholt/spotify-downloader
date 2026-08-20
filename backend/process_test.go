package backend

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

func TestManagedCommandCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	name, args := longRunningCommand()
	cmd := newManagedCommand(ctx, name, args...)
	done := make(chan error, 1)
	go func() { done <- cmd.Run() }()

	select {
	case <-time.After(time.Second):
		t.Fatal("managed command did not start")
	default:
	}

	// Give the child a moment to start before asking the process-tree
	// cancellation hook to terminate it.
	time.Sleep(50 * time.Millisecond)
	cancel()

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("managed command completed successfully after cancellation")
		}
		if !errors.Is(err, context.Canceled) && !errors.Is(err, os.ErrProcessDone) && ctx.Err() == nil {
			t.Fatalf("managed command error = %v, want cancellation", err)
		}
	case <-time.After(time.Second):
		t.Fatal("managed command ignored cancellation")
	}
}

func TestDownloadRequestCancellation(t *testing.T) {
	started := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(started)
		<-r.Context().Done()
	}))
	defer server.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	downloader := &SpotiDownloader{
		httpClient:   newHTTPClient(10 * time.Second),
		sessionToken: "test-token",
	}
	done := make(chan error, 1)
	outputBase := filepath.Join(t.TempDir(), "audio")
	go func() {
		_, err := downloader.downloadFileContext(ctx, server.URL, outputBase, ".mp3")
		done <- err
	}()

	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("download request did not reach test server")
	}
	cancel()

	select {
	case err := <-done:
		if err == nil || !errors.Is(err, context.Canceled) {
			t.Fatalf("download cancellation error = %v, want context.Canceled", err)
		}
	case <-time.After(time.Second):
		t.Fatal("download request ignored cancellation")
	}
}

func TestConvertAudioContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := convertDownloadedAudioContext(ctx, filepath.Join(t.TempDir(), "input.mp3"), filepath.Join(t.TempDir(), "output"), "flac")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("conversion cancellation error = %v, want context.Canceled", err)
	}
}

func longRunningCommand() (string, []string) {
	if runtime.GOOS == "windows" {
		return "cmd", []string{"/c", "ping", "-n", "30", "127.0.0.1", ">", "NUL"}
	}
	return "sh", []string{"-c", "sleep 30"}
}
