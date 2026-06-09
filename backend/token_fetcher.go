package backend

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/chromedp/cdproto/network"
	"github.com/chromedp/chromedp"
)

const (
	tokenProbeURL        = "https://open.spotify.com/track/53iuhJlwXhSER5J2IYYv1W"
	sessionEndpointMatch = "api.spotidownloader.com/session"
)

func FetchSessionToken() (string, error) {
	return FetchSessionTokenWithParams(15, 1)
}

var ErrChromeNotInstalled = fmt.Errorf("chrome_not_installed")

func FetchSessionTokenWithParams(timeout int, retry int) (string, error) {
	if timeout < 15 {
		timeout = 15
	}

	browserInstalled, browserPath, err := IsChromeInstalled()
	if err != nil {
		return "", fmt.Errorf("failed to check Chrome installation: %v", err)
	}
	if !browserInstalled {
		return "", ErrChromeNotInstalled
	}

	var lastErr error
	if retry < 0 {
		retry = 0
	}
	maxAttempts := retry + 1

	for attempt := 1; attempt <= maxAttempts; attempt++ {
		fmt.Printf("[TokenFetcher] Attempt %d/%d (timeout: %ds)\n", attempt, maxAttempts, timeout)

		token, err := fetchSessionTokenWithBrowser(timeout, browserPath)
		if err != nil {
			lastErr = fmt.Errorf("browser token fetch failed (attempt %d): %v", attempt, err)
			fmt.Printf("[TokenFetcher] %v\n", lastErr)
			if attempt < maxAttempts {
				time.Sleep(1 * time.Second)
			}
			continue
		}

		if token == "" {
			lastErr = fmt.Errorf("browser token fetch returned empty token (attempt %d)", attempt)
			fmt.Printf("[TokenFetcher] %v\n", lastErr)
			if attempt < maxAttempts {
				time.Sleep(1 * time.Second)
			}
			continue
		}

		if !isLikelySessionToken(token) {
			lastErr = fmt.Errorf("browser token fetch returned invalid token (attempt %d): %s", attempt, token)
			fmt.Printf("[TokenFetcher] %v\n", lastErr)
			if attempt < maxAttempts {
				time.Sleep(1 * time.Second)
			}
			continue
		}

		fmt.Printf("[TokenFetcher] Token fetched successfully on attempt %d\n", attempt)
		return token, nil
	}

	return "", fmt.Errorf("failed to fetch token after %d attempts: %v", maxAttempts, lastErr)
}

func fetchSessionTokenWithBrowser(timeout int, browserPath string) (string, error) {
	profileDir, err := os.MkdirTemp("", "spotidownloader-token-*")
	if err != nil {
		return "", fmt.Errorf("failed to create temporary browser profile: %w", err)
	}
	defer os.RemoveAll(profileDir)

	totalTimeout := time.Duration(timeout+20) * time.Second
	rootCtx, cancel := context.WithTimeout(context.Background(), totalTimeout)
	defer cancel()

	allocatorOptions := append(chromedp.DefaultExecAllocatorOptions[:],
		chromedp.ExecPath(browserPath),
		chromedp.UserDataDir(profileDir),
		chromedp.Flag("headless", false),
		chromedp.Flag("no-first-run", true),
		chromedp.Flag("no-default-browser-check", true),
		chromedp.Flag("disable-background-networking", true),
		chromedp.Flag("disable-features", "Translate,MediaRouter,OptimizationHints"),
		chromedp.Flag("window-position", "-32000,-32000"),
		chromedp.WindowSize(900, 700),
	)

	allocCtx, cancelAllocator := chromedp.NewExecAllocator(rootCtx, allocatorOptions...)
	defer cancelAllocator()

	ctx, cancelBrowser := chromedp.NewContext(allocCtx)
	defer cancelBrowser()

	tokenCh := make(chan string, 1)
	var sessionRequests sync.Map
	chromedp.ListenTarget(ctx, func(ev interface{}) {
		switch event := ev.(type) {
		case *network.EventResponseReceived:
			if strings.Contains(event.Response.URL, sessionEndpointMatch) {
				sessionRequests.Store(event.RequestID, struct{}{})
			}
		case *network.EventLoadingFinished:
			if _, ok := sessionRequests.LoadAndDelete(event.RequestID); !ok {
				return
			}

			go func(requestID network.RequestID) {
				bodyCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
				defer cancel()

				body, err := network.GetResponseBody(requestID).Do(bodyCtx)
				if err != nil {
					return
				}

				token, err := extractSessionTokenFromResponse(body)
				if err != nil || !isLikelySessionToken(token) {
					return
				}

				select {
				case tokenCh <- token:
				default:
				}
			}(event.RequestID)
		}
	})

	var token string
	err = chromedp.Run(ctx,
		network.Enable(),
		chromedp.Navigate("https://spotidownloader.com/"),
		chromedp.WaitVisible(".searchInput", chromedp.ByQuery),
		chromedp.Evaluate(captureSessionTokenScript(), nil),
		chromedp.Evaluate(submitTokenProbeScript(), nil),
		chromedp.Sleep(1*time.Second),
		waitForSessionToken(tokenCh, time.Duration(timeout)*time.Second, &token),
	)
	if err != nil {
		return "", err
	}

	return strings.TrimSpace(token), nil
}

func waitForSessionToken(tokenCh <-chan string, maxWait time.Duration, token *string) chromedp.ActionFunc {
	return func(ctx context.Context) error {
		timeout := time.NewTimer(maxWait)
		defer timeout.Stop()

		ticker := time.NewTicker(500 * time.Millisecond)
		defer ticker.Stop()

		for {
			select {
			case capturedToken := <-tokenCh:
				if isLikelySessionToken(capturedToken) {
					*token = capturedToken
					return nil
				}
			case <-ticker.C:
				var pageToken string
				if err := chromedp.Evaluate(`window.sessionToken || ""`, &pageToken).Do(ctx); err == nil && isLikelySessionToken(pageToken) {
					*token = pageToken
					return nil
				}
			case <-timeout.C:
				return fmt.Errorf("session token was not observed within %s", maxWait)
			case <-ctx.Done():
				return ctx.Err()
			}
		}
	}
}

func captureSessionTokenScript() string {
	return `(() => {
		const originalFetch = window.fetch.bind(window);
		window.sessionToken = null;
		window.fetch = async (...args) => {
			const response = await originalFetch(...args);
			try {
				if (response.url.includes('` + sessionEndpointMatch + `')) {
					const data = await response.clone().json();
					if (data && data.token) window.sessionToken = data.token;
				}
			} catch (_) {}
			return response;
		};
	})();`
}

func submitTokenProbeScript() string {
	body, _ := json.Marshal(tokenProbeURL)
	return `(() => {
		const input = document.querySelector('.searchInput');
		if (!input) throw new Error('search input not found');

		const value = ` + string(body) + `;
		const setter = Object.getOwnPropertyDescriptor(window.HTMLInputElement.prototype, 'value').set;
		setter.call(input, value);
		input.dispatchEvent(new Event('input', { bubbles: true }));
		input.dispatchEvent(new Event('change', { bubbles: true }));

		const button = document.querySelector('button[type="submit"]');
		if (!button) throw new Error('submit button not found');
		button.click();
	})();`
}

func extractSessionTokenFromResponse(body []byte) (string, error) {
	var payload struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return "", fmt.Errorf("failed to decode session response: %w", err)
	}

	token := strings.TrimSpace(payload.Token)
	if token == "" {
		return "", fmt.Errorf("session response did not contain token")
	}
	return token, nil
}

func isLikelySessionToken(token string) bool {
	token = strings.TrimSpace(token)
	return strings.HasPrefix(token, "eyJ") && len(token) > 20
}
