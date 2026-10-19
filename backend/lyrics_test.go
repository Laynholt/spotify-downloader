package backend

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestLyricsAllSourcesRetainsProviderFailures(t *testing.T) {
	client := &LyricsClient{httpClient: &http.Client{
		Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			switch {
			case req.URL.Host == "spotify-lyrics-api-pi.vercel.app":
				return &http.Response{StatusCode: 503, Body: io.NopCloser(strings.NewReader("unavailable")), Header: make(http.Header)}, nil
			case req.URL.Path == "/api/search":
				return nil, errors.New("lookup connection refused")
			default:
				return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"instrumental":true,"plainLyrics":"","syncedLyrics":""}`)), Header: make(http.Header)}, nil
			}
		}),
	}}
	_, _, err := client.FetchLyricsAllSourcesContext(context.Background(), "track-id", "Track (Remastered)", "Artist", 180)
	if err == nil {
		t.Fatal("expected source diagnostics")
	}
	for _, want := range []string{"Spotify Lyrics API", "status 503", "LRCLIB exact: no lyrics returned", "LRCLIB search:", "lookup connection refused", "LRCLIB exact (simplified): no lyrics returned", "LRCLIB search (simplified):"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("missing %q in %v", want, err)
		}
	}
}

func TestLyricsAllSourcesPreservesFallbackSuccess(t *testing.T) {
	requests := 0
	client := &LyricsClient{httpClient: &http.Client{
		Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			requests++
			if req.URL.Host == "spotify-lyrics-api-pi.vercel.app" {
				return &http.Response{StatusCode: 404, Body: http.NoBody, Header: make(http.Header)}, nil
			}
			return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"plainLyrics":"A test lyric","syncedLyrics":""}`)), Header: make(http.Header)}, nil
		}),
	}}
	resp, source, err := client.FetchLyricsAllSourcesContext(context.Background(), "track-id", "Track", "Artist", 180)
	if err != nil || source != "LRCLIB" || resp == nil || len(resp.Lines) != 1 || resp.Lines[0].Words != "A test lyric" {
		t.Fatalf("lyrics = %+v, source = %q, err = %v", resp, source, err)
	}
	if requests != 2 {
		t.Fatalf("requests = %d, want 2", requests)
	}
}

func TestLyricsAllSourcesStopsOnCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	requests := 0
	client := &LyricsClient{httpClient: &http.Client{
		Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			requests++
			cancel()
			return nil, ctx.Err()
		}),
	}}
	_, _, err := client.FetchLyricsAllSourcesContext(ctx, "track-id", "Track", "Artist", 180)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want cancellation", err)
	}
	if requests != 1 {
		t.Fatalf("requests = %d, want 1", requests)
	}
}
