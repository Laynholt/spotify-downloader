package backend

import (
	"fmt"
	"os"
	"path/filepath"
)

// GetUserDataDir returns the legacy per-user data directory used for history
// and other user-owned state. Runtime helpers and temporary files use the
// portable runtime paths instead.
func GetUserDataDir() (string, error) {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("failed to get user home directory: %w", err)
	}
	return filepath.Join(homeDir, ".spotidownloader"), nil
}

func GetDefaultMusicPath() string {

	homeDir, err := os.UserHomeDir()
	if err != nil {

		return "C:\\Users\\Public\\Music"
	}

	return filepath.Join(homeDir, "Music")
}
