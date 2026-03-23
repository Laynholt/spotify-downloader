package backend

import (
	"strings"
	"testing"
	"time"
)

func TestBuildFailedDownloadsReport(t *testing.T) {
	t.Parallel()

	report, count, hasFailed := BuildFailedDownloadsReport(DownloadQueueInfo{
		Queue: []DownloadItem{
			{
				Status:       StatusFailed,
				TrackName:    "Track",
				ArtistName:   "Artist",
				AlbumName:    "Album",
				SpotifyID:    "abc123",
				ErrorMessage: "boom",
			},
			{
				Status:    StatusCompleted,
				TrackName: "Ignore",
			},
		},
	}, time.Date(2026, 3, 24, 12, 0, 0, 0, time.UTC))

	if !hasFailed {
		t.Fatal("expected hasFailed to be true")
	}
	if count != 1 {
		t.Fatalf("expected count 1, got %d", count)
	}
	if !strings.Contains(report, "Track - Artist (Album)") {
		t.Fatalf("expected report to contain failed item, got %q", report)
	}
	if !strings.Contains(report, "https://open.spotify.com/track/abc123") {
		t.Fatalf("expected report to contain spotify URL, got %q", report)
	}
}
