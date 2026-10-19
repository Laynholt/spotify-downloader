package backend

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type LRCLibResponse struct {
	ID           int     `json:"id"`
	Name         string  `json:"name"`
	TrackName    string  `json:"trackName"`
	ArtistName   string  `json:"artistName"`
	AlbumName    string  `json:"albumName"`
	Duration     float64 `json:"duration"`
	Instrumental bool    `json:"instrumental"`
	PlainLyrics  string  `json:"plainLyrics"`
	SyncedLyrics string  `json:"syncedLyrics"`
}

type LyricsLine struct {
	StartTimeMs string `json:"startTimeMs"`
	Words       string `json:"words"`
	EndTimeMs   string `json:"endTimeMs"`
}

type LyricsResponse struct {
	Error    bool         `json:"error"`
	SyncType string       `json:"syncType"`
	Lines    []LyricsLine `json:"lines"`
}

type SpotifyLyricsLine struct {
	TimeTag string `json:"timeTag"`
	Words   string `json:"words"`
}

type SpotifyLyricsAPIResponse struct {
	Error    bool                `json:"error"`
	SyncType string              `json:"syncType"`
	Lines    []SpotifyLyricsLine `json:"lines"`
}

type LyricsDownloadRequest struct {
	SpotifyID           string `json:"spotify_id"`
	TrackName           string `json:"track_name"`
	ArtistName          string `json:"artist_name"`
	AlbumName           string `json:"album_name"`
	AlbumArtist         string `json:"album_artist"`
	ReleaseDate         string `json:"release_date"`
	OutputDir           string `json:"output_dir"`
	FilenameFormat      string `json:"filename_format"`
	TrackNumber         bool   `json:"track_number"`
	Position            int    `json:"position"`
	UseAlbumTrackNumber bool   `json:"use_album_track_number"`
	DiscNumber          int    `json:"disc_number"`
}

type LyricsDownloadResponse struct {
	Success       bool   `json:"success"`
	Message       string `json:"message"`
	File          string `json:"file,omitempty"`
	Error         string `json:"error,omitempty"`
	AlreadyExists bool   `json:"already_exists,omitempty"`
}

type LyricsClient struct {
	httpClient *http.Client
}

func NewLyricsClient() *LyricsClient {
	return &LyricsClient{
		httpClient: newHTTPClient(15 * time.Second),
	}
}

func (c *LyricsClient) FetchLyricsWithMetadata(trackName, artistName string, duration int) (*LyricsResponse, error) {
	return c.FetchLyricsWithMetadataContext(context.Background(), trackName, artistName, duration)
}

func (c *LyricsClient) FetchLyricsWithMetadataContext(ctx context.Context, trackName, artistName string, duration int) (*LyricsResponse, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	apiBase, _ := base64.StdEncoding.DecodeString("aHR0cHM6Ly9scmNsaWIubmV0L2FwaS9nZXQ/YXJ0aXN0X25hbWU9")
	apiURL := fmt.Sprintf("%s%s&track_name=%s",
		string(apiBase),
		url.QueryEscape(artistName),
		url.QueryEscape(trackName))

	if duration > 0 {
		apiURL = fmt.Sprintf("%s&duration=%d", apiURL, duration)
	}

	req, err := newHTTPRequestWithContext(ctx, http.MethodGet, apiURL, nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, ctxErr
		}
		return nil, fmt.Errorf("failed to fetch from LRCLIB: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("LRCLIB returned status %d", resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, ctxErr
		}
		return nil, fmt.Errorf("failed to read LRCLIB response: %v", err)
	}

	var lrcLibResp LRCLibResponse
	if err := json.Unmarshal(body, &lrcLibResp); err != nil {
		return nil, fmt.Errorf("failed to parse LRCLIB response: %v", err)
	}

	return c.convertLRCLibToLyricsResponse(&lrcLibResp), nil
}

func (c *LyricsClient) convertLRCLibToLyricsResponse(lrcLib *LRCLibResponse) *LyricsResponse {
	resp := &LyricsResponse{
		Error:    false,
		SyncType: "LINE_SYNCED",
		Lines:    []LyricsLine{},
	}

	lyricsText := lrcLib.SyncedLyrics
	if lyricsText == "" {
		lyricsText = lrcLib.PlainLyrics
		resp.SyncType = "UNSYNCED"
	}

	if lyricsText == "" {
		resp.Error = true
		return resp
	}

	lines := strings.Split(lyricsText, "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}

		if strings.HasPrefix(line, "[") && len(line) > 10 {
			closeBracket := strings.Index(line, "]")
			if closeBracket > 0 {
				timestamp := line[1:closeBracket]
				words := strings.TrimSpace(line[closeBracket+1:])

				ms := lrcTimestampToMs(timestamp)
				resp.Lines = append(resp.Lines, LyricsLine{
					StartTimeMs: fmt.Sprintf("%d", ms),
					Words:       words,
				})
				continue
			}
		}

		resp.Lines = append(resp.Lines, LyricsLine{
			StartTimeMs: "",
			Words:       line,
		})
	}

	return resp
}

func lrcTimestampToMs(timestamp string) int64 {
	var minutes, seconds, centiseconds int64

	n, _ := fmt.Sscanf(timestamp, "%d:%d.%d", &minutes, &seconds, &centiseconds)
	if n >= 2 {
		return minutes*60*1000 + seconds*1000 + centiseconds*10
	}
	return 0
}

func (c *LyricsClient) FetchLyricsFromLRCLibSearch(trackName, artistName string) (*LyricsResponse, error) {
	return c.FetchLyricsFromLRCLibSearchContext(context.Background(), trackName, artistName)
}

func (c *LyricsClient) FetchLyricsFromLRCLibSearchContext(ctx context.Context, trackName, artistName string) (*LyricsResponse, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	query := fmt.Sprintf("%s %s", artistName, trackName)
	apiBase, _ := base64.StdEncoding.DecodeString("aHR0cHM6Ly9scmNsaWIubmV0L2FwaS9zZWFyY2g/cT0=")
	apiURL := fmt.Sprintf("%s%s", string(apiBase), url.QueryEscape(query))

	req, err := newHTTPRequestWithContext(ctx, http.MethodGet, apiURL, nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, ctxErr
		}
		return nil, fmt.Errorf("request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("status %d", resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, ctxErr
		}
		return nil, fmt.Errorf("read failed: %v", err)
	}

	var results []LRCLibResponse
	if err := json.Unmarshal(body, &results); err != nil {
		return nil, fmt.Errorf("parse failed: %v", err)
	}

	if len(results) == 0 {
		return nil, fmt.Errorf("no results found")
	}

	var best *LRCLibResponse
	for i := range results {
		if results[i].SyncedLyrics != "" {
			best = &results[i]
			break
		}
		if best == nil && results[i].PlainLyrics != "" {
			best = &results[i]
		}
	}

	if best == nil {
		best = &results[0]
	}

	return c.convertLRCLibToLyricsResponse(best), nil
}

func (c *LyricsClient) FetchLyricsFromSpotifyAPI(spotifyID string) (*LyricsResponse, error) {
	return c.FetchLyricsFromSpotifyAPIContext(context.Background(), spotifyID)
}

func (c *LyricsClient) FetchLyricsFromSpotifyAPIContext(ctx context.Context, spotifyID string) (*LyricsResponse, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if spotifyID == "" {
		return nil, fmt.Errorf("spotify ID is empty")
	}

	apiURL := fmt.Sprintf("https://spotify-lyrics-api-pi.vercel.app/?trackid=%s&format=lrc", spotifyID)

	req, err := newHTTPRequestWithContext(ctx, http.MethodGet, apiURL, nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, ctxErr
		}
		return nil, fmt.Errorf("failed to fetch from Spotify Lyrics API: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("Spotify Lyrics API returned status %d", resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, ctxErr
		}
		return nil, fmt.Errorf("failed to read Spotify Lyrics API response: %v", err)
	}

	var apiResp SpotifyLyricsAPIResponse
	if err := json.Unmarshal(body, &apiResp); err != nil {
		return nil, fmt.Errorf("failed to parse Spotify Lyrics API response: %v", err)
	}

	if apiResp.Error {
		return nil, fmt.Errorf("Spotify Lyrics API returned error")
	}

	result := &LyricsResponse{
		Error:    false,
		SyncType: apiResp.SyncType,
		Lines:    []LyricsLine{},
	}

	for _, line := range apiResp.Lines {
		if line.TimeTag == "" && line.Words == "" {
			continue
		}
		ms := lrcTimestampToMs(line.TimeTag)
		result.Lines = append(result.Lines, LyricsLine{
			StartTimeMs: fmt.Sprintf("%d", ms),
			Words:       line.Words,
		})
	}

	if len(result.Lines) == 0 {
		return nil, fmt.Errorf("Spotify Lyrics API returned empty lines")
	}

	return result, nil
}

func simplifyTrackName(name string) string {

	if idx := strings.Index(name, "("); idx > 0 {
		name = strings.TrimSpace(name[:idx])
	}

	if idx := strings.Index(name, " - "); idx > 0 {
		name = strings.TrimSpace(name[:idx])
	}
	return name
}

func (c *LyricsClient) FetchLyricsAllSources(spotifyID, trackName, artistName string, duration int) (*LyricsResponse, string, error) {
	return c.FetchLyricsAllSourcesContext(context.Background(), spotifyID, trackName, artistName, duration)
}

func (c *LyricsClient) FetchLyricsAllSourcesContext(ctx context.Context, spotifyID, trackName, artistName string, duration int) (*LyricsResponse, string, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, "", err
	}
	var failures []string
	recordFailure := func(source string, err error) {
		detail := "no lyrics returned"
		if err != nil {
			detail = err.Error()
		}
		failures = append(failures, source+": "+detail)
	}

	resp, err := c.FetchLyricsFromSpotifyAPIContext(ctx, spotifyID)
	if err == nil && resp != nil && !resp.Error && len(resp.Lines) > 0 {
		return resp, "Spotify", nil
	}
	if ctxErr := ctx.Err(); ctxErr != nil {
		return nil, "", ctxErr
	}
	fmt.Printf("   Spotify Lyrics API: %v\n", err)
	recordFailure("Spotify Lyrics API", err)

	resp, err = c.FetchLyricsWithMetadataContext(ctx, trackName, artistName, duration)
	if err == nil && resp != nil && !resp.Error && len(resp.Lines) > 0 {
		return resp, "LRCLIB", nil
	}
	if ctxErr := ctx.Err(); ctxErr != nil {
		return nil, "", ctxErr
	}
	fmt.Printf("   LRCLIB exact: %v\n", err)
	recordFailure("LRCLIB exact", err)

	resp, err = c.FetchLyricsFromLRCLibSearchContext(ctx, trackName, artistName)
	if err == nil && resp != nil && !resp.Error && len(resp.Lines) > 0 {
		return resp, "LRCLIB Search", nil
	}
	if ctxErr := ctx.Err(); ctxErr != nil {
		return nil, "", ctxErr
	}
	fmt.Printf("   LRCLIB search: %v\n", err)
	recordFailure("LRCLIB search", err)

	simplifiedTrack := simplifyTrackName(trackName)
	if simplifiedTrack != trackName {
		fmt.Printf("   Trying simplified name: %s\n", simplifiedTrack)

		resp, err = c.FetchLyricsWithMetadataContext(ctx, simplifiedTrack, artistName, duration)
		if err == nil && resp != nil && !resp.Error && len(resp.Lines) > 0 {
			return resp, "LRCLIB (simplified)", nil
		}
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, "", ctxErr
		}
		recordFailure("LRCLIB exact (simplified)", err)

		resp, err = c.FetchLyricsFromLRCLibSearchContext(ctx, simplifiedTrack, artistName)
		if err == nil && resp != nil && !resp.Error && len(resp.Lines) > 0 {
			return resp, "LRCLIB Search (simplified)", nil
		}
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, "", ctxErr
		}
		recordFailure("LRCLIB search (simplified)", err)
	}

	return nil, "", fmt.Errorf("no lyrics retrieved; %s", strings.Join(failures, "; "))
}

func (c *LyricsClient) ConvertToLRC(lyrics *LyricsResponse, trackName, artistName string) string {
	var sb strings.Builder

	sb.WriteString(fmt.Sprintf("[ti:%s]\n", trackName))
	sb.WriteString(fmt.Sprintf("[ar:%s]\n", artistName))
	sb.WriteString("[by:SpotiDownloader]\n")
	sb.WriteString("\n")

	for _, line := range lyrics.Lines {
		if line.Words == "" {
			continue
		}

		if line.StartTimeMs == "" {
			sb.WriteString(fmt.Sprintf("%s\n", line.Words))
		} else {

			timestamp := msToLRCTimestamp(line.StartTimeMs)
			sb.WriteString(fmt.Sprintf("%s%s\n", timestamp, line.Words))
		}
	}

	return sb.String()
}

func msToLRCTimestamp(msStr string) string {
	var ms int64
	fmt.Sscanf(msStr, "%d", &ms)

	totalSeconds := ms / 1000
	minutes := totalSeconds / 60
	seconds := totalSeconds % 60
	centiseconds := (ms % 1000) / 10

	return fmt.Sprintf("[%02d:%02d.%02d]", minutes, seconds, centiseconds)
}

func buildLyricsFilename(trackName, artistName, albumName, albumArtist, releaseDate, filenameFormat string, includeTrackNumber bool, position, discNumber int) string {
	return SanitizeFilename(BuildFilename(trackName, artistName, albumName, albumArtist, releaseDate, discNumber, filenameFormat, includeTrackNumber, position, false, "", "")) + ".lrc"
}

func findAudioFileForLyrics(dir, trackName, artistName string) string {

	safeTitle := SanitizeFilename(trackName)
	safeArtist := SanitizeFilename(artistName)

	audioExts := []string{".flac", ".mp3", ".m4a", ".FLAC", ".MP3", ".M4A"}

	patterns := []string{
		fmt.Sprintf("%s - %s", safeTitle, safeArtist),
		fmt.Sprintf("%s - %s", safeArtist, safeTitle),
		safeTitle,
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		return ""
	}

	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}

		filename := entry.Name()
		baseName := strings.TrimSuffix(filename, filepath.Ext(filename))

		for _, pattern := range patterns {
			if strings.HasPrefix(baseName, pattern) || strings.Contains(baseName, pattern) {
				ext := strings.ToLower(filepath.Ext(filename))
				for _, audioExt := range audioExts {
					if ext == strings.ToLower(audioExt) {
						return filepath.Join(dir, filename)
					}
				}
			}
		}
	}

	return ""
}

func (c *LyricsClient) DownloadLyrics(req LyricsDownloadRequest) (*LyricsDownloadResponse, error) {
	return c.DownloadLyricsContext(context.Background(), req)
}

func (c *LyricsClient) DownloadLyricsContext(ctx context.Context, req LyricsDownloadRequest) (*LyricsDownloadResponse, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return &LyricsDownloadResponse{Success: false, Error: err.Error()}, err
	}
	if req.SpotifyID == "" {
		return &LyricsDownloadResponse{
			Success: false,
			Error:   "Spotify ID is required",
		}, fmt.Errorf("spotify ID is required")
	}

	outputDir := req.OutputDir
	if outputDir == "" {
		outputDir = GetDefaultMusicPath()
	} else {
		outputDir = NormalizePath(outputDir)
	}

	folderArtist := req.AlbumArtist
	if folderArtist == "" {
		folderArtist = req.ArtistName
	}

	if folderArtist != "" && req.AlbumName != "" {
		safeArtist, safeAlbum := SanitizeFilename(folderArtist), SanitizeFilename(req.AlbumName)
		artistAlbumPath := filepath.Join(outputDir, safeArtist, safeAlbum)
		if info, err := os.Stat(artistAlbumPath); err == nil && info.IsDir() {
			outputDir = artistAlbumPath
		} else {

			artistPath := filepath.Join(outputDir, safeArtist)
			if info, err := os.Stat(artistPath); err == nil && info.IsDir() {
				outputDir = artistPath
			}
		}
	}

	if err := os.MkdirAll(outputDir, 0755); err != nil {
		return &LyricsDownloadResponse{
			Success: false,
			Error:   fmt.Sprintf("failed to create output directory: %v", err),
		}, err
	}

	filenameFormat := req.FilenameFormat
	if filenameFormat == "" {
		filenameFormat = "title-artist"
	}
	filename := buildLyricsFilename(req.TrackName, req.ArtistName, req.AlbumName, req.AlbumArtist, req.ReleaseDate, filenameFormat, req.TrackNumber, req.Position, req.DiscNumber)
	filePath := filepath.Join(outputDir, filename)

	if fileInfo, err := os.Stat(filePath); err == nil && fileInfo.Size() > 0 {
		return &LyricsDownloadResponse{
			Success:       true,
			Message:       "Lyrics file already exists",
			File:          filePath,
			AlreadyExists: true,
		}, nil
	}

	audioDuration := 0
	audioFile := findAudioFileForLyrics(outputDir, req.TrackName, req.ArtistName)
	if audioFile != "" {
		duration, err := GetAudioDurationContext(ctx, audioFile)
		if err == nil && duration > 0 {
			audioDuration = int(duration)
			fmt.Printf("[DownloadLyrics] Found audio file, duration: %d seconds\n", audioDuration)
		}
	}

	lyrics, _, err := c.FetchLyricsAllSourcesContext(ctx, req.SpotifyID, req.TrackName, req.ArtistName, audioDuration)
	if err != nil {
		return &LyricsDownloadResponse{
			Success: false,
			Error:   err.Error(),
		}, err
	}

	lrcContent := c.ConvertToLRC(lyrics, req.TrackName, req.ArtistName)

	if err := os.WriteFile(filePath, []byte(lrcContent), 0644); err != nil {
		return &LyricsDownloadResponse{
			Success: false,
			Error:   fmt.Sprintf("failed to write LRC file: %v", err),
		}, err
	}

	return &LyricsDownloadResponse{
		Success: true,
		Message: "Lyrics downloaded successfully",
		File:    filePath,
	}, nil
}
