package backend

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/url"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (fn roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return fn(req)
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

	resp, err := downloader.requestDownloadLink(context.Background(), "track-id")
	if err != nil {
		t.Fatalf("requestDownloadLink returned error: %v", err)
	}
	if attempts != 2 {
		t.Fatalf("expected 2 attempts, got %d", attempts)
	}
	if resp.Link != "https://example.com/file.mp3" {
		t.Fatalf("unexpected link: %q", resp.Link)
	}
}

func TestGetDownloadLinkRetriesTransientServerErrors(t *testing.T) {
	for _, status := range []int{500, 502, 503, 504} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			t.Parallel()
			attempts := 0
			downloader := &SpotiDownloader{httpClient: &http.Client{
				Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
					attempts++
					body, err := io.ReadAll(req.Body)
					if err != nil || string(body) != `{"id":"track-id"}` {
						t.Fatalf("attempt %d request body = %q, err = %v", attempts, body, err)
					}
					_ = req.Body.Close()
					response := &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(`{"success":false,"message":"ERR_UNEXPECTED","statusCode":500,"error":3}`)), Header: make(http.Header)}
					if attempts == 2 {
						response.StatusCode = http.StatusOK
						response.Body = io.NopCloser(strings.NewReader(`{"success":true,"link":"https://example.com/file.mp3"}`))
					}
					return response, nil
				}),
			}}
			resp, err := downloader.getDownloadLink(context.Background(), "track-id")
			if err != nil || resp == nil || resp.Link != "https://example.com/file.mp3" {
				t.Fatalf("getDownloadLink = %+v, %v", resp, err)
			}
			if attempts != 2 {
				t.Fatalf("requests = %d, want 2", attempts)
			}
		})
	}
}

func TestGetDownloadLinkFailureDiagnosticsAndRetryBudget(t *testing.T) {
	for _, tc := range []struct {
		name         string
		status       int
		networkFirst bool
		wantAttempts int
	}{
		{name: "server failure", status: 500, wantAttempts: 3},
		{name: "network and server share budget", status: 500, networkFirst: true, wantAttempts: 3},
		{name: "bad request", status: 400, wantAttempts: 1},
		{name: "not found", status: 404, wantAttempts: 1},
		{name: "rate limit", status: 429, wantAttempts: 1},
		{name: "unsuccessful payload", status: 200, wantAttempts: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			attempts := 0
			body := `{"success":false,"message":"ERR_UNEXPECTED","statusCode":500,"error":3}`
			downloader := &SpotiDownloader{httpClient: &http.Client{
				Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
					attempts++
					_ = req.Body.Close()
					if tc.networkFirst && attempts == 1 {
						return nil, errors.New("connection reset")
					}
					return &http.Response{StatusCode: tc.status, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}, nil
				}),
			}}
			_, err := downloader.getDownloadLink(context.Background(), "track-id")
			if err == nil || !strings.Contains(err.Error(), body) {
				t.Fatalf("expected response diagnostics, got %v", err)
			}
			if attempts != tc.wantAttempts {
				t.Fatalf("requests = %d, want %d", attempts, tc.wantAttempts)
			}
		})
	}
}

func TestGetDownloadLinkCancelsServerRetry(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	attempts := 0
	downloader := &SpotiDownloader{httpClient: &http.Client{
		Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			attempts++
			_ = req.Body.Close()
			cancel()
			return &http.Response{StatusCode: 500, Body: io.NopCloser(strings.NewReader(`{"message":"ERR_UNEXPECTED"}`)), Header: make(http.Header)}, nil
		}),
	}}
	_, err := downloader.getDownloadLink(ctx, "track-id")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want cancellation", err)
	}
	if attempts != 1 {
		t.Fatalf("requests = %d, want 1", attempts)
	}
}

func TestRefreshSessionTokenRunsHelperOnceForConcurrentWorkers(t *testing.T) {
	RememberSessionToken("old")
	previous := fetchSessionToken
	t.Cleanup(func() { fetchSessionToken = previous })
	var calls atomic.Int32
	release := make(chan struct{})
	fetchSessionToken = func(context.Context, int, int) (string, error) {
		calls.Add(1)
		<-release
		return "new", nil
	}

	workers := make([]*SpotiDownloader, 3)
	var wg sync.WaitGroup
	for i := range workers {
		workers[i] = &SpotiDownloader{sessionToken: "old"}
		wg.Add(1)
		go func(s *SpotiDownloader) {
			defer wg.Done()
			if err := s.refreshSessionToken(context.Background()); err != nil {
				t.Error(err)
			}
		}(workers[i])
	}
	for calls.Load() == 0 {
		runtime.Gosched()
	}
	close(release)
	wg.Wait()

	late := &SpotiDownloader{sessionToken: "old"}
	if err := late.refreshSessionToken(context.Background()); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 1 {
		t.Fatalf("token helper ran %d times, want 1", calls.Load())
	}
	for _, s := range append(workers, late) {
		if s.sessionToken != "new" {
			t.Fatalf("worker token = %q, want new", s.sessionToken)
		}
	}
}
