//go:build windows

package backend

import (
	"os/exec"
	"syscall"
)

func tokenHelperName() string {
	return "get_token.exe"
}

func newTokenCmd(exePath string, args ...string) *exec.Cmd {
	cmd := exec.Command(exePath, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	return cmd
}
