package backend

import (
	"errors"
	"os"
	"testing"
	"time"
)

func TestRetryWithBackoffStopsAfterSuccess(t *testing.T) {
	t.Parallel()

	attempts := 0
	err := RetryWithBackoff(5, 0, 0, func(int) error {
		attempts++
		if attempts < 3 {
			return errors.New("retry me")
		}
		return nil
	})
	if err != nil {
		t.Fatalf("RetryWithBackoff returned error: %v", err)
	}
	if attempts != 3 {
		t.Fatalf("expected 3 attempts, got %d", attempts)
	}
}

func TestRetryWithBackoffReturnsLastError(t *testing.T) {
	t.Parallel()

	wantErr := errors.New("still failing")
	attempts := 0
	err := RetryWithBackoff(3, 0, 0, func(int) error {
		attempts++
		return wantErr
	})
	if !errors.Is(err, wantErr) {
		t.Fatalf("expected %v, got %v", wantErr, err)
	}
	if attempts != 3 {
		t.Fatalf("expected 3 attempts, got %d", attempts)
	}
}

func TestWaitForReadableFile(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	filePath := dir + "/delayed.txt"

	go func() {
		time.Sleep(25 * time.Millisecond)
		_ = os.WriteFile(filePath, []byte("ok"), 0o644)
	}()

	if err := WaitForReadableFile(filePath, 5, 20*time.Millisecond); err != nil {
		t.Fatalf("WaitForReadableFile returned error: %v", err)
	}
}
