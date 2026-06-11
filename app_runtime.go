package main

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/afkarxyz/SpotiDownloader/backend"
	"github.com/wailsapp/wails/v2/pkg/runtime"
)

type TokenResponse struct {
	Token     string `json:"token"`
	ExpiresAt int64  `json:"expires_at"`
}

type DownloadFFmpegResponse struct {
	Success bool   `json:"success"`
	Message string `json:"message"`
	Error   string `json:"error,omitempty"`
}

func (a *App) OpenFolder(path string) error {
	if path == "" {
		return fmt.Errorf("path is required")
	}
	if err := backend.OpenFolderInExplorer(path); err != nil {
		return fmt.Errorf("failed to open folder: %v", err)
	}
	return nil
}

func (a *App) SelectFolder(defaultPath string) (string, error) {
	return backend.SelectFolderDialog(a.ctx, defaultPath)
}

func (a *App) SelectFile() (string, error) {
	return backend.SelectFileDialog(a.ctx)
}

func (a *App) AnalyzeTrack(filePath string) (string, error) {
	if filePath == "" {
		return "", fmt.Errorf("file path is required")
	}

	result, err := backend.AnalyzeTrack(filePath)
	if err != nil {
		return "", fmt.Errorf("failed to analyze track: %v", err)
	}

	jsonData, err := json.Marshal(result)
	if err != nil {
		return "", fmt.Errorf("failed to encode response: %v", err)
	}

	return string(jsonData), nil
}

func (a *App) GetDefaults() map[string]string {
	return map[string]string{
		"downloadPath": backend.GetDefaultMusicPath(),
	}
}

func (a *App) FetchSessionTokenWithParams(timeout int, retry int) (TokenResponse, error) {
	token, err := backend.FetchSessionTokenWithParams(timeout, retry)
	if err != nil {
		if err == backend.ErrChromeNotInstalled {
			message := backend.GetChromeInstallationMessage()
			return TokenResponse{}, fmt.Errorf("CHROME_NOT_INSTALLED: %s", message)
		}
		return TokenResponse{}, fmt.Errorf("failed to fetch session token: %v", err)
	}

	backend.RememberSessionToken(token)

	return TokenResponse{
		Token:     token,
		ExpiresAt: time.Now().Add(3 * time.Minute).Unix(),
	}, nil
}

func (a *App) CheckFFmpegInstalled() (bool, error) {
	return backend.IsFFmpegInstalled()
}

func (a *App) IsFFprobeInstalled() (bool, error) {
	return backend.IsFFprobeInstalled()
}

func (a *App) GetFFmpegPath() (string, error) {
	return backend.GetFFmpegPath()
}

func (a *App) UploadImage(filePath string) (string, error) {
	if err := backend.ValidateUploadMediaFile(filePath); err != nil {
		return "", err
	}
	return backend.UploadToSendNow(filePath)
}

func (a *App) UploadImageBytes(filename string, base64Data string) (string, error) {
	if idx := strings.Index(base64Data, ","); idx != -1 {
		base64Data = base64Data[idx+1:]
	}

	data, err := base64.StdEncoding.DecodeString(base64Data)
	if err != nil {
		return "", fmt.Errorf("failed to decode base64: %v", err)
	}
	return backend.UploadBytesToSendNow(filename, data)
}

func (a *App) SelectImageVideo() ([]string, error) {
	return backend.SelectImageVideoDialog(a.ctx)
}

func (a *App) DownloadFFmpeg() DownloadFFmpegResponse {
	runtime.EventsEmit(a.ctx, "ffmpeg:status", "starting")
	err := backend.DownloadFFmpeg(func(progress int) {
		runtime.EventsEmit(a.ctx, "ffmpeg:progress", progress)
	})
	if err != nil {
		runtime.EventsEmit(a.ctx, "ffmpeg:status", "failed")
		return DownloadFFmpegResponse{Success: false, Error: err.Error()}
	}

	runtime.EventsEmit(a.ctx, "ffmpeg:status", "completed")
	return DownloadFFmpegResponse{Success: true, Message: "FFmpeg installed successfully"}
}

func (a *App) CheckAppUpdate(currentVersion string) (backend.AppUpdateStatus, error) {
	return backend.CheckAppUpdate(currentVersion)
}

func (a *App) DownloadAppUpdate(currentVersion string) (backend.AppUpdateResult, error) {
	runtime.EventsEmit(a.ctx, "app-update:status", "downloading")
	result, err := backend.DownloadAppUpdate(currentVersion, func(progress int) {
		runtime.EventsEmit(a.ctx, "app-update:progress", progress)
	})
	if err != nil {
		runtime.EventsEmit(a.ctx, "app-update:status", "failed")
		return result, err
	}
	if result.Success {
		runtime.EventsEmit(a.ctx, "app-update:status", "restarting")
		go func() {
			time.Sleep(500 * time.Millisecond)
			runtime.Quit(a.ctx)
		}()
	}
	return result, nil
}

func (a *App) GetPreviewURL(trackID string) (string, error) {
	return backend.GetPreviewURL(trackID)
}

func (a *App) GetOSInfo() (string, error) {
	return backend.GetOSInfo()
}

func (a *App) Quit() {
	if a.ctx != nil {
		runtime.Quit(a.ctx)
	}
}

func (a *App) GetConfigPath() (string, error) {
	dir, err := backend.GetFFmpegDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "config.json"), nil
}
