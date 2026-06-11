package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"

	"path/filepath"
	"strings"
	"time"

	"github.com/afkarxyz/SpotiDownloader/backend"
)

type App struct {
	ctx context.Context
}

const inlineLyricsWaitTimeout = 3 * time.Second

func NewApp() *App {
	return &App{}
}

func (a *App) startup(ctx context.Context) {
	a.ctx = ctx

	if err := backend.InitHistoryDB("SpotiDownloader"); err != nil {
		fmt.Printf("Failed to init history DB: %v\n", err)
	}
}

func (a *App) shutdown(ctx context.Context) {
	backend.CloseHistoryDB()
}

func (a *App) embedLyrics(filename, lyrics string) {
	if strings.TrimSpace(lyrics) == "" {
		fmt.Println("No lyrics found to embed.")
		return
	}

	fmt.Printf("\n--- Full LRC Content ---\n")
	fmt.Println(lyrics)
	fmt.Printf("--- End LRC Content ---\n\n")

	fmt.Printf("Embedding into: %s\n", filename)
	if err := backend.EmbedLyricsOnly(filename, lyrics); err != nil {
		fmt.Printf("Failed to embed lyrics: %v\n", err)
		return
	}

	fmt.Printf("Lyrics embedded successfully!\n")
	if err := backend.FinalizeTaggedAudioFile(filename); err != nil {
		fmt.Printf("Failed to finalize tagged MP3 file: %v\n", err)
	}
}

func (a *App) finishLyricsEmbeddingAsync(filename string, lyricsChan <-chan string) {
	lyrics, ok := <-lyricsChan
	if !ok || strings.TrimSpace(lyrics) == "" {
		fmt.Printf("Lyrics were not found for %s.\n", filepath.Base(filename))
		return
	}

	fmt.Printf("Lyrics fetch completed in background for %s.\n", filepath.Base(filename))
	a.embedLyrics(filename, lyrics)
}

func (a *App) embedLyricsIfReady(filename string, lyricsChan <-chan string) {
	timer := time.NewTimer(inlineLyricsWaitTimeout)
	defer timer.Stop()

	fmt.Printf("\nWaiting up to %v for lyrics fetch...\n", inlineLyricsWaitTimeout)

	select {
	case lyrics, ok := <-lyricsChan:
		if !ok {
			fmt.Println("No lyrics found to embed.")
			return
		}
		a.embedLyrics(filename, lyrics)
	case <-timer.C:
		fmt.Printf("Lyrics fetch is still running for %s; finishing download now and embedding later if lyrics arrive.\n", filepath.Base(filename))
		go a.finishLyricsEmbeddingAsync(filename, lyricsChan)
	}
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
	PlaylistName         string `json:"playlist_name,omitempty"`
	PlaylistOwner        string `json:"playlist_owner,omitempty"`
	UseFirstArtistOnly   bool   `json:"use_first_artist_only,omitempty"`
	UseSingleGenre       bool   `json:"use_single_genre,omitempty"`
	EmbedGenre           bool   `json:"embed_genre,omitempty"`
}

type DownloadResponse struct {
	Success                 bool    `json:"success"`
	Message                 string  `json:"message"`
	File                    string  `json:"file,omitempty"`
	Error                   string  `json:"error,omitempty"`
	AlreadyExists           bool    `json:"already_exists,omitempty"`
	ItemID                  string  `json:"item_id,omitempty"`
	MovedOriginalPath       string  `json:"moved_original_path,omitempty"`
	ReplacementPath         string  `json:"replacement_path,omitempty"`
	ExpectedDurationSeconds float64 `json:"expected_duration_seconds,omitempty"`
	ActualDurationSeconds   float64 `json:"actual_duration_seconds,omitempty"`
	DurationDeltaSeconds    float64 `json:"duration_delta_seconds,omitempty"`
	Suspicious              bool    `json:"suspicious,omitempty"`
	ValidationWarning       string  `json:"validation_warning,omitempty"`
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

	data, err := backend.GetFilteredSpotifyData(ctx, req.URL, req.Batch, time.Duration(req.Delay*float64(time.Second)))
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

	backend.SetDownloading(true)
	backend.StartDownloadItem(itemID)
	defer backend.SetDownloading(false)

	enrichDownloadRequestMetadata(&req)

	if expectedPath, exists := findExistingDownloadPath(req); exists {
		backend.SkipDownloadItem(itemID, expectedPath)
		return DownloadResponse{
			Success:       true,
			Message:       "File already exists",
			File:          expectedPath,
			AlreadyExists: true,
			ItemID:        itemID,
		}, nil
	}

	trackID := primaryTrackID(req)
	lyricsChan := startLyricsFetch(req, trackID)

	filename, err := downloadTrackFile(req, trackID)
	if err != nil {
		backend.FailDownloadItem(itemID, fmt.Sprintf("Download failed: %v", err))
		return DownloadResponse{
			Success: false,
			Error:   fmt.Sprintf("Download failed: %v", err),
			ItemID:  itemID,
		}, err
	}

	alreadyExists := false
	if strings.HasPrefix(filename, "EXISTS:") {
		alreadyExists = true
		filename = strings.TrimPrefix(filename, "EXISTS:")
	}

	if !alreadyExists && trackID != "" && req.EmbedLyrics && (strings.HasSuffix(filename, ".flac") || strings.HasSuffix(filename, ".mp3") || strings.HasSuffix(filename, ".m4a")) {
		a.embedLyricsIfReady(filename, lyricsChan)
	} else {
		select {
		case <-lyricsChan:
		default:
		}
	}

	message := "Download completed successfully"
	if alreadyExists {
		message = "File already exists"
		backend.SkipDownloadItem(itemID, filename)
	} else {
		validation := validateDownloadedFileDuration(filename, req.Duration)
		if validation.Suspicious {
			movedValidation, moveErr := moveSuspiciousDownloadIfNeeded(filename, req.CollectionDir, validation)
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
				}, moveErr
			}
			validation = movedValidation
			filename = validation.File
		}
		completeDownloadTracking(itemID, filename)
		addHistoryItemAsync(req, filename)
		return DownloadResponse{
			Success:                 true,
			Message:                 message,
			File:                    filename,
			AlreadyExists:           alreadyExists,
			ItemID:                  itemID,
			MovedOriginalPath:       validation.MovedOriginalPath,
			ReplacementPath:         validation.ReplacementPath,
			ExpectedDurationSeconds: validation.ExpectedDurationSeconds,
			ActualDurationSeconds:   validation.ActualDurationSeconds,
			DurationDeltaSeconds:    validation.DurationDeltaSeconds,
			Suspicious:              validation.Suspicious,
			ValidationWarning:       validation.ValidationWarning,
		}, nil
	}

	return DownloadResponse{
		Success:       true,
		Message:       message,
		File:          filename,
		AlreadyExists: alreadyExists,
		ItemID:        itemID,
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
	return backend.MoveFileWithFallback(oldPath, newPath)
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

	return os.WriteFile(configPath, data, 0600)
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
