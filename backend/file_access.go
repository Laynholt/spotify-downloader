package backend

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const (
	maxReadableTextFileBytes  = 2 * 1024 * 1024
	maxReadableImageFileBytes = 25 * 1024 * 1024
)

var readableTextExtensions = map[string]bool{
	".lrc": true,
	".txt": true,
}

var readableImageExtensions = map[string]bool{
	".jpg":  true,
	".jpeg": true,
	".png":  true,
	".gif":  true,
	".webp": true,
}

var uploadMediaExtensions = map[string]bool{
	".jpg":  true,
	".jpeg": true,
	".png":  true,
	".gif":  true,
	".webp": true,
	".mp4":  true,
	".mov":  true,
	".webm": true,
}

func validateRegularFile(path string, allowedExtensions map[string]bool, maxBytes int64) error {
	cleanedPath := filepath.Clean(path)
	if cleanedPath == "" {
		return fmt.Errorf("file path is required")
	}
	if !filepath.IsAbs(cleanedPath) {
		return fmt.Errorf("file path must be absolute")
	}
	if !allowedExtensions[strings.ToLower(filepath.Ext(cleanedPath))] {
		return fmt.Errorf("unsupported file type: %s", filepath.Ext(cleanedPath))
	}
	info, err := os.Stat(cleanedPath)
	if err != nil {
		return err
	}
	if info.IsDir() {
		return fmt.Errorf("path is a directory")
	}
	if maxBytes > 0 && info.Size() > maxBytes {
		return fmt.Errorf("file is too large")
	}
	return nil
}

func ValidateReadableTextFile(path string) error {
	return validateRegularFile(path, readableTextExtensions, maxReadableTextFileBytes)
}

func ValidateReadableImageFile(path string) error {
	return validateRegularFile(path, readableImageExtensions, maxReadableImageFileBytes)
}

func ValidateUploadMediaFile(path string) error {
	return validateRegularFile(path, uploadMediaExtensions, 0)
}

func BuildSiblingRenamePath(oldPath, newName string) (string, error) {
	cleanedOldPath := filepath.Clean(oldPath)
	if cleanedOldPath == "" || !filepath.IsAbs(cleanedOldPath) {
		return "", fmt.Errorf("file path must be absolute")
	}
	if strings.TrimSpace(newName) == "" {
		return "", fmt.Errorf("new name is required")
	}
	if newName != filepath.Base(newName) || strings.ContainsAny(newName, `/\`) {
		return "", fmt.Errorf("new name must not contain path separators")
	}
	safeName := SanitizeFilename(newName)
	if safeName == "" {
		return "", fmt.Errorf("new name is invalid")
	}
	return filepath.Join(filepath.Dir(cleanedOldPath), safeName+filepath.Ext(cleanedOldPath)), nil
}
