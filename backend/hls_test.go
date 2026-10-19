package backend

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

// Production break caught: an HLS playlist was saved as "track.mp3", then
// rejected by FFprobe as unreadable audio.
func TestFetchAudioDownloadsHLSStreamWithFFmpeg(t *testing.T) {
	installFakeFFmpegRuntime(t)
	t.Setenv(testHelperFFmpegSuccess, "1")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/octet-stream")
		_, _ = w.Write([]byte("#EXTM3U\n#EXT-X-VERSION:7\n#EXTINF:10,\nhttps://cdn.invalid/seg1.m4s\n"))
	}))
	defer server.Close()

	s := &SpotiDownloader{sessionToken: "token", httpClient: server.Client()}
	path, err := s.fetchAudio(context.Background(), server.URL+"/playlist", filepath.Join(t.TempDir(), "track"), ".mp3", "")
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Ext(path) != ".m4a" {
		t.Fatalf("path = %q, want the remuxed .m4a", path)
	}
	if data, err := os.ReadFile(path); err != nil || string(data) == "" {
		t.Fatalf("remuxed file missing: %v", err)
	}
}

func TestIsHLSPlaylist(t *testing.T) {
	for _, tc := range []struct {
		contentType string
		sniff       string
		want        bool
	}{
		{"", "#EXTM3U\n", true},
		{"", "\xef\xbb\xbf#EXTM3U\n", true},
		{"audio/mpegurl", "", true},
		{"application/vnd.apple.mpegurl", "", true},
		{"audio/mpeg", "ID3\x03", false},
		{"", "fLaC", false},
	} {
		if got := isHLSPlaylist(tc.contentType, []byte(tc.sniff)); got != tc.want {
			t.Errorf("isHLSPlaylist(%q, %q) = %v, want %v", tc.contentType, tc.sniff, got, tc.want)
		}
	}
}
