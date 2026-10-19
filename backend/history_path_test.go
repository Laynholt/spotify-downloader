package backend

import (
	"path/filepath"
	"strings"
	"testing"
)

// Production break caught: helper binaries and update markers remain in the
// legacy profile, while history is accidentally moved beside the executable.
func TestHelperPathsUsePortableBinButHistoryRemainsInUserData(t *testing.T) {
	paths := installFakeFFmpegRuntime(t)

	ffmpeg, err := GetFFmpegPath()
	if err != nil {
		t.Fatal(err)
	}
	ytdlp, err := GetYtDlpPath()
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Dir(ffmpeg) != paths.BinDir {
		t.Fatalf("ffmpeg path: %s", ffmpeg)
	}
	if filepath.Dir(ytdlp) != paths.BinDir {
		t.Fatalf("yt-dlp path: %s", ytdlp)
	}
	markerPath, err := getYtDlpUpdateCheckPath()
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Dir(markerPath) != paths.BinDir {
		t.Fatalf("yt-dlp update marker path: %s", markerPath)
	}
	dataDir, err := GetUserDataDir()
	if err != nil {
		t.Fatal(err)
	}
	if strings.HasPrefix(dataDir, paths.RootDir) {
		t.Fatalf("history moved into runtime: %s", dataDir)
	}
}
