package backend

import (
	"os"
	"path/filepath"
	"testing"
)

func TestIsSuspiciousDurationUsesStrictThreeSecondThreshold(t *testing.T) {
	tests := []struct {
		name     string
		expected float64
		actual   float64
		want     bool
	}{
		{name: "exactly three seconds is not suspicious", expected: 180, actual: 183, want: false},
		{name: "more than three seconds is suspicious", expected: 180, actual: 183.01, want: true},
		{name: "negative delta more than three seconds is suspicious", expected: 180, actual: 176.9, want: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := IsSuspiciousDuration(tt.expected, tt.actual)
			if got != tt.want {
				t.Fatalf("IsSuspiciousDuration(%v, %v) = %v, want %v", tt.expected, tt.actual, got, tt.want)
			}
		})
	}
}

func TestMoveSuspiciousOriginalAvoidsFilenameCollisions(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "Song.mp3")
	if err := os.WriteFile(src, []byte("original"), 0644); err != nil {
		t.Fatal(err)
	}
	suspiciousDir := filepath.Join(dir, "Suspicious")
	if err := os.MkdirAll(suspiciousDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(suspiciousDir, "Song.mp3"), []byte("existing"), 0644); err != nil {
		t.Fatal(err)
	}

	movedPath, err := MoveSuspiciousOriginal(src, suspiciousDir)
	if err != nil {
		t.Fatal(err)
	}

	if filepath.Base(movedPath) != "Song (1).mp3" {
		t.Fatalf("moved basename = %q, want Song (1).mp3", filepath.Base(movedPath))
	}
	if _, err := os.Stat(src); !os.IsNotExist(err) {
		t.Fatalf("source still exists or stat failed unexpectedly: %v", err)
	}
}
