//go:build !windows

package backend

import "os"

func atomicReplaceFile(tempPath, targetPath string) error {
	return os.Rename(tempPath, targetPath)
}
