//go:build windows
// +build windows

package backend

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"syscall"
	"time"
	"unsafe"

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

		killCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		systemDir, err := windows.GetSystemDirectory()
		if err != nil {
			return killWindowsProcessTree(uint32(process.Pid))
		}
		killer := exec.CommandContext(killCtx, filepath.Join(systemDir, "taskkill.exe"), "/PID", strconv.Itoa(process.Pid), "/T", "/F")
		killer.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
		return terminateWindowsProcessTree(process, killer)
	}
	return cmd
}

func terminateWindowsProcessTree(process *os.Process, killer *exec.Cmd) error {
	if err := killer.Run(); err != nil {
		if fallbackErr := killWindowsProcessTree(uint32(process.Pid)); fallbackErr != nil {
			return fmt.Errorf("taskkill: %v; process-tree fallback: %w", err, fallbackErr)
		}
	}
	return nil
}

// ponytail: the fallback snapshots existing descendants; use a Job Object if
// helpers start spawning children during cancellation.
func killWindowsProcessTree(root uint32) error {
	snapshot, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return err
	}
	defer windows.CloseHandle(snapshot)
	entry := windows.ProcessEntry32{Size: uint32(unsafe.Sizeof(windows.ProcessEntry32{}))}
	children := make(map[uint32][]uint32)
	for err = windows.Process32First(snapshot, &entry); err == nil; err = windows.Process32Next(snapshot, &entry) {
		children[entry.ParentProcessID] = append(children[entry.ParentProcessID], entry.ProcessID)
	}
	if !errors.Is(err, windows.ERROR_NO_MORE_FILES) {
		return err
	}
	pids := []uint32{root}
	seen := map[uint32]bool{root: true}
	for i := 0; i < len(pids); i++ {
		for _, child := range children[pids[i]] {
			if !seen[child] {
				pids = append(pids, child)
				seen[child] = true
			}
		}
	}
	var handles []windows.Handle
	var failures []error
	for _, pid := range pids {
		handle, err := windows.OpenProcess(windows.PROCESS_TERMINATE|windows.SYNCHRONIZE, false, pid)
		if errors.Is(err, windows.ERROR_INVALID_PARAMETER) {
			continue
		}
		if err != nil {
			failures = append(failures, err)
			continue
		}
		defer windows.CloseHandle(handle)
		handles = append(handles, handle)
	}
	for _, handle := range handles {
		if err := windows.TerminateProcess(handle, 1); err != nil {
			state, _ := windows.WaitForSingleObject(handle, 0)
			if state != windows.WAIT_OBJECT_0 {
				failures = append(failures, err)
			}
		}
	}
	return errors.Join(failures...)
}
