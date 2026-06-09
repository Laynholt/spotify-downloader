package backend

import (
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
)

const SuspiciousDurationThresholdSeconds = 3.0

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
