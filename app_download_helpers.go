package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/afkarxyz/SpotiDownloader/backend"
)

func normalizeDownloadRequest(req *DownloadRequest) error {
	if req.TrackID == "" && req.SpotifyID == "" {
		return fmt.Errorf("track ID or Spotify ID is required")
	}
	if req.SessionToken == "" {
		return fmt.Errorf("session token is required")
	}

	if req.OutputDir == "" {
		req.OutputDir = "."
	} else {
		if req.PlaylistName != "" {
			sanitizedPlaylist := backend.SanitizeFilename(req.PlaylistName)
			req.OutputDir = filepath.Join(req.OutputDir, sanitizedPlaylist)
		}
		req.OutputDir = backend.SanitizeFolderPath(req.OutputDir)
	}

	if req.AudioFormat == "" {
		req.AudioFormat = "mp3"
	}
	if req.FilenameFormat == "" {
		req.FilenameFormat = "title-artist"
	}

	return nil
}

func ensureDownloadQueueItem(req DownloadRequest) string {
	if req.ItemID != "" {
		return req.ItemID
	}

	trackIDForItemID := primaryTrackID(req)
	itemID := fmt.Sprintf("%s-%d", trackIDForItemID, time.Now().UnixNano())
	_ = backend.AddQueueItem(req.QueueID, itemID, req.TrackName, req.ArtistName, req.AlbumName, trackIDForItemID)
	return itemID
}

func (a *App) AddDownloadJob(queueID, jobID, spotifyID, trackName, artistName, albumName string) (string, error) {
	if err := backend.AddQueueItem(queueID, jobID, trackName, artistName, albumName, spotifyID); err != nil {
		return "", err
	}
	return jobID, nil
}

func primaryTrackID(req DownloadRequest) string {
	if req.TrackID != "" {
		return req.TrackID
	}
	return req.SpotifyID
}

func buildTrackPipelineRequest(req DownloadRequest, itemID string) backend.TrackPipelineRequest {
	trackNumber := req.AlbumTrackNumber
	if trackNumber == 0 {
		trackNumber = 1
	}
	trackID := primaryTrackID(req)
	return backend.TrackPipelineRequest{
		ItemID:               itemID,
		TrackID:              trackID,
		OutputDir:            req.OutputDir,
		AudioFormat:          req.AudioFormat,
		FilenameFormat:       req.FilenameFormat,
		IncludeTrackNumber:   req.TrackNumber,
		Position:             req.Position,
		UseAlbumTrackNumber:  req.UseAlbumTrackNumber,
		PlaylistName:         req.PlaylistName,
		PlaylistOwner:        req.PlaylistOwner,
		UseFirstArtistOnly:   req.UseFirstArtistOnly,
		EmbedMaxQualityCover: req.EmbedMaxQualityCover,
		EmbedGenre:           req.EmbedGenre,
		UseSingleGenre:       req.UseSingleGenre,
		EmbedLyrics:          req.EmbedLyrics,
		ExpectedDurationMS:   req.Duration,
		CoverURL:             req.CoverURL,
		BaseMetadata: backend.Metadata{
			Title:       req.TrackName,
			Artist:      req.ArtistName,
			Album:       req.AlbumName,
			AlbumArtist: req.AlbumArtist,
			Date:        req.ReleaseDate,
			ReleaseDate: req.ReleaseDate,
			TrackNumber: trackNumber,
			TotalTracks: req.TotalTracks,
			DiscNumber:  req.DiscNumber,
			TotalDiscs:  req.SpotifyTotalDiscs,
			URL:         fmt.Sprintf("https://open.spotify.com/track/%s", trackID),
			Copyright:   req.Copyright,
			Publisher:   req.Publisher,
			Description: "https://github.com/afkarxyz/SpotiDownloader",
		},
	}
}

func (a *App) runTrackPipelineWithRequest(ctx context.Context, req DownloadRequest, pipelineReq backend.TrackPipelineRequest) (backend.TrackPipelineResult, error) {
	if a.runTrackPipeline != nil {
		return a.runTrackPipeline(ctx, pipelineReq)
	}
	downloader := backend.NewSpotiDownloader(req.SessionToken)
	defer downloader.CloseIdleConnections()
	return downloader.RunTrackPipeline(ctx, pipelineReq)
}

func completeDownloadTracking(itemID, filename string, timings backend.PipelineTimings) {
	if fileInfo, statErr := os.Stat(filename); statErr == nil {
		finalSize := float64(fileInfo.Size()) / (1024 * 1024)
		backend.CompleteDownloadItem(itemID, filename, finalSize, timings)
		return
	}

	backend.CompleteDownloadItem(itemID, filename, 0, timings)
}

func addHistoryItemAsync(req DownloadRequest, filename string) {
	go func() {
		if err := backend.AddHistoryItemFromFile(filename, req.TrackName, req.ArtistName, req.AlbumName, req.SpotifyID, req.CoverURL, "SpotiDownloader"); err != nil {
			fmt.Printf("[History] Failed to add history item for %s: %v\n", filename, err)
		}
	}()
}

// validateDownloadedDuration consumes a duration that has already been
// measured by the pipeline, so the App never probes the same file twice.
func validateDownloadedDuration(filename string, expectedDurationMs int, actualDurationSeconds float64) DownloadResponse {
	if expectedDurationMs <= 0 || filename == "" {
		return DownloadResponse{}
	}
	return buildDownloadValidationResult(expectedDurationMs, actualDurationSeconds, nil)
}

func buildDownloadValidationResult(expectedDurationMs int, actualSeconds float64, probeErr error) DownloadResponse {
	if expectedDurationMs <= 0 {
		return DownloadResponse{}
	}

	expectedSeconds := float64(expectedDurationMs) / 1000.0
	result := DownloadResponse{
		ExpectedDurationSeconds: expectedSeconds,
	}

	if probeErr != nil {
		result.ValidationWarning = fmt.Sprintf("failed to probe audio duration: %v", probeErr)
		return result
	}

	result.ActualDurationSeconds = actualSeconds
	result.DurationDeltaSeconds = actualSeconds - expectedSeconds
	result.Suspicious = backend.IsSuspiciousDuration(expectedSeconds, actualSeconds)
	return result
}

func moveSuspiciousDownloadIfNeeded(filename, collectionDir string, validation DownloadResponse) (DownloadResponse, error) {
	return moveSuspiciousDownloadIfNeededWithMover(filename, collectionDir, validation, backend.MoveSuspiciousOriginal)
}

func (a *App) moveSuspiciousDownloadIfNeeded(filename, collectionDir string, validation DownloadResponse) (DownloadResponse, error) {
	mover := a.moveSuspiciousDownload
	if mover == nil {
		mover = backend.MoveSuspiciousOriginal
	}
	return moveSuspiciousDownloadIfNeededWithMover(filename, collectionDir, validation, mover)
}

func moveSuspiciousDownloadIfNeededWithMover(filename, collectionDir string, validation DownloadResponse, mover func(string, string) (string, error)) (DownloadResponse, error) {
	if !validation.Suspicious {
		return validation, nil
	}

	if collectionDir == "" {
		collectionDir = filepath.Dir(filename)
	}

	movedPath, err := mover(filename, filepath.Join(collectionDir, backend.SuspiciousTracksDirName))
	if err != nil {
		return validation, err
	}

	validation.File = movedPath
	validation.MovedOriginalPath = movedPath
	validation.ReplacementPath = filename
	return validation, nil
}
