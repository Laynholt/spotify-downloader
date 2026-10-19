package backend

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"os/exec"

	id3v2 "github.com/bogem/id3v2/v2"
	"github.com/go-flac/go-flac"
)

func TestValidateLyricsDurationUsesProvidedDuration(t *testing.T) {
	lyrics := "[00:00.50]keep\n[00:01.00]boundary\n[00:01.01]drop\nplain text"

	got, err := ValidateLyricsDuration(lyrics, 1.0)
	if err != nil {
		t.Fatalf("validate lyrics duration: %v", err)
	}

	want := "[00:00.50]keep\n[00:01.00]boundary\nplain text"
	if got != want {
		t.Fatalf("validated lyrics = %q, want %q", got, want)
	}
}

func TestExtractLyricsM4ADoesNotProbe(t *testing.T) {
	installFakeFFmpegRuntime(t)
	probeCount := filepath.Join(t.TempDir(), "ffprobe-count")
	t.Setenv(testHelperFFprobeCount, probeCount)
	t.Setenv(testHelperFFprobeOutput, `{"format":{"duration":"1.0","tags":{"lyrics":"[00:00.50]line"}}}`)

	lyrics, err := ExtractLyrics(filepath.Join(t.TempDir(), "track.m4a"))
	if err != nil {
		t.Fatalf("extract m4a lyrics: %v", err)
	}
	if lyrics != "" {
		t.Fatalf("m4a lyrics = %q, want empty legacy wrapper result", lyrics)
	}
	if count := readMetadataTestCounter(t, probeCount); count != 0 {
		t.Fatalf("m4a ExtractLyrics ffprobe calls = %d, want 0", count)
	}
}

func TestConvertAudioContextUsesOneProbeAndMetadataPassForLyrics(t *testing.T) {
	systemFFmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skipf("ffmpeg not available: %v", err)
	}
	dir := t.TempDir()
	inputPath := filepath.Join(dir, "source.mp3")
	writeFFmpegFixture(t, systemFFmpeg, []string{
		"-f", "lavfi", "-i", "sine=frequency=1000:duration=1", "-q:a", "2", "-y", inputPath,
	})
	tag, err := id3v2.Open(inputPath, id3v2.Options{Parse: true})
	if err != nil {
		t.Fatalf("open mp3 fixture: %v", err)
	}
	tag.AddUnsynchronisedLyricsFrame(id3v2.UnsynchronisedLyricsFrame{
		Encoding:          id3v2.EncodingUTF8,
		Language:          "eng",
		ContentDescriptor: "",
		Lyrics:            "[00:00.50]line",
	})
	if err := saveMP3Tag(tag, inputPath); err != nil {
		t.Fatalf("save mp3 lyrics fixture: %v", err)
	}

	installFakeFFmpegRuntime(t)
	t.Setenv(testHelperFFmpegSuccess, "1")
	ffprobeCount := filepath.Join(dir, "ffprobe-count")
	ffmpegMetadataCount := filepath.Join(dir, "ffmpeg-metadata-count")
	t.Setenv(testHelperFFprobeCount, ffprobeCount)
	t.Setenv(testHelperFFmpegMetadata, ffmpegMetadataCount)
	t.Setenv(testHelperFFprobeOutput, `{"format":{"duration":"1.0","tags":{"lyrics":"[00:00.50]line"}}}`)

	results, err := ConvertAudioContext(context.Background(), ConvertAudioRequest{
		InputFiles:   []string{inputPath},
		OutputFormat: "m4a",
		Bitrate:      "128k",
	})
	if err != nil {
		t.Fatalf("convert audio: %v", err)
	}
	if len(results) != 1 || !results[0].Success {
		t.Fatalf("conversion results = %#v", results)
	}
	if count := readMetadataTestCounter(t, ffprobeCount); count != 1 {
		t.Fatalf("conversion ffprobe calls = %d, want 1", count)
	}
	if count := readMetadataTestCounter(t, ffmpegMetadataCount); count != 1 {
		t.Fatalf("metadata ffmpeg passes = %d, want 1", count)
	}
}

func readMetadataTestCounter(t *testing.T, path string) int {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return 0
		}
		t.Fatalf("read counter %s: %v", path, err)
	}
	count, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		t.Fatalf("parse counter %s: %v", path, err)
	}
	return count
}

func TestFinalizeMediaWritesMP3TagsLyricsAndCoverInOnePass(t *testing.T) {
	ffmpegPath, ffprobePath := installSystemFFmpegRuntime(t)
	dir := t.TempDir()
	audioPath := filepath.Join(dir, "track.mp3")
	coverPath := filepath.Join(dir, "cover.jpg")

	writeFFmpegFixture(t, ffmpegPath, []string{
		"-f", "lavfi", "-i", "sine=frequency=1000:duration=1", "-q:a", "2", "-y", audioPath,
	})
	writeFFmpegFixture(t, ffmpegPath, []string{
		"-f", "lavfi", "-i", "color=c=red:s=32x32:d=1", "-frames:v", "1", "-y", coverPath,
	})

	metadata := Metadata{Title: "One Pass", Artist: "Artist", Lyrics: "[00:00.50]line"}
	if err := FinalizeMedia(context.Background(), audioPath, metadata, coverPath); err != nil {
		t.Fatalf("finalize mp3: %v", err)
	}

	lyrics, err := ExtractLyrics(audioPath)
	if err != nil {
		t.Fatalf("extract mp3 lyrics: %v", err)
	}
	if !strings.Contains(lyrics, "[00:00.50]line") {
		t.Fatalf("mp3 lyrics = %q", lyrics)
	}

	tag, err := id3v2.Open(audioPath, id3v2.Options{Parse: true})
	if err != nil {
		t.Fatalf("open mp3 tags: %v", err)
	}
	defer tag.Close()
	if pictures := tag.GetFrames(tag.CommonID("Attached picture")); len(pictures) != 1 {
		t.Fatalf("mp3 cover frames = %d, want 1", len(pictures))
	}
	if _, err := os.Stat(audioPath + "-id3v2"); !os.IsNotExist(err) {
		t.Fatalf("mp3 temporary tag file remains: %v", err)
	}
	_ = ffprobePath
}

func TestFinalizeMediaWritesFLACTagsLyricsAndCoverInOnePass(t *testing.T) {
	ffmpegPath, _ := installSystemFFmpegRuntime(t)
	dir := t.TempDir()
	audioPath := filepath.Join(dir, "track.flac")
	coverPath := filepath.Join(dir, "cover.jpg")

	writeFFmpegFixture(t, ffmpegPath, []string{
		"-f", "lavfi", "-i", "sine=frequency=1000:duration=1", "-c:a", "flac", "-y", audioPath,
	})
	writeFFmpegFixture(t, ffmpegPath, []string{
		"-f", "lavfi", "-i", "color=c=red:s=32x32:d=1", "-frames:v", "1", "-y", coverPath,
	})

	metadata := Metadata{Title: "One Pass", Artist: "Artist", Lyrics: "[00:00.50]line"}
	if err := FinalizeMedia(context.Background(), audioPath, metadata, coverPath); err != nil {
		t.Fatalf("finalize flac: %v", err)
	}

	lyrics, err := ExtractLyrics(audioPath)
	if err != nil {
		t.Fatalf("extract flac lyrics: %v", err)
	}
	if !strings.Contains(lyrics, "[00:00.50]line") {
		t.Fatalf("flac lyrics = %q", lyrics)
	}
	flacFile, err := flac.ParseFile(audioPath)
	if err != nil {
		t.Fatalf("parse finalized flac: %v", err)
	}
	var commentBlocks, pictureBlocks int
	for _, block := range flacFile.Meta {
		switch block.Type {
		case flac.VorbisComment:
			commentBlocks++
		case flac.Picture:
			pictureBlocks++
		}
	}
	if commentBlocks != 1 || pictureBlocks != 1 {
		t.Fatalf("flac metadata blocks = comments:%d pictures:%d, want one each", commentBlocks, pictureBlocks)
	}
}

func TestFinalizeMediaWritesM4ATagsLyricsAndCoverInOnePass(t *testing.T) {
	ffmpegPath, ffprobePath := installSystemFFmpegRuntime(t)
	dir := t.TempDir()
	audioPath := filepath.Join(dir, "track.m4a")
	coverPath := filepath.Join(dir, "cover.jpg")

	writeFFmpegFixture(t, ffmpegPath, []string{
		"-f", "lavfi", "-i", "sine=frequency=1000:duration=1", "-c:a", "aac", "-y", audioPath,
	})
	writeFFmpegFixture(t, ffmpegPath, []string{
		"-f", "lavfi", "-i", "color=c=red:s=32x32:d=1", "-frames:v", "1", "-y", coverPath,
	})

	metadata := Metadata{Title: "One Pass", Artist: "Artist", Lyrics: "[00:00.50]line"}
	reportPath := "task7-ffmpeg-report-" + strconv.FormatInt(time.Now().UnixNano(), 10) + ".log"
	t.Cleanup(func() { _ = os.Remove(reportPath) })
	t.Setenv("FFREPORT", "file="+reportPath+":level=32")
	if err := FinalizeMedia(context.Background(), audioPath, metadata, coverPath); err != nil {
		t.Fatalf("finalize m4a: %v", err)
	}
	report, err := os.ReadFile(reportPath)
	if err != nil {
		t.Fatalf("read ffmpeg report: %v", err)
	}
	if invocations := strings.Count(string(report), "ffmpeg version"); invocations != 1 {
		t.Fatalf("finalize ffmpeg invocations = %d, want 1", invocations)
	}

	lyricsOutput := runFFprobeFixture(t, ffprobePath, audioPath, "-show_entries", "format_tags=lyrics")
	if !strings.Contains(lyricsOutput, "[00:00.50]line") {
		t.Fatalf("m4a lyrics metadata = %q", lyricsOutput)
	}
	streamTypes := runFFprobeFixture(t, ffprobePath, audioPath, "-show_entries", "stream=codec_type")
	if !strings.Contains(streamTypes, "video") {
		t.Fatalf("m4a cover stream missing: %q", streamTypes)
	}
}

func installSystemFFmpegRuntime(t *testing.T) (string, string) {
	t.Helper()
	ffmpegPath, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skipf("ffmpeg not available: %v", err)
	}
	ffprobePath, err := exec.LookPath("ffprobe")
	if err != nil {
		t.Skipf("ffprobe not available: %v", err)
	}
	paths, err := ResolveRuntimePaths(filepath.Join(t.TempDir(), "SpotiDownloader.exe"), "metadata")
	if err != nil {
		t.Fatal(err)
	}
	if err := paths.Prepare(); err != nil {
		t.Fatal(err)
	}
	installRuntimePathsForTest(t, paths)
	for _, source := range []string{ffmpegPath, ffprobePath} {
		name := filepath.Base(source)
		if err := copyMetadataFixtureFile(source, filepath.Join(paths.BinDir, name)); err != nil {
			t.Fatalf("copy helper %s: %v", source, err)
		}
	}
	return filepath.Join(paths.BinDir, filepath.Base(ffmpegPath)), filepath.Join(paths.BinDir, filepath.Base(ffprobePath))
}

func writeFFmpegFixture(t *testing.T, ffmpegPath string, args []string) {
	t.Helper()
	cmd := exec.Command(ffmpegPath, args...)
	setHideWindow(cmd)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("ffmpeg fixture: %v\n%s", err, string(output))
	}
}

func copyMetadataFixtureFile(sourcePath, destinationPath string) error {
	data, err := os.ReadFile(sourcePath)
	if err != nil {
		return err
	}
	return os.WriteFile(destinationPath, data, 0755)
}

func runFFprobeFixture(t *testing.T, ffprobePath, filePath string, args ...string) string {
	t.Helper()
	probeArgs := append([]string{"-v", "quiet", "-of", "default=nw=1:nk=1"}, args...)
	probeArgs = append(probeArgs, filePath)
	cmd := exec.Command(ffprobePath, probeArgs...)
	setHideWindow(cmd)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("ffprobe fixture: %v\n%s", err, string(output))
	}
	return string(output)
}

func TestEmbedMetadataContextMP3VerificationCancellation(t *testing.T) {
	installFakeFFmpegRuntime(t)
	marker := filepath.Join(t.TempDir(), "ffprobe-started")
	t.Setenv("SPOTI_FAKE_FFPROBE_MARKER", marker)

	filePath := filepath.Join(t.TempDir(), "track.mp3")
	if err := os.WriteFile(filePath, []byte("audio"), 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		doneErr := verifyEmbeddedMetadataContext(ctx, filePath, Metadata{Title: "title"})
		done <- doneErr
	}()
	if err := waitForFile(marker, helperProcessTimeout); err != nil {
		cancel()
		t.Fatal("metadata verification helper did not start:", err)
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("metadata verification cancellation error = %v, want context.Canceled", err)
		}
	case <-time.After(helperProcessTimeout):
		t.Fatal("metadata verification ignored cancellation")
	}
}

func TestEmbedLyricsOnlyContextM4ACancellation(t *testing.T) {
	installFakeFFmpegRuntime(t)
	marker := filepath.Join(t.TempDir(), "lyrics-started")
	t.Setenv(testHelperMarker, marker)

	filePath := filepath.Join(t.TempDir(), "track.m4a")
	if err := os.WriteFile(filePath, []byte("audio"), 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		err := EmbedLyricsOnlyContext(ctx, filePath, "[00:00.00]test")
		done <- err
	}()
	if err := waitForFile(marker, helperProcessTimeout); err != nil {
		cancel()
		t.Fatal("lyrics helper did not start:", err)
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("lyrics cancellation error = %v, want context.Canceled", err)
		}
	case <-time.After(helperProcessTimeout):
		t.Fatal("lyrics embedding ignored cancellation")
	}
	if _, err := os.Stat(filePath + ".tmp.m4a"); !os.IsNotExist(err) {
		t.Fatalf("canceled lyrics embedding left temp output: %v", err)
	}
}

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
