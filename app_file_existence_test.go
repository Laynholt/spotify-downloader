package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCheckTrackExistenceFindsOutputFile(t *testing.T) {
	t.Parallel()

	outputDir := t.TempDir()
	track := CheckFileExistenceRequest{
		SpotifyID:      "abc",
		TrackName:      "Track",
		ArtistName:     "Artist",
		AudioFormat:    "mp3",
		FilenameFormat: "title-artist",
	}

	filenameBase := buildTrackFilenameBase(track)
	expectedPath := filepath.Join(outputDir, filenameBase+".mp3")
	if err := os.WriteFile(expectedPath, make([]byte, 150*1024), 0o644); err != nil {
		t.Fatalf("failed to write fixture file: %v", err)
	}

	res := checkTrackExistence(outputDir, "mp3", track)
	if !res.Exists {
		t.Fatal("expected file to exist")
	}
	if res.FilePath != expectedPath {
		t.Fatalf("expected %q, got %q", expectedPath, res.FilePath)
	}
}

func TestResolveMissingTrackPathsUsesRootDirFallback(t *testing.T) {
	t.Parallel()

	rootDir := t.TempDir()
	track := CheckFileExistenceRequest{
		SpotifyID:      "abc",
		TrackName:      "Track",
		ArtistName:     "Artist",
		AudioFormat:    "mp3",
		FilenameFormat: "title-artist",
	}

	filenameBase := buildTrackFilenameBase(track)
	expectedPath := filepath.Join(rootDir, filenameBase+".mp3")
	if err := os.WriteFile(expectedPath, make([]byte, 150*1024), 0o644); err != nil {
		t.Fatalf("failed to write fixture file: %v", err)
	}

	results := []CheckFileExistenceResult{{
		SpotifyID:  track.SpotifyID,
		TrackName:  track.TrackName,
		ArtistName: track.ArtistName,
		FilePath:   filenameBase,
	}}
	resolveMissingTrackPaths(results, []CheckFileExistenceRequest{track}, []int{0}, collectAudioFilesByName(rootDir), "mp3")

	if !results[0].Exists {
		t.Fatal("expected fallback match to mark file as existing")
	}
	if results[0].FilePath != expectedPath {
		t.Fatalf("expected %q, got %q", expectedPath, results[0].FilePath)
	}
}
