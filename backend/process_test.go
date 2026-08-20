package backend

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

const (
	testHelperModeEnv = "SPOTI_DOWNLOAD_TEST_HELPER_MODE"
	testHelperMarker  = "SPOTI_DOWNLOAD_TEST_HELPER_MARKER"
	testHelperRelease = "SPOTI_DOWNLOAD_TEST_HELPER_RELEASE"
)

func TestMain(m *testing.M) {
	mode := os.Getenv(testHelperModeEnv)
	if mode == "" {
		switch strings.ToLower(filepath.Base(os.Args[0])) {
		case "ffmpeg", "ffmpeg.exe":
			mode = "fake-ffmpeg"
		case "ffprobe", "ffprobe.exe":
			mode = "fake-ffprobe"
		}
	}
	switch mode {
	case "managed-parent":
		os.Exit(runManagedParentHelper())
	case "managed-grandchild":
		os.Exit(runManagedGrandchildHelper())
	case "fake-ffmpeg":
		os.Exit(runFakeFFmpegHelper())
	case "fake-ffprobe":
		os.Exit(runFakeFFprobeHelper())
	}
	os.Exit(m.Run())
}

func TestManagedCommandCancellation(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "grandchild-running")
	release := filepath.Join(t.TempDir(), "grandchild-release")
	survived := filepath.Join(t.TempDir(), "grandchild-survived")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cmd := newManagedCommand(ctx, os.Args[0], "-test.run=TestManagedCommandHelperProcess")
	cmd.Env = append(os.Environ(),
		testHelperModeEnv+"=managed-parent",
		testHelperMarker+"="+marker,
		testHelperRelease+"="+release,
		"SPOTI_DOWNLOAD_TEST_HELPER_SURVIVED="+survived,
	)
	done := make(chan error, 1)
	go func() { done <- cmd.Run() }()

	if err := waitForFile(marker, time.Second); err != nil {
		cancel()
		t.Fatal("managed command did not start and spawn its descendant:", err)
	}

	cancel()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("managed command completed successfully after cancellation")
		}
		if ctx.Err() == nil {
			t.Fatalf("managed command returned %v without context cancellation", err)
		}
	case <-time.After(time.Second):
		t.Fatal("managed command ignored cancellation")
	}

	// A surviving descendant is instructed to leave a marker only after the
	// parent command has returned. This proves the process-tree kill, without
	// relying on a sleep-based startup race.
	if err := os.WriteFile(release, []byte("release"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := waitForFile(survived, 300*time.Millisecond); err == nil {
		t.Fatal("managed command left a descendant alive after cancellation")
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
	if matches, err := filepath.Glob(outputBase + "*"); err != nil {
		t.Fatal(err)
	} else if len(matches) != 0 {
		t.Fatalf("partial download files remain: %v", matches)
	}
}

func TestConvertDownloadedAudioContextCancellation(t *testing.T) {
	installFakeFFmpegRuntime(t)
	marker := filepath.Join(t.TempDir(), "ffmpeg-started")
	t.Setenv(testHelperMarker, marker)

	inputPath := filepath.Join(t.TempDir(), "input.mp3")
	outputBase := filepath.Join(t.TempDir(), "converted")
	if err := os.WriteFile(inputPath, []byte("downloaded input"), 0600); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := convertDownloadedAudioContext(ctx, inputPath, outputBase, "flac")
		done <- err
	}()
	if err := waitForFile(marker, time.Second); err != nil {
		cancel()
		t.Fatal("conversion helper did not start:", err)
	}
	cancel()

	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("conversion cancellation error = %v, want context.Canceled", err)
		}
	case <-time.After(time.Second):
		t.Fatal("conversion ignored cancellation")
	}
	if _, err := os.Stat(inputPath); !os.IsNotExist(err) {
		t.Fatalf("canceled conversion left downloaded input: %v", err)
	}
	if matches, err := filepath.Glob(outputBase + "*"); err != nil {
		t.Fatal(err)
	} else if len(matches) != 0 {
		t.Fatalf("canceled conversion left output files: %v", matches)
	}
}

func TestConvertAudioContextActiveCancellation(t *testing.T) {
	installFakeFFmpegRuntime(t)
	marker := filepath.Join(t.TempDir(), "ffmpeg-convert-started")
	t.Setenv(testHelperMarker, marker)

	inputPath := filepath.Join(t.TempDir(), "input.wav")
	if err := os.WriteFile(inputPath, []byte("source"), 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := ConvertAudioContext(ctx, ConvertAudioRequest{
			InputFiles:   []string{inputPath},
			OutputFormat: "flac",
		})
		done <- err
	}()
	if err := waitForFile(marker, time.Second); err != nil {
		cancel()
		t.Fatal("ConvertAudio helper did not start:", err)
	}
	cancel()

	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("ConvertAudio cancellation error = %v, want context.Canceled", err)
		}
	case <-time.After(time.Second):
		t.Fatal("ConvertAudio ignored cancellation")
	}
	if _, err := os.Stat(inputPath); !os.IsNotExist(err) {
		t.Fatalf("canceled ConvertAudio left input: %v", err)
	}
	outputPath := filepath.Join(filepath.Dir(inputPath), "FLAC", "input.flac")
	if _, err := os.Stat(outputPath); !os.IsNotExist(err) {
		t.Fatalf("canceled ConvertAudio left output: %v", err)
	}
}

func TestDownloadCoverImageCancellationRemovesTemp(t *testing.T) {
	paths := installRuntimePathsForCancellationTest(t)
	started := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(started)
		_, _ = io.WriteString(w, "partial cover")
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
		<-r.Context().Done()
	}))
	defer server.Close()

	downloader := &SpotiDownloader{httpClient: newHTTPClient(10 * time.Second)}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := downloader.downloadCoverImageContext(ctx, server.URL, t.TempDir(), false)
		done <- err
	}()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("cover request did not start")
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("cover cancellation error = %v, want context.Canceled", err)
		}
	case <-time.After(time.Second):
		t.Fatal("cover request ignored cancellation")
	}
	entries, err := os.ReadDir(paths.RunTempDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("partial cover temp files remain: %v", entries)
	}
}

func TestManagedCommandHelperProcess(t *testing.T) {}

func runManagedParentHelper() int {
	marker := os.Getenv(testHelperMarker)
	child := exec.Command(os.Args[0], "-test.run=TestManagedCommandGrandchildHelper")
	child.Env = helperEnvironment(map[string]string{
		testHelperModeEnv:                     "managed-grandchild",
		testHelperMarker:                      marker,
		testHelperRelease:                     os.Getenv(testHelperRelease),
		"SPOTI_DOWNLOAD_TEST_HELPER_SURVIVED": os.Getenv("SPOTI_DOWNLOAD_TEST_HELPER_SURVIVED"),
	})
	if err := child.Start(); err != nil {
		return 2
	}
	if err := child.Wait(); err != nil {
		return 1
	}
	return 0
}

func helperEnvironment(overrides map[string]string) []string {
	env := os.Environ()
	for key := range overrides {
		prefix := key + "="
		filtered := env[:0]
		for _, value := range env {
			if !strings.HasPrefix(value, prefix) {
				filtered = append(filtered, value)
			}
		}
		env = filtered
	}
	for key, value := range overrides {
		env = append(env, key+"="+value)
	}
	return env
}

func runManagedGrandchildHelper() int {
	marker := os.Getenv(testHelperMarker)
	release := os.Getenv(testHelperRelease)
	survived := os.Getenv("SPOTI_DOWNLOAD_TEST_HELPER_SURVIVED")
	if err := os.WriteFile(marker, []byte("running"), 0600); err != nil {
		return 2
	}
	for {
		if _, err := os.Stat(release); err == nil {
			if err := os.WriteFile(survived, []byte("survived"), 0600); err != nil {
				return 3
			}
			return 0
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func runFakeFFmpegHelper() int {
	if len(os.Args) > 1 && os.Args[1] == "-version" {
		return 0
	}
	marker := os.Getenv(testHelperMarker)
	if marker != "" {
		_ = os.WriteFile(marker, []byte("started"), 0600)
	}
	if len(os.Args) > 1 {
		output := os.Args[len(os.Args)-1]
		if output != "" && !strings.HasPrefix(output, "-") {
			_ = os.MkdirAll(filepath.Dir(output), 0755)
			_ = os.WriteFile(output, []byte("partial output"), 0600)
		}
	}
	for {
		time.Sleep(10 * time.Millisecond)
	}
}

func runFakeFFprobeHelper() int {
	if len(os.Args) > 1 && os.Args[1] == "-version" {
		return 0
	}
	if marker := os.Getenv("SPOTI_FAKE_FFPROBE_MARKER"); marker != "" {
		_ = os.WriteFile(marker, []byte("started"), 0600)
		for {
			time.Sleep(10 * time.Millisecond)
		}
	}
	_, _ = io.WriteString(os.Stdout, "{}")
	return 0
}

func installFakeFFmpegRuntime(t *testing.T) RuntimePaths {
	paths := installRuntimePathsForCancellationTest(t)
	for _, name := range []string{"ffmpeg", "ffprobe"} {
		if runtime.GOOS == "windows" {
			name += ".exe"
		}
		destination := filepath.Join(paths.BinDir, name)
		data, err := os.ReadFile(os.Args[0])
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(destination, data, 0755); err != nil {
			t.Fatal(err)
		}
	}
	return paths
}

func installRuntimePathsForCancellationTest(t *testing.T) RuntimePaths {
	paths, err := ResolveRuntimePaths(filepath.Join(t.TempDir(), "SpotiDownloader.exe"), "cancellation")
	if err != nil {
		t.Fatal(err)
	}
	if err := paths.Prepare(); err != nil {
		t.Fatal(err)
	}
	installRuntimePathsForTest(t, paths)
	return paths
}

func waitForFile(path string, timeout time.Duration) error {
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	ticker := time.NewTicker(5 * time.Millisecond)
	defer ticker.Stop()
	for {
		if _, err := os.Stat(path); err == nil {
			return nil
		}
		select {
		case <-deadline.C:
			return fmt.Errorf("timed out waiting for %s", path)
		case <-ticker.C:
		}
	}
}
