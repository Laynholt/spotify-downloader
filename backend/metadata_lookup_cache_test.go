package backend

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestMetadataLookupCacheTTLsAndSingleflight(t *testing.T) {
	var spotifyCalls atomic.Int32
	var songLinkCalls atomic.Int32
	var musicBrainzCalls atomic.Int32
	var chosicTrackCalls atomic.Int32
	var chosicArtistCalls atomic.Int32

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodPost && strings.Contains(r.URL.Path, "pathfinder"):
			spotifyCalls.Add(1)
			_, _ = w.Write([]byte(`{"data":{"trackUnion":{"id":"track-1","name":"Track","trackNumber":1,"discNumber":1,"artists":{"items":[{"profile":{"name":"Artist"}}]},"duration":{"totalMilliseconds":180000},"albumOfTrack":{"name":"Album","artists":{"items":[{"profile":{"name":"Artist"}}]},"date":{"isoString":"2024-01-02"},"tracks":{"totalCount":1},"copyright":{"items":[{"type":"C","text":"Copyright"}]}}}}}`))
		case r.Method == http.MethodGet && strings.Contains(r.URL.Path, "v1-alpha.1/links"):
			songLinkCalls.Add(1)
			_, _ = w.Write([]byte(`{"linksByPlatform":{"deezer":{"url":"https://www.deezer.com/track/123"}}}`))
		case r.Method == http.MethodGet && r.URL.Path == "/track/123":
			_, _ = w.Write([]byte(`{"id":123,"isrc":"ISRC-123","title":"Track"}`))
		case r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/recording"):
			musicBrainzCalls.Add(1)
			_, _ = w.Write([]byte(`{"recordings":[{"id":"recording-1","title":"Track","tags":[{"count":10,"name":"electronic"}]}]}`))
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/handshake/"):
			_, _ = w.Write([]byte(`{"success":true}`))
		case r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/tracks/track-1"):
			chosicTrackCalls.Add(1)
			_, _ = w.Write([]byte(`{"artists":[{"id":"artist-1"}]}`))
		case r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/artists/artist-1"):
			chosicArtistCalls.Add(1)
			_, _ = w.Write([]byte(`{"genres":["indie"]}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	base, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	rewrite := func(next http.RoundTripper) http.RoundTripper {
		return roundTripFunc(func(req *http.Request) (*http.Response, error) {
			clone := req.Clone(req.Context())
			clone.URL.Scheme = base.Scheme
			clone.URL.Host = base.Host
			return next.RoundTrip(clone)
		})
	}
	serverClient := server.Client()
	serverTransport := serverClient.Transport
	serverClient.Transport = rewrite(serverTransport)

	previousSpotifyFactory := metadataSpotifyClientFactory
	previousSongLinkFactory := metadataSongLinkClientFactory
	previousChosicFactory := metadataChosicClientFactory
	previousHTTPFactory := metadataHTTPClientFactory
	previousMusicBrainzFactory := musicBrainzHTTPClientFactory
	defer func() {
		metadataSpotifyClientFactory = previousSpotifyFactory
		metadataSongLinkClientFactory = previousSongLinkFactory
		metadataChosicClientFactory = previousChosicFactory
		metadataHTTPClientFactory = previousHTTPFactory
		musicBrainzHTTPClientFactory = previousMusicBrainzFactory
	}()
	metadataSpotifyClientFactory = func() *SpotifyClient {
		return &SpotifyClient{
			client:        serverClient,
			accessToken:   "access-token",
			clientToken:   "client-token",
			clientVersion: "test",
			cookies:       make(map[string]string),
		}
	}
	metadataSongLinkClientFactory = func() *SongLinkClient {
		return &SongLinkClient{client: serverClient, minCallDelay: 0, apiCallResetTime: time.Now()}
	}
	metadataChosicClientFactory = func() (*chosicClient, error) {
		return &chosicClient{httpClient: serverClient}, nil
	}
	metadataHTTPClientFactory = func(timeout time.Duration) *http.Client {
		clone := *serverClient
		clone.Timeout = timeout
		return &clone
	}
	musicBrainzHTTPClientFactory = metadataHTTPClientFactory

	now := time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC)
	cache, warnings := OpenMetadataCache("", CacheOptions{Now: func() time.Time { return now }})
	if len(warnings) != 0 {
		t.Fatalf("cache warnings = %v", warnings)
	}
	defer cache.Close()

	lookup := func() taggingMetadataResult {
		result, warnings := fetchTaggingMetadata(context.Background(), cache, TaggingMetadataRequest{
			TrackID:        "track-1",
			TrackName:      "Track",
			ArtistName:     "Artist",
			AlbumName:      "Album",
			UseSingleGenre: true,
			EmbedGenre:     true,
		})
		if len(warnings) != 0 {
			t.Fatalf("lookup warnings = %v", warnings)
		}
		return result
	}

	first := lookup()
	if first.ReleaseDate != "2024-01-02" || first.ISRC != "ISRC-123" || first.Genre != "electronic" {
		t.Fatalf("first lookup = %+v", first)
	}
	second := lookup()
	if second != first {
		t.Fatalf("second lookup = %+v, first = %+v", second, first)
	}
	if got := spotifyCalls.Load(); got != 1 {
		t.Fatalf("spotify calls = %d", got)
	}
	if got := songLinkCalls.Load(); got != 1 {
		t.Fatalf("song.link calls = %d", got)
	}
	if got := musicBrainzCalls.Load(); got != 1 {
		t.Fatalf("MusicBrainz calls = %d", got)
	}
	if got := chosicTrackCalls.Load(); got != 1 {
		t.Fatalf("chosic track calls = %d", got)
	}
	if got := chosicArtistCalls.Load(); got != 1 {
		t.Fatalf("chosic artist calls = %d", got)
	}

	now = now.Add(8 * 24 * time.Hour)
	_ = lookup()
	if got := spotifyCalls.Load(); got != 2 {
		t.Fatalf("spotify calls after 8 days = %d", got)
	}
	if got := songLinkCalls.Load(); got != 2 {
		t.Fatalf("song.link calls after 8 days = %d", got)
	}
	if got := musicBrainzCalls.Load(); got != 2 {
		t.Fatalf("MusicBrainz calls after 8 days = %d", got)
	}
	if got := chosicTrackCalls.Load(); got != 2 {
		t.Fatalf("chosic track calls after 8 days = %d", got)
	}
	if got := chosicArtistCalls.Load(); got != 1 {
		t.Fatalf("chosic artist calls after 8 days = %d", got)
	}

	now = now.Add(23 * 24 * time.Hour)
	_ = lookup()
	if got := chosicArtistCalls.Load(); got != 2 {
		t.Fatalf("chosic artist calls after 31 days = %d", got)
	}
}

func TestMetadataLookupCacheKeys(t *testing.T) {
	for _, test := range []struct {
		name string
		got  string
		want string
	}{
		{name: "spotify", got: spotifyCacheKey("track"), want: "spotify:track"},
		{name: "isrc", got: isrcCacheKey("track"), want: "isrc:track"},
		{name: "chosic track", got: chosicTrackCacheKey("track"), want: "chosic-track:track"},
		{name: "artist genre", got: artistGenreCacheKey("artist"), want: "genre:artist"},
	} {
		t.Run(test.name, func(t *testing.T) {
			if test.got != test.want {
				t.Fatalf("key = %q, want %q", test.got, test.want)
			}
		})
	}
}

func TestSpotifyMetadataCacheSingleflightUsesOneHTTPRequest(t *testing.T) {
	var calls atomic.Int32
	entered := make(chan struct{})
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			close(entered)
			<-release
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":{"trackUnion":{"id":"track-singleflight","name":"Track","trackNumber":1,"discNumber":1,"artists":{"items":[{"profile":{"name":"Artist"}}]},"duration":{"totalMilliseconds":180000},"albumOfTrack":{"name":"Album","artists":{"items":[{"profile":{"name":"Artist"}}]},"date":{"isoString":"2024-01-02"},"tracks":{"totalCount":1}}}}}`))
	}))
	defer server.Close()
	base, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	client := server.Client()
	next := client.Transport
	client.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		clone := req.Clone(req.Context())
		clone.URL.Scheme = base.Scheme
		clone.URL.Host = base.Host
		return next.RoundTrip(clone)
	})

	previousFactory := metadataSpotifyClientFactory
	defer func() { metadataSpotifyClientFactory = previousFactory }()
	metadataSpotifyClientFactory = func() *SpotifyClient {
		return &SpotifyClient{client: client, accessToken: "access", clientToken: "client", clientVersion: "test", cookies: make(map[string]string)}
	}

	cache, warnings := OpenMetadataCache("", CacheOptions{Now: time.Now})
	if len(warnings) != 0 {
		t.Fatalf("cache warnings = %v", warnings)
	}
	defer cache.Close()

	const callers = 8
	start := make(chan struct{})
	results := make(chan error, callers)
	var wg sync.WaitGroup
	for i := 0; i < callers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			data, err := GetFilteredSpotifyDataWithCache(context.Background(), cache, "https://open.spotify.com/track/track-singleflight", false, 0)
			if err == nil {
				if _, ok := data.(TrackResponse); !ok {
					err = fmt.Errorf("unexpected response type %T", data)
				}
			}
			results <- err
		}()
	}
	close(start)
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for the owner request")
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("HTTP calls while owner blocked = %d", got)
	}
	close(release)
	wg.Wait()
	close(results)
	for err := range results {
		if err != nil {
			t.Fatal(err)
		}
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("HTTP calls after shared completion = %d", got)
	}
}
