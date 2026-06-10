package backend

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

type TrackMetadataUpdateRequest struct {
	FilePath             string `json:"file_path"`
	TrackID              string `json:"track_id,omitempty"`
	TrackName            string `json:"track_name,omitempty"`
	ArtistName           string `json:"artist_name,omitempty"`
	AlbumName            string `json:"album_name,omitempty"`
	AlbumArtist          string `json:"album_artist,omitempty"`
	ReleaseDate          string `json:"release_date,omitempty"`
	CoverURL             string `json:"cover_url,omitempty"`
	TrackNumber          int    `json:"track_number,omitempty"`
	TotalTracks          int    `json:"total_tracks,omitempty"`
	DiscNumber           int    `json:"disc_number,omitempty"`
	TotalDiscs           int    `json:"total_discs,omitempty"`
	Copyright            string `json:"copyright,omitempty"`
	Publisher            string `json:"publisher,omitempty"`
	ISRC                 string `json:"isrc,omitempty"`
	Genre                string `json:"genre,omitempty"`
	Duration             int    `json:"duration,omitempty"`
	EmbedLyrics          bool   `json:"embed_lyrics,omitempty"`
	EmbedMaxQualityCover bool   `json:"embed_max_quality_cover,omitempty"`
	UseSingleGenre       bool   `json:"use_single_genre,omitempty"`
	EmbedGenre           bool   `json:"embed_genre,omitempty"`
}

type TrackMetadataUpdateResult struct {
	Success                 bool    `json:"success"`
	Message                 string  `json:"message"`
	FilePath                string  `json:"file_path,omitempty"`
	Error                   string  `json:"error,omitempty"`
	TrackID                 string  `json:"track_id,omitempty"`
	ExpectedDurationSeconds float64 `json:"expected_duration_seconds,omitempty"`
	ActualDurationSeconds   float64 `json:"actual_duration_seconds,omitempty"`
	DurationDeltaSeconds    float64 `json:"duration_delta_seconds,omitempty"`
	Suspicious              bool    `json:"suspicious,omitempty"`
	ValidationWarning       string  `json:"validation_warning,omitempty"`
}

type SuspiciousRedownloadRequest struct {
	OriginalFilePath    string                     `json:"original_file_path"`
	ReplacementFilePath string                     `json:"replacement_file_path,omitempty"`
	CollectionDir       string                     `json:"collection_dir,omitempty"`
	AudioFormat         string                     `json:"audio_format,omitempty"`
	Metadata            TrackMetadataUpdateRequest `json:"metadata"`
}

type SuspiciousRedownloadResult struct {
	Success                 bool    `json:"success"`
	Status                  string  `json:"status"`
	TrackID                 string  `json:"track_id,omitempty"`
	OriginalFilePath        string  `json:"original_file_path,omitempty"`
	MovedOriginalPath       string  `json:"moved_original_path,omitempty"`
	ReplacementPath         string  `json:"replacement_path,omitempty"`
	Error                   string  `json:"error,omitempty"`
	ExpectedDurationSeconds float64 `json:"expected_duration_seconds,omitempty"`
	ActualDurationSeconds   float64 `json:"actual_duration_seconds,omitempty"`
	DurationDeltaSeconds    float64 `json:"duration_delta_seconds,omitempty"`
	Suspicious              bool    `json:"suspicious,omitempty"`
	ValidationWarning       string  `json:"validation_warning,omitempty"`
}

func BuildTrackMetadataPayload(req TrackMetadataUpdateRequest) Metadata {
	metadata := Metadata{
		Title:       strings.TrimSpace(req.TrackName),
		Artist:      strings.TrimSpace(req.ArtistName),
		Album:       strings.TrimSpace(req.AlbumName),
		AlbumArtist: strings.TrimSpace(req.AlbumArtist),
		Date:        strings.TrimSpace(req.ReleaseDate),
		TrackNumber: req.TrackNumber,
		TotalTracks: req.TotalTracks,
		DiscNumber:  req.DiscNumber,
		TotalDiscs:  req.TotalDiscs,
		Copyright:   strings.TrimSpace(req.Copyright),
		Publisher:   strings.TrimSpace(req.Publisher),
		Description: "https://github.com/afkarxyz/SpotiDownloader",
		ISRC:        strings.TrimSpace(req.ISRC),
		Genre:       strings.TrimSpace(req.Genre),
	}

	if trackID := strings.TrimSpace(req.TrackID); trackID != "" {
		metadata.URL = fmt.Sprintf("https://open.spotify.com/track/%s", trackID)
	}

	return metadata
}

func BuildYtDlpSearchQuery(artistName, trackName string) string {
	return fmt.Sprintf("ytsearch1:%s - %s audio", strings.TrimSpace(artistName), strings.TrimSpace(trackName))
}

var downloadWithYtDlp = DownloadWithYtDlp
var updateTrackMetadata = UpdateTrackMetadata

func normalizeYouTubeFallbackMetadata(req TrackMetadataUpdateRequest) TrackMetadataUpdateRequest {
	if req.TrackNumber <= 0 || req.TrackNumber > 99 {
		req.TrackNumber = 1
	}
	if req.DiscNumber <= 0 || req.DiscNumber > 99 {
		req.DiscNumber = 1
	}
	return req
}

func UpdateTrackMetadata(req TrackMetadataUpdateRequest) TrackMetadataUpdateResult {
	result := TrackMetadataUpdateResult{
		FilePath: req.FilePath,
		TrackID:  req.TrackID,
	}

	if strings.TrimSpace(req.FilePath) == "" {
		result.Error = "file path is required"
		return result
	}

	if info, err := os.Stat(req.FilePath); err != nil {
		result.Error = fmt.Sprintf("failed to stat file: %v", err)
		return result
	} else if info.IsDir() {
		result.Error = "file path points to a directory"
		return result
	}

	metadata := BuildTrackMetadataPayload(req)
	if req.EmbedGenre || metadata.Date == "" {
		tagging := <-fetchTaggingMetadataInBackground(req.TrackID, req.TrackName, req.ArtistName, req.AlbumName, req.ReleaseDate, req.UseSingleGenre, req.EmbedGenre)
		if metadata.Date == "" {
			metadata.Date = strings.TrimSpace(tagging.ReleaseDate)
		}
		if metadata.ISRC == "" {
			metadata.ISRC = strings.TrimSpace(tagging.ISRC)
		}
		if metadata.Genre == "" {
			metadata.Genre = strings.TrimSpace(tagging.Genre)
		}
	}

	if req.EmbedLyrics && strings.TrimSpace(req.TrackID) != "" {
		client := NewLyricsClient()
		resp, _, err := client.FetchLyricsAllSources(req.TrackID, req.TrackName, req.ArtistName, req.Duration)
		if err == nil && resp != nil && len(resp.Lines) > 0 {
			metadata.Lyrics = client.ConvertToLRC(resp, req.TrackName, req.ArtistName)
		}
	}

	var coverPath string
	if strings.TrimSpace(req.CoverURL) != "" {
		downloader := NewSpotiDownloader("")
		var coverErr error
		coverPath, coverErr = downloader.downloadCoverImage(req.CoverURL, filepath.Dir(req.FilePath), req.EmbedMaxQualityCover)
		downloader.CloseIdleConnections()
		if coverErr != nil {
			fmt.Printf("Warning: Failed to download cover image for metadata update: %v\n", coverErr)
			coverPath = ""
		}
	}
	if coverPath != "" {
		defer os.Remove(coverPath)
	}

	if err := EmbedMetadata(req.FilePath, metadata, coverPath); err != nil {
		result.Error = fmt.Sprintf("failed to embed metadata: %v", err)
		return result
	}

	if err := FinalizeTaggedAudioFile(req.FilePath); err != nil {
		result.Error = fmt.Sprintf("failed to finalize tagged file: %v", err)
		return result
	}

	populateDurationValidation(&result, req.Duration)
	result.Success = true
	result.Message = "Metadata updated successfully"
	return result
}

func UpdateTracksMetadata(reqs []TrackMetadataUpdateRequest) []TrackMetadataUpdateResult {
	results := make([]TrackMetadataUpdateResult, len(reqs))
	for i, req := range reqs {
		results[i] = UpdateTrackMetadata(req)
	}
	return results
}

func RedownloadSuspiciousTrackFromYouTube(req SuspiciousRedownloadRequest) SuspiciousRedownloadResult {
	result := SuspiciousRedownloadResult{
		TrackID:          req.Metadata.TrackID,
		OriginalFilePath: req.OriginalFilePath,
	}

	originalPath := strings.TrimSpace(req.OriginalFilePath)
	replacementPath := strings.TrimSpace(req.ReplacementFilePath)
	if originalPath == "" && replacementPath == "" {
		result.Status = "failed"
		result.Error = "original or replacement file path is required"
		return result
	}

	collectionDir := strings.TrimSpace(req.CollectionDir)
	if collectionDir == "" && originalPath != "" {
		collectionDir = filepath.Dir(originalPath)
	} else if collectionDir == "" {
		collectionDir = filepath.Dir(replacementPath)
	}
	suspiciousDir := filepath.Join(collectionDir, SuspiciousTracksDirName)

	movedPath := ""
	if originalPath != "" && replacementPath == "" {
		var err error
		replacementPath = originalPath
		movedPath, err = MoveSuspiciousOriginal(originalPath, suspiciousDir)
		if err != nil {
			result.Status = "failed"
			result.Error = fmt.Sprintf("failed to move suspicious original: %v", err)
			return result
		}
	} else if originalPath != "" && !IsPathInsideDir(originalPath, suspiciousDir) {
		var err error
		movedPath, err = MoveSuspiciousOriginal(originalPath, suspiciousDir)
		if err != nil {
			result.Status = "failed"
			result.Error = fmt.Sprintf("failed to move suspicious original: %v", err)
			return result
		}
	} else if originalPath != "" {
		movedPath = originalPath
	}
	result.MovedOriginalPath = movedPath

	outputBase := strings.TrimSuffix(replacementPath, filepath.Ext(replacementPath))
	query := BuildYtDlpSearchQuery(req.Metadata.ArtistName, req.Metadata.TrackName)
	downloadResult := downloadWithYtDlp(YtDlpDownloadRequest{
		Query:       query,
		OutputBase:  outputBase,
		AudioFormat: req.AudioFormat,
	})
	if !downloadResult.Success {
		result.Status = "failed"
		result.Error = downloadResult.Error
		return result
	}

	metadataReq := normalizeYouTubeFallbackMetadata(req.Metadata)
	metadataReq.FilePath = downloadResult.File
	updateResult := updateTrackMetadata(metadataReq)
	result.ReplacementPath = downloadResult.File
	result.ExpectedDurationSeconds = updateResult.ExpectedDurationSeconds
	result.ActualDurationSeconds = updateResult.ActualDurationSeconds
	result.DurationDeltaSeconds = updateResult.DurationDeltaSeconds
	result.ValidationWarning = updateResult.ValidationWarning
	result.Suspicious = updateResult.Suspicious

	if !updateResult.Success {
		result.Status = "failed"
		result.Error = updateResult.Error
		return result
	}

	result.Success = true
	if updateResult.Suspicious {
		result.Status = "still_suspicious"
	} else {
		result.Status = "replaced"
	}
	return result
}

func RedownloadSuspiciousTracksFromYouTube(reqs []SuspiciousRedownloadRequest) []SuspiciousRedownloadResult {
	results := make([]SuspiciousRedownloadResult, len(reqs))
	for i, req := range reqs {
		results[i] = RedownloadSuspiciousTrackFromYouTube(req)
	}
	return results
}

func populateDurationValidation(result *TrackMetadataUpdateResult, expectedDurationMs int) {
	if result == nil || expectedDurationMs <= 0 || strings.TrimSpace(result.FilePath) == "" {
		return
	}

	expectedSeconds := float64(expectedDurationMs) / 1000.0
	actualSeconds, err := GetAudioDuration(result.FilePath)
	if err != nil {
		result.ExpectedDurationSeconds = expectedSeconds
		result.ValidationWarning = fmt.Sprintf("failed to probe audio duration: %v", err)
		return
	}

	result.ExpectedDurationSeconds = expectedSeconds
	result.ActualDurationSeconds = actualSeconds
	result.DurationDeltaSeconds = actualSeconds - expectedSeconds
	result.Suspicious = IsSuspiciousDuration(expectedSeconds, actualSeconds)
}
