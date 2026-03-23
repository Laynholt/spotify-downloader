package main

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/afkarxyz/SpotiDownloader/backend"
)

type CheckFileExistenceRequest struct {
	SpotifyID           string `json:"spotify_id"`
	TrackName           string `json:"track_name"`
	ArtistName          string `json:"artist_name"`
	AlbumName           string `json:"album_name,omitempty"`
	AlbumArtist         string `json:"album_artist,omitempty"`
	ReleaseDate         string `json:"release_date,omitempty"`
	TrackNumber         int    `json:"track_number,omitempty"`
	DiscNumber          int    `json:"disc_number,omitempty"`
	Position            int    `json:"position,omitempty"`
	UseAlbumTrackNumber bool   `json:"use_album_track_number,omitempty"`
	FilenameFormat      string `json:"filename_format,omitempty"`
	IncludeTrackNumber  bool   `json:"include_track_number,omitempty"`
	AudioFormat         string `json:"audio_format,omitempty"`
	RelativePath        string `json:"relative_path,omitempty"`
}

type CheckFileExistenceResult struct {
	SpotifyID  string `json:"spotify_id"`
	Exists     bool   `json:"exists"`
	FilePath   string `json:"file_path,omitempty"`
	TrackName  string `json:"track_name,omitempty"`
	ArtistName string `json:"artist_name,omitempty"`
}

func (a *App) CheckFilesExistence(outputDir string, rootDir string, audioFormat string, tracks []CheckFileExistenceRequest) []CheckFileExistenceResult {
	if len(tracks) == 0 {
		return []CheckFileExistenceResult{}
	}

	outputDir = backend.NormalizePath(outputDir)
	if rootDir != "" {
		rootDir = backend.NormalizePath(rootDir)
	}

	type result struct {
		index  int
		result CheckFileExistenceResult
	}

	resultsChan := make(chan result, len(tracks))
	for i, track := range tracks {
		go func(idx int, t CheckFileExistenceRequest) {
			resultsChan <- result{index: idx, result: checkTrackExistence(outputDir, audioFormat, t)}
		}(i, track)
	}

	results := make([]CheckFileExistenceResult, len(tracks))
	missingIndices := make([]int, 0, len(tracks))
	for i := 0; i < len(tracks); i++ {
		r := <-resultsChan
		results[r.index] = r.result
		if !r.result.Exists {
			missingIndices = append(missingIndices, r.index)
		}
	}

	if len(missingIndices) > 0 && rootDir != "" {
		resolveMissingTrackPaths(results, tracks, missingIndices, collectAudioFilesByName(rootDir), audioFormat)
	} else {
		clearMissingTrackPaths(results, missingIndices)
	}

	return results
}

func checkTrackExistence(outputDir, defaultAudioFormat string, track CheckFileExistenceRequest) CheckFileExistenceResult {
	res := CheckFileExistenceResult{
		SpotifyID:  track.SpotifyID,
		TrackName:  track.TrackName,
		ArtistName: track.ArtistName,
		Exists:     false,
	}

	if track.TrackName == "" || track.ArtistName == "" {
		return res
	}

	filenameBase := buildTrackFilenameBase(track)
	targetDir := outputDir
	if track.RelativePath != "" {
		targetDir = filepath.Join(outputDir, track.RelativePath)
	}

	audioFormatToCheck := track.AudioFormat
	if audioFormatToCheck == "" {
		audioFormatToCheck = defaultAudioFormat
	}

	for _, ext := range backend.PossibleAudioExtensions(audioFormatToCheck) {
		expectedPath := filepath.Join(targetDir, filenameBase+ext)
		if fileInfo, err := os.Stat(expectedPath); err == nil && fileInfo.Size() > 100*1024 {
			res.Exists = true
			res.FilePath = expectedPath
			return res
		}
	}

	res.FilePath = filenameBase
	return res
}

func buildTrackFilenameBase(track CheckFileExistenceRequest) string {
	filenameFormat := track.FilenameFormat
	if filenameFormat == "" {
		filenameFormat = "title-artist"
	}

	trackNumber := track.Position
	if track.UseAlbumTrackNumber && track.TrackNumber > 0 {
		trackNumber = track.TrackNumber
	}

	filenameBase := backend.BuildFilename(
		track.TrackName,
		track.ArtistName,
		track.AlbumName,
		track.AlbumArtist,
		track.ReleaseDate,
		track.DiscNumber,
		filenameFormat,
		track.IncludeTrackNumber,
		trackNumber,
		track.UseAlbumTrackNumber,
		"",
		"",
	)

	return backend.SanitizeFilename(filenameBase)
}

func collectAudioFilesByName(rootDir string) map[string]string {
	files := make(map[string]string)
	filepath.Walk(rootDir, func(path string, info os.FileInfo, err error) error {
		if err != nil || info == nil || info.IsDir() {
			return nil
		}

		if isAudioFile(path) {
			files[info.Name()] = path
		}
		return nil
	})
	return files
}

func resolveMissingTrackPaths(results []CheckFileExistenceResult, tracks []CheckFileExistenceRequest, missingIndices []int, filesMap map[string]string, defaultAudioFormat string) {
	if len(filesMap) == 0 {
		clearMissingTrackPaths(results, missingIndices)
		return
	}

	for _, idx := range missingIndices {
		filenameBase := buildTrackFilenameBase(tracks[idx])
		audioFormatToCheck := tracks[idx].AudioFormat
		if audioFormatToCheck == "" {
			audioFormatToCheck = defaultAudioFormat
		}

		for _, ext := range backend.PossibleAudioExtensions(audioFormatToCheck) {
			if path, ok := filesMap[filepath.Base(filenameBase+ext)]; ok {
				results[idx].Exists = true
				results[idx].FilePath = path
				break
			}
		}

		if !results[idx].Exists {
			results[idx].FilePath = ""
		}
	}
}

func clearMissingTrackPaths(results []CheckFileExistenceResult, missingIndices []int) {
	for _, idx := range missingIndices {
		results[idx].FilePath = ""
	}
}

func isAudioFile(path string) bool {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".flac", ".mp3", ".m4a":
		return true
	default:
		return false
	}
}
