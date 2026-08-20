//go:build linux || darwin

package backend

import (
	"os/exec"
)

func tokenHelperName() string {
	return "get_token"
}

func newTokenCmd(exePath string, args ...string) *exec.Cmd {
	return exec.Command(exePath, args...)
}
