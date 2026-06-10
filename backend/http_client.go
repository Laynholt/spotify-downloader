package backend

import (
	"net"
	"net/http"
	"net/url"
	"time"
)

func newHTTPClient(timeout time.Duration) *http.Client {
	return &http.Client{
		Timeout:   timeout,
		Transport: newHTTPTransport(),
	}
}

func newHTTPTransport() http.RoundTripper {
	return directFallbackTransport{
		primary: newProxiedHTTPTransport(),
		direct:  newDirectHTTPTransport(),
	}
}

func newProxiedHTTPTransport() *http.Transport {
	transport := newDirectHTTPTransport()
	transport.Proxy = proxyFromEnvironmentOrSystem
	return transport
}

func newDirectHTTPTransport() *http.Transport {
	return &http.Transport{
		DialContext:           (&net.Dialer{Timeout: 30 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          100,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
	}
}

type directFallbackTransport struct {
	primary http.RoundTripper
	direct  http.RoundTripper
}

func (t directFallbackTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	resp, err := t.primary.RoundTrip(req)
	if err == nil || !isTransientNetworkError(err) || !canReplayRequest(req) {
		return resp, err
	}

	if resp != nil && resp.Body != nil {
		_ = resp.Body.Close()
	}

	retryReq := req.Clone(req.Context())
	if req.Body != nil && req.Body != http.NoBody && req.GetBody != nil {
		body, bodyErr := req.GetBody()
		if bodyErr != nil {
			return nil, err
		}
		retryReq.Body = body
	}

	return t.direct.RoundTrip(retryReq)
}

func canReplayRequest(req *http.Request) bool {
	return req.Body == nil || req.Body == http.NoBody || req.GetBody != nil
}

func proxyFromEnvironmentOrSystem(req *http.Request) (*url.URL, error) {
	proxyURL, err := http.ProxyFromEnvironment(req)
	if proxyURL != nil || err != nil {
		return proxyURL, err
	}
	return systemProxyFromOS(req)
}
