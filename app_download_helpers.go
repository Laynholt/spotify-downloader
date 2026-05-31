package main

import (
	"context"
	"encoding/json"
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
	backend.AddToQueue(itemID, req.TrackName, req.ArtistName, req.AlbumName, trackIDForItemID)
	return itemID
}

func primaryTrackID(req DownloadRequest) string {
	if req.TrackID != "" {
		return req.TrackID
	}
	return req.SpotifyID
}

func metadataTrackID(req DownloadRequest) string {
	if req.SpotifyID != "" {
		return req.SpotifyID
	}
	return req.TrackID
}

func enrichDownloadRequestMetadata(req *DownloadRequest) {
	spotifyTrackID := metadataTrackID(*req)
	if spotifyTrackID == "" {
		return
	}

	needsSpotifyMetadata := req.Copyright == "" ||
		req.Publisher == "" ||
		req.SpotifyTotalDiscs == 0 ||
		req.ReleaseDate == "" ||
		req.TotalTracks == 0 ||
		req.AlbumTrackNumber == 0
	if !needsSpotifyMetadata {
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	trackURL := fmt.Sprintf("https://open.spotify.com/track/%s", spotifyTrackID)
	trackData, err := backend.GetFilteredSpotifyData(ctx, trackURL, false, 0)
	if err != nil {
		return
	}

	var trackResp struct {
		Track struct {
			Copyright   string `json:"copyright"`
			Publisher   string `json:"publisher"`
			TotalDiscs  int    `json:"total_discs"`
			TotalTracks int    `json:"total_tracks"`
			TrackNumber int    `json:"track_number"`
			ReleaseDate string `json:"release_date"`
		} `json:"track"`
	}
	jsonData, jsonErr := json.Marshal(trackData)
	if jsonErr != nil || json.Unmarshal(jsonData, &trackResp) != nil {
		return
	}

	if req.Copyright == "" && trackResp.Track.Copyright != "" {
		req.Copyright = trackResp.Track.Copyright
	}
	if req.Publisher == "" && trackResp.Track.Publisher != "" {
		req.Publisher = trackResp.Track.Publisher
	}
	if req.SpotifyTotalDiscs == 0 && trackResp.Track.TotalDiscs > 0 {
		req.SpotifyTotalDiscs = trackResp.Track.TotalDiscs
	}
	if req.TotalTracks == 0 && trackResp.Track.TotalTracks > 0 {
		req.TotalTracks = trackResp.Track.TotalTracks
	}
	if req.AlbumTrackNumber == 0 && trackResp.Track.TrackNumber > 0 {
		req.AlbumTrackNumber = trackResp.Track.TrackNumber
	}
	if req.ReleaseDate == "" && trackResp.Track.ReleaseDate != "" {
		req.ReleaseDate = trackResp.Track.ReleaseDate
	}
}

func findExistingDownloadPath(req DownloadRequest) (string, bool) {
	if req.TrackName == "" || req.ArtistName == "" {
		return "", false
	}

	filenameBase := backend.BuildFilename(req.TrackName, req.ArtistName, req.AlbumName, req.AlbumArtist, req.ReleaseDate, req.DiscNumber, req.FilenameFormat, req.TrackNumber, req.Position, req.UseAlbumTrackNumber, req.PlaylistName, req.PlaylistOwner)
	filenameBase = backend.SanitizeFilename(filenameBase)

	for _, ext := range backend.PossibleAudioExtensions(req.AudioFormat) {
		expectedPath := filepath.Join(req.OutputDir, filenameBase+ext)
		if fileInfo, err := os.Stat(expectedPath); err == nil && fileInfo.Size() > 0 {
			return expectedPath, true
		}
	}

	return "", false
}

func startLyricsFetch(req DownloadRequest, trackID string) <-chan string {
	lyricsChan := make(chan string, 1)
	if !req.EmbedLyrics || trackID == "" {
		close(lyricsChan)
		return lyricsChan
	}

	go func() {
		defer close(lyricsChan)

		fmt.Println("Fetching lyrics in background...")
		client := backend.NewLyricsClient()
		resp, _, err := client.FetchLyricsAllSources(trackID, req.TrackName, req.ArtistName, req.Duration)
		if err == nil && resp != nil && len(resp.Lines) > 0 {
			lyricsChan <- client.ConvertToLRC(resp, req.TrackName, req.ArtistName)
			return
		}

		lyricsChan <- ""
	}()

	return lyricsChan
}

func downloadTrackFile(req DownloadRequest, trackID string) (string, error) {
	downloader := backend.NewSpotiDownloader(req.SessionToken)
	defer downloader.CloseIdleConnections()

	actualTrackNumber := req.AlbumTrackNumber
	if actualTrackNumber == 0 {
		actualTrackNumber = 1
	}

	return downloader.DownloadTrack(
		trackID,
		req.OutputDir,
		req.AudioFormat,
		req.FilenameFormat,
		req.TrackNumber,
		req.Position,
		req.TrackName,
		req.ArtistName,
		req.AlbumName,
		req.AlbumArtist,
		req.ReleaseDate,
		req.CoverURL,
		actualTrackNumber,
		req.DiscNumber,
		req.TotalTracks,
		req.UseAlbumTrackNumber,
		req.EmbedMaxQualityCover,
		req.SpotifyTotalDiscs,
		req.Copyright,
		req.Publisher,
		req.PlaylistName,
		req.PlaylistOwner,
		req.UseFirstArtistOnly,
		req.UseSingleGenre,
		req.EmbedGenre,
	)
}

func completeDownloadTracking(itemID, filename string) {
	if fileInfo, statErr := os.Stat(filename); statErr == nil {
		finalSize := float64(fileInfo.Size()) / (1024 * 1024)
		backend.CompleteDownloadItem(itemID, filename, finalSize)
		return
	}

	backend.CompleteDownloadItem(itemID, filename, 0)
}

func addHistoryItemAsync(req DownloadRequest, filename string) {
	go func() {
		if err := backend.AddHistoryItemFromFile(filename, req.TrackName, req.ArtistName, req.AlbumName, req.SpotifyID, req.CoverURL, "SpotiDownloader"); err != nil {
			fmt.Printf("[History] Failed to add history item for %s: %v\n", filename, err)
		}
	}()
}
