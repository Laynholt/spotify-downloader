package backend

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

// Production break caught: song.link retired its public API (401 for every
// request), and three retries 15s apart made every track wait out the 10s
// metadata budget, which then dropped the genres other sources had found.
func TestSongLinkDoesNotRetryClientErrors(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		http.Error(w, `{"statusCode":401,"code":"PUBLIC_API_ACCESS_DEPRECATED"}`, http.StatusUnauthorized)
	}))
	defer server.Close()

	client := &SongLinkClient{client: tokenTestHTTPClient(server), minCallDelay: songLinkMinCallDelay, apiCallResetTime: time.Now()}
	started := time.Now()
	if _, err := client.GetISRCContext(context.Background(), "track-1"); err == nil {
		t.Fatal("want the 401 error")
	}
	if requests.Load() != 1 || time.Since(started) > time.Second {
		t.Fatalf("requests = %d after %v, want one fast attempt", requests.Load(), time.Since(started))
	}
}
