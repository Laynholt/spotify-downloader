package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"

	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/afkarxyz/SpotiDownloader/backend"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/runtime"
)

type activeDownload struct {
	QueueID string
	Context context.Context
	Cancel  context.CancelFunc
}

type App struct {
	ctx                        context.Context
	runtimePaths               backend.RuntimePaths
	runtimeInitErr             error
	metadataCache              *backend.MetadataCache
	activeMu                   sync.Mutex
	activeWG                   sync.WaitGroup
	shuttingDown               bool
	activeDownloads            map[string]activeDownload
	runTrackPipeline           func(context.Context, backend.TrackPipelineRequest) (backend.TrackPipelineResult, error)
	moveSuspiciousDownload     func(string, string) (string, error)
	redownloadTrackFromYouTube func(context.Context, backend.SuspiciousRedownloadRequest) backend.SuspiciousRedownloadResult
}

func NewApp() *App {
	paths, err := backend.InitRuntimePaths()
	return &App{runtimePaths: paths, runtimeInitErr: err}
}

func (a *App) startup(ctx context.Context) {
	a.ctx = ctx

	if a.runtimeInitErr != nil {
		_, _ = runtime.MessageDialog(ctx, runtime.MessageDialogOptions{
			Type:    runtime.ErrorDialog,
			Title:   "SpotiDownloader cannot start",
			Message: a.runtimeInitErr.Error(),
		})
		runtime.Quit(ctx)
		return
	}

	var cacheReady atomic.Bool
	cache, cacheWarnings := backend.OpenMetadataCache(a.runtimePaths.MetadataCacheFile, backend.CacheOptions{
		MaxEntries: 5000,
		WriteDelay: 100 * time.Millisecond,
		OnWarning: func(message string) {
			if cacheReady.Load() {
				fmt.Printf("Metadata cache warning: %s\n", message)
			}
		},
	})
	cacheReady.Store(true)
	for _, warning := range cacheWarnings {
		fmt.Printf("Metadata cache warning: %s\n", warning)
	}
	if active := backend.InstallMetadataCache(cache); active != cache {
		_ = cache.Close()
		cache = active
	}
	a.metadataCache = cache

	if err := backend.InitHistoryDB("SpotiDownloader"); err != nil {
		fmt.Printf("Failed to init history DB: %v\n", err)
	}
}

func (a *App) shutdown(ctx context.Context) {
	a.activeMu.Lock()
	a.shuttingDown = true
	a.activeMu.Unlock()
	a.CancelActiveDownloads()
	a.activeWG.Wait()
	if a.metadataCache != nil {
		if err := a.metadataCache.Close(); err != nil {
			fmt.Printf("Metadata cache close warning: %v\n", err)
		}
	}
	backend.CloseHistoryDB()
	if a.runtimeInitErr == nil {
		if err := a.runtimePaths.CleanupRun(); err != nil {
			fmt.Printf("Runtime cleanup warning: %v\n", err)
		}
	}
}

func (a *App) onSecondInstanceLaunch(options.SecondInstanceData) {
	if a.ctx == nil {
		return
	}
	runtime.WindowUnminimise(a.ctx)
	runtime.Show(a.ctx)
}

type SpotifyMetadataRequest struct {
	URL     string  `json:"url"`
	Batch   bool    `json:"batch"`
	Delay   float64 `json:"delay"`
	Timeout float64 `json:"timeout"`
}

type DownloadRequest struct {
	TrackID             string `json:"track_id,omitempty"`
	SessionToken        string `json:"session_token"`
	TrackName           string `json:"track_name,omitempty"`
	ArtistName          string `json:"artist_name,omitempty"`
	AlbumName           string `json:"album_name,omitempty"`
	AlbumArtist         string `json:"album_artist,omitempty"`
	ReleaseDate         string `json:"release_date,omitempty"`
	CoverURL            string `json:"cover_url,omitempty"`
	AlbumTrackNumber    int    `json:"album_track_number,omitempty"`
	DiscNumber          int    `json:"disc_number,omitempty"`
	TotalTracks         int    `json:"total_tracks,omitempty"`
	SpotifyTotalDiscs   int    `json:"spotify_total_discs,omitempty"`
	Copyright           string `json:"copyright,omitempty"`
	Publisher           string `json:"publisher,omitempty"`
	OutputDir           string `json:"output_dir,omitempty"`
	CollectionDir       string `json:"collection_dir,omitempty"`
	AudioFormat         string `json:"audio_format,omitempty"`
	FilenameFormat      string `json:"filename_format,omitempty"`
	TrackNumber         bool   `json:"track_number,omitempty"`
	Position            int    `json:"position,omitempty"`
	UseAlbumTrackNumber bool   `json:"use_album_track_number,omitempty"`
	SpotifyID           string `json:"spotify_id,omitempty"`
	Duration            int    `json:"duration,omitempty"`

	EmbedLyrics          bool   `json:"embed_lyrics,omitempty"`
	EmbedMaxQualityCover bool   `json:"embed_max_quality_cover,omitempty"`
	ItemID               string `json:"item_id,omitempty"`
	QueueID              string `json:"queue_id,omitempty"`
	Force                bool   `json:"force,omitempty"`
	PlaylistName         string `json:"playlist_name,omitempty"`
	PlaylistOwner        string `json:"playlist_owner,omitempty"`
	UseFirstArtistOnly   bool   `json:"use_first_artist_only,omitempty"`
	UseSingleGenre       bool   `json:"use_single_genre,omitempty"`
	EmbedGenre           bool   `json:"embed_genre,omitempty"`
}

type DownloadResponse struct {
	Success                 bool                            `json:"success"`
	Message                 string                          `json:"message"`
	File                    string                          `json:"file,omitempty"`
	Error                   string                          `json:"error,omitempty"`
	AlreadyExists           bool                            `json:"already_exists,omitempty"`
	ItemID                  string                          `json:"item_id,omitempty"`
	MovedOriginalPath       string                          `json:"moved_original_path,omitempty"`
	ReplacementPath         string                          `json:"replacement_path,omitempty"`
	ExpectedDurationSeconds float64                         `json:"expected_duration_seconds,omitempty"`
	ActualDurationSeconds   float64                         `json:"actual_duration_seconds,omitempty"`
	DurationDeltaSeconds    float64                         `json:"duration_delta_seconds,omitempty"`
	Suspicious              bool                            `json:"suspicious,omitempty"`
	ValidationWarning       string                          `json:"validation_warning,omitempty"`
	Warnings                []string                        `json:"warnings,omitempty"`
	CacheOutcomes           map[string]backend.CacheOutcome `json:"cache_outcomes,omitempty"`
	Timings                 backend.PipelineTimings         `json:"timings,omitempty"`
}

func (a *App) GetSpotifyMetadata(req SpotifyMetadataRequest) (string, error) {
	if req.URL == "" {
		return "", fmt.Errorf("URL parameter is required")
	}

	if req.Delay == 0 {
		req.Delay = 1.0
	}
	if req.Timeout == 0 {
		req.Timeout = 300.0
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(req.Timeout*float64(time.Second)))
	defer cancel()

	cache := a.metadataCache
	if cache == nil {
		cache = backend.CurrentMetadataCache()
	}
	data, err := backend.GetFilteredSpotifyDataWithCache(ctx, cache, req.URL, req.Batch, time.Duration(req.Delay*float64(time.Second)))
	if err != nil {
		return "", fmt.Errorf("failed to fetch metadata: %v", err)
	}

	jsonData, err := json.MarshalIndent(data, "", "  ")
	if err != nil {
		return "", fmt.Errorf("failed to encode response: %v", err)
	}

	return string(jsonData), nil
}

type SpotifySearchRequest struct {
	Query string `json:"query"`
	Limit int    `json:"limit"`
}

func (a *App) SearchSpotify(req SpotifySearchRequest) (*backend.SearchResponse, error) {
	if req.Query == "" {
		return nil, fmt.Errorf("search query is required")
	}

	if req.Limit <= 0 {
		req.Limit = 10
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	return backend.SearchSpotify(ctx, req.Query, req.Limit)
}

type SpotifySearchByTypeRequest struct {
	Query      string `json:"query"`
	SearchType string `json:"search_type"`
	Limit      int    `json:"limit"`
	Offset     int    `json:"offset"`
}

func (a *App) SearchSpotifyByType(req SpotifySearchByTypeRequest) ([]backend.SearchResult, error) {
	if req.Query == "" {
		return nil, fmt.Errorf("search query is required")
	}

	if req.SearchType == "" {
		return nil, fmt.Errorf("search type is required")
	}

	if req.Limit <= 0 {
		req.Limit = 50
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	return backend.SearchSpotifyByType(ctx, req.Query, req.SearchType, req.Limit, req.Offset)
}

func (a *App) DownloadTrack(req DownloadRequest) (DownloadResponse, error) {
	if err := normalizeDownloadRequest(&req); err != nil {
		return DownloadResponse{
			Success: false,
			Error:   err.Error(),
		}, err
	}

	itemID := ensureDownloadQueueItem(req)
	trackCtx, cleanup := a.beginTrack(itemID, req.QueueID)
	defer cleanup()
	backend.StartDownloadItem(itemID)

	pipelineReq := buildTrackPipelineRequest(req, itemID)
	result, err := a.runTrackPipelineWithRequest(trackCtx, req, pipelineReq)
	if err != nil {
		message := err.Error()
		for _, warning := range result.Warnings {
			backend.AddItemWarning(itemID, warning)
		}
		if errors.Is(err, context.Canceled) {
			backend.CancelDownloadItem(itemID)
		} else {
			backend.FailDownloadItem(itemID, message)
		}
		return DownloadResponse{
			Success:       false,
			Error:         message,
			ItemID:        itemID,
			Warnings:      append([]string(nil), result.Warnings...),
			CacheOutcomes: result.CacheOutcomes,
			Timings:       result.Timings,
		}, err
	}
	if cancelErr := trackCtx.Err(); cancelErr != nil {
		backend.CancelDownloadItem(itemID)
		return DownloadResponse{
			Success:       false,
			Error:         cancelErr.Error(),
			ItemID:        itemID,
			Warnings:      append([]string(nil), result.Warnings...),
			CacheOutcomes: result.CacheOutcomes,
			Timings:       result.Timings,
		}, cancelErr
	}
	if strings.TrimSpace(result.File) == "" {
		err = errors.New("track pipeline returned an empty file")
		backend.FailDownloadItem(itemID, err.Error())
		return DownloadResponse{
			Success:       false,
			Error:         err.Error(),
			ItemID:        itemID,
			Warnings:      append([]string(nil), result.Warnings...),
			CacheOutcomes: result.CacheOutcomes,
			Timings:       result.Timings,
		}, err
	}

	for _, warning := range result.Warnings {
		backend.AddItemWarning(itemID, warning)
	}

	validation := DownloadResponse{}
	filename := result.File
	if !result.AlreadyExists && result.DurationSeconds > 0 {
		validation = validateDownloadedDuration(filename, req.Duration, result.DurationSeconds)
		if validation.Suspicious {
			movedValidation, moveErr := a.moveSuspiciousDownloadIfNeeded(filename, req.CollectionDir, validation)
			if moveErr != nil {
				backend.FailDownloadItem(itemID, fmt.Sprintf("Failed to move suspicious download: %v", moveErr))
				return DownloadResponse{
					Success:                 false,
					Error:                   fmt.Sprintf("Failed to move suspicious download: %v", moveErr),
					File:                    filename,
					ItemID:                  itemID,
					ExpectedDurationSeconds: validation.ExpectedDurationSeconds,
					ActualDurationSeconds:   validation.ActualDurationSeconds,
					DurationDeltaSeconds:    validation.DurationDeltaSeconds,
					Suspicious:              validation.Suspicious,
					ValidationWarning:       validation.ValidationWarning,
					Warnings:                append([]string(nil), result.Warnings...),
					CacheOutcomes:           result.CacheOutcomes,
					Timings:                 result.Timings,
				}, moveErr
			}
			validation = movedValidation
			filename = validation.File
		}
	}
	if validation.ValidationWarning != "" {
		backend.AddItemWarning(itemID, validation.ValidationWarning)
	}

	if result.AlreadyExists {
		backend.SkipDownloadItem(itemID, filename)
	} else {
		completeDownloadTracking(itemID, filename, result.Timings)
		a.addHistoryItemAsync(req, filename)
	}

	warnings := append([]string(nil), result.Warnings...)
	if validation.ValidationWarning != "" {
		warnings = append(warnings, validation.ValidationWarning)
	}
	message := "Download completed successfully"
	if result.AlreadyExists {
		message = "File already exists"
	}
	return DownloadResponse{
		Success:                 true,
		Message:                 message,
		File:                    filename,
		AlreadyExists:           result.AlreadyExists,
		ItemID:                  itemID,
		MovedOriginalPath:       validation.MovedOriginalPath,
		ReplacementPath:         validation.ReplacementPath,
		ExpectedDurationSeconds: validation.ExpectedDurationSeconds,
		ActualDurationSeconds:   validation.ActualDurationSeconds,
		DurationDeltaSeconds:    validation.DurationDeltaSeconds,
		Suspicious:              validation.Suspicious,
		ValidationWarning:       validation.ValidationWarning,
		Warnings:                warnings,
		CacheOutcomes:           result.CacheOutcomes,
		Timings:                 result.Timings,
	}, nil
}

type ConvertAudioRequest struct {
	InputFiles   []string `json:"input_files"`
	OutputFormat string   `json:"output_format"`
	Bitrate      string   `json:"bitrate"`
	Codec        string   `json:"codec"`
}

func (a *App) ConvertAudio(req ConvertAudioRequest) ([]backend.ConvertAudioResult, error) {
	backendReq := backend.ConvertAudioRequest{
		InputFiles:   req.InputFiles,
		OutputFormat: req.OutputFormat,
		Bitrate:      req.Bitrate,
		Codec:        req.Codec,
	}
	return backend.ConvertAudio(backendReq)
}

func (a *App) SelectAudioFiles() ([]string, error) {

	files, err := backend.SelectMultipleFiles(a.ctx)
	if err != nil {
		return nil, err
	}
	return files, nil
}

func (a *App) GetFileSizes(files []string) map[string]int64 {
	return backend.GetFileSizes(files)
}

func (a *App) ListDirectoryFiles(dirPath string) ([]backend.FileInfo, error) {
	if dirPath == "" {
		return nil, fmt.Errorf("directory path is required")
	}
	return backend.ListDirectory(dirPath)
}

func (a *App) ListAudioFilesInDir(dirPath string) ([]backend.FileInfo, error) {
	if dirPath == "" {
		return nil, fmt.Errorf("directory path is required")
	}
	return backend.ListAudioFiles(dirPath)
}

func (a *App) ReadFileMetadata(filePath string) (*backend.AudioMetadata, error) {
	if filePath == "" {
		return nil, fmt.Errorf("file path is required")
	}
	return backend.ReadAudioMetadata(filePath)
}

func (a *App) PreviewRenameFiles(files []string, format string) []backend.RenamePreview {
	return backend.PreviewRename(files, format)
}

func (a *App) RenameFilesByMetadata(files []string, format string) []backend.RenameResult {
	return backend.RenameFiles(files, format)
}

func (a *App) ReadTextFile(filePath string) (string, error) {
	if err := backend.ValidateReadableTextFile(filePath); err != nil {
		return "", err
	}
	content, err := os.ReadFile(filePath)
	if err != nil {
		return "", err
	}
	return string(content), nil
}

func (a *App) RenameFileTo(oldPath, newName string) error {
	newPath, err := backend.BuildSiblingRenamePath(oldPath, newName)
	if err != nil {
		return err
	}
	return backend.RenameNoReplace(oldPath, newPath)
}

func (a *App) ReadImageAsBase64(filePath string) (string, error) {
	if err := backend.ValidateReadableImageFile(filePath); err != nil {
		return "", err
	}
	content, err := os.ReadFile(filePath)
	if err != nil {
		return "", err
	}

	ext := strings.ToLower(filepath.Ext(filePath))
	var mimeType string
	switch ext {
	case ".jpg", ".jpeg":
		mimeType = "image/jpeg"
	case ".png":
		mimeType = "image/png"
	case ".gif":
		mimeType = "image/gif"
	case ".webp":
		mimeType = "image/webp"
	default:
		mimeType = "image/jpeg"
	}

	encoded := base64.StdEncoding.EncodeToString(content)
	return fmt.Sprintf("data:%s;base64,%s", mimeType, encoded), nil
}

func (a *App) SaveSettings(settings map[string]interface{}) error {
	configPath, err := a.GetConfigPath()
	if err != nil {
		return err
	}

	dir := filepath.Dir(configPath)
	if _, err := os.Stat(dir); os.IsNotExist(err) {
		if err := os.MkdirAll(dir, 0755); err != nil {
			return err
		}
	}

	data, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return err
	}

	return backend.AtomicWriteFile(configPath, data, 0600)
}

func (a *App) LoadSettings() (map[string]interface{}, error) {
	configPath, err := a.GetConfigPath()
	if err != nil {
		return nil, err
	}

	if _, err := os.Stat(configPath); os.IsNotExist(err) {
		return nil, nil
	}

	data, err := os.ReadFile(configPath)
	if err != nil {
		return nil, err
	}

	var settings map[string]interface{}
	if err := json.Unmarshal(data, &settings); err != nil {
		return nil, err
	}

	return settings, nil
}

func (a *App) CreateM3U8File(m3u8Name string, outputDir string, filePaths []string) error {
	if len(filePaths) == 0 {
		return nil
	}

	if err := os.MkdirAll(outputDir, 0755); err != nil {
		return err
	}

	fnName := m3u8Name

	safeName := backend.SanitizeFilename(fnName)
	if safeName == "" {
		safeName = "playlist"
	}

	m3u8Path := filepath.Join(outputDir, safeName+".m3u8")

	f, err := os.Create(m3u8Path)
	if err != nil {
		return err
	}
	defer f.Close()

	if _, err := f.WriteString("#EXTM3U\n"); err != nil {
		return err
	}

	for _, path := range filePaths {
		if path == "" {
			continue
		}

		relPath, err := filepath.Rel(outputDir, path)
		if err != nil {

			relPath = path
		}

		relPath = filepath.ToSlash(relPath)

		if _, err := f.WriteString(relPath + "\n"); err != nil {
			return err
		}
	}

	return nil
}
