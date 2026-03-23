package backend

import (
	"os"
	"time"
)

func RetryWithBackoff(attempts int, initialDelay, maxDelay time.Duration, operation func(attempt int) error) error {
	if attempts <= 0 {
		attempts = 1
	}

	delay := initialDelay
	var lastErr error

	for attempt := 1; attempt <= attempts; attempt++ {
		lastErr = operation(attempt)
		if lastErr == nil {
			return nil
		}

		if attempt == attempts || delay <= 0 {
			continue
		}

		time.Sleep(delay)

		if maxDelay > 0 && delay < maxDelay {
			delay *= 2
			if delay > maxDelay {
				delay = maxDelay
			}
		}
	}

	return lastErr
}

func WaitForReadableFile(filePath string, attempts int, delay time.Duration) error {
	return RetryWithBackoff(attempts, delay, delay, func(int) error {
		file, err := os.Open(filePath)
		if err != nil {
			return err
		}
		file.Close()
		return nil
	})
}
