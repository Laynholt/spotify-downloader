//go:build !windows

package backend

import (
	"context"
	"errors"
	"os"
	"sync"
	"syscall"
	"time"
)

func acquireDenoFileLockContext(ctx context.Context, path string) (func() error, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	ticker := time.NewTicker(25 * time.Millisecond)
	defer ticker.Stop()
	for {
		err = syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			if ctxErr := ctx.Err(); ctxErr != nil {
				_ = syscall.Flock(int(file.Fd()), syscall.LOCK_UN)
				_ = file.Close()
				return nil, ctxErr
			}
			var once sync.Once
			var releaseErr error
			return func() error {
				once.Do(func() {
					releaseErr = errors.Join(
						syscall.Flock(int(file.Fd()), syscall.LOCK_UN),
						file.Close(),
					)
				})
				return releaseErr
			}, nil
		}
		if !errors.Is(err, syscall.EWOULDBLOCK) && !errors.Is(err, syscall.EAGAIN) {
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
