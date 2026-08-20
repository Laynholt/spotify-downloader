package backend

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

func getSpotiDownloaderDir() (string, error) {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("failed to get user home directory: %v", err)
	}
	return filepath.Join(homeDir, ".spotidownloader"), nil
}

func FetchSessionToken() (string, error) {
	return FetchSessionTokenWithParams(15, 1)
}

var ErrChromeNotInstalled = fmt.Errorf("chrome_not_installed")

func portableTokenHelperPath(paths RuntimePaths) (string, error) {
	helperPath := filepath.Join(paths.BinDir, tokenHelperName())
	info, err := os.Stat(helperPath)
	if err != nil || !info.Mode().IsRegular() || info.Size() == 0 {
		return "", fmt.Errorf("portable helper is missing: %s", helperPath)
	}
	return helperPath, nil
}

func FetchSessionTokenWithParams(timeout int, retry int) (string, error) {
	if timeout < 15 {
		timeout = 15
	}

	paths, err := CurrentRuntimePaths()
	if err != nil {
		return "", err
	}
	exePath, err := portableTokenHelperPath(paths)
	if err != nil {
		return "", err
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

		args := []string{"--timeout", fmt.Sprintf("%d", timeout), "--retry", "1"}
		if browserPath != "" {
			args = append(args, "--browser-path", browserPath)
		}

		cmd := newTokenCmd(exePath, args...)
		output, err := cmd.CombinedOutput()
		outputStr := strings.TrimSpace(string(output))

		if err != nil {
			lastErr = fmt.Errorf("get_token execution failed (attempt %d): %v, output: %s", attempt, err, outputStr)
			fmt.Printf("[TokenFetcher] %v\n", lastErr)
			if attempt < maxAttempts {
				time.Sleep(1 * time.Second)
			}
			continue
		}

		if outputStr == "" {
			lastErr = fmt.Errorf("get_token returned empty output (attempt %d)", attempt)
			fmt.Printf("[TokenFetcher] %v\n", lastErr)
			if attempt < maxAttempts {
				time.Sleep(1 * time.Second)
			}
			continue
		}

		if !strings.HasPrefix(outputStr, "eyJ") {
			lastErr = fmt.Errorf("get_token returned invalid token (attempt %d): %s", attempt, outputStr)
			fmt.Printf("[TokenFetcher] %v\n", lastErr)
			if attempt < maxAttempts {
				time.Sleep(1 * time.Second)
			}
			continue
		}

		fmt.Printf("[TokenFetcher] Token fetched successfully on attempt %d\n", attempt)
		return outputStr, nil
	}

	return "", fmt.Errorf("failed to fetch token after %d attempts: %v", maxAttempts, lastErr)
}
