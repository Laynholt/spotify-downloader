package backend

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// Production break caught: a supported platform selects a similarly named or
// mismatched checksum asset instead of Deno's exact release pair.
func TestSelectDenoAssetForSupportedPlatforms(t *testing.T) {
	tests := []struct {
		goos    string
		goarch  string
		archive string
	}{
		{"windows", "amd64", "deno-x86_64-pc-windows-msvc.zip"},
		{"windows", "arm64", "deno-aarch64-pc-windows-msvc.zip"},
		{"linux", "amd64", "deno-x86_64-unknown-linux-gnu.zip"},
		{"linux", "arm64", "deno-aarch64-unknown-linux-gnu.zip"},
		{"darwin", "amd64", "deno-x86_64-apple-darwin.zip"},
		{"darwin", "arm64", "deno-aarch64-apple-darwin.zip"},
	}

	for _, test := range tests {
		t.Run(test.goos+"/"+test.goarch, func(t *testing.T) {
			assets := []GitHubReleaseAsset{
				{Name: test.archive + ".sig", BrowserDownloadURL: "wrong-signature"},
				{Name: test.archive + ".sha256sum", BrowserDownloadURL: "checksum"},
				{Name: test.archive, BrowserDownloadURL: "archive"},
			}
			archive, checksum, ok := SelectDenoAsset(test.goos, test.goarch, assets)
			if !ok || archive.Name != test.archive || checksum.Name != test.archive+".sha256sum" {
				t.Fatalf("SelectDenoAsset() = %#v, %#v, %v", archive, checksum, ok)
			}
		})
	}
}

// Production break caught: an unsupported platform or incomplete pair is
// accepted and later downloads an unverifiable archive.
func TestSelectDenoAssetRejectsUnsupportedOrIncompletePair(t *testing.T) {
	archive := GitHubReleaseAsset{Name: "deno-x86_64-unknown-linux-gnu.zip"}
	checksum := GitHubReleaseAsset{Name: archive.Name + ".sha256sum"}
	for name, test := range map[string]struct {
		goos   string
		goarch string
		assets []GitHubReleaseAsset
	}{
		"unsupported architecture": {"linux", "386", []GitHubReleaseAsset{archive, checksum}},
		"missing checksum":         {"linux", "amd64", []GitHubReleaseAsset{archive}},
		"missing archive":          {"linux", "amd64", []GitHubReleaseAsset{checksum}},
	} {
		t.Run(name, func(t *testing.T) {
			if _, _, ok := SelectDenoAsset(test.goos, test.goarch, test.assets); ok {
				t.Fatal("incomplete or unsupported release pair was accepted")
			}
		})
	}
}

func writeDenoMarker(t *testing.T, modified time.Time) string {
	t.Helper()
	marker := filepath.Join(t.TempDir(), "deno.update-check")
	if err := os.WriteFile(marker, []byte("checked"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(marker, modified, modified); err != nil {
		t.Fatal(err)
	}
	return marker
}

// Production break caught: a marker exactly 24 hours old suppresses the next
// release check for longer than the configured TTL.
func TestShouldSkipDenoReleaseCheckExact24HourBoundary(t *testing.T) {
	now := time.Date(2026, 8, 23, 12, 0, 0, 0, time.UTC)
	marker := writeDenoMarker(t, now.Add(-24*time.Hour))
	if shouldSkipDenoReleaseCheck(marker, now, true) {
		t.Fatal("exactly 24 hours must trigger a release check")
	}
}

func TestShouldSkipDenoReleaseCheckRequiresInstalledDenoAndFreshMarker(t *testing.T) {
	now := time.Date(2026, 8, 23, 12, 0, 0, 0, time.UTC)
	marker := writeDenoMarker(t, now.Add(-24*time.Hour+time.Second))
	if !shouldSkipDenoReleaseCheck(marker, now, true) {
		t.Fatal("fresh marker for installed Deno must skip the release check")
	}
	if shouldSkipDenoReleaseCheck(marker, now, false) {
		t.Fatal("missing Deno must never skip installation")
	}
}

func currentDenoArchiveName(t *testing.T) string {
	t.Helper()
	switch runtime.GOOS + "/" + runtime.GOARCH {
	case "windows/amd64":
		return "deno-x86_64-pc-windows-msvc.zip"
	case "windows/arm64":
		return "deno-aarch64-pc-windows-msvc.zip"
	case "linux/amd64":
		return "deno-x86_64-unknown-linux-gnu.zip"
	case "linux/arm64":
		return "deno-aarch64-unknown-linux-gnu.zip"
	case "darwin/amd64":
		return "deno-x86_64-apple-darwin.zip"
	case "darwin/arm64":
		return "deno-aarch64-apple-darwin.zip"
	default:
		t.Fatalf("test does not support %s/%s", runtime.GOOS, runtime.GOARCH)
		return ""
	}
}

func currentDenoExecutableName() string {
	if runtime.GOOS == "windows" {
		return "deno.exe"
	}
	return "deno"
}

func makeDenoZip(t *testing.T, name string, body []byte, mode os.FileMode) []byte {
	t.Helper()
	var data bytes.Buffer
	zw := zip.NewWriter(&data)
	header := &zip.FileHeader{Name: name, Method: zip.Store}
	header.SetMode(mode)
	w, err := zw.CreateHeader(header)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write(body); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return data.Bytes()
}

func denoChecksumLine(archiveName string, archive []byte) string {
	hash := sha256.Sum256(archive)
	return fmt.Sprintf("%x *%s\n", hash, archiveName)
}

type denoRewriteRoundTripper struct{ base string }

func (rt denoRewriteRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	base, err := url.Parse(rt.base)
	if err != nil {
		return nil, err
	}
	clone := req.Clone(req.Context())
	clone.URL.Scheme = base.Scheme
	clone.URL.Host = base.Host
	resp, err := http.DefaultTransport.RoundTrip(clone)
	if resp != nil {
		// Redirect resolution must keep seeing the original HTTPS host while the
		// test transport sends bytes to httptest.Server.
		resp.Request = req
	}
	return resp, err
}

func installDenoHTTPServer(t *testing.T, handler http.Handler) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	previous := denoHTTPClientFactory
	t.Cleanup(func() { denoHTTPClientFactory = previous })
	denoHTTPClientFactory = func(timeout time.Duration) *http.Client {
		return &http.Client{Timeout: timeout, Transport: denoRewriteRoundTripper{base: server.URL}}
	}
	return server
}

func installDenoRuntimeForTest(t *testing.T) RuntimePaths {
	t.Helper()
	paths, err := ResolveRuntimePaths(filepath.Join(t.TempDir(), "SpotiDownloader.exe"), "deno-test-run")
	if err != nil {
		t.Fatal(err)
	}
	if err := paths.Prepare(); err != nil {
		t.Fatal(err)
	}
	installRuntimePathsForTest(t, paths)
	return paths
}

type denoCommandCall struct {
	path string
	args []string
}

func installDenoCommandStub(t *testing.T, paths RuntimePaths, result func(path string, contents []byte) (version string, fail bool)) *[]denoCommandCall {
	t.Helper()
	previous := denoManagedCommand
	t.Cleanup(func() { denoManagedCommand = previous })
	calls := &[]denoCommandCall{}
	denoManagedCommand = func(ctx context.Context, path string, args ...string) *exec.Cmd {
		contents, _ := os.ReadFile(path)
		version, fail := result(path, contents)
		*calls = append(*calls, denoCommandCall{path: path, args: append([]string(nil), args...)})
		cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=TestDenoVersionHelperProcess", "--")
		cmd.Env = append(os.Environ(),
			"GO_WANT_DENO_VERSION_HELPER=1",
			"DENO_HELPER_VERSION="+version,
			fmt.Sprintf("DENO_HELPER_FAIL=%t", fail),
			"DENO_HELPER_WANT_CACHE="+paths.DenoCacheDir,
			"DENO_HELPER_WANT_TEMP="+paths.RunTempDir,
		)
		return cmd
	}
	return calls
}

func TestDenoVersionHelperProcess(t *testing.T) {
	if os.Getenv("GO_WANT_DENO_VERSION_HELPER") != "1" {
		return
	}
	wantCache := os.Getenv("DENO_HELPER_WANT_CACHE")
	wantTemp := os.Getenv("DENO_HELPER_WANT_TEMP")
	if os.Getenv("DENO_DIR") != wantCache ||
		os.Getenv("TMPDIR") != wantTemp ||
		os.Getenv("TMP") != wantTemp ||
		os.Getenv("TEMP") != wantTemp {
		_, _ = fmt.Fprintf(os.Stderr, "non-portable Deno environment: DENO_DIR=%q TMPDIR=%q TMP=%q TEMP=%q", os.Getenv("DENO_DIR"), os.Getenv("TMPDIR"), os.Getenv("TMP"), os.Getenv("TEMP"))
		os.Exit(2)
	}
	if os.Getenv("DENO_HELPER_FAIL") == "true" {
		os.Exit(3)
	}
	_, _ = fmt.Fprintf(os.Stdout, "deno %s\nv8 test\ntypescript test\n", os.Getenv("DENO_HELPER_VERSION"))
	os.Exit(0)
}

func denoReleaseHandler(t *testing.T, archiveName, tag string, archive []byte, checksum string) http.Handler {
	t.Helper()
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/denoland/deno/releases/latest":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"tag_name": tag,
				"assets": []GitHubReleaseAsset{
					{Name: archiveName, BrowserDownloadURL: "https://github.com/denoland/deno/releases/download/fixture/archive"},
					{Name: archiveName + ".sha256sum", BrowserDownloadURL: "https://github.com/denoland/deno/releases/download/fixture/checksum"},
				},
			})
		case "/denoland/deno/releases/download/fixture/archive":
			_, _ = w.Write(archive)
		case "/denoland/deno/releases/download/fixture/checksum":
			_, _ = io.WriteString(w, checksum)
		default:
			http.NotFound(w, r)
		}
	})
}

func assertNoDenoArtifacts(t *testing.T, paths RuntimePaths) {
	t.Helper()
	entries, err := os.ReadDir(paths.RunTempDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("Deno run-temp artifacts leaked: %v", entries)
	}
	binEntries, err := os.ReadDir(paths.BinDir)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range binEntries {
		if strings.Contains(entry.Name(), ".deno-stage-") || strings.HasSuffix(entry.Name(), ".bak") {
			t.Fatalf("Deno install artifact leaked: %s", entry.Name())
		}
	}
}

// Production break caught: checksum parsing accepts the first hash in the
// file instead of the line naming the exact selected archive.
func TestParseDenoChecksumMatchesExactArchiveFilename(t *testing.T) {
	want := strings.Repeat("a", 64)
	data := []byte(strings.Repeat("b", 64) + " *deno-other.zip\n" + want + " *deno-target.zip\n")
	got, err := parseDenoChecksum(data, "deno-target.zip")
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("checksum = %q, want %q", got, want)
	}
	if _, err := parseDenoChecksum(data, "target.zip"); err == nil {
		t.Fatal("partial archive-name match was accepted")
	}
}

// Production break caught: a complete first install escapes the portable
// cache/temp environment, skips --version validation, or records no marker.
func TestEnsureDenoInstalledUsesPortableEnvironmentAndCleansTemps(t *testing.T) {
	paths := installDenoRuntimeForTest(t)
	archiveName := currentDenoArchiveName(t)
	archive := makeDenoZip(t, currentDenoExecutableName(), []byte("new-deno"), 0755)
	installDenoHTTPServer(t, denoReleaseHandler(t, archiveName, "v2.4.0", archive, denoChecksumLine(archiveName, archive)))
	calls := installDenoCommandStub(t, paths, func(_ string, contents []byte) (string, bool) {
		if string(contents) != "new-deno" {
			return "", true
		}
		return "2.4.0", false
	})

	status, err := EnsureDenoInstalledOrUpdatedContext(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !status.Installed || !status.Updated || status.Version != "2.4.0" || status.Latest != "2.4.0" {
		t.Fatalf("unexpected Deno status: %#v", status)
	}
	if len(*calls) != 2 {
		t.Fatalf("managed --version calls = %d, want staged and final validation", len(*calls))
	}
	for _, call := range *calls {
		if len(call.args) != 1 || call.args[0] != "--version" {
			t.Fatalf("managed command args = %q", call.args)
		}
	}
	marker := filepath.Join(paths.BinDir, "deno.update-check")
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("successful install marker missing: %v", err)
	}
	assertNoDenoArtifacts(t, paths)
}

// Production break caught: a mismatched archive is opened as ZIP data before
// its SHA-256 is checked, or leaves a partial first install behind.
func TestEnsureDenoRejectsChecksumMismatchBeforeOpeningZip(t *testing.T) {
	paths := installDenoRuntimeForTest(t)
	archiveName := currentDenoArchiveName(t)
	archive := []byte("not even a zip")
	installDenoHTTPServer(t, denoReleaseHandler(t, archiveName, "v2.4.0", archive, strings.Repeat("0", 64)+" *"+archiveName+"\n"))
	installDenoCommandStub(t, paths, func(_ string, _ []byte) (string, bool) { return "", true })

	_, err := EnsureDenoInstalledOrUpdatedContext(context.Background())
	if err == nil || !strings.Contains(err.Error(), "checksum mismatch") {
		t.Fatalf("error = %v, want checksum mismatch before ZIP parsing", err)
	}
	if _, err := os.Stat(filepath.Join(paths.BinDir, currentDenoExecutableName())); !os.IsNotExist(err) {
		t.Fatalf("invalid first install exists: %v", err)
	}
	if _, err := os.Stat(filepath.Join(paths.BinDir, "deno.update-check")); !os.IsNotExist(err) {
		t.Fatalf("failed install recorded marker: %v", err)
	}
	assertNoDenoArtifacts(t, paths)
}

// Production break caught: ZIP extraction accepts traversal, extra/missing,
// empty, or non-regular entries as the managed Deno executable.
func TestEnsureDenoRejectsUnsafeOrInvalidZipEntries(t *testing.T) {
	tests := []struct {
		name  string
		entry string
		body  []byte
		mode  os.FileMode
	}{
		{"traversal", "../" + currentDenoExecutableName(), []byte("bad"), 0755},
		{"missing executable", "README.md", []byte("bad"), 0644},
		{"empty executable", currentDenoExecutableName(), nil, 0755},
		{"symlink executable", currentDenoExecutableName(), []byte("target"), os.ModeSymlink | 0777},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			paths := installDenoRuntimeForTest(t)
			archiveName := currentDenoArchiveName(t)
			archive := makeDenoZip(t, test.entry, test.body, test.mode)
			installDenoHTTPServer(t, denoReleaseHandler(t, archiveName, "v2.4.0", archive, denoChecksumLine(archiveName, archive)))
			installDenoCommandStub(t, paths, func(_ string, _ []byte) (string, bool) { return "2.4.0", false })

			if _, err := EnsureDenoInstalledOrUpdatedContext(context.Background()); err == nil {
				t.Fatal("unsafe or invalid Deno ZIP was accepted")
			}
			if _, err := os.Stat(filepath.Join(paths.BinDir, currentDenoExecutableName())); !os.IsNotExist(err) {
				t.Fatalf("invalid executable exists: %v", err)
			}
			assertNoDenoArtifacts(t, paths)
		})
	}
}

// Production break caught: cancellation after the ZIP is opened clears the
// named result before deferred cleanup and leaks a partially staged binary.
func TestExtractDenoArchiveCancellationCleansStage(t *testing.T) {
	paths := installDenoRuntimeForTest(t)
	archive := makeDenoZip(t, currentDenoExecutableName(), []byte("new-deno"), 0755)
	archivePath := filepath.Join(t.TempDir(), "deno.zip")
	if err := os.WriteFile(archivePath, archive, 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := extractDenoArchiveToStageContext(ctx, archivePath); !errors.Is(err, context.Canceled) {
		t.Fatalf("extraction cancellation error = %v", err)
	}
	assertNoDenoArtifacts(t, paths)
}

// Production break caught: cancellation leaves the partially downloaded ZIP
// outside the portable run temp or fails to remove it.
func TestEnsureDenoCancellationDuringDownloadCleansPortableTemps(t *testing.T) {
	paths := installDenoRuntimeForTest(t)
	archiveName := currentDenoArchiveName(t)
	started := make(chan struct{})
	installDenoHTTPServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/denoland/deno/releases/latest":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"tag_name": "v2.4.0",
				"assets": []GitHubReleaseAsset{
					{Name: archiveName, BrowserDownloadURL: "https://github.com/denoland/deno/releases/download/fixture/archive"},
					{Name: archiveName + ".sha256sum", BrowserDownloadURL: "https://github.com/denoland/deno/releases/download/fixture/checksum"},
				},
			})
		case "/denoland/deno/releases/download/fixture/checksum":
			_, _ = io.WriteString(w, strings.Repeat("0", 64)+" *"+archiveName+"\n")
		case "/denoland/deno/releases/download/fixture/archive":
			close(started)
			_, _ = io.WriteString(w, "partial")
			if flusher, ok := w.(http.Flusher); ok {
				flusher.Flush()
			}
			<-r.Context().Done()
		default:
			http.NotFound(w, r)
		}
	}))
	installDenoCommandStub(t, paths, func(_ string, _ []byte) (string, bool) { return "2.4.0", false })

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := EnsureDenoInstalledOrUpdatedContext(ctx)
		done <- err
	}()
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("Deno archive download did not start")
	}
	entries, err := os.ReadDir(paths.RunTempDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) == 0 {
		t.Fatal("partial Deno download was not created below RunTempDir")
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("cancellation error = %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Deno download ignored cancellation")
	}
	assertNoDenoArtifacts(t, paths)
}

// Production break caught: replacing an installed Deno discards the working
// binary when validation fails after the staged rename.
func TestEnsureDenoInvalidUpdateRollsBackInstalledBinary(t *testing.T) {
	paths := installDenoRuntimeForTest(t)
	finalPath := filepath.Join(paths.BinDir, currentDenoExecutableName())
	if err := os.WriteFile(finalPath, []byte("old-deno"), 0755); err != nil {
		t.Fatal(err)
	}
	archiveName := currentDenoArchiveName(t)
	archive := makeDenoZip(t, currentDenoExecutableName(), []byte("new-deno"), 0755)
	installDenoHTTPServer(t, denoReleaseHandler(t, archiveName, "v2.4.0", archive, denoChecksumLine(archiveName, archive)))
	installDenoCommandStub(t, paths, func(path string, contents []byte) (string, bool) {
		switch string(contents) {
		case "old-deno":
			return "1.0.0", false
		case "new-deno":
			return "2.4.0", filepath.Clean(path) == filepath.Clean(finalPath)
		default:
			return "", true
		}
	})

	status, err := EnsureDenoInstalledOrUpdatedContext(context.Background())
	if err != nil {
		t.Fatalf("working installed Deno should survive update failure: %v", err)
	}
	if !status.Installed || status.Updated || status.Warning == "" {
		t.Fatalf("unexpected rollback status: %#v", status)
	}
	contents, err := os.ReadFile(finalPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(contents) != "old-deno" {
		t.Fatalf("installed Deno was not restored: %q", contents)
	}
	if _, err := os.Stat(filepath.Join(paths.BinDir, "deno.update-check")); !os.IsNotExist(err) {
		t.Fatalf("failed update recorded marker: %v", err)
	}
	assertNoDenoArtifacts(t, paths)
}

// Production break caught: an allowed first redirect disables validation for
// a later hop and permits an untrusted release host.
func TestEnsureDenoRejectsDisallowedRedirectAtAnyHop(t *testing.T) {
	paths := installDenoRuntimeForTest(t)
	archiveName := currentDenoArchiveName(t)
	archive := makeDenoZip(t, currentDenoExecutableName(), []byte("new-deno"), 0755)
	installDenoHTTPServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/denoland/deno/releases/latest":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"tag_name": "v2.4.0",
				"assets": []GitHubReleaseAsset{
					{Name: archiveName, BrowserDownloadURL: "https://github.com/denoland/deno/releases/download/fixture/archive"},
					{Name: archiveName + ".sha256sum", BrowserDownloadURL: "https://github.com/denoland/deno/releases/download/fixture/checksum"},
				},
			})
		case "/denoland/deno/releases/download/fixture/checksum":
			_, _ = io.WriteString(w, denoChecksumLine(archiveName, archive))
		case "/denoland/deno/releases/download/fixture/archive":
			http.Redirect(w, r, "https://objects.githubusercontent.com/allowed-hop", http.StatusFound)
		case "/allowed-hop":
			http.Redirect(w, r, "https://evil.example/deno.zip", http.StatusFound)
		default:
			http.NotFound(w, r)
		}
	}))
	installDenoCommandStub(t, paths, func(_ string, _ []byte) (string, bool) { return "2.4.0", false })

	_, err := EnsureDenoInstalledOrUpdatedContext(context.Background())
	if err == nil || !strings.Contains(err.Error(), "unexpected Deno release host") {
		t.Fatalf("redirect error = %v", err)
	}
	assertNoDenoArtifacts(t, paths)
}
