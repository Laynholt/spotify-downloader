package backend

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
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
	ytDlpLatestReleaseURL    = "https://api.github.com/repos/yt-dlp/yt-dlp/releases/latest"
	ytDlpUpdateCheckFilename = "yt-dlp.update-check"
	ytDlpUpdateCheckTTL      = 24 * time.Hour
)

var ytDlpHTTPClientFactory = newHTTPClient

type GitHubReleaseAsset struct {
	Name               string `json:"name"`
	BrowserDownloadURL string `json:"browser_download_url"`
}

type ytDlpRelease struct {
	TagName string               `json:"tag_name"`
	Assets  []GitHubReleaseAsset `json:"assets"`
}

type YtDlpStatus struct {
	Installed bool   `json:"installed"`
	Path      string `json:"path,omitempty"`
	Version   string `json:"version,omitempty"`
	Latest    string `json:"latest,omitempty"`
	Updated   bool   `json:"updated,omitempty"`
	Warning   string `json:"warning,omitempty"`
	Error     string `json:"error,omitempty"`
}

type YtDlpDownloadRequest struct {
	Query       string `json:"query"`
	OutputBase  string `json:"output_base"`
	AudioFormat string `json:"audio_format"`
}

type YtDlpDownloadResult struct {
	Success bool   `json:"success"`
	File    string `json:"file,omitempty"`
	Error   string `json:"error,omitempty"`
	Output  string `json:"output,omitempty"`
}

func SelectYtDlpAsset(goos, goarch string, assets []GitHubReleaseAsset) (GitHubReleaseAsset, bool) {
	candidates := []string{"yt-dlp"}
	switch strings.ToLower(goos) {
	case "windows":
		if strings.EqualFold(goarch, "arm64") {
			candidates = []string{"yt-dlp_arm64.exe", "yt-dlp.exe"}
		} else if strings.EqualFold(goarch, "386") {
			candidates = []string{"yt-dlp_x86.exe", "yt-dlp.exe"}
		} else {
			candidates = []string{"yt-dlp.exe"}
		}
	case "linux":
		if strings.EqualFold(goarch, "arm64") {
			candidates = []string{"yt-dlp_linux_aarch64", "yt-dlp"}
		} else {
			candidates = []string{"yt-dlp_linux", "yt-dlp"}
		}
	case "darwin":
		candidates = []string{"yt-dlp_macos", "yt-dlp"}
	}

	for _, candidate := range candidates {
		for _, asset := range assets {
			if asset.Name == candidate {
				return asset, true
			}
		}
	}

	return GitHubReleaseAsset{}, false
}

func GetYtDlpDir() (string, error) {
	paths, err := CurrentRuntimePaths()
	if err != nil {
		return "", err
	}
	return paths.BinDir, nil
}

func GetYtDlpPath() (string, error) {
	paths, err := CurrentRuntimePaths()
	if err != nil {
		return "", err
	}

	name := "yt-dlp"
	if runtime.GOOS == "windows" {
		name = "yt-dlp.exe"
	}
	return filepath.Join(paths.BinDir, name), nil
}

func getYtDlpUpdateCheckPath() (string, error) {
	dir, err := GetYtDlpDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, ytDlpUpdateCheckFilename), nil
}

func shouldSkipYtDlpReleaseCheck(current YtDlpStatus, markerPath string, now time.Time) bool {
	if !current.Installed {
		return false
	}
	info, err := os.Stat(markerPath)
	if err != nil || info.IsDir() {
		return false
	}
	return now.Sub(info.ModTime()) < ytDlpUpdateCheckTTL
}

func recordYtDlpReleaseCheck(markerPath string, checkedAt time.Time) error {
	if err := os.MkdirAll(filepath.Dir(markerPath), 0755); err != nil {
		return err
	}
	if err := os.WriteFile(markerPath, []byte(checkedAt.UTC().Format(time.RFC3339Nano)), 0600); err != nil {
		return err
	}
	return os.Chtimes(markerPath, checkedAt, checkedAt)
}

func CheckYtDlpInstalled() (YtDlpStatus, error) {
	return CheckYtDlpInstalledContext(context.Background())
}

func CheckYtDlpInstalledContext(ctx context.Context) (YtDlpStatus, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return YtDlpStatus{Installed: false, Error: err.Error()}, err
	}
	path, err := GetYtDlpPath()
	if err != nil {
		return YtDlpStatus{Installed: false, Error: err.Error()}, err
	}

	status := YtDlpStatus{Path: path}
	if _, err := os.Stat(path); err != nil {
		status.Installed = false
		if !os.IsNotExist(err) {
			status.Error = err.Error()
		}
		return status, nil
	}

	cmd := newManagedCommand(ctx, path, "--version")
	output, err := cmd.Output()
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return status, ctxErr
		}
		status.Error = err.Error()
		return status, nil
	}
	if err := ctx.Err(); err != nil {
		return status, err
	}

	status.Installed = true
	status.Version = strings.TrimSpace(string(output))
	return status, nil
}

func EnsureYtDlpInstalledOrUpdated() (YtDlpStatus, error) {
	return EnsureYtDlpInstalledOrUpdatedContext(context.Background())
}

func EnsureYtDlpInstalledOrUpdatedContext(ctx context.Context) (YtDlpStatus, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return YtDlpStatus{Error: err.Error()}, err
	}
	current, checkErr := CheckYtDlpInstalledContext(ctx)
	if checkErr != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return current, ctxErr
		}
	}
	checkedAt := time.Now()
	markerPath, markerErr := getYtDlpUpdateCheckPath()
	if markerErr == nil && shouldSkipYtDlpReleaseCheck(current, markerPath, checkedAt) {
		return current, nil
	}
	if err := ctx.Err(); err != nil {
		return current, err
	}
	release, err := fetchLatestYtDlpReleaseContext(ctx)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			current.Error = ctxErr.Error()
			return current, ctxErr
		}
		if current.Installed {
			current.Warning = fmt.Sprintf("failed to check latest yt-dlp release: %v", err)
			return current, nil
		}
		current.Error = err.Error()
		return current, err
	}
	if markerErr == nil {
		if err := recordYtDlpReleaseCheck(markerPath, checkedAt); err != nil {
			current.Warning = fmt.Sprintf("failed to record yt-dlp update check: %v", err)
		}
	}
	if err := ctx.Err(); err != nil {
		return current, err
	}

	current.Latest = strings.TrimPrefix(release.TagName, "v")
	if current.Installed && current.Version == current.Latest {
		return current, nil
	}

	asset, ok := SelectYtDlpAsset(runtime.GOOS, runtime.GOARCH, release.Assets)
	if !ok {
		err := fmt.Errorf("yt-dlp release has no asset for %s", runtime.GOOS)
		if current.Installed {
			current.Warning = err.Error()
			return current, nil
		}
		current.Error = err.Error()
		return current, err
	}

	path, err := downloadYtDlpAssetContext(ctx, asset, release.Assets)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			current.Error = ctxErr.Error()
			return current, ctxErr
		}
		if current.Installed {
			current.Warning = fmt.Sprintf("failed to update yt-dlp: %v", err)
			return current, nil
		}
		current.Error = err.Error()
		return current, err
	}

	updated, checkErr := CheckYtDlpInstalledContext(ctx)
	updated.Path = path
	updated.Latest = current.Latest
	updated.Updated = true
	if checkErr != nil {
		return updated, checkErr
	}
	return updated, nil
}

func DownloadWithYtDlp(req YtDlpDownloadRequest) YtDlpDownloadResult {
	return DownloadWithYtDlpContext(context.Background(), req)
}

func DownloadWithYtDlpContext(ctx context.Context, req YtDlpDownloadRequest) YtDlpDownloadResult {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return YtDlpDownloadResult{Error: err.Error()}
	}
	if strings.TrimSpace(req.Query) == "" {
		return YtDlpDownloadResult{Error: "query is required"}
	}
	if strings.TrimSpace(req.OutputBase) == "" {
		return YtDlpDownloadResult{Error: "output base is required"}
	}

	status, err := EnsureYtDlpInstalledOrUpdatedContext(ctx)
	if err != nil {
		return YtDlpDownloadResult{Error: err.Error()}
	}
	if err := ctx.Err(); err != nil {
		return YtDlpDownloadResult{Error: err.Error()}
	}

	dir := filepath.Dir(req.OutputBase)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return YtDlpDownloadResult{Error: err.Error()}
	}

	audioFormat := normalizeYtDlpAudioFormat(req.AudioFormat)
	outputTemplate := req.OutputBase + ".%(ext)s"
	args := []string{
		"--no-playlist",
		"--newline",
		"--extract-audio",
		"--audio-format", audioFormat,
		"--audio-quality", "0",
		"-o", outputTemplate,
	}
	if ffmpegPath, err := GetFFmpegPath(); err == nil && ffmpegPath != "" {
		args = append(args, "--ffmpeg-location", filepath.Dir(ffmpegPath))
	}
	args = append(args, req.Query)

	cmd := newManagedCommand(ctx, status.Path, args...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return YtDlpDownloadResult{Error: ctxErr.Error(), Output: strings.TrimSpace(string(output))}
		}
		return YtDlpDownloadResult{Error: fmt.Sprintf("yt-dlp failed: %v", err), Output: strings.TrimSpace(string(output))}
	}
	if err := ctx.Err(); err != nil {
		return YtDlpDownloadResult{Error: err.Error(), Output: strings.TrimSpace(string(output))}
	}

	file := findYtDlpOutputFile(req.OutputBase, audioFormat)
	if file == "" {
		return YtDlpDownloadResult{Error: "yt-dlp completed but output file was not found", Output: strings.TrimSpace(string(output))}
	}

	return YtDlpDownloadResult{Success: true, File: file, Output: strings.TrimSpace(string(output))}
}

func fetchLatestYtDlpRelease() (ytDlpRelease, error) {
	return fetchLatestYtDlpReleaseContext(context.Background())
}

func fetchLatestYtDlpReleaseContext(ctx context.Context) (ytDlpRelease, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return ytDlpRelease{}, err
	}
	client := ytDlpHTTPClientFactory(30 * time.Second)
	req, err := newHTTPRequestWithContext(ctx, http.MethodGet, ytDlpLatestReleaseURL, nil)
	if err != nil {
		return ytDlpRelease{}, err
	}
	resp, err := client.Do(req)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ytDlpRelease{}, ctxErr
		}
		return ytDlpRelease{}, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ytDlpRelease{}, ctxErr
		}
		return ytDlpRelease{}, fmt.Errorf("GitHub returned HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var release ytDlpRelease
	if err := json.NewDecoder(resp.Body).Decode(&release); err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ytDlpRelease{}, ctxErr
		}
		return ytDlpRelease{}, err
	}
	if err := ctx.Err(); err != nil {
		return ytDlpRelease{}, err
	}
	if strings.TrimSpace(release.TagName) == "" {
		return ytDlpRelease{}, fmt.Errorf("GitHub release response did not include tag_name")
	}
	return release, nil
}

func downloadYtDlpAsset(asset GitHubReleaseAsset, releaseAssets []GitHubReleaseAsset) (string, error) {
	return downloadYtDlpAssetContext(context.Background(), asset, releaseAssets)
}

func downloadYtDlpAssetContext(ctx context.Context, asset GitHubReleaseAsset, releaseAssets []GitHubReleaseAsset) (path string, err error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	path, err = GetYtDlpPath()
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return "", err
	}
	if err := validateGitHubReleaseDownloadURL(asset.BrowserDownloadURL); err != nil {
		return "", err
	}

	client := ytDlpHTTPClientFactory(5 * time.Minute)
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
		if ctxErr := ctx.Err(); ctxErr != nil {
			return "", ctxErr
		}
		return "", fmt.Errorf("failed to download yt-dlp: HTTP %d %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	tmpPath := path + ".tmp"
	defer func() {
		if err != nil || ctx.Err() != nil {
			_ = os.Remove(tmpPath)
		}
	}()
	out, err := os.OpenFile(tmpPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0755)
	if err != nil {
		return "", err
	}
	copyErr := error(nil)
	hasher := sha256.New()
	if _, err := io.Copy(io.MultiWriter(out, hasher), resp.Body); err != nil {
		copyErr = err
	}
	closeErr := out.Close()
	if copyErr != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return "", ctxErr
		}
		_ = os.Remove(tmpPath)
		return "", copyErr
	}
	if closeErr != nil {
		_ = os.Remove(tmpPath)
		return "", closeErr
	}
	if err := verifyDownloadedAssetChecksumContext(ctx, asset.Name, hex.EncodeToString(hasher.Sum(nil)), releaseAssets); err != nil {
		_ = os.Remove(tmpPath)
		return "", err
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if runtime.GOOS != "windows" {
		_ = os.Chmod(tmpPath, 0755)
	}
	if err := replaceFileWithTemp(tmpPath, path); err != nil {
		_ = os.Remove(tmpPath)
		return "", err
	}
	return path, nil
}

func validateGitHubReleaseDownloadURL(rawURL string) error {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return err
	}
	if parsed.Scheme != "https" || parsed.Hostname() != "github.com" {
		return fmt.Errorf("unexpected release asset host: %s", rawURL)
	}
	return nil
}

func verifyDownloadedAssetChecksum(assetName, actualHash string, releaseAssets []GitHubReleaseAsset) error {
	return verifyDownloadedAssetChecksumContext(context.Background(), assetName, actualHash, releaseAssets)
}

func verifyDownloadedAssetChecksumContext(ctx context.Context, assetName, actualHash string, releaseAssets []GitHubReleaseAsset) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	checksumAsset, ok := findReleaseAsset("SHA2-256SUMS", releaseAssets)
	if !ok {
		return fmt.Errorf("yt-dlp release does not include SHA2-256SUMS")
	}
	if err := validateGitHubReleaseDownloadURL(checksumAsset.BrowserDownloadURL); err != nil {
		return err
	}
	client := ytDlpHTTPClientFactory(30 * time.Second)
	req, err := newHTTPRequestWithContext(ctx, http.MethodGet, checksumAsset.BrowserDownloadURL, nil)
	if err != nil {
		return err
	}
	resp, err := client.Do(req)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
		return fmt.Errorf("failed to download yt-dlp checksums: HTTP %d %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 2*1024*1024))
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	expectedHash := parseSHA256Sums(body)[assetName]
	if expectedHash == "" {
		return fmt.Errorf("yt-dlp checksum missing for %s", assetName)
	}
	if !strings.EqualFold(expectedHash, actualHash) {
		return fmt.Errorf("yt-dlp checksum mismatch for %s", assetName)
	}
	return nil
}

func findReleaseAsset(name string, assets []GitHubReleaseAsset) (GitHubReleaseAsset, bool) {
	for _, asset := range assets {
		if asset.Name == name {
			return asset, true
		}
	}
	return GitHubReleaseAsset{}, false
}

func parseSHA256Sums(data []byte) map[string]string {
	hashes := make(map[string]string)
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		name := strings.TrimPrefix(fields[1], "*")
		hashes[name] = fields[0]
	}
	return hashes
}

func normalizeYtDlpAudioFormat(audioFormat string) string {
	switch strings.ToLower(strings.TrimSpace(audioFormat)) {
	case "flac":
		return "flac"
	case "m4a", "aac", "mp4":
		return "m4a"
	default:
		return "mp3"
	}
}

func findYtDlpOutputFile(outputBase, audioFormat string) string {
	for _, ext := range PossibleAudioExtensions(audioFormat) {
		path := outputBase + ext
		if info, err := os.Stat(path); err == nil && info.Size() > 0 {
			return path
		}
	}
	for _, ext := range []string{".webm", ".opus"} {
		path := outputBase + ext
		if info, err := os.Stat(path); err == nil && info.Size() > 0 {
			return path
		}
	}
	return ""
}
