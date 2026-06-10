package backend

import (
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
)

const SuspiciousDurationThresholdSeconds = 3.0
const SuspiciousTracksDirName = "Suspicious"

func IsSuspiciousDuration(expectedSeconds, actualSeconds float64) bool {
	if expectedSeconds <= 0 || actualSeconds <= 0 {
		return false
	}
	return math.Abs(actualSeconds-expectedSeconds) > SuspiciousDurationThresholdSeconds
}

func CollisionSafePath(path string) string {
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return path
	}

	dir := filepath.Dir(path)
	ext := filepath.Ext(path)
	base := strings.TrimSuffix(filepath.Base(path), ext)
	for i := 1; ; i++ {
		candidate := filepath.Join(dir, fmt.Sprintf("%s (%d)%s", base, i, ext))
		if _, err := os.Stat(candidate); os.IsNotExist(err) {
			return candidate
		}
	}
}

func MoveSuspiciousOriginal(srcPath, suspiciousDir string) (string, error) {
	if err := os.MkdirAll(suspiciousDir, 0755); err != nil {
		return "", err
	}

	targetPath := CollisionSafePath(filepath.Join(suspiciousDir, filepath.Base(srcPath)))
	if err := MoveFileWithFallback(srcPath, targetPath); err != nil {
		return "", err
	}
	return targetPath, nil
}

func IsPathInsideDir(path, dir string) bool {
	absPath, err := filepath.Abs(path)
	if err != nil {
		return false
	}
	absDir, err := filepath.Abs(dir)
	if err != nil {
		return false
	}
	rel, err := filepath.Rel(absDir, absPath)
	if err != nil {
		return false
	}
	return rel != "." && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}
