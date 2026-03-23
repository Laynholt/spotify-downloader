package backend

import (
	"fmt"
	"path/filepath"
	"strings"
)

func AddHistoryItemFromFile(filePath, title, artist, album, spotifyID, coverURL, appName string) error {
	quality := "Unknown"
	durationStr := "--:--"

	meta, err := GetTrackMetadata(filePath)
	if err == nil && meta != nil {
		quality = buildHistoryQuality(meta)
		durationStr = buildHistoryDuration(meta.Duration)
	} else if err != nil {
		return fmt.Errorf("failed to get metadata: %w", err)
	}

	item := HistoryItem{
		SpotifyID:   spotifyID,
		Title:       title,
		Artists:     artist,
		Album:       album,
		DurationStr: durationStr,
		CoverURL:    coverURL,
		Quality:     quality,
		Format:      strings.TrimPrefix(strings.ToUpper(filepath.Ext(filePath)), "."),
		Path:        filePath,
	}
	if item.Format == "" {
		item.Format = strings.ToUpper(strings.TrimPrefix(filepath.Ext(filePath), "."))
	}

	return AddHistoryItem(item, appName)
}

func buildHistoryQuality(meta *AnalysisResult) string {
	if meta == nil {
		return "Unknown"
	}
	if meta.BitsPerSample > 0 {
		return fmt.Sprintf("%d-bit/%.1fkHz", meta.BitsPerSample, float64(meta.SampleRate)/1000.0)
	}
	if meta.Bitrate > 0 {
		return fmt.Sprintf("%dkbps/%.1fkHz", meta.Bitrate/1000, float64(meta.SampleRate)/1000.0)
	}
	if meta.SampleRate > 0 {
		return fmt.Sprintf("%.1fkHz", float64(meta.SampleRate)/1000.0)
	}
	return "Unknown"
}

func buildHistoryDuration(duration float64) string {
	if duration <= 0 {
		return "--:--"
	}

	seconds := int(duration)
	return fmt.Sprintf("%d:%02d", seconds/60, seconds%60)
}
