//go:build windows
// +build windows

package backend

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"syscall"
	"time"

	"golang.org/x/sys/windows"
)

// newManagedCommand starts a helper in a new process group. Windows does not
// expose a portable process-group kill through os.Process, so cancellation
// invokes taskkill recursively for the process tree.
func newManagedCommand(ctx context.Context, name string, args ...string) *exec.Cmd {
	if ctx == nil {
		ctx = context.Background()
	}
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{
		CreationFlags: windows.CREATE_NEW_PROCESS_GROUP,
		HideWindow:    true,
	}
	cmd.WaitDelay = 2 * time.Second
	cmd.Cancel = func() error {
		process := cmd.Process
		if process == nil {
			return os.ErrProcessDone
		}

		killer := exec.Command("taskkill", "/PID", strconv.Itoa(process.Pid), "/T", "/F")
		killer.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
		if err := killer.Run(); err != nil {
			var exitErr *exec.ExitError
			if errors.As(err, &exitErr) {
				return os.ErrProcessDone
			}
			return fmt.Errorf("taskkill process tree: %w", err)
		}
		return nil
	}
	return cmd
}
