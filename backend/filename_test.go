package backend

import (
	"strings"
	"testing"
	"unicode/utf16"
)

func TestSanitizeFilenameProducesUsableWindowsNames(t *testing.T) {
	for input, want := range map[string]string{
		"AC/DC: Live?":  "AC DC Live",
		"con":           "_con",
		"Nul.txt":       "_Nul.txt",
		"COM1":          "_COM1",
		"Console":       "Console",
		" ...  ":        "Unknown",
		"a\tb\x00c\x7f": "a bc",
	} {
		if got := SanitizeFilename(input); got != want {
			t.Errorf("SanitizeFilename(%q) = %q, want %q", input, got, want)
		}
	}

	long := SanitizeFilename(strings.Repeat("😀", 150) + " end")
	if units := len(utf16.Encode([]rune(long))); units > maxFilenameUnits {
		t.Fatalf("long name has %d UTF-16 units, want <= %d", units, maxFilenameUnits)
	}
	if !strings.HasPrefix(long, "😀") || strings.ContainsRune(long, '�') {
		t.Fatalf("long name was cut inside a character: %q", long)
	}
}

func TestSidecarFilenamesMatchAudioFilename(t *testing.T) {
	audio := SanitizeFilename(BuildFilename("Song", "Artist", "Album", "Artist", "2024-01-02", 1, "{track}. {artist}/{title}", true, 3, false, "", ""))
	if got := buildCoverFilename("Song", "Artist", "Album", "Artist", "2024-01-02", "{track}. {artist}/{title}", true, 3, 1); got != audio+".jpg" {
		t.Fatalf("cover filename = %q, want %q", got, audio+".jpg")
	}
	if got := buildLyricsFilename("Song", "Artist", "Album", "Artist", "2024-01-02", "{track}. {artist}/{title}", true, 3, 1); got != audio+".lrc" {
		t.Fatalf("lyrics filename = %q, want %q", got, audio+".lrc")
	}
}
