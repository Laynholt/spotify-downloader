package backend

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const (
	spotidownloaderAPIBase = "https://api.spotidownloader.com"
)

const (
	networkRetryAttempts = 3
	networkRetryDelay    = 750 * time.Millisecond
	networkRetryMaxDelay = 3 * time.Second
)

type SpotiDownloader struct {
	sessionToken string
	httpClient   *http.Client
}

type sessionTokenError struct {
	statusCode int
	body       string
}

type apiErrorPayload struct {
	Message string `json:"message"`
	Error   string `json:"error"`
	Code    string `json:"code"`
	Detail  string `json:"detail"`
}

func (e *sessionTokenError) Error() string {
	if e.body == "" {
		return fmt.Sprintf("API returned status %d", e.statusCode)
	}
	return fmt.Sprintf("API returned status %d: %s", e.statusCode, e.body)
}

var (
	sessionTokenMu       sync.RWMutex
	rememberedSessionTok string
)

type FlacAvailableRequest struct {
	ID string `json:"id"`
}

type FlacAvailableResponse struct {
	Available bool `json:"available"`
}

type DownloadRequest struct {
	ID string `json:"id"`
}

type DownloadResponse struct {
	Success  bool   `json:"success"`
	Link     string `json:"link"`
	LinkFlac string `json:"linkFlac"`
}

type taggingMetadataResult struct {
	ISRC        string
	ReleaseDate string
	Genre       string
}

func NewSpotiDownloader(sessionToken string) *SpotiDownloader {
	preferredToken := getPreferredSessionToken(sessionToken)
	if preferredToken != "" {
		RememberSessionToken(preferredToken)
	}

	return &SpotiDownloader{
		sessionToken: preferredToken,
		httpClient:   newHTTPClient(60 * time.Second),
	}
}

func (s *SpotiDownloader) CloseIdleConnections() {
	if s == nil || s.httpClient == nil {
		return
	}
	s.httpClient.CloseIdleConnections()
}

func isTransientNetworkError(err error) bool {
	if err == nil {
		return false
	}

	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return true
	}

	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "proxyconnect") ||
		strings.Contains(msg, "connectex") ||
		strings.Contains(msg, "connection refused") ||
		strings.Contains(msg, "connection reset") ||
		strings.Contains(msg, "i/o timeout") ||
		strings.Contains(msg, "timeout awaiting response headers")
}

func (s *SpotiDownloader) doRequestWithNetworkRetry(req *http.Request) (*http.Response, error) {
	var resp *http.Response
	var finalErr error

	_ = RetryWithBackoff(networkRetryAttempts, networkRetryDelay, networkRetryMaxDelay, func(attempt int) error {
		if attempt > 1 && req.GetBody != nil {
			body, err := req.GetBody()
			if err != nil {
				finalErr = err
				return nil
			}
			req.Body = body
		}

		var err error
		resp, err = s.httpClient.Do(req)
		if err == nil {
			finalErr = nil
			return nil
		}

		finalErr = err
		if isTransientNetworkError(err) {
			return err
		}
		return nil
	})

	if finalErr != nil {
		return nil, finalErr
	}
	return resp, nil
}

func fetchTaggingMetadataInBackground(trackID, trackName, artistName, albumName, releaseDate string, useSingleGenre, embedGenre bool) <-chan taggingMetadataResult {
	resultCh := make(chan taggingMetadataResult, 1)

	go func() {
		defer close(resultCh)

		result := taggingMetadataResult{
			ReleaseDate: strings.TrimSpace(releaseDate),
		}
		if strings.TrimSpace(trackID) == "" {
			resultCh <- result
			return
		}

		startedAt := time.Now()
		var wg sync.WaitGroup
		var mu sync.Mutex
		var musicBrainzGenre string
		var fallbackGenre string

		if result.ReleaseDate == "" {
			wg.Add(1)
			go func() {
				defer wg.Done()
				if fetchedReleaseDate, _, _ := fetchTrackTaggingMetadata(trackID); fetchedReleaseDate != "" {
					mu.Lock()
					result.ReleaseDate = strings.TrimSpace(fetchedReleaseDate)
					mu.Unlock()
				}
			}()
		}

		if embedGenre {
			wg.Add(1)
			go func() {
				defer wg.Done()

				client := NewSongLinkClient()
				isrc, err := client.GetISRC(trackID)
				if err != nil || isrc == "" {
					return
				}

				mu.Lock()
				result.ISRC = isrc
				mu.Unlock()

				fmt.Println("Fetching MusicBrainz metadata...")
				fetchedMeta, err := FetchMusicBrainzMetadata(isrc, trackName, artistName, albumName, useSingleGenre, embedGenre)
				if err != nil {
					fmt.Printf("Warning: Failed to fetch MusicBrainz metadata: %v\n", err)
					return
				}

				mu.Lock()
				musicBrainzGenre = strings.TrimSpace(fetchedMeta.Genre)
				mu.Unlock()
			}()

			wg.Add(1)
			go func() {
				defer wg.Done()

				if chosicGenres, err := fetchGenresFromChosic(trackID); err == nil && len(chosicGenres) > 0 {
					genre := strings.TrimSpace(chosicGenres[0])
					if !useSingleGenre {
						genre = strings.TrimSpace(strings.Join(chosicGenres, ", "))
					}
					if genre != "" {
						mu.Lock()
						fallbackGenre = genre
						mu.Unlock()
						return
					}
				}

				if deezerGenre, err := fetchGenreFromDeezer(trackID); err == nil && deezerGenre != "" {
					mu.Lock()
					fallbackGenre = strings.TrimSpace(deezerGenre)
					mu.Unlock()
				}
			}()
		}

		wg.Wait()

		mu.Lock()
		if musicBrainzGenre != "" {
			result.Genre = musicBrainzGenre
		} else {
			result.Genre = fallbackGenre
		}
		mu.Unlock()

		if elapsed := time.Since(startedAt); elapsed > time.Second {
			fmt.Printf("Tagging metadata fetched in %.1fs\n", elapsed.Seconds())
		}

		resultCh <- result
	}()

	return resultCh
}

func RememberSessionToken(token string) {
	token = strings.TrimSpace(token)
	if token == "" {
		return
	}

	sessionTokenMu.Lock()
	rememberedSessionTok = token
	sessionTokenMu.Unlock()
}

func getPreferredSessionToken(token string) string {
	sessionTokenMu.RLock()
	cachedToken := strings.TrimSpace(rememberedSessionTok)
	sessionTokenMu.RUnlock()

	if cachedToken != "" {
		return cachedToken
	}

	token = strings.TrimSpace(token)
	if token != "" {
		RememberSessionToken(token)
	}
	return token
}

func (s *SpotiDownloader) setSessionToken(token string) {
	token = strings.TrimSpace(token)
	if token == "" {
		return
	}

	s.sessionToken = token
	RememberSessionToken(token)
}

func formatStatusBody(body []byte) string {
	bodyStr := strings.TrimSpace(string(body))
	if len(bodyStr) > 200 {
		bodyStr = bodyStr[:200] + "..."
	}
	return bodyStr
}

func isSessionTokenStatus(statusCode int) bool {
	return statusCode == http.StatusUnauthorized || statusCode == http.StatusForbidden
}

func bodySignalsSessionTokenIssue(body []byte) bool {
	body = bytes.TrimSpace(body)
	if len(body) == 0 {
		return false
	}

	candidates := []string{string(body)}

	var payload apiErrorPayload
	if err := json.Unmarshal(body, &payload); err == nil {
		candidates = append(candidates, payload.Message, payload.Error, payload.Code, payload.Detail)
	}

	for _, candidate := range candidates {
		value := strings.ToLower(strings.TrimSpace(candidate))
		if value == "" {
			continue
		}

		switch {
		case value == "err_request_invalid":
			return true
		case strings.Contains(value, "jwt expired"):
			return true
		case strings.Contains(value, "token expired"):
			return true
		case strings.Contains(value, "expired token"):
			return true
		case strings.Contains(value, "invalid token"):
			return true
		case strings.Contains(value, "bearer token"):
			return true
		case strings.Contains(value, "authorization"):
			return true
		case strings.Contains(value, "unauthorized"):
			return true
		}
	}

	return false
}

func isSessionTokenResponse(statusCode int, body []byte) bool {
	if isSessionTokenStatus(statusCode) {
		return true
	}

	if statusCode == http.StatusBadRequest && bodySignalsSessionTokenIssue(body) {
		return true
	}

	return false
}

func statusError(statusCode int, body []byte) error {
	bodyStr := formatStatusBody(body)
	if isSessionTokenResponse(statusCode, body) {
		return &sessionTokenError{statusCode: statusCode, body: bodyStr}
	}
	if bodyStr == "" {
		return fmt.Errorf("API returned status %d", statusCode)
	}
	return fmt.Errorf("API returned status %d: %s", statusCode, bodyStr)
}

func normalizeAudioExtension(ext string) string {
	ext = strings.ToLower(strings.TrimSpace(ext))
	switch ext {
	case ".mp3", "mp3":
		return ".mp3"
	case ".flac", "flac":
		return ".flac"
	case ".m4a", "m4a", ".mp4", "mp4", ".aac", "aac":
		return ".m4a"
	default:
		return ""
	}
}

func PossibleAudioExtensions(audioFormat string) []string {
	var candidates []string
	switch strings.ToLower(strings.TrimSpace(audioFormat)) {
	case "flac":
		candidates = []string{".flac", ".m4a", ".mp3"}
	case "m4a", "aac", "mp4":
		candidates = []string{".m4a", ".mp3", ".flac"}
	default:
		candidates = []string{".mp3", ".m4a", ".flac"}
	}

	seen := make(map[string]struct{}, len(candidates))
	var result []string
	for _, ext := range candidates {
		normalized := normalizeAudioExtension(ext)
		if normalized == "" {
			continue
		}
		if _, ok := seen[normalized]; ok {
			continue
		}
		seen[normalized] = struct{}{}
		result = append(result, normalized)
	}
	return result
}

func preferredAudioExtension(audioFormat string) string {
	switch strings.ToLower(strings.TrimSpace(audioFormat)) {
	case "flac":
		return ".flac"
	case "m4a", "aac", "mp4":
		return ".m4a"
	default:
		return ".mp3"
	}
}

func detectAudioExtension(contentType string, sniff []byte, fallbackExt string) string {
	normalizedFallback := normalizeAudioExtension(fallbackExt)
	contentType = strings.ToLower(strings.TrimSpace(strings.Split(contentType, ";")[0]))

	switch {
	case strings.Contains(contentType, "flac"):
		return ".flac"
	case strings.Contains(contentType, "mp4"), strings.Contains(contentType, "m4a"), strings.Contains(contentType, "aac"), strings.Contains(contentType, "quicktime"):
		return ".m4a"
	case strings.Contains(contentType, "mpeg"), strings.Contains(contentType, "mp3"):
		return ".mp3"
	}

	if len(sniff) >= 4 && string(sniff[:4]) == "fLaC" {
		return ".flac"
	}
	if len(sniff) >= 12 && string(sniff[4:8]) == "ftyp" {
		return ".m4a"
	}
	if len(sniff) >= 3 && string(sniff[:3]) == "ID3" {
		return ".mp3"
	}

	if normalizedFallback != "" {
		return normalizedFallback
	}
	return ".mp3"
}

func convertDownloadedAudio(inputPath, outputBasePath, audioFormat string) (string, error) {
	desiredExt := preferredAudioExtension(audioFormat)
	currentExt := normalizeAudioExtension(filepath.Ext(inputPath))
	if desiredExt == "" || currentExt == "" || desiredExt == currentExt {
		return inputPath, nil
	}

	ffmpegInstalled, err := IsFFmpegInstalled()
	if err != nil || !ffmpegInstalled {
		if err != nil {
			return "", fmt.Errorf("downloaded format mismatch (%s -> %s), and ffmpeg is unavailable: %w", currentExt, desiredExt, err)
		}
		return "", fmt.Errorf("downloaded format mismatch (%s -> %s), and ffmpeg is not installed", currentExt, desiredExt)
	}

	ffmpegPath, err := GetFFmpegPath()
	if err != nil {
		return "", fmt.Errorf("failed to get ffmpeg path for conversion: %w", err)
	}

	outputPath := outputBasePath + desiredExt
	tempOutputPath := outputBasePath + ".convert" + desiredExt
	_ = os.Remove(tempOutputPath)

	args := []string{
		"-i", inputPath,
		"-vn",
		"-map", "0:a:0",
		"-y",
	}

	switch desiredExt {
	case ".mp3":
		args = append(args,
			"-codec:a", "libmp3lame",
			"-b:a", "320k",
			"-id3v2_version", "3",
		)
	case ".flac":
		args = append(args,
			"-codec:a", "flac",
		)
	case ".m4a":
		args = append(args,
			"-codec:a", "aac",
			"-b:a", "256k",
			"-movflags", "+faststart",
			"-f", "ipod",
		)
	default:
		return "", fmt.Errorf("unsupported requested conversion target: %s", desiredExt)
	}

	args = append(args, tempOutputPath)

	fmt.Printf("Converting downloaded audio from %s to %s...\n", currentExt, desiredExt)

	cmd := exec.Command(ffmpegPath, args...)
	setHideWindow(cmd)
	output, err := cmd.CombinedOutput()
	if err != nil {
		_ = os.Remove(tempOutputPath)
		return "", fmt.Errorf("ffmpeg conversion failed: %s - %w", strings.TrimSpace(string(output)), err)
	}

	if err := os.Remove(inputPath); err != nil {
		_ = os.Remove(tempOutputPath)
		return "", fmt.Errorf("failed to remove source file after conversion: %w", err)
	}

	if err := MoveFileWithFallback(tempOutputPath, outputPath); err != nil {
		_ = os.Remove(tempOutputPath)
		return "", fmt.Errorf("failed to finalize converted file: %w", err)
	}

	return outputPath, nil
}

func isSessionTokenError(err error) bool {
	var tokenErr *sessionTokenError
	return errors.As(err, &tokenErr)
}

func (s *SpotiDownloader) refreshSessionToken() error {
	fmt.Println("Session token expired, fetching a fresh token...")

	token, err := FetchSessionTokenWithParams(15, 1)
	if err != nil {
		return fmt.Errorf("failed to refresh session token: %w", err)
	}

	s.setSessionToken(token)
	return nil
}

func withTokenRetry[T any](s *SpotiDownloader, fn func() (T, error)) (T, error) {
	result, err := fn()
	if !isSessionTokenError(err) {
		return result, err
	}

	if refreshErr := s.refreshSessionToken(); refreshErr != nil {
		var zero T
		return zero, refreshErr
	}

	return fn()
}

func withTokenRetryVoid(s *SpotiDownloader, fn func() error) error {
	_, err := withTokenRetry(s, func() (struct{}, error) {
		return struct{}{}, fn()
	})
	return err
}

func (s *SpotiDownloader) IsFlacAvailable(trackID string) (bool, error) {
	return withTokenRetry(s, func() (bool, error) {
		return s.isFlacAvailable(trackID)
	})
}

func (s *SpotiDownloader) isFlacAvailable(trackID string) (bool, error) {
	reqBody := FlacAvailableRequest{ID: trackID}
	jsonData, err := json.Marshal(reqBody)
	if err != nil {
		return false, err
	}

	req, err := http.NewRequest("POST", spotidownloaderAPIBase+"/isFlacAvailable", bytes.NewBuffer(jsonData))
	if err != nil {
		return false, err
	}

	req.Header.Set("Authorization", "Bearer "+s.sessionToken)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", "https://spotidownloader.com")
	req.Header.Set("Referer", "https://spotidownloader.com/")

	resp, err := s.doRequestWithNetworkRetry(req)
	if err != nil {
		return false, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return false, statusError(resp.StatusCode, body)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return false, fmt.Errorf("failed to read response body: %w", err)
	}

	if len(body) == 0 {
		return false, fmt.Errorf("API returned empty response")
	}

	var result FlacAvailableResponse
	if err := json.Unmarshal(body, &result); err != nil {

		bodyStr := string(body)
		if len(bodyStr) > 200 {
			bodyStr = bodyStr[:200] + "..."
		}
		return false, fmt.Errorf("failed to decode response: %w (response: %s)", err, bodyStr)
	}

	return result.Available, nil
}

func (s *SpotiDownloader) GetDownloadLink(trackID string) (*DownloadResponse, error) {
	return withTokenRetry(s, func() (*DownloadResponse, error) {
		return s.getDownloadLink(trackID)
	})
}

func (s *SpotiDownloader) getDownloadLink(trackID string) (*DownloadResponse, error) {
	reqBody := DownloadRequest{ID: trackID}
	jsonData, err := json.Marshal(reqBody)
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequest("POST", spotidownloaderAPIBase+"/download", bytes.NewBuffer(jsonData))
	if err != nil {
		return nil, err
	}

	req.Header.Set("Authorization", "Bearer "+s.sessionToken)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", "https://spotidownloader.com")
	req.Header.Set("Referer", "https://spotidownloader.com/")

	resp, err := s.doRequestWithNetworkRetry(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, statusError(resp.StatusCode, body)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response body: %w", err)
	}

	if len(body) == 0 {
		return nil, fmt.Errorf("API returned empty response")
	}

	var result DownloadResponse
	if err := json.Unmarshal(body, &result); err != nil {

		bodyStr := string(body)
		if len(bodyStr) > 200 {
			bodyStr = bodyStr[:200] + "..."
		}
		return nil, fmt.Errorf("failed to decode API response: %w (response: %s)", err, bodyStr)
	}

	if !result.Success {
		return nil, fmt.Errorf("download request failed")
	}

	return &result, nil
}

func (s *SpotiDownloader) DownloadFile(downloadURL, outputBasePath, fallbackExt string) (string, error) {
	return withTokenRetry(s, func() (string, error) {
		return s.downloadFile(downloadURL, outputBasePath, fallbackExt)
	})
}

func (s *SpotiDownloader) downloadFile(downloadURL, outputBasePath, fallbackExt string) (_ string, err error) {
	req, err := http.NewRequest("GET", downloadURL, nil)
	if err != nil {
		return "", err
	}

	req.Header.Set("Authorization", "Bearer "+s.sessionToken)
	req.Header.Set("Referer", "https://spotidownloader.com/")
	req.Header.Set("Origin", "https://spotidownloader.com")

	resp, err := s.doRequestWithNetworkRetry(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		err := statusError(resp.StatusCode, body)
		if isSessionTokenError(err) {
			return "", err
		}
		return "", fmt.Errorf("failed to download file: %w", err)
	}

	reader := bufio.NewReader(resp.Body)
	sniff, _ := reader.Peek(32)
	actualExt := detectAudioExtension(resp.Header.Get("Content-Type"), sniff, fallbackExt)
	outputPath := outputBasePath + actualExt

	dir := filepath.Dir(outputPath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return "", err
	}

	out, err := os.Create(outputPath)
	if err != nil {
		return "", err
	}
	defer func() {
		_ = out.Close()
		if err != nil {
			_ = os.Remove(outputPath)
		}
	}()

	progressWriter := NewProgressWriter(out)

	_, err = io.Copy(progressWriter, reader)

	if err == nil {
		mbDownloaded := float64(progressWriter.GetTotal()) / (1024 * 1024)
		fmt.Printf("\rDownloaded: %.2f MB - Complete\n", mbDownloaded)
	}

	if normalizedFallback := normalizeAudioExtension(fallbackExt); normalizedFallback != "" && normalizedFallback != actualExt {
		fmt.Printf("Detected downloaded audio container %s, saving with that extension instead of %s\n", actualExt, normalizedFallback)
	}

	return outputPath, err
}

func (s *SpotiDownloader) DownloadTrack(
	trackID string,
	outputDir string,
	audioFormat string,
	filenameFormat string,
	includeTrackNumber bool,
	position int,
	trackName string,
	artistName string,
	albumName string,
	albumArtist string,
	releaseDate string,
	coverURL string,
	actualTrackNumber int,
	discNumber int,
	totalTracks int,
	useAlbumTrackNumber bool,
	embedMaxQualityCover bool,
	totalDiscs int,
	copyright string,
	publisher string,
	playlistName string,
	playlistOwner string,
	useFirstArtistOnly bool,
	useSingleGenre bool,
	embedGenre bool,
) (string, error) {

	outputDir = NormalizePath(outputDir)

	metaChan := fetchTaggingMetadataInBackground(trackID, trackName, artistName, albumName, releaseDate, useSingleGenre, embedGenre)

	filenameArtist := artistName
	filenameAlbumArtist := albumArtist
	if useFirstArtistOnly {
		filenameArtist = GetFirstArtist(artistName)
		filenameAlbumArtist = GetFirstArtist(albumArtist)
	}

	resolveDownloadTarget := func() (string, string, error) {
		downloadResp, err := s.getDownloadLink(trackID)
		if err != nil {
			return "", "", err
		}

		if audioFormat == "flac" && downloadResp.LinkFlac != "" {
			return downloadResp.LinkFlac, ".flac", nil
		}

		if downloadResp.Link == "" {
			return "", "", fmt.Errorf("no download link available")
		}
		return downloadResp.Link, ".mp3", nil
	}

	var outputPath string
	var downloadCompleted bool
	for attempt := 0; attempt < 2; attempt++ {
		downloadURL, fileExt, err := resolveDownloadTarget()
		if err != nil {
			if attempt == 0 && isSessionTokenError(err) {
				if refreshErr := s.refreshSessionToken(); refreshErr != nil {
					return "", fmt.Errorf("failed to get download link: %v", refreshErr)
				}
				continue
			}
			return "", fmt.Errorf("failed to get download link: %v", err)
		}

		filenameBase := BuildFilename(trackName, filenameArtist, albumName, filenameAlbumArtist, releaseDate, discNumber, filenameFormat, includeTrackNumber, position, useAlbumTrackNumber, playlistName, playlistOwner)
		filenameBase = SanitizeFilename(filenameBase)
		outputBasePath := filepath.Join(outputDir, filenameBase)

		for _, ext := range PossibleAudioExtensions(audioFormat) {
			candidatePath := outputBasePath + ext
			if fileInfo, statErr := os.Stat(candidatePath); statErr == nil && fileInfo.Size() > 0 {
				fmt.Printf("File already exists: %s (%.2f MB)\n", candidatePath, float64(fileInfo.Size())/(1024*1024))
				return "EXISTS:" + candidatePath, nil
			}
		}

		outputPath, err = s.downloadFile(downloadURL, outputBasePath, fileExt)
		if err != nil {
			if attempt == 0 && isSessionTokenError(err) {
				if refreshErr := s.refreshSessionToken(); refreshErr != nil {
					return "", fmt.Errorf("failed to download file: %v", refreshErr)
				}
				continue
			}
			return "", fmt.Errorf("failed to download file: %v", err)
		}

		outputPath, err = convertDownloadedAudio(outputPath, outputBasePath, audioFormat)
		if err != nil {
			return "", fmt.Errorf("failed to convert downloaded file: %v", err)
		}

		downloadCompleted = true
		break
	}

	if !downloadCompleted {
		return "", fmt.Errorf("failed to download file: retry limit reached")
	}

	var coverPath string
	if coverURL != "" {
		var coverErr error
		coverPath, coverErr = s.downloadCoverImage(coverURL, outputDir, embedMaxQualityCover)
		if coverErr != nil {
			fmt.Printf("Warning: Failed to download cover image: %v\n", coverErr)
			coverPath = ""
		}
	}

	result := <-metaChan
	isrc := strings.TrimSpace(result.ISRC)
	resolvedReleaseDate := strings.TrimSpace(releaseDate)
	if resolvedReleaseDate == "" {
		resolvedReleaseDate = strings.TrimSpace(result.ReleaseDate)
	}
	resolvedGenre := strings.TrimSpace(result.Genre)

	if isrc != "" {
		fmt.Printf("Found ISRC: %s\n", isrc)
	}

	metadata := Metadata{
		Title:       trackName,
		Artist:      artistName,
		Album:       albumName,
		AlbumArtist: albumArtist,
		Date:        resolvedReleaseDate,
		TrackNumber: actualTrackNumber,
		TotalTracks: totalTracks,
		DiscNumber:  discNumber,
		TotalDiscs:  totalDiscs,
		URL:         fmt.Sprintf("https://open.spotify.com/track/%s", trackID),
		Copyright:   copyright,
		Publisher:   publisher,
		Description: "https://github.com/afkarxyz/SpotiDownloader",
		ISRC:        isrc,
		Genre:       resolvedGenre,
	}

	if err := EmbedMetadata(outputPath, metadata, coverPath); err != nil {
		fmt.Printf("Warning: Failed to embed metadata: %v\n", err)
	}

	if finalizeErr := FinalizeTaggedAudioFile(outputPath); finalizeErr != nil {
		fmt.Printf("Warning: Failed to finalize MP3 tag temp file: %v\n", finalizeErr)
	}

	if coverPath != "" {
		os.Remove(coverPath)
	}

	return outputPath, nil
}

func (s *SpotiDownloader) downloadCoverImage(coverURL, outputDir string, embedMaxQualityCover bool) (string, error) {

	coverURL = convertSmallToMedium(coverURL)

	if embedMaxQualityCover {
		coverClient := NewCoverClient()
		coverURL = coverClient.getMaxResolutionURL(coverURL)
	}

	resp, err := s.httpClient.Get(coverURL)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("failed to download cover: status %d", resp.StatusCode)
	}

	_ = outputDir

	out, err := os.CreateTemp("", "spotidownloader-cover-*.jpg")
	if err != nil {
		return "", err
	}
	defer out.Close()

	_, err = io.Copy(out, resp.Body)
	if err != nil {
		return "", err
	}

	return out.Name(), nil
}

func fetchTrackTaggingMetadata(trackID string) (string, string, bool) {
	if strings.TrimSpace(trackID) == "" {
		return "", "", false
	}

	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
	defer cancel()

	client := NewSpotifyMetadataClient()
	data, err := client.GetFilteredData(ctx, fmt.Sprintf("https://open.spotify.com/track/%s", trackID), false, 0)
	if err != nil {
		return "", "", false
	}

	switch resp := data.(type) {
	case TrackResponse:
		return strings.TrimSpace(resp.Track.ReleaseDate), "", false
	case *TrackResponse:
		if resp != nil {
			return strings.TrimSpace(resp.Track.ReleaseDate), "", false
		}
	}

	return "", "", false
}

func fetchGenreFromDeezer(trackID string) (string, error) {
	if strings.TrimSpace(trackID) == "" {
		return "", fmt.Errorf("empty track ID")
	}
	client := NewSongLinkClient()
	genre, err := client.GetGenre(trackID)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(genre), nil
}
