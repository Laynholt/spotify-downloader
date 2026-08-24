package backend

import (
	"archive/zip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

const (
	denoLatestReleaseURL    = "https://api.github.com/repos/denoland/deno/releases/latest"
	denoUpdateCheckFilename = "deno.update-check"
	denoUpdateCheckTTL      = 24 * time.Hour
)

var denoAssets = map[string]string{
	"windows/amd64": "deno-x86_64-pc-windows-msvc.zip",
	"windows/arm64": "deno-aarch64-pc-windows-msvc.zip",
	"linux/amd64":   "deno-x86_64-unknown-linux-gnu.zip",
	"linux/arm64":   "deno-aarch64-unknown-linux-gnu.zip",
	"darwin/amd64":  "deno-x86_64-apple-darwin.zip",
	"darwin/arm64":  "deno-aarch64-apple-darwin.zip",
}

var (
	denoHTTPClientFactory = newHTTPClient
	denoManagedCommand    = newManagedCommand
)

type denoRelease struct {
	TagName string               `json:"tag_name"`
	Assets  []GitHubReleaseAsset `json:"assets"`
}

type DenoStatus struct {
	Installed bool   `json:"installed"`
	Path      string `json:"path,omitempty"`
	Version   string `json:"version,omitempty"`
	Latest    string `json:"latest,omitempty"`
	Updated   bool   `json:"updated,omitempty"`
	Warning   string `json:"warning,omitempty"`
	Error     string `json:"error,omitempty"`
}

func SelectDenoAsset(goos, goarch string, assets []GitHubReleaseAsset) (archive GitHubReleaseAsset, checksum GitHubReleaseAsset, ok bool) {
	archiveName, supported := denoAssets[strings.ToLower(goos)+"/"+strings.ToLower(goarch)]
	if !supported {
		return GitHubReleaseAsset{}, GitHubReleaseAsset{}, false
	}
	archive, archiveOK := findReleaseAsset(archiveName, assets)
	checksum, checksumOK := findReleaseAsset(archiveName+".sha256sum", assets)
	return archive, checksum, archiveOK && checksumOK
}

func GetDenoPath() (string, error) {
	paths, err := CurrentRuntimePaths()
	if err != nil {
		return "", err
	}
	name := "deno"
	if runtime.GOOS == "windows" {
		name = "deno.exe"
	}
	return filepath.Join(paths.BinDir, name), nil
}

func getDenoUpdateCheckPath() (string, error) {
	paths, err := CurrentRuntimePaths()
	if err != nil {
		return "", err
	}
	return filepath.Join(paths.BinDir, denoUpdateCheckFilename), nil
}

func shouldSkipDenoReleaseCheck(markerPath string, now time.Time, installed bool) bool {
	if !installed {
		return false
	}
	info, err := os.Stat(markerPath)
	if err != nil || !info.Mode().IsRegular() {
		return false
	}
	return now.Sub(info.ModTime()) < denoUpdateCheckTTL
}

func recordDenoReleaseCheck(markerPath string, checkedAt time.Time) error {
	if err := os.MkdirAll(filepath.Dir(markerPath), 0755); err != nil {
		return err
	}
	if err := os.WriteFile(markerPath, []byte(checkedAt.UTC().Format(time.RFC3339Nano)), 0600); err != nil {
		return err
	}
	return os.Chtimes(markerPath, checkedAt, checkedAt)
}

func EnsureDenoInstalledOrUpdated() (DenoStatus, error) {
	return EnsureDenoInstalledOrUpdatedContext(context.Background())
}

func EnsureDenoInstalledOrUpdatedContext(ctx context.Context) (DenoStatus, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return DenoStatus{Error: err.Error()}, err
	}

	current, err := checkDenoInstalledContext(ctx)
	if err != nil {
		current.Error = err.Error()
		return current, err
	}
	markerPath, err := getDenoUpdateCheckPath()
	if err != nil {
		current.Error = err.Error()
		return current, err
	}
	checkedAt := time.Now()
	if shouldSkipDenoReleaseCheck(markerPath, checkedAt, current.Installed) {
		return current, nil
	}

	release, err := fetchLatestDenoReleaseContext(ctx)
	if err != nil {
		return denoUpdateFailure(current, "failed to check latest Deno release", err)
	}
	latest := strings.TrimPrefix(strings.TrimSpace(release.TagName), "v")
	current.Latest = latest
	if current.Installed && current.Version == latest {
		if err := recordDenoReleaseCheck(markerPath, checkedAt); err != nil {
			current.Warning = fmt.Sprintf("failed to record Deno update check: %v", err)
		}
		return current, nil
	}

	archive, checksum, ok := SelectDenoAsset(runtime.GOOS, runtime.GOARCH, release.Assets)
	if !ok {
		return denoUpdateFailure(current, "failed to select Deno release asset", fmt.Errorf("Deno release has no archive/checksum pair for %s/%s", runtime.GOOS, runtime.GOARCH))
	}
	path, version, err := installDenoReleaseContext(ctx, archive, checksum)
	if err != nil {
		return denoUpdateFailure(current, "failed to install Deno", err)
	}

	status := DenoStatus{
		Installed: true,
		Path:      path,
		Version:   version,
		Latest:    latest,
		Updated:   true,
	}
	if err := recordDenoReleaseCheck(markerPath, checkedAt); err != nil {
		status.Warning = fmt.Sprintf("failed to record Deno update check: %v", err)
	}
	return status, nil
}

func denoUpdateFailure(current DenoStatus, action string, err error) (DenoStatus, error) {
	if current.Installed && !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
		current.Warning = fmt.Sprintf("%s: %v", action, err)
		return current, nil
	}
	current.Error = err.Error()
	return current, err
}

func checkDenoInstalledContext(ctx context.Context) (DenoStatus, error) {
	path, err := GetDenoPath()
	if err != nil {
		return DenoStatus{Error: err.Error()}, err
	}
	status := DenoStatus{Path: path}
	version, err := validateDenoExecutableContext(ctx, path)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return status, ctxErr
		}
		if !errors.Is(err, os.ErrNotExist) {
			status.Error = err.Error()
		}
		return status, nil
	}
	status.Installed = true
	status.Version = version
	return status, nil
}

func validateDenoExecutableContext(ctx context.Context, path string) (string, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	info, err := os.Stat(path)
	if err != nil {
		return "", fmt.Errorf("stat Deno executable: %w", err)
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("Deno executable is not a regular file: %s", path)
	}
	if info.Size() == 0 {
		return "", fmt.Errorf("Deno executable is empty: %s", path)
	}
	if runtime.GOOS != "windows" && info.Mode()&0111 == 0 {
		return "", fmt.Errorf("Deno file is not executable: %s", path)
	}

	paths, err := CurrentRuntimePaths()
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(paths.DenoCacheDir, 0755); err != nil {
		return "", err
	}
	if err := os.MkdirAll(paths.RunTempDir, 0755); err != nil {
		return "", err
	}
	cmd := denoManagedCommand(ctx, path, "--version")
	cmd.Env = denoPortableEnvironment(cmd.Environ(), paths)
	output, err := cmd.CombinedOutput()
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return "", ctxErr
		}
		return "", fmt.Errorf("validate Deno executable: %w: %s", err, strings.TrimSpace(string(output)))
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	return parseDenoVersion(output)
}

func denoPortableEnvironment(base []string, paths RuntimePaths) []string {
	overrides := map[string]string{
		"DENO_DIR": paths.DenoCacheDir,
		"TMPDIR":   paths.RunTempDir,
		"TMP":      paths.RunTempDir,
		"TEMP":     paths.RunTempDir,
	}
	env := make([]string, 0, len(base)+len(overrides))
	for _, item := range base {
		key, _, _ := strings.Cut(item, "=")
		if _, replaced := overrides[strings.ToUpper(key)]; !replaced {
			env = append(env, item)
		}
	}
	for _, key := range []string{"DENO_DIR", "TMPDIR", "TMP", "TEMP"} {
		env = append(env, key+"="+overrides[key])
	}
	return env
}

func parseDenoVersion(output []byte) (string, error) {
	line := strings.TrimSpace(strings.SplitN(string(output), "\n", 2)[0])
	fields := strings.Fields(line)
	if len(fields) < 2 || !strings.EqualFold(fields[0], "deno") {
		return "", fmt.Errorf("unexpected Deno --version output: %q", line)
	}
	version := strings.TrimPrefix(fields[1], "v")
	if version == "" {
		return "", fmt.Errorf("Deno --version did not include a version")
	}
	return version, nil
}

func fetchLatestDenoReleaseContext(ctx context.Context) (denoRelease, error) {
	if err := validateDenoReleaseDownloadURL(denoLatestReleaseURL); err != nil {
		return denoRelease{}, err
	}
	client := newDenoHTTPClient(30 * time.Second)
	req, err := newHTTPRequestWithContext(ctx, http.MethodGet, denoLatestReleaseURL, nil)
	if err != nil {
		return denoRelease{}, err
	}
	resp, err := client.Do(req)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return denoRelease{}, ctxErr
		}
		return denoRelease{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return denoRelease{}, fmt.Errorf("GitHub returned HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	var release denoRelease
	if err := json.NewDecoder(io.LimitReader(resp.Body, 2*1024*1024)).Decode(&release); err != nil {
		return denoRelease{}, err
	}
	if err := ctx.Err(); err != nil {
		return denoRelease{}, err
	}
	if strings.TrimSpace(release.TagName) == "" || len(release.Assets) == 0 {
		return denoRelease{}, fmt.Errorf("GitHub Deno release response is incomplete")
	}
	return release, nil
}

func newDenoHTTPClient(timeout time.Duration) *http.Client {
	client := denoHTTPClientFactory(timeout)
	client.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if len(via) >= 10 {
			return fmt.Errorf("stopped after 10 Deno release redirects")
		}
		return validateDenoReleaseDownloadURL(req.URL.String())
	}
	return client
}

func validateDenoReleaseDownloadURL(rawURL string) error {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return err
	}
	if !strings.EqualFold(parsed.Scheme, "https") {
		return fmt.Errorf("Deno release URL must use HTTPS")
	}
	switch strings.ToLower(parsed.Hostname()) {
	case "github.com", "api.github.com", "objects.githubusercontent.com", "release-assets.githubusercontent.com":
		return nil
	default:
		return fmt.Errorf("unexpected Deno release host: %s", parsed.Hostname())
	}
}

func installDenoReleaseContext(ctx context.Context, archive, checksum GitHubReleaseAsset) (path, version string, err error) {
	checksumPath, err := downloadDenoAssetContext(ctx, checksum, "deno-checksum-*")
	if err != nil {
		return "", "", err
	}
	defer os.Remove(checksumPath)

	archivePath, err := downloadDenoAssetContext(ctx, archive, "deno-archive-*.zip")
	if err != nil {
		return "", "", err
	}
	defer os.Remove(archivePath)

	expectedHash, err := readDenoChecksum(checksumPath, archive.Name)
	if err != nil {
		return "", "", err
	}
	if err := verifyDenoArchiveChecksumContext(ctx, archivePath, archive.Name, expectedHash); err != nil {
		return "", "", err
	}
	stagedPath, err := extractDenoArchiveToStageContext(ctx, archivePath)
	if err != nil {
		return "", "", err
	}
	defer os.Remove(stagedPath)

	path, err = GetDenoPath()
	if err != nil {
		return "", "", err
	}
	version, err = replaceDenoExecutableContext(ctx, stagedPath, path)
	if err != nil {
		return "", "", err
	}
	return path, version, nil
}

func downloadDenoAssetContext(ctx context.Context, asset GitHubReleaseAsset, pattern string) (path string, err error) {
	if err := validateDenoReleaseDownloadURL(asset.BrowserDownloadURL); err != nil {
		return "", err
	}
	temp, err := CreateRunTemp(pattern)
	if err != nil {
		return "", err
	}
	tempPath := temp.Name()
	path = tempPath
	completed := false
	defer func() {
		if !completed {
			closeErr := temp.Close()
			removeErr := os.Remove(tempPath)
			if closeErr != nil {
				err = errors.Join(err, closeErr)
			}
			if removeErr != nil && !os.IsNotExist(removeErr) {
				err = errors.Join(err, fmt.Errorf("remove partial Deno download: %w", removeErr))
			}
		}
	}()

	client := newDenoHTTPClient(5 * time.Minute)
	req, err := newHTTPRequestWithContext(ctx, http.MethodGet, asset.BrowserDownloadURL, nil)
	if err != nil {
		return "", err
	}
	resp, err := client.Do(req)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return "", ctxErr
		}
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return "", fmt.Errorf("failed to download %s: HTTP %d %s", asset.Name, resp.StatusCode, strings.TrimSpace(string(body)))
	}
	if _, err = io.Copy(temp, resp.Body); err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return "", ctxErr
		}
		return "", err
	}
	if err = temp.Close(); err != nil {
		return "", err
	}
	if err = ctx.Err(); err != nil {
		return "", err
	}
	completed = true
	return path, nil
}

func readDenoChecksum(path, archiveName string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, 2*1024*1024))
	if err != nil {
		return "", err
	}
	return parseDenoChecksum(data, archiveName)
}

func parseDenoChecksum(data []byte, archiveName string) (string, error) {
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 || strings.TrimPrefix(fields[1], "*") != archiveName {
			continue
		}
		hash := strings.ToLower(fields[0])
		decoded, err := hex.DecodeString(hash)
		if err != nil || len(decoded) != sha256.Size {
			return "", fmt.Errorf("invalid Deno checksum for %s", archiveName)
		}
		return hash, nil
	}
	return "", fmt.Errorf("Deno checksum missing for %s", archiveName)
}

func verifyDenoArchiveChecksumContext(ctx context.Context, path, archiveName, expectedHash string) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	hasher := sha256.New()
	buffer := make([]byte, 32*1024)
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		n, readErr := file.Read(buffer)
		if n > 0 {
			_, _ = hasher.Write(buffer[:n])
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			return readErr
		}
	}
	actualHash := hex.EncodeToString(hasher.Sum(nil))
	if !strings.EqualFold(actualHash, expectedHash) {
		return fmt.Errorf("Deno checksum mismatch for %s", archiveName)
	}
	return nil
}

func extractDenoArchiveToStageContext(ctx context.Context, archivePath string) (path string, err error) {
	reader, err := zip.OpenReader(archivePath)
	if err != nil {
		return "", fmt.Errorf("open Deno ZIP: %w", err)
	}
	defer reader.Close()
	expectedName := "deno"
	stagePattern := ".deno-stage-*"
	if runtime.GOOS == "windows" {
		expectedName = "deno.exe"
		stagePattern = ".deno-stage-*.exe"
	}
	if len(reader.File) != 1 || reader.File[0].Name != expectedName {
		return "", fmt.Errorf("Deno ZIP must contain only root-level %s", expectedName)
	}
	entry := reader.File[0]
	if !entry.FileInfo().Mode().IsRegular() {
		return "", fmt.Errorf("Deno ZIP executable is not a regular file")
	}
	if entry.UncompressedSize64 == 0 {
		return "", fmt.Errorf("Deno ZIP executable is empty")
	}

	paths, err := CurrentRuntimePaths()
	if err != nil {
		return "", err
	}
	staged, err := os.CreateTemp(paths.BinDir, stagePattern)
	if err != nil {
		return "", err
	}
	stagePath := staged.Name()
	path = stagePath
	completed := false
	defer func() {
		if !completed {
			_ = staged.Close()
			if removeErr := os.Remove(stagePath); removeErr != nil && !os.IsNotExist(removeErr) {
				err = errors.Join(err, fmt.Errorf("remove staged Deno executable: %w", removeErr))
			}
		}
	}()
	source, err := entry.Open()
	if err != nil {
		return "", err
	}
	defer source.Close()
	buffer := make([]byte, 32*1024)
	for {
		if err = ctx.Err(); err != nil {
			return "", err
		}
		var n int
		n, err = source.Read(buffer)
		if n > 0 {
			if _, writeErr := staged.Write(buffer[:n]); writeErr != nil {
				return "", writeErr
			}
		}
		if err == io.EOF {
			err = nil
			break
		}
		if err != nil {
			return "", err
		}
	}
	if err = staged.Close(); err != nil {
		return "", err
	}
	if runtime.GOOS != "windows" {
		if err = os.Chmod(path, 0755); err != nil {
			return "", err
		}
	}
	info, err := os.Stat(path)
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() || info.Size() == 0 {
		return "", fmt.Errorf("staged Deno executable is invalid")
	}
	completed = true
	return path, nil
}

func replaceDenoExecutableContext(ctx context.Context, stagedPath, finalPath string) (string, error) {
	if _, err := validateDenoExecutableContext(ctx, stagedPath); err != nil {
		return "", fmt.Errorf("validate staged Deno: %w", err)
	}
	backupPath := finalPath + ".bak"
	hadCurrent := false
	if info, err := os.Lstat(finalPath); err == nil {
		if !info.Mode().IsRegular() {
			return "", fmt.Errorf("installed Deno is not a regular file")
		}
		if _, backupErr := os.Lstat(backupPath); backupErr == nil {
			return "", fmt.Errorf("Deno backup already exists: %s", backupPath)
		} else if !os.IsNotExist(backupErr) {
			return "", backupErr
		}
		if err := os.Rename(finalPath, backupPath); err != nil {
			return "", err
		}
		hadCurrent = true
	} else if !os.IsNotExist(err) {
		return "", err
	}

	if err := os.Rename(stagedPath, finalPath); err != nil {
		if hadCurrent {
			if rollbackErr := os.Rename(backupPath, finalPath); rollbackErr != nil {
				return "", errors.Join(err, fmt.Errorf("restore Deno backup: %w", rollbackErr))
			}
		}
		return "", err
	}
	version, validationErr := validateDenoExecutableContext(ctx, finalPath)
	if validationErr != nil {
		rollbackErr := os.Remove(finalPath)
		if hadCurrent {
			rollbackErr = errors.Join(rollbackErr, os.Rename(backupPath, finalPath))
		}
		if rollbackErr != nil {
			return "", errors.Join(validationErr, fmt.Errorf("rollback Deno update: %w", rollbackErr))
		}
		return "", validationErr
	}
	if hadCurrent {
		if err := os.Remove(backupPath); err != nil {
			return "", err
		}
	}
	return version, nil
}
