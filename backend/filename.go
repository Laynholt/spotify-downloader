package backend

import (
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf16"
)

func BuildFilename(trackName, artistName, albumName, albumArtist, releaseDate string, discNumber int, format string, includeTrackNumber bool, position int, useAlbumTrackNumber bool, playlistName, playlistOwner string) string {
	safeTitle := SanitizeFilename(trackName)
	safeArtist := SanitizeFilename(artistName)
	safeAlbum := SanitizeFilename(albumName)
	safeAlbumArtist := SanitizeFilename(albumArtist)

	safePlaylist := SanitizeFilename(playlistName)
	safeCreator := SanitizeFilename(playlistOwner)

	year := ""
	if len(releaseDate) >= 4 {
		year = releaseDate[:4]
	}

	var filename string

	if strings.Contains(format, "{") {
		filename = format
		filename = strings.ReplaceAll(filename, "{title}", safeTitle)
		filename = strings.ReplaceAll(filename, "{artist}", safeArtist)
		filename = strings.ReplaceAll(filename, "{album}", safeAlbum)
		filename = strings.ReplaceAll(filename, "{album_artist}", safeAlbumArtist)
		filename = strings.ReplaceAll(filename, "{year}", year)
		filename = strings.ReplaceAll(filename, "{date}", SanitizeFilename(releaseDate))
		filename = strings.ReplaceAll(filename, "{playlist}", safePlaylist)
		filename = strings.ReplaceAll(filename, "{creator}", safeCreator)

		if discNumber > 0 {
			filename = strings.ReplaceAll(filename, "{disc}", fmt.Sprintf("%d", discNumber))
		} else {
			filename = strings.ReplaceAll(filename, "{disc}", "")
		}

		if position > 0 {
			filename = strings.ReplaceAll(filename, "{track}", fmt.Sprintf("%02d", position))
		} else {
			filename = trackDotPlaceholder.ReplaceAllString(filename, "")
			filename = trackDashPlaceholder.ReplaceAllString(filename, "")
			filename = trackPlaceholder.ReplaceAllString(filename, "")
		}
		return filename
	}

	switch format {
	case "artist-title":
		filename = fmt.Sprintf("%s - %s", safeArtist, safeTitle)
	case "title":
		filename = safeTitle
	default:
		filename = fmt.Sprintf("%s - %s", safeTitle, safeArtist)
	}

	if includeTrackNumber && position > 0 {
		filename = fmt.Sprintf("%02d. %s", position, filename)
	}

	return filename
}

// maxFilenameUnits keeps a name (plus its extension and temp suffix) under the
// 255 UTF-16 unit limit of NTFS and most other filesystems.
const maxFilenameUnits = 200

var (
	filenameInvalidChars = strings.NewReplacer("/", " ", "\\", " ", ":", " ", "*", " ", "?", " ", "\"", " ", "<", " ", ">", " ", "|", " ")
	whitespaceRun        = regexp.MustCompile(`\s+`)
	underscoreRun        = regexp.MustCompile(`_+`)
	trackDotPlaceholder  = regexp.MustCompile(`\{track\}\.\s*`)
	trackDashPlaceholder = regexp.MustCompile(`\{track\}\s*-\s*`)
	trackPlaceholder     = regexp.MustCompile(`\{track\}\s*`)
	windowsReservedName  = regexp.MustCompile(`(?i)^(con|prn|aux|nul|com[1-9]|lpt[1-9])(\..*)?$`)
)

func SanitizeFilename(filename string) string {
	result := strings.Map(func(r rune) rune {
		if unicode.IsSpace(r) {
			return ' '
		}
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, filenameInvalidChars.Replace(strings.ToValidUTF8(filename, "_")))
	result = whitespaceRun.ReplaceAllString(result, " ")
	result = underscoreRun.ReplaceAllString(result, "_")
	result = truncateUTF16(strings.Trim(result, ". "), maxFilenameUnits)
	// Truncation can expose a trailing dot or space again; Windows strips them.
	result = strings.TrimRight(result, ". ")
	if result == "" {
		return "Unknown"
	}
	if windowsReservedName.MatchString(result) {
		return "_" + result
	}
	return result
}

func truncateUTF16(s string, maxUnits int) string {
	units := 0
	for i, r := range s {
		units += utf16.RuneLen(r)
		if units > maxUnits {
			return s[:i]
		}
	}
	return s
}

func GetFirstArtist(artistString string) string {
	if artistString == "" {
		return ""
	}
	delimiters := []string{", ", " & ", " feat. ", " ft. ", " featuring "}
	for _, d := range delimiters {
		if idx := strings.Index(strings.ToLower(artistString), d); idx != -1 {
			return strings.TrimSpace(artistString[:idx])
		}
	}
	return artistString
}

func NormalizePath(folderPath string) string {

	return strings.ReplaceAll(folderPath, "/", string(filepath.Separator))
}

func SanitizeFolderPath(folderPath string) string {

	normalizedPath := strings.ReplaceAll(folderPath, "/", string(filepath.Separator))

	sep := string(filepath.Separator)

	parts := strings.Split(normalizedPath, sep)
	sanitizedParts := make([]string, 0, len(parts))

	for i, part := range parts {

		if i == 0 && len(part) == 2 && part[1] == ':' {
			sanitizedParts = append(sanitizedParts, part)
			continue
		}

		if i == 0 && part == "" {
			sanitizedParts = append(sanitizedParts, part)
			continue
		}

		sanitized := sanitizeFolderName(part)
		if sanitized != "" {
			sanitizedParts = append(sanitizedParts, sanitized)
		}
	}

	return strings.Join(sanitizedParts, sep)
}

func sanitizeFolderName(name string) string {

	return SanitizeFilename(name)
}
