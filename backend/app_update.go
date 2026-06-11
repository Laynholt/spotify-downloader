package backend

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
)

const appLatestReleaseURL = "https://api.github.com/repos/Laynholt/spotify-downloader/releases/latest"

type appRelease struct {
	TagName     string               `json:"tag_name"`
	HTMLURL     string               `json:"html_url"`
	PublishedAt string               `json:"published_at"`
	Assets      []GitHubReleaseAsset `json:"assets"`
}

type AppUpdateStatus struct {
	Available      bool   `json:"available"`
	CurrentVersion string `json:"current_version"`
	LatestVersion  string `json:"latest_version"`
	ReleaseDate    string `json:"release_date,omitempty"`
	ReleaseURL     string `json:"release_url,omitempty"`
	AssetName      string `json:"asset_name,omitempty"`
	AssetURL       string `json:"asset_url,omitempty"`
	Platform       string `json:"platform"`
	Error          string `json:"error,omitempty"`
}

type AppUpdateResult struct {
	Success        bool   `json:"success"`
	Message        string `json:"message"`
	DownloadedPath string `json:"downloaded_path,omitempty"`
	TargetPath     string `json:"target_path,omitempty"`
	ScriptPath     string `json:"script_path,omitempty"`
	Error          string `json:"error,omitempty"`
}

func CheckAppUpdate(currentVersion string) (AppUpdateStatus, error) {
	status := AppUpdateStatus{
		CurrentVersion: strings.TrimPrefix(strings.TrimSpace(currentVersion), "v"),
		Platform:       runtime.GOOS + "/" + runtime.GOARCH,
	}
	release, err := fetchLatestAppRelease()
	if err != nil {
		status.Error = err.Error()
		return status, err
	}
	status.LatestVersion = strings.TrimPrefix(strings.TrimSpace(release.TagName), "v")
	status.ReleaseDate = release.PublishedAt
	status.ReleaseURL = release.HTMLURL
	status.Available = IsAppVersionNewer(status.LatestVersion, status.CurrentVersion)
	if !status.Available {
		return status, nil
	}
	asset, ok := SelectAppUpdateAsset(runtime.GOOS, runtime.GOARCH, release.Assets)
	if !ok {
		err := fmt.Errorf("release has no update asset for %s", status.Platform)
		status.Error = err.Error()
		return status, err
	}
	status.AssetName = asset.Name
	status.AssetURL = asset.BrowserDownloadURL
	return status, nil
}

func DownloadAppUpdate(currentVersion string, progressCallback func(int)) (AppUpdateResult, error) {
	status, err := CheckAppUpdate(currentVersion)
	if err != nil {
		return AppUpdateResult{Error: err.Error()}, err
	}
	if !status.Available {
		return AppUpdateResult{Success: false, Message: "Application is already up to date"}, nil
	}

	targetPath, err := currentAppTargetPath()
	if err != nil {
		return AppUpdateResult{Error: err.Error()}, err
	}
	downloadPath := filepath.Join(filepath.Dir(targetPath), status.AssetName+".download")
	if runtime.GOOS == "darwin" {
		downloadPath = filepath.Join(filepath.Dir(targetPath), status.AssetName)
	}
	if err := downloadUpdateAsset(status.AssetURL, downloadPath, progressCallback); err != nil {
		return AppUpdateResult{Error: err.Error()}, err
	}
	scriptPath, err := scheduleAppUpdateScript(downloadPath, targetPath)
	if err != nil {
		return AppUpdateResult{Error: err.Error(), DownloadedPath: downloadPath, TargetPath: targetPath}, err
	}
	return AppUpdateResult{
		Success:        true,
		Message:        "Update downloaded. Application will restart to apply it.",
		DownloadedPath: downloadPath,
		TargetPath:     targetPath,
		ScriptPath:     scriptPath,
	}, nil
}

func SelectAppUpdateAsset(goos, goarch string, assets []GitHubReleaseAsset) (GitHubReleaseAsset, bool) {
	candidates := appUpdateAssetCandidates(goos, goarch)
	for _, candidate := range candidates {
		for _, asset := range assets {
			if strings.EqualFold(asset.Name, candidate) {
				return asset, true
			}
		}
	}
	for _, asset := range assets {
		name := strings.ToLower(asset.Name)
		switch strings.ToLower(goos) {
		case "windows":
			if strings.HasSuffix(name, ".exe") {
				return asset, true
			}
		case "linux":
			if strings.HasSuffix(name, ".appimage") {
				return asset, true
			}
		case "darwin":
			if strings.EqualFold(goarch, "amd64") && strings.Contains(name, "intel") && strings.HasSuffix(name, ".dmg") {
				return asset, true
			}
			if strings.EqualFold(goarch, "arm64") && !strings.Contains(name, "intel") && strings.HasSuffix(name, ".dmg") {
				return asset, true
			}
		}
	}
	return GitHubReleaseAsset{}, false
}

func IsAppVersionNewer(latest, current string) bool {
	latestParts := parseVersionParts(latest)
	currentParts := parseVersionParts(current)
	maxLen := len(latestParts)
	if len(currentParts) > maxLen {
		maxLen = len(currentParts)
	}
	if maxLen < 3 {
		maxLen = 3
	}
	for i := 0; i < maxLen; i++ {
		latestPart := 0
		currentPart := 0
		if i < len(latestParts) {
			latestPart = latestParts[i]
		}
		if i < len(currentParts) {
			currentPart = currentParts[i]
		}
		if latestPart > currentPart {
			return true
		}
		if latestPart < currentPart {
			return false
		}
	}
	return false
}

func appUpdateAssetCandidates(goos, goarch string) []string {
	switch strings.ToLower(goos) {
	case "windows":
		return []string{"SpotiDownloader.exe"}
	case "linux":
		return []string{"SpotiDownloader.AppImage"}
	case "darwin":
		if strings.EqualFold(goarch, "amd64") {
			return []string{"SpotiDownloader-Intel.dmg"}
		}
		return []string{"SpotiDownloader.dmg"}
	default:
		return nil
	}
}

func parseVersionParts(value string) []int {
	cleaned := strings.TrimPrefix(strings.TrimSpace(value), "v")
	fields := strings.FieldsFunc(cleaned, func(r rune) bool {
		return r == '-' || r == '+'
	})
	if len(fields) == 0 {
		return nil
	}
	cleaned = fields[0]
	parts := strings.Split(cleaned, ".")
	result := make([]int, 0, len(parts))
	for _, part := range parts {
		parsed, err := strconv.Atoi(part)
		if err != nil {
			result = append(result, 0)
			continue
		}
		result = append(result, parsed)
	}
	return result
}

func fetchLatestAppRelease() (appRelease, error) {
	resp, err := newHTTPClient(30 * time.Second).Get(appLatestReleaseURL)
	if err != nil {
		return appRelease{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return appRelease{}, fmt.Errorf("GitHub returned HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	var release appRelease
	if err := json.NewDecoder(resp.Body).Decode(&release); err != nil {
		return appRelease{}, err
	}
	if strings.TrimSpace(release.TagName) == "" {
		return appRelease{}, fmt.Errorf("GitHub release response did not include tag_name")
	}
	return release, nil
}

func downloadUpdateAsset(assetURL, downloadPath string, progressCallback func(int)) error {
	if err := validateGitHubReleaseDownloadURL(assetURL); err != nil {
		return err
	}
	resp, err := newHTTPClient(10 * time.Minute).Get(assetURL)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("failed to download update: HTTP %d %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	tmpPath := downloadPath + ".tmp"
	out, err := os.OpenFile(tmpPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0755)
	if err != nil {
		return err
	}
	defer os.Remove(tmpPath)

	totalSize := resp.ContentLength
	downloaded := int64(0)
	buf := make([]byte, 64*1024)
	for {
		n, readErr := resp.Body.Read(buf)
		if n > 0 {
			if _, err := out.Write(buf[:n]); err != nil {
				out.Close()
				return err
			}
			downloaded += int64(n)
			if totalSize > 0 && progressCallback != nil {
				progressCallback(int(float64(downloaded) / float64(totalSize) * 100))
			}
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			out.Close()
			return readErr
		}
	}
	if err := out.Close(); err != nil {
		return err
	}
	if progressCallback != nil {
		progressCallback(100)
	}
	return replaceFileWithTemp(tmpPath, downloadPath)
}

func currentAppTargetPath() (string, error) {
	executablePath, err := os.Executable()
	if err != nil {
		return "", err
	}
	if runtime.GOOS == "linux" {
		if appImagePath := strings.TrimSpace(os.Getenv("APPIMAGE")); appImagePath != "" {
			return appImagePath, nil
		}
	}
	if runtime.GOOS == "darwin" {
		bundleMarker := ".app" + string(filepath.Separator)
		if idx := strings.Index(executablePath, bundleMarker); idx >= 0 {
			return executablePath[:idx+len(".app")], nil
		}
	}
	return executablePath, nil
}

func scheduleAppUpdateScript(downloadPath, targetPath string) (string, error) {
	switch runtime.GOOS {
	case "windows":
		return scheduleWindowsUpdateScript(downloadPath, targetPath, os.Getpid())
	case "linux":
		return scheduleUnixExecutableUpdateScript(downloadPath, targetPath, os.Getpid())
	case "darwin":
		return scheduleMacOSUpdateScript(downloadPath, targetPath, os.Getpid())
	default:
		return "", fmt.Errorf("unsupported operating system: %s", runtime.GOOS)
	}
}

func scheduleWindowsUpdateScript(downloadPath, targetPath string, pid int) (string, error) {
	scriptPath := filepath.Join(os.TempDir(), fmt.Sprintf("spotidownloader-update-%d.cmd", time.Now().UnixNano()))
	script := fmt.Sprintf(`@echo off
setlocal
set "PID=%d"
set "SRC=%s"
set "DST=%s"
:wait
tasklist /FI "PID eq %%PID%%" | find "%%PID%%" >nul
if not errorlevel 1 (
  timeout /t 1 /nobreak >nul
  goto wait
)
copy /Y "%%SRC%%" "%%DST%%" >nul
if errorlevel 1 exit /b 1
start "" "%%DST%%"
del /Q "%%SRC%%" >nul 2>nul
del /Q "%%~f0" >nul 2>nul
`, pid, downloadPath, targetPath)
	if err := os.WriteFile(scriptPath, []byte(script), 0600); err != nil {
		return "", err
	}
	cmd := exec.Command("cmd", "/C", "start", "", "/MIN", scriptPath)
	setHideWindow(cmd)
	if err := cmd.Start(); err != nil {
		return "", err
	}
	return scriptPath, nil
}

func scheduleUnixExecutableUpdateScript(downloadPath, targetPath string, pid int) (string, error) {
	scriptPath := filepath.Join(os.TempDir(), fmt.Sprintf("spotidownloader-update-%d.sh", time.Now().UnixNano()))
	script := fmt.Sprintf(`#!/bin/sh
PID=%d
SRC=%s
DST=%s
while kill -0 "$PID" 2>/dev/null; do
  sleep 1
done
chmod +x "$SRC"
mv -f "$SRC" "$DST"
nohup "$DST" >/dev/null 2>&1 &
rm -f "$0"
`, pid, shellQuote(downloadPath), shellQuote(targetPath))
	if err := os.WriteFile(scriptPath, []byte(script), 0700); err != nil {
		return "", err
	}
	if err := exec.Command("sh", scriptPath).Start(); err != nil {
		return "", err
	}
	return scriptPath, nil
}

func scheduleMacOSUpdateScript(downloadPath, targetPath string, pid int) (string, error) {
	if !strings.HasSuffix(targetPath, ".app") {
		return "", fmt.Errorf("current executable is not inside a macOS app bundle")
	}
	scriptPath := filepath.Join(os.TempDir(), fmt.Sprintf("spotidownloader-update-%d.sh", time.Now().UnixNano()))
	script := fmt.Sprintf(`#!/bin/sh
PID=%d
DMG=%s
APP=%s
while kill -0 "$PID" 2>/dev/null; do
  sleep 1
done
MOUNT=$(mktemp -d)
hdiutil attach "$DMG" -mountpoint "$MOUNT" -nobrowse -quiet || exit 1
NEW_APP=$(find "$MOUNT" -maxdepth 1 -name "*.app" -type d | head -n 1)
if [ -z "$NEW_APP" ]; then
  hdiutil detach "$MOUNT" -quiet
  exit 1
fi
rm -rf "$APP"
ditto "$NEW_APP" "$APP"
hdiutil detach "$MOUNT" -quiet
open "$APP"
rm -f "$DMG"
rm -f "$0"
`, pid, shellQuote(downloadPath), shellQuote(targetPath))
	if err := os.WriteFile(scriptPath, []byte(script), 0700); err != nil {
		return "", err
	}
	if err := exec.Command("sh", scriptPath).Start(); err != nil {
		return "", err
	}
	return scriptPath, nil
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'"
}
