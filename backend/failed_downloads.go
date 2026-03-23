package backend

import (
	"fmt"
	"strings"
	"time"
)

func BuildFailedDownloadsReport(queueInfo DownloadQueueInfo, generatedAt time.Time) (string, int, bool) {
	var lines []string

	lines = append(lines, fmt.Sprintf("Failed Downloads Report - %s", generatedAt.Format("2006-01-02 15:04:05")))
	lines = append(lines, strings.Repeat("-", 50))
	lines = append(lines, "")

	count := 0
	for _, item := range queueInfo.Queue {
		if item.Status != StatusFailed {
			continue
		}

		count++
		line := fmt.Sprintf("%d. %s - %s", count, item.TrackName, item.ArtistName)
		if item.AlbumName != "" {
			line += fmt.Sprintf(" (%s)", item.AlbumName)
		}
		lines = append(lines, line)
		lines = append(lines, fmt.Sprintf("   Error: %s", item.ErrorMessage))

		if item.SpotifyID != "" {
			lines = append(lines, fmt.Sprintf("   ID: %s", item.SpotifyID))
			if !strings.HasPrefix(item.SpotifyID, "http") {
				lines = append(lines, fmt.Sprintf("   URL: https://open.spotify.com/track/%s", item.SpotifyID))
			}
		}
		lines = append(lines, "")
	}

	return strings.Join(lines, "\n"), count, count > 0
}
