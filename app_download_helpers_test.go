package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/afkarxyz/SpotiDownloader/backend"
)

func TestNormalizeDownloadRequestSetsDefaults(t *testing.T) {
	t.Parallel()

	req := DownloadRequest{
		TrackID:      "track123",
		SessionToken: "session-token",
		OutputDir:    t.TempDir(),
	}

	if err := normalizeDownloadRequest(&req); err != nil {
		t.Fatalf("normalizeDownloadRequest returned error: %v", err)
	}
	if req.AudioFormat != "mp3" {
		t.Fatalf("expected default audio format mp3, got %q", req.AudioFormat)
	}
	if req.FilenameFormat != "title-artist" {
		t.Fatalf("expected default filename format title-artist, got %q", req.FilenameFormat)
	}
}

func TestFindExistingDownloadPath(t *testing.T) {
	t.Parallel()

	outputDir := t.TempDir()
	req := DownloadRequest{
		TrackName:      "Track",
		ArtistName:     "Artist",
		AlbumName:      "Album",
		OutputDir:      outputDir,
		AudioFormat:    "mp3",
		FilenameFormat: "title-artist",
	}

	filenameBase := backend.SanitizeFilename(backend.BuildFilename(req.TrackName, req.ArtistName, req.AlbumName, req.AlbumArtist, req.ReleaseDate, req.DiscNumber, req.FilenameFormat, req.TrackNumber, req.Position, req.UseAlbumTrackNumber, req.PlaylistName, req.PlaylistOwner))
	expectedPath := filepath.Join(outputDir, filenameBase+".mp3")
	if err := os.WriteFile(expectedPath, []byte("audio"), 0o644); err != nil {
		t.Fatalf("failed to write fixture file: %v", err)
	}

	foundPath, found := findExistingDownloadPath(req)
	if !found {
		t.Fatal("expected existing download to be found")
	}
	if foundPath != expectedPath {
		t.Fatalf("expected %q, got %q", expectedPath, foundPath)
	}
}

func TestStartLyricsFetchDisabledClosesChannel(t *testing.T) {
	t.Parallel()

	lyricsChan := startLyricsFetch(DownloadRequest{}, "")

	select {
	case _, ok := <-lyricsChan:
		if ok {
			t.Fatal("expected closed lyrics channel")
		}
	case <-time.After(100 * time.Millisecond):
		t.Fatal("timed out waiting for closed lyrics channel")
	}
}
