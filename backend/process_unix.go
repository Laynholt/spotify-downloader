//go:build !windows
// +build !windows

package backend

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"syscall"
	"time"
)

// newManagedCommand starts a helper in its own process group so cancellation
// can terminate the helper and any descendants it created.
func newManagedCommand(ctx context.Context, name string, args ...string) *exec.Cmd {
	if ctx == nil {
		ctx = context.Background()
	}
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.WaitDelay = 2 * time.Second
	cmd.Cancel = func() error {
		process := cmd.Process
		if process == nil {
			return os.ErrProcessDone
		}

		if err := syscall.Kill(-process.Pid, syscall.SIGKILL); err != nil {
			if errors.Is(err, syscall.ESRCH) {
				return os.ErrProcessDone
			}
			return err
		}
		return nil
	}
	return cmd
}
