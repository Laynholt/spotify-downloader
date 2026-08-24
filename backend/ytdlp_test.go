package backend

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

const (
	ytDlpTestHelperModeEnv = "SPOTI_YTDLP_TEST_HELPER_MODE"
	ytDlpTestOrderFileEnv  = "SPOTI_YTDLP_TEST_ORDER_FILE"
)

func init() {
	if os.Getenv(ytDlpTestHelperModeEnv) != "diagnostics" {
		return
	}
	if path := os.Getenv(ytDlpTestOrderFileEnv); path != "" {
		file, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
		if err != nil {
			os.Exit(2)
		}
		_, _ = io.WriteString(file, "command\n")
		_ = file.Close()
	}
	_, _ = io.WriteString(os.Stderr, ytDlpDiagnosticFixture())
	os.Exit(1)
}

func ytDlpDiagnosticFixture() string {
	const size = 50 * 1024
	suffix := "\r\n[ExtractAudio]\tDestination: final sentinel.mp3\r\n"
	return strings.Repeat("x", size-len(suffix)) + suffix
}

func TestYtDlpDiagnosticsPreservesValidUTF8AtByteLimit(t *testing.T) {
	want := strings.Repeat("y", 4091) + "終"
	diagnostics := normalizeYtDlpDiagnostics([]byte("x界" + want))
	if !utf8.ValidString(diagnostics) {
		t.Fatalf("diagnostics contain invalid UTF-8: %q", diagnostics[:8])
	}
	if diagnostics != want || len(diagnostics) > ytDlpDiagnosticsLimit {
		t.Fatalf("diagnostics length = %d, want %d-byte newest suffix", len(diagnostics), len(want))
	}

	wire, err := json.Marshal(YtDlpDownloadResult{Diagnostics: diagnostics, Output: diagnostics})
	if err != nil {
		t.Fatal(err)
	}
	var decoded YtDlpDownloadResult
	if err := json.Unmarshal(wire, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.Diagnostics != want || decoded.Output != want || len(decoded.Diagnostics) > ytDlpDiagnosticsLimit || len(decoded.Output) > ytDlpDiagnosticsLimit {
		t.Fatalf("wire result escaped diagnostic contract: diagnostics=%d output=%d", len(decoded.Diagnostics), len(decoded.Output))
	}
}

func TestYtDlpStageFromOutputRecognizesAnchoredProgressAndErrors(t *testing.T) {
	tests := []struct {
		name   string
		output string
		want   YtDlpStage
	}{
		{name: "search progress", output: "[youtube] Extracting URL: https://youtu.be/track", want: YtDlpStageSearch},
		{name: "search error", output: "ERROR: [youtube] track: Video unavailable", want: YtDlpStageSearch},
		{name: "download progress", output: "[download] Destination: track.webm", want: YtDlpStageDownload},
		{name: "download error", output: "ERROR: unable to download video data: HTTP Error 403: Forbidden", want: YtDlpStageDownload},
		{name: "conversion progress", output: "[ExtractAudio] Destination: track.mp3", want: YtDlpStageConversion},
		{name: "conversion error", output: "ERROR: Postprocessing: ffprobe and ffmpeg not found", want: YtDlpStageConversion},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := ytDlpStageFromOutput([]byte(test.output)); got != test.want {
				t.Fatalf("stage = %q, want %q for %q", got, test.want, test.output)
			}
		})
	}
}

func TestYtDlpStageFromOutputIgnoresUnanchoredMarkers(t *testing.T) {
	tests := []struct {
		name   string
		output string
		want   YtDlpStage
	}{
		{
			name:   "search URL",
			output: "[youtube] Extracting URL: https://example.invalid/[download]/[ExtractAudio]",
			want:   YtDlpStageSearch,
		},
		{
			name:   "download filename",
			output: "[download] Destination: C:\\music\\[ExtractAudio]\\track.webm",
			want:   YtDlpStageDownload,
		},
		{
			name:   "later error filename",
			output: "[download] Destination: track.webm\nERROR: unable to open C:\\music\\[ExtractAudio]\\track.webm",
			want:   YtDlpStageDownload,
		},
		{
			name:   "plain marker-like text",
			output: "failed to open /music/[download]/track.webm",
			want:   YtDlpStageSearch,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := ytDlpStageFromOutput([]byte(test.output)); got != test.want {
				t.Fatalf("stage = %q, want %q for %q", got, test.want, test.output)
			}
		})
	}
}

func TestYtDlpStageFromOutputUsesLastAnchoredOperation(t *testing.T) {
	output := strings.Join([]string{
		"[youtube] Extracting URL: https://youtu.be/track",
		"[download] Destination: track.webm",
		"[ExtractAudio] Destination: track.mp3",
		"ERROR: unable to download video data: connection reset",
	}, "\r\n")
	if got := ytDlpStageFromOutput([]byte(output)); got != YtDlpStageDownload {
		t.Fatalf("stage = %q, want last anchored operation %q", got, YtDlpStageDownload)
	}
}

func TestBuildYtDlpArgsIncludesPortableRuntimePaths(t *testing.T) {
	root := t.TempDir()
	req := YtDlpDownloadRequest{
		Query:       "https://example.invalid/track",
		OutputBase:  filepath.Join(root, "track"),
		AudioFormat: "flac",
	}
	denoPath := filepath.Join(root, "deno.exe")
	ffmpegPath := filepath.Join(root, "ffmpeg.exe")

	got := BuildYtDlpArgs(req, denoPath, ffmpegPath)
	want := []string{
		"--no-playlist",
		"--newline",
		"--extract-audio",
		"--audio-format", "flac",
		"--audio-quality", "0",
		"-o", req.OutputBase + ".%(ext)s",
		"--js-runtimes", "deno:" + denoPath,
		"--ffmpeg-location", filepath.Dir(ffmpegPath),
		req.Query,
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("BuildYtDlpArgs() = %#v, want %#v", got, want)
	}
}

func TestDownloadWithYtDlpEnsuresUpdateThenDenoBeforeCommand(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	orderPath := filepath.Join(t.TempDir(), "order")
	t.Setenv(ytDlpTestHelperModeEnv, "diagnostics")
	t.Setenv(ytDlpTestOrderFileEnv, orderPath)
	installYtDlpDownloadHooks(t,
		func(context.Context) (YtDlpStatus, error) {
			appendYtDlpTestOrder(t, orderPath, "yt-dlp-update")
			return YtDlpStatus{Installed: true, Path: executable}, nil
		},
		func(context.Context) (DenoStatus, error) {
			appendYtDlpTestOrder(t, orderPath, "deno")
			return DenoStatus{Installed: true, Path: executable}, nil
		},
		func() (string, error) { return executable, nil },
	)

	result := DownloadWithYtDlpContext(context.Background(), YtDlpDownloadRequest{
		Query:       "https://example.invalid/track",
		OutputBase:  filepath.Join(t.TempDir(), "track"),
		AudioFormat: "mp3",
	})
	if result.Success {
		t.Fatalf("fake yt-dlp unexpectedly succeeded: %#v", result)
	}
	data, err := os.ReadFile(orderPath)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := strings.Fields(string(data)), []string{"yt-dlp-update", "deno", "command"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("operation order = %v, want %v", got, want)
	}
}

func TestDownloadWithYtDlpRequiresAbsoluteRegularNonemptyPathsBeforeDenoCommand(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	emptyPath := filepath.Join(t.TempDir(), "empty")
	if err := os.WriteFile(emptyPath, nil, 0755); err != nil {
		t.Fatal(err)
	}
	directoryPath := t.TempDir()
	tests := []struct {
		name       string
		ytDlpPath  string
		denoPath   string
		ffmpegPath string
		stage      YtDlpStage
	}{
		{name: "relative yt-dlp", ytDlpPath: "yt-dlp", denoPath: executable, ffmpegPath: executable, stage: YtDlpStageReleaseCheck},
		{name: "directory yt-dlp", ytDlpPath: directoryPath, denoPath: executable, ffmpegPath: executable, stage: YtDlpStageReleaseCheck},
		{name: "empty yt-dlp", ytDlpPath: emptyPath, denoPath: executable, ffmpegPath: executable, stage: YtDlpStageReleaseCheck},
		{name: "relative Deno", ytDlpPath: executable, denoPath: "deno", ffmpegPath: executable, stage: YtDlpStageRuntime},
		{name: "directory Deno", ytDlpPath: executable, denoPath: directoryPath, ffmpegPath: executable, stage: YtDlpStageRuntime},
		{name: "empty Deno", ytDlpPath: executable, denoPath: emptyPath, ffmpegPath: executable, stage: YtDlpStageRuntime},
		{name: "relative FFmpeg", ytDlpPath: executable, denoPath: executable, ffmpegPath: "ffmpeg", stage: YtDlpStageRuntime},
		{name: "directory FFmpeg", ytDlpPath: executable, denoPath: executable, ffmpegPath: directoryPath, stage: YtDlpStageRuntime},
		{name: "empty FFmpeg", ytDlpPath: executable, denoPath: executable, ffmpegPath: emptyPath, stage: YtDlpStageRuntime},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			commandMarker := filepath.Join(t.TempDir(), "command")
			t.Setenv(ytDlpTestHelperModeEnv, "diagnostics")
			t.Setenv(ytDlpTestOrderFileEnv, commandMarker)
			installYtDlpDownloadHooks(t,
				func(context.Context) (YtDlpStatus, error) {
					return YtDlpStatus{Installed: true, Path: test.ytDlpPath}, nil
				},
				func(context.Context) (DenoStatus, error) {
					return DenoStatus{Installed: true, Path: test.denoPath}, nil
				},
				func() (string, error) { return test.ffmpegPath, nil },
			)

			result := DownloadWithYtDlpContext(context.Background(), YtDlpDownloadRequest{
				Query:      "https://example.invalid/track",
				OutputBase: filepath.Join(t.TempDir(), "track"),
			})
			if result.Success || result.Error == "" || result.Stage != test.stage {
				t.Fatalf("result = %#v, want failure at %q", result, test.stage)
			}
			if _, err := os.Stat(commandMarker); !os.IsNotExist(err) {
				t.Fatalf("invalid helper path reached yt-dlp command: %v", err)
			}
		})
	}
}

func TestDownloadWithYtDlpDenoCancellationKillsHelperTree(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(t.TempDir(), "grandchild-running")
	release := filepath.Join(t.TempDir(), "grandchild-release")
	survived := filepath.Join(t.TempDir(), "grandchild-survived")
	t.Setenv(testHelperModeEnv, "managed-parent")
	t.Setenv(testHelperMarker, marker)
	t.Setenv(testHelperRelease, release)
	t.Setenv("SPOTI_DOWNLOAD_TEST_HELPER_SURVIVED", survived)
	installYtDlpDownloadHooks(t,
		func(context.Context) (YtDlpStatus, error) { return YtDlpStatus{Installed: true, Path: executable}, nil },
		func(context.Context) (DenoStatus, error) { return DenoStatus{Installed: true, Path: executable}, nil },
		func() (string, error) { return executable, nil },
	)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan YtDlpDownloadResult, 1)
	go func() {
		done <- DownloadWithYtDlpContext(ctx, YtDlpDownloadRequest{
			Query:      "https://example.invalid/track",
			OutputBase: filepath.Join(t.TempDir(), "track"),
		})
	}()
	if err := waitForFile(marker, time.Second); err != nil {
		cancel()
		t.Fatal("yt-dlp helper did not start its descendant:", err)
	}
	cancel()
	select {
	case result := <-done:
		if result.Error != context.Canceled.Error() || result.Stage != YtDlpStageSearch {
			t.Fatalf("cancellation result = %#v", result)
		}
	case <-time.After(time.Second):
		t.Fatal("yt-dlp command ignored cancellation")
	}
	if err := os.WriteFile(release, []byte("release"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := waitForFile(survived, 300*time.Millisecond); err == nil {
		t.Fatal("yt-dlp cancellation left a descendant alive")
	}
}

func TestYtDlpDiagnosticsLogsFullOutputAndBoundsResponse(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv(ytDlpTestHelperModeEnv, "diagnostics")
	installYtDlpDownloadHooks(t,
		func(context.Context) (YtDlpStatus, error) { return YtDlpStatus{Installed: true, Path: executable}, nil },
		func(context.Context) (DenoStatus, error) { return DenoStatus{Installed: true, Path: executable}, nil },
		func() (string, error) { return executable, nil },
	)
	var logged string
	logCalls := 0
	previousLogf := ytDlpDebugLogf
	ytDlpDebugLogf = func(format string, args ...any) {
		logCalls++
		logged = fmt.Sprintf(format, args...)
	}
	t.Cleanup(func() { ytDlpDebugLogf = previousLogf })

	result := DownloadWithYtDlpContext(context.Background(), YtDlpDownloadRequest{
		Query:      "https://example.invalid/track",
		OutputBase: filepath.Join(t.TempDir(), "track"),
	})
	if logCalls != 1 {
		t.Fatalf("debug logger calls = %d, want exactly 1", logCalls)
	}
	if logged != ytDlpDiagnosticFixture() {
		t.Fatalf("debug logger received %d bytes, want full %d-byte output", len(logged), len(ytDlpDiagnosticFixture()))
	}
	if result.Stage != YtDlpStageConversion || result.Success {
		t.Fatalf("diagnostic result = %#v, want conversion failure", result)
	}
	if len(result.Diagnostics) > 4096 || len(result.Output) > 4096 {
		t.Fatalf("diagnostics escaped response cap: diagnostics=%d output=%d", len(result.Diagnostics), len(result.Output))
	}
	if result.Diagnostics != result.Output {
		t.Fatalf("compatibility output differs from diagnostics")
	}
	if strings.ContainsAny(result.Diagnostics, "\r\n\t") || !strings.HasSuffix(result.Diagnostics, "[ExtractAudio] Destination: final sentinel.mp3") {
		t.Fatalf("diagnostics were not normalized from the tail: %q", result.Diagnostics)
	}
}

func installYtDlpDownloadHooks(
	t *testing.T,
	ensureYtDlp func(context.Context) (YtDlpStatus, error),
	ensureDeno func(context.Context) (DenoStatus, error),
	getFFmpegPath func() (string, error),
) {
	t.Helper()
	previousYtDlp := ytDlpEnsureInstalledOrUpdatedContext
	previousDeno := ytDlpEnsureDenoInstalledOrUpdatedContext
	previousFFmpeg := ytDlpGetFFmpegPath
	ytDlpEnsureInstalledOrUpdatedContext = ensureYtDlp
	ytDlpEnsureDenoInstalledOrUpdatedContext = ensureDeno
	ytDlpGetFFmpegPath = getFFmpegPath
	t.Cleanup(func() {
		ytDlpEnsureInstalledOrUpdatedContext = previousYtDlp
		ytDlpEnsureDenoInstalledOrUpdatedContext = previousDeno
		ytDlpGetFFmpegPath = previousFFmpeg
	})
}

func appendYtDlpTestOrder(t *testing.T, path, operation string) {
	t.Helper()
	file, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.WriteString(file, operation+"\n"); err != nil {
		_ = file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
}

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
