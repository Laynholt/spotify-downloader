package backend

import (
	"path/filepath"
	"testing"
)

func TestBuildTrackMetadataPayloadMapsRequestFields(t *testing.T) {
	req := TrackMetadataUpdateRequest{
		TrackID:     "spotify-track",
		TrackName:   "Title",
		ArtistName:  "Artist",
		AlbumName:   "Album",
		AlbumArtist: "Album Artist",
		ReleaseDate: "2026-06-09",
		TrackNumber: 7,
		TotalTracks: 12,
		DiscNumber:  2,
		TotalDiscs:  3,
		Copyright:   "Copyright",
		Publisher:   "Publisher",
		ISRC:        "ISRC123",
		Genre:       "Pop",
	}

	got := BuildTrackMetadataPayload(req)

	if got.Title != req.TrackName || got.Artist != req.ArtistName || got.Album != req.AlbumName {
		t.Fatalf("basic metadata mismatch: %#v", got)
	}
	if got.URL != "https://open.spotify.com/track/spotify-track" {
		t.Fatalf("URL = %q", got.URL)
	}
	if got.TrackNumber != 7 || got.TotalTracks != 12 || got.DiscNumber != 2 || got.TotalDiscs != 3 {
		t.Fatalf("number metadata mismatch: %#v", got)
	}
	if got.ISRC != req.ISRC || got.Genre != req.Genre {
		t.Fatalf("extended metadata mismatch: %#v", got)
	}
}

func TestBuildYtDlpSearchQueryUsesArtistAndTitle(t *testing.T) {
	got := BuildYtDlpSearchQuery("Artist", "Song")
	want := "ytsearch1:Artist - Song audio"
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestRedownloadSuspiciousTrackFromYouTubeUsesReplacementPathForAlreadyMovedOriginal(t *testing.T) {
	dir := t.TempDir()
	collectionDir := filepath.Join(dir, "Playlist")
	suspiciousDir := filepath.Join(collectionDir, "Suspicious")
	originalPath := filepath.Join(suspiciousDir, "Song.mp3")
	replacementPath := filepath.Join(collectionDir, "Song.mp3")

	downloadCalled := false
	originalDownloadWithYtDlp := downloadWithYtDlp
	originalUpdateTrackMetadata := updateTrackMetadata
	t.Cleanup(func() {
		downloadWithYtDlp = originalDownloadWithYtDlp
		updateTrackMetadata = originalUpdateTrackMetadata
	})

	downloadWithYtDlp = func(req YtDlpDownloadRequest) YtDlpDownloadResult {
		downloadCalled = true
		wantOutputBase := filepath.Join(collectionDir, "Song")
		if req.OutputBase != wantOutputBase {
			t.Fatalf("OutputBase = %q, want %q", req.OutputBase, wantOutputBase)
		}
		return YtDlpDownloadResult{Success: true, File: replacementPath}
	}
	updateTrackMetadata = func(req TrackMetadataUpdateRequest) TrackMetadataUpdateResult {
		if req.FilePath != replacementPath {
			t.Fatalf("metadata FilePath = %q, want replacement path %q", req.FilePath, replacementPath)
		}
		return TrackMetadataUpdateResult{Success: true, FilePath: req.FilePath, TrackID: req.TrackID}
	}

	result := RedownloadSuspiciousTrackFromYouTube(SuspiciousRedownloadRequest{
		OriginalFilePath:    originalPath,
		ReplacementFilePath: replacementPath,
		CollectionDir:       collectionDir,
		AudioFormat:         "mp3",
		Metadata: TrackMetadataUpdateRequest{
			TrackID:    "spotify-track",
			TrackName:  "Song",
			ArtistName: "Artist",
		},
	})

	if !downloadCalled {
		t.Fatal("expected yt-dlp download to be called")
	}
	if !result.Success || result.Status != "replaced" {
		t.Fatalf("unexpected result: %#v", result)
	}
	if result.MovedOriginalPath != originalPath {
		t.Fatalf("MovedOriginalPath = %q, want already moved original path %q", result.MovedOriginalPath, originalPath)
	}
	if result.ReplacementPath != replacementPath {
		t.Fatalf("ReplacementPath = %q, want %q", result.ReplacementPath, replacementPath)
	}
}

func TestRedownloadSuspiciousTrackFromYouTubeDownloadsFailedTrackWithoutOriginal(t *testing.T) {
	dir := t.TempDir()
	collectionDir := filepath.Join(dir, "Playlist")
	replacementPath := filepath.Join(collectionDir, "Failed Song.mp3")

	originalDownloadWithYtDlp := downloadWithYtDlp
	originalUpdateTrackMetadata := updateTrackMetadata
	t.Cleanup(func() {
		downloadWithYtDlp = originalDownloadWithYtDlp
		updateTrackMetadata = originalUpdateTrackMetadata
	})

	downloadWithYtDlp = func(req YtDlpDownloadRequest) YtDlpDownloadResult {
		wantOutputBase := filepath.Join(collectionDir, "Failed Song")
		if req.OutputBase != wantOutputBase {
			t.Fatalf("OutputBase = %q, want %q", req.OutputBase, wantOutputBase)
		}
		return YtDlpDownloadResult{Success: true, File: replacementPath}
	}
	updateTrackMetadata = func(req TrackMetadataUpdateRequest) TrackMetadataUpdateResult {
		if req.FilePath != replacementPath {
			t.Fatalf("metadata FilePath = %q, want %q", req.FilePath, replacementPath)
		}
		if req.TrackNumber != 1 {
			t.Fatalf("TrackNumber = %d, want fallback 1", req.TrackNumber)
		}
		if req.DiscNumber != 1 {
			t.Fatalf("DiscNumber = %d, want fallback 1", req.DiscNumber)
		}
		return TrackMetadataUpdateResult{Success: true, FilePath: req.FilePath, TrackID: req.TrackID}
	}

	result := RedownloadSuspiciousTrackFromYouTube(SuspiciousRedownloadRequest{
		ReplacementFilePath: replacementPath,
		CollectionDir:       collectionDir,
		AudioFormat:         "mp3",
		Metadata: TrackMetadataUpdateRequest{
			TrackID:     "spotify-track",
			TrackName:   "Failed Song",
			ArtistName:  "Artist",
			TrackNumber: 301,
			DiscNumber:  0,
		},
	})

	if !result.Success || result.Status != "replaced" {
		t.Fatalf("unexpected result: %#v", result)
	}
	if result.MovedOriginalPath != "" {
		t.Fatalf("MovedOriginalPath = %q, want empty for failed track without original", result.MovedOriginalPath)
	}
	if result.ReplacementPath != replacementPath {
		t.Fatalf("ReplacementPath = %q, want %q", result.ReplacementPath, replacementPath)
	}
}
