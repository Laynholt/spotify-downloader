package backend

import (
	"os"
	"path/filepath"
)

// AtomicWriteFile preserves the previous file if writing or replacement fails.
func AtomicWriteFile(path string, data []byte, perm os.FileMode) error {
	file, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+"-*")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	defer file.Close()
	if err := file.Chmod(perm); err != nil {
		return err
	}
	if _, err := file.Write(data); err != nil {
		return err
	}
	if err := file.Sync(); err != nil {
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	return atomicReplaceFile(file.Name(), path)
}
