//go:build windows

package backend

import (
	"context"
	"errors"
	"os"
	"sync"
	"time"

	"golang.org/x/sys/windows"
)

func acquireDenoFileLockContext(ctx context.Context, path string) (func() error, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	handle := windows.Handle(file.Fd())
	flags := uint32(windows.LOCKFILE_EXCLUSIVE_LOCK | windows.LOCKFILE_FAIL_IMMEDIATELY)
	ticker := time.NewTicker(25 * time.Millisecond)
	defer ticker.Stop()
	for {
		err = windows.LockFileEx(handle, flags, 0, 1, 0, &windows.Overlapped{})
		if err == nil {
			if ctxErr := ctx.Err(); ctxErr != nil {
				_ = windows.UnlockFileEx(handle, 0, 1, 0, &windows.Overlapped{})
				_ = file.Close()
				return nil, ctxErr
			}
			var once sync.Once
			var releaseErr error
			return func() error {
				once.Do(func() {
					releaseErr = errors.Join(
						windows.UnlockFileEx(handle, 0, 1, 0, &windows.Overlapped{}),
						file.Close(),
					)
				})
				return releaseErr
			}, nil
		}
		if !errors.Is(err, windows.ERROR_LOCK_VIOLATION) && !errors.Is(err, windows.ERROR_IO_PENDING) {
			_ = file.Close()
			return nil, err
		}
		select {
		case <-ctx.Done():
			_ = file.Close()
			return nil, ctx.Err()
		case <-ticker.C:
		}
	}
}
