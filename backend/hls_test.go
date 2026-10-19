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

// Production break caught: an Azure-backed CDN answered 400 to our Bearer
// header, which was then mistaken for an expired token and refreshed in vain.
func TestFetchAudioSendsNoSessionTokenToCDN(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "" {
			http.Error(w, "<Error><Code>InvalidAuthenticationInfo</Code><Message>Check the value of Authorization header.</Message></Error>", http.StatusBadRequest)
			return
		}
		if r.URL.Path == "/gone" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "audio/mp4")
		_, _ = w.Write([]byte("audio"))
	}))
	defer server.Close()

	s := &SpotiDownloader{sessionToken: "token", httpClient: server.Client(), refreshToken: func(context.Context) error {
		t.Fatal("a CDN rejection must not refresh the session token")
		return nil
	}}
	path, err := s.downloadAudio(context.Background(), server.URL+"/track", filepath.Join(t.TempDir(), "track"), ".mp3", "")
	if err != nil || filepath.Ext(path) != ".m4a" {
		t.Fatalf("path = %q, err = %v", path, err)
	}
	if _, err := s.downloadAudio(context.Background(), server.URL+"/gone", filepath.Join(t.TempDir(), "track"), ".mp3", ""); err == nil {
		t.Fatal("want the CDN error")
	}
	if !isSpotiDownloaderURL("https://api.spotidownloader.com/download/x") || isSpotiDownloaderURL("https://aac.saavncdn.com/x.mp4") ||
		isSpotiDownloaderURL("https://evilspotidownloader.com/x") {
		t.Fatal("isSpotiDownloaderURL host match is wrong")
	}
}

func TestHLSProgress(t *testing.T) {
	playlist := "#EXTM3U\n#EXT-X-MAP:URI=\"init.mp4\"\n#EXTINF:9.98,\nseg1.m4s\n#EXTINF:10.02,\nseg2.m4s\n#EXTINF:5,\nseg3.m4s\n"
	if got := hlsDurationSeconds([]byte(playlist)); got != 25 {
		t.Fatalf("duration = %v, want 25", got)
	}

	ClearAllDownloads()
	t.Cleanup(ClearAllDownloads)
	if err := AddQueueItem("q", "hls", "t", "a", "al", "id"); err != nil {
		t.Fatal(err)
	}
	StartDownloadItem("hls")
	item := func() DownloadItem { return GetDownloadQueue().Queue[0] }
	r := hlsProgressReporter{itemID: "hls", totalSeconds: 20}
	for _, line := range []string{"total_size=N/A", "out_time_us=N/A", "progress=continue"} {
		r.line(line)
	}
	if got := item(); got.DownloadedBytes != 0 || got.TotalBytes != 0 {
		t.Fatalf("before any data: %d/%d", got.DownloadedBytes, got.TotalBytes)
	}
	for _, line := range []string{"total_size=1000", "out_time_us=5000000", "progress=continue"} {
		r.line(line)
	}
	if got := item(); got.DownloadedBytes != 1000 || got.TotalBytes != 4000 {
		t.Fatalf("quarter way: %d/%d, want 1000/4000", got.DownloadedBytes, got.TotalBytes)
	}
	for _, line := range []string{"total_size=3900", "out_time_us=20000000", "progress=end"} {
		r.line(line)
	}
	if got := item(); got.DownloadedBytes != 3900 || got.TotalBytes != 3900 {
		t.Fatalf("at end: %d/%d, want 3900/3900", got.DownloadedBytes, got.TotalBytes)
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
