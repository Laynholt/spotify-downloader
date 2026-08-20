package backend

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"testing"
	"time"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (fn roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return fn(req)
}

func TestDownloadTrackContextPreservesFinalizedOutputAfterCancellation(t *testing.T) {
	dir := t.TempDir()
	outputPath := filepath.Join(dir, "track - artist.flac")
	client := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.Method == http.MethodPost {
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     make(http.Header),
				Body:       io.NopCloser(bytes.NewBufferString(`{"success":true,"linkFlac":"https://download.test/audio"}`)),
				Request:    req,
			}, nil
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"audio/flac"}},
			Body:       io.NopCloser(bytes.NewBufferString("partial flac")),
			Request:    req,
		}, nil
	})}
	downloader := &SpotiDownloader{httpClient: client}

	finalized := make(chan struct{})
	previousFinalize := finalizeTaggedAudioFileForDownload
	finalizeTaggedAudioFileForDownload = func(path string) error {
		if err := previousFinalize(path); err != nil {
			return err
		}
		close(finalized)
		return nil
	}
	t.Cleanup(func() { finalizeTaggedAudioFileForDownload = previousFinalize })

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct {
		path string
		err  error
	}, 1)
	go func() {
		path, err := downloader.DownloadTrackContext(ctx, "", dir, "flac", "default", false, 0,
			"track", "artist", "album", "artist", "", "", 0, 0, 0, false, false, 0,
			"", "", "", "", false, false, false)
		done <- struct {
			path string
			err  error
		}{path, err}
	}()
	select {
	case <-finalized:
		cancel()
	case <-time.After(time.Second):
		cancel()
		t.Fatal("download did not reach finalization barrier")
	}
	result := <-done
	if result.err != nil {
		t.Fatalf("download returned error after finalized cancellation: %v", result.err)
	}
	if result.path != outputPath {
		t.Fatalf("download path = %q, want %q", result.path, outputPath)
	}
	if _, err := os.Stat(outputPath); err != nil {
		t.Fatalf("finalized output was removed after cancellation: %v", err)
	}
}

func TestStatusErrorTreatsUnauthorizedStatusesAsSessionTokenErrors(t *testing.T) {
	err := statusError(http.StatusUnauthorized, []byte(`{"message":"unauthorized"}`))
	if !isSessionTokenError(err) {
		t.Fatalf("expected sessionTokenError for 401, got %T", err)
	}
}

func TestStatusErrorTreatsRequestInvalidBodyAsSessionTokenError(t *testing.T) {
	err := statusError(http.StatusBadRequest, []byte(`{"success":false,"message":"ERR_REQUEST_INVALID","statusCode":400}`))
	if !isSessionTokenError(err) {
		t.Fatalf("expected sessionTokenError for expired-token 400 body, got %T", err)
	}
}

func TestStatusErrorKeepsRegularBadRequestsAsNormalErrors(t *testing.T) {
	err := statusError(http.StatusBadRequest, []byte(`{"success":false,"message":"TRACK_NOT_FOUND","statusCode":400}`))
	if isSessionTokenError(err) {
		t.Fatalf("did not expect sessionTokenError for regular 400 response")
	}

	var tokenErr *sessionTokenError
	if errors.As(err, &tokenErr) {
		t.Fatalf("did not expect wrapped sessionTokenError")
	}
}

func TestGetDownloadLinkRetriesTransientProxyError(t *testing.T) {
	t.Parallel()

	attempts := 0
	downloader := &SpotiDownloader{
		sessionToken: "token",
		httpClient: &http.Client{
			Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
				attempts++
				if attempts == 1 {
					return nil, &url.Error{
						Op:  "Post",
						URL: req.URL.String(),
						Err: errors.New("proxyconnect tcp: dial tcp 127.0.0.1:10808: connectex: failed"),
					}
				}

				return &http.Response{
					StatusCode: http.StatusOK,
					Body:       io.NopCloser(bytes.NewBufferString(`{"success":true,"link":"https://example.com/file.mp3"}`)),
					Header:     make(http.Header),
					Request:    req,
				}, nil
			}),
		},
	}

	resp, err := downloader.getDownloadLink("track-id")
	if err != nil {
		t.Fatalf("getDownloadLink returned error: %v", err)
	}
	if attempts != 2 {
		t.Fatalf("expected 2 attempts, got %d", attempts)
	}
	if resp.Link != "https://example.com/file.mp3" {
		t.Fatalf("unexpected link: %q", resp.Link)
	}
}
