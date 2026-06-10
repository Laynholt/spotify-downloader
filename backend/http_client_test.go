package backend

import (
	"bytes"
	"errors"
	"io"
	"net/http"
	"net/url"
	"testing"
)

func TestDirectFallbackTransportRetriesTransientProxyError(t *testing.T) {
	primaryCalls := 0
	directCalls := 0

	transport := directFallbackTransport{
		primary: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			primaryCalls++
			return nil, &url.Error{
				Op:  "Get",
				URL: req.URL.String(),
				Err: errors.New("proxyconnect tcp: dial tcp 127.0.0.1:2080: connectex: failed"),
			}
		}),
		direct: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			directCalls++
			return &http.Response{
				StatusCode: http.StatusOK,
				Body:       io.NopCloser(bytes.NewBufferString("ok")),
				Header:     make(http.Header),
				Request:    req,
			}, nil
		}),
	}

	req, err := http.NewRequest(http.MethodGet, "https://open.spotify.com", nil)
	if err != nil {
		t.Fatal(err)
	}

	resp, err := transport.RoundTrip(req)
	if err != nil {
		t.Fatalf("RoundTrip returned error: %v", err)
	}
	defer resp.Body.Close()

	if primaryCalls != 1 {
		t.Fatalf("expected 1 primary call, got %d", primaryCalls)
	}
	if directCalls != 1 {
		t.Fatalf("expected 1 direct fallback call, got %d", directCalls)
	}
}
