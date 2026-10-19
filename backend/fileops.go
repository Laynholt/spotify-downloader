package backend

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

var ErrFileExists = errors.New("file already exists")

// RenameNoReplace moves srcPath to dstPath but never replaces another file:
// os.Rename on Windows silently overwrites an existing destination.
func RenameNoReplace(srcPath, dstPath string) error {
	dstInfo, err := os.Stat(dstPath)
	if os.IsNotExist(err) {
		return MoveFileWithFallback(srcPath, dstPath)
	}
	if err != nil {
		return err
	}
	// A case-only rename on Windows finds the source itself at dstPath.
	if srcInfo, srcErr := os.Stat(srcPath); srcErr != nil || !os.SameFile(srcInfo, dstInfo) {
		return fmt.Errorf("%w: %s", ErrFileExists, filepath.Base(dstPath))
	}
	return MoveFileWithFallback(srcPath, dstPath)
}

func MoveFileWithFallback(srcPath, dstPath string) error {
	srcPath = filepath.Clean(srcPath)
	dstPath = filepath.Clean(dstPath)

	if srcPath == dstPath {
		return nil
	}

	if err := os.Rename(srcPath, dstPath); err == nil {
		return nil
	}

	src, err := os.Open(srcPath)
	if err != nil {
		return fmt.Errorf("open source file: %w", err)
	}
	defer src.Close()

	info, err := src.Stat()
	if err != nil {
		return fmt.Errorf("stat source file: %w", err)
	}

	dst, err := os.OpenFile(dstPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, info.Mode())
	if err != nil {
		return fmt.Errorf("open destination file: %w", err)
	}

	copyErr := error(nil)
	if _, err := io.Copy(dst, src); err != nil {
		copyErr = err
	}
	closeErr := dst.Close()
	if copyErr != nil {
		return fmt.Errorf("copy file contents: %w", copyErr)
	}
	if closeErr != nil {
		return fmt.Errorf("close destination file: %w", closeErr)
	}

	if err := os.Chmod(dstPath, info.Mode()); err != nil && !os.IsPermission(err) {
		return fmt.Errorf("set destination file mode: %w", err)
	}

	if err := os.Remove(srcPath); err != nil {
		return fmt.Errorf("remove source file: %w", err)
	}

	return nil
}
