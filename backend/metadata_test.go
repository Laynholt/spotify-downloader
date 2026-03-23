package backend

import (
	"os"
	"path/filepath"
	"testing"

	"os/exec"
)

func TestPromoteMP3TagTempFileReplacesOriginal(t *testing.T) {
	dir := t.TempDir()
	originalPath := filepath.Join(dir, "track.mp3")
	tempPath := originalPath + "-id3v2"

	if err := os.WriteFile(originalPath, []byte("original"), 0644); err != nil {
		t.Fatalf("write original: %v", err)
	}
	if err := os.WriteFile(tempPath, []byte("tagged"), 0644); err != nil {
		t.Fatalf("write temp: %v", err)
	}

	if err := promoteMP3TagTempFile(tempPath, originalPath); err != nil {
		t.Fatalf("promote temp mp3: %v", err)
	}

	data, err := os.ReadFile(originalPath)
	if err != nil {
		t.Fatalf("read final file: %v", err)
	}
	if string(data) != "tagged" {
		t.Fatalf("expected promoted content, got %q", string(data))
	}

	if _, err := os.Stat(tempPath); !os.IsNotExist(err) {
		t.Fatalf("expected temp file to be removed, got err=%v", err)
	}
}

func TestEmbedMetadataAppliesToOriginalMP3(t *testing.T) {
	ffmpegPath, err := GetFFmpegPath()
	if err != nil {
		t.Skipf("ffmpeg not available: %v", err)
	}

	dir := t.TempDir()
	audioPath := filepath.Join(dir, "East Duo - ჩუბინა.mp3")

	cmd := exec.Command(ffmpegPath,
		"-f", "lavfi",
		"-i", "sine=frequency=1000:duration=1",
		"-q:a", "2",
		"-y",
		audioPath,
	)
	setHideWindow(cmd)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("generate mp3 fixture: %v\n%s", err, string(output))
	}

	metadata := Metadata{
		Title:       "ჩუბინა",
		Artist:      "East Duo",
		Album:       "Test Album",
		AlbumArtist: "East Duo",
		Date:        "2026-03-24",
		TrackNumber: 1,
		TotalTracks: 1,
		Genre:       "Folk",
	}

	if err := EmbedMetadata(audioPath, metadata, ""); err != nil {
		t.Fatalf("embed metadata: %v", err)
	}

	if err := EmbedLyricsOnly(audioPath, "[00:00.00]test"); err != nil {
		t.Fatalf("embed lyrics: %v", err)
	}

	if err := FinalizeTaggedAudioFile(audioPath); err != nil {
		t.Fatalf("finalize tagged audio file: %v", err)
	}

	if _, err := os.Stat(audioPath + "-id3v2"); !os.IsNotExist(err) {
		t.Fatalf("expected mp3 temp file to be removed, got err=%v", err)
	}

	actual, err := ExtractFullMetadataFromFile(audioPath)
	if err != nil {
		t.Fatalf("extract metadata from original file: %v", err)
	}

	if actual.Title == "" || actual.Artist == "" || actual.Album == "" {
		t.Fatalf("expected metadata on original file, got %+v", actual)
	}
}

func TestConvertedM4AToMP3KeepsMetadataOnOriginalFile(t *testing.T) {
	ffmpegPath, err := GetFFmpegPath()
	if err != nil {
		t.Skipf("ffmpeg not available: %v", err)
	}

	dir := t.TempDir()
	inputPath := filepath.Join(dir, "East Duo - ჩუბინა.m4a")
	outputBase := filepath.Join(dir, "East Duo - ჩუბინა")
	coverPath := filepath.Join(dir, "cover.jpg")

	audioCmd := exec.Command(ffmpegPath,
		"-f", "lavfi",
		"-i", "sine=frequency=800:duration=1",
		"-c:a", "aac",
		"-b:a", "192k",
		"-y",
		inputPath,
	)
	setHideWindow(audioCmd)
	if output, err := audioCmd.CombinedOutput(); err != nil {
		t.Fatalf("generate m4a fixture: %v\n%s", err, string(output))
	}

	coverCmd := exec.Command(ffmpegPath,
		"-f", "lavfi",
		"-i", "color=c=red:s=300x300:d=1",
		"-frames:v", "1",
		"-y",
		coverPath,
	)
	setHideWindow(coverCmd)
	if output, err := coverCmd.CombinedOutput(); err != nil {
		t.Fatalf("generate cover fixture: %v\n%s", err, string(output))
	}

	convertedPath, err := convertDownloadedAudio(inputPath, outputBase, "mp3")
	if err != nil {
		t.Fatalf("convert m4a to mp3: %v", err)
	}

	metadata := Metadata{
		Title:       "ჩუბინა",
		Artist:      "East Duo",
		Album:       "Converted Album",
		AlbumArtist: "East Duo",
		Date:        "2026-03-24",
		TrackNumber: 1,
		TotalTracks: 1,
		Genre:       "Folk",
	}

	if err := EmbedMetadata(convertedPath, metadata, coverPath); err != nil {
		t.Fatalf("embed metadata into converted mp3: %v", err)
	}

	if err := EmbedLyricsOnly(convertedPath, "[00:00.00]test"); err != nil {
		t.Fatalf("embed lyrics into converted mp3: %v", err)
	}

	if err := FinalizeTaggedAudioFile(convertedPath); err != nil {
		t.Fatalf("finalize converted mp3: %v", err)
	}

	if _, err := os.Stat(convertedPath + "-id3v2"); !os.IsNotExist(err) {
		t.Fatalf("expected converted mp3 temp file to be removed, got err=%v", err)
	}

	actual, err := ExtractFullMetadataFromFile(convertedPath)
	if err != nil {
		t.Fatalf("extract metadata from converted mp3: %v", err)
	}

	if actual.Title == "" || actual.Artist == "" || actual.Album == "" {
		t.Fatalf("expected metadata on converted mp3, got %+v", actual)
	}
}
