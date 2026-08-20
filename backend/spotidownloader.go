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
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	spotidownloaderAPIBase = "https://api.spotidownloader.com"
)

var finalizeTaggedAudioFileForDownload = FinalizeTaggedAudioFile

const (
	networkRetryAttempts = 3
	networkRetryDelay    = 750 * time.Millisecond
	networkRetryMaxDelay = 3 * time.Second
	trackMetadataTTL     = 7 * 24 * time.Hour
	artistGenreTTL       = 30 * 24 * time.Hour
)

func spotifyCacheKey(trackID string) string      { return "spotify:" + trackID }
func isrcCacheKey(trackID string) string         { return "isrc:" + trackID }
func chosicTrackCacheKey(trackID string) string  { return "chosic-track:" + trackID }
func artistGenreCacheKey(artistID string) string { return "genre:" + artistID }

func trimGenreList(genres []string) []string {
	trimmed := make([]string, 0, len(genres))
	seen := make(map[string]struct{}, len(genres))
	for _, genre := range genres {
		genre = strings.TrimSpace(genre)
		if genre == "" {
			continue
		}
		key := strings.ToLower(genre)
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		trimmed = append(trimmed, genre)
	}
	return trimmed
}

func cachedMetadataLookup[T any](ctx context.Context, cache *MetadataCache, key string, ttl time.Duration, cacheable func(T) bool, load func(context.Context) (T, error)) (T, CacheOutcome, error) {
	if cache == nil {
		value, err := load(ctx)
		return value, CacheMiss, err
	}
	return CachedLookup(ctx, cache, key, ttl, cacheable, load)
}

type SpotiDownloader struct {
	sessionToken  string
	httpClient    *http.Client
	metadataCache *MetadataCache
	// pipelineDeps is a package-internal seam for deterministic backend tests.
	// Production instances leave it zero-valued and use the defaults in
	// track_pipeline.go.
	pipelineDeps      trackPipelineDeps
	trackPipelineDeps trackPipelineDeps
	trackDeps         trackPipelineDeps
	// refreshToken is a per-instance seam for deterministic pipeline retry
	// tests. Production instances use refreshSessionToken below.
	refreshToken func() error
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

type TaggingMetadataRequest struct {
	TrackID        string
	TrackName      string
	ArtistName     string
	AlbumName      string
	ReleaseDate    string
	UseSingleGenre bool
	EmbedGenre     bool
}

type cachedISRCMetadata struct {
	ISRC    string
	Genres  []string
	Warning string
}

var (
	metadataHTTPClientFactory = func(timeout time.Duration) *http.Client {
		return newHTTPClient(timeout)
	}
	metadataSongLinkClientFactory = func() *SongLinkClient {
		return NewSongLinkClient()
	}
	metadataChosicClientFactory = func() (*chosicClient, error) {
		return newChosicClient()
	}
)

func NewSpotiDownloader(sessionToken string) *SpotiDownloader {
	preferredToken := getPreferredSessionToken(sessionToken)
	if preferredToken != "" {
		RememberSessionToken(preferredToken)
	}

	return &SpotiDownloader{
		sessionToken:  preferredToken,
		httpClient:    newHTTPClient(60 * time.Second),
		metadataCache: CurrentMetadataCache(),
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
	if req == nil {
		return nil, fmt.Errorf("nil HTTP request")
	}
	ctx := req.Context()
	if ctx == nil {
		ctx = context.Background()
	}

	delay := networkRetryDelay
	var finalErr error
	for attempt := 1; attempt <= networkRetryAttempts; attempt++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}

		if attempt > 1 && req.GetBody != nil {
			body, err := req.GetBody()
			if err != nil {
				return nil, err
			}
			req.Body = body
		}

		resp, err := s.httpClient.Do(req)
		if err == nil {
			return resp, nil
		}
		finalErr = err
		if !isTransientNetworkError(err) || attempt == networkRetryAttempts {
			return nil, err
		}

		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			return nil, ctx.Err()
		case <-timer.C:
		}
		if networkRetryMaxDelay > 0 && delay < networkRetryMaxDelay {
			delay *= 2
			if delay > networkRetryMaxDelay {
				delay = networkRetryMaxDelay
			}
		}
	}

	return nil, finalErr
}

func fetchTaggingMetadata(ctx context.Context, cache *MetadataCache, req TaggingMetadataRequest) (taggingMetadataResult, []string) {
	result, warnings, _ := fetchTaggingMetadataWithOutcomes(ctx, cache, req)
	return result, warnings
}

func fetchTaggingMetadataWithOutcomes(ctx context.Context, cache *MetadataCache, req TaggingMetadataRequest) (taggingMetadataResult, []string, map[string]CacheOutcome) {
	if ctx == nil {
		ctx = context.Background()
	}

	result := taggingMetadataResult{ReleaseDate: strings.TrimSpace(req.ReleaseDate)}
	trackID := strings.TrimSpace(req.TrackID)
	if trackID == "" {
		return result, nil, map[string]CacheOutcome{}
	}
	req.TrackID = trackID

	warnings := make([]string, 0, 4)
	outcomes := make(map[string]CacheOutcome, 3)
	var warningMu sync.Mutex
	addWarning := func(format string, args ...interface{}) {
		warningMu.Lock()
		warnings = append(warnings, fmt.Sprintf(format, args...))
		warningMu.Unlock()
	}

	var wg sync.WaitGroup
	var resultMu sync.Mutex
	var musicBrainzGenre string
	var fallbackGenre string

	if result.ReleaseDate == "" {
		wg.Add(1)
		go func() {
			defer wg.Done()
			releaseDate, err := fetchTrackTaggingMetadataContext(ctx, cache, req.TrackID)
			if err != nil {
				addWarning("Spotify metadata lookup failed: %v", err)
				return
			}
			if releaseDate != "" {
				resultMu.Lock()
				result.ReleaseDate = strings.TrimSpace(releaseDate)
				resultMu.Unlock()
			}
		}()
	}

	if req.EmbedGenre {
		wg.Add(1)
		go func() {
			defer wg.Done()
			key := isrcCacheKey(req.TrackID)
			cached, outcome, err := cachedMetadataLookup(ctx, cache, key, trackMetadataTTL,
				func(value cachedISRCMetadata) bool {
					return strings.TrimSpace(value.ISRC) != "" && len(trimGenreList(value.Genres)) > 0
				},
				func(loadCtx context.Context) (cachedISRCMetadata, error) {
					client := metadataSongLinkClientFactory()
					isrc, lookupErr := client.GetISRCContext(loadCtx, req.TrackID)
					isrc = strings.TrimSpace(isrc)
					if lookupErr != nil || isrc == "" {
						if lookupErr == nil {
							lookupErr = fmt.Errorf("empty ISRC")
						}
						return cachedISRCMetadata{}, lookupErr
					}

					genres, musicBrainzErr := fetchMusicBrainzGenresContext(loadCtx, isrc, req.TrackName, req.ArtistName, req.AlbumName)
					if musicBrainzErr != nil {
						return cachedISRCMetadata{ISRC: isrc, Warning: musicBrainzErr.Error()}, nil
					}
					return cachedISRCMetadata{ISRC: isrc, Genres: trimGenreList(genres)}, nil
				})
			resultMu.Lock()
			outcomes[key] = outcome
			resultMu.Unlock()
			if err != nil {
				addWarning("ISRC/MusicBrainz lookup failed: %v", err)
				return
			}
			resultMu.Lock()
			result.ISRC = strings.TrimSpace(cached.ISRC)
			genres := trimGenreList(cached.Genres)
			if len(genres) > 0 {
				if req.UseSingleGenre {
					musicBrainzGenre = genres[0]
				} else {
					musicBrainzGenre = strings.Join(genres, ", ")
				}
			}
			resultMu.Unlock()
			if strings.TrimSpace(cached.Warning) != "" {
				addWarning("MusicBrainz lookup failed: %s", strings.TrimSpace(cached.Warning))
			}
		}()

		wg.Add(1)
		go func() {
			defer wg.Done()
			key := chosicTrackCacheKey(req.TrackID)
			artistID, outcome, err := cachedMetadataLookup(ctx, cache, key, trackMetadataTTL,
				func(value string) bool { return strings.TrimSpace(value) != "" },
				func(loadCtx context.Context) (string, error) {
					client, clientErr := metadataChosicClientFactory()
					if clientErr != nil {
						return "", clientErr
					}
					return client.fetchArtistIDContext(loadCtx, req.TrackID)
				})
			resultMu.Lock()
			outcomes[key] = outcome
			resultMu.Unlock()
			if err != nil {
				addWarning("Chosic track lookup failed: %v", err)
				return
			}
			artistID = strings.TrimSpace(artistID)
			if artistID == "" {
				return
			}

			genreKey := artistGenreCacheKey(artistID)
			genres, outcome, err := cachedMetadataLookup(ctx, cache, genreKey, artistGenreTTL,
				func(value []string) bool { return len(trimGenreList(value)) > 0 },
				func(loadCtx context.Context) ([]string, error) {
					client, clientErr := metadataChosicClientFactory()
					if clientErr != nil {
						return nil, clientErr
					}
					return client.fetchArtistGenresContext(loadCtx, artistID)
				})
			resultMu.Lock()
			outcomes[genreKey] = outcome
			resultMu.Unlock()
			if err == nil {
				genres = trimGenreList(genres)
			}
			if err != nil || len(genres) == 0 {
				if err != nil {
					addWarning("Chosic artist lookup failed: %v", err)
				}
				if deezerGenre, deezerErr := fetchGenreFromDeezerContext(ctx, req.TrackID); deezerErr == nil {
					resultMu.Lock()
					fallbackGenre = strings.TrimSpace(deezerGenre)
					resultMu.Unlock()
				}
				return
			}

			genre := strings.TrimSpace(genres[0])
			if !req.UseSingleGenre {
				genre = strings.TrimSpace(strings.Join(genres, ", "))
			}
			resultMu.Lock()
			fallbackGenre = genre
			resultMu.Unlock()
		}()
	}

	wg.Wait()
	resultMu.Lock()
	if musicBrainzGenre != "" {
		result.Genre = musicBrainzGenre
	} else {
		result.Genre = strings.TrimSpace(fallbackGenre)
	}
	resultMu.Unlock()
	sort.Strings(warnings)
	return result, warnings, outcomes
}

func fetchTaggingMetadataInBackground(trackID, trackName, artistName, albumName, releaseDate string, useSingleGenre, embedGenre bool) <-chan taggingMetadataResult {
	resultCh := make(chan taggingMetadataResult, 1)
	go func() {
		defer close(resultCh)
		result, _ := fetchTaggingMetadata(context.Background(), CurrentMetadataCache(), TaggingMetadataRequest{
			TrackID: trackID, TrackName: trackName, ArtistName: artistName, AlbumName: albumName,
			ReleaseDate: releaseDate, UseSingleGenre: useSingleGenre, EmbedGenre: embedGenre,
		})
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
	return convertDownloadedAudioContext(context.Background(), inputPath, outputBasePath, audioFormat)
}

func convertDownloadedAudioContext(ctx context.Context, inputPath, outputBasePath, audioFormat string) (string, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	desiredExt := preferredAudioExtension(audioFormat)
	currentExt := normalizeAudioExtension(filepath.Ext(inputPath))
	if desiredExt == "" || currentExt == "" || desiredExt == currentExt {
		return inputPath, nil
	}

	ffmpegInstalled, err := IsFFmpegInstalledContext(ctx)
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
	defer func() {
		if ctx.Err() != nil {
			_ = os.Remove(inputPath)
			_ = os.Remove(tempOutputPath)
			_ = os.Remove(outputPath)
		}
	}()
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

	cmd := newManagedCommand(ctx, ffmpegPath, args...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		_ = os.Remove(tempOutputPath)
		if ctxErr := ctx.Err(); ctxErr != nil {
			return "", ctxErr
		}
		return "", fmt.Errorf("ffmpeg conversion failed: %s - %w", strings.TrimSpace(string(output)), err)
	}
	if err := ctx.Err(); err != nil {
		_ = os.Remove(tempOutputPath)
		return "", err
	}

	if err := os.Remove(inputPath); err != nil {
		_ = os.Remove(tempOutputPath)
		return "", fmt.Errorf("failed to remove source file after conversion: %w", err)
	}

	if err := MoveFileWithFallback(tempOutputPath, outputPath); err != nil {
		_ = os.Remove(tempOutputPath)
		return "", fmt.Errorf("failed to finalize converted file: %w", err)
	}
	if err := ctx.Err(); err != nil {
		_ = os.Remove(outputPath)
		return "", err
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

func (s *SpotiDownloader) refreshSessionTokenForPipeline() error {
	if s != nil && s.refreshToken != nil {
		return s.refreshToken()
	}
	return s.refreshSessionToken()
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

func (s *SpotiDownloader) IsFlacAvailable(trackID string) (bool, error) {
	return withTokenRetry(s, func() (bool, error) {
		return s.isFlacAvailableContext(context.Background(), trackID)
	})
}

func (s *SpotiDownloader) isFlacAvailable(trackID string) (bool, error) {
	return s.isFlacAvailableContext(context.Background(), trackID)
}

func (s *SpotiDownloader) IsFlacAvailableContext(ctx context.Context, trackID string) (bool, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	result, err := s.isFlacAvailableContext(ctx, trackID)
	if !isSessionTokenError(err) {
		return result, err
	}
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if refreshErr := s.refreshSessionToken(); refreshErr != nil {
		return false, refreshErr
	}
	return s.isFlacAvailableContext(ctx, trackID)
}

func (s *SpotiDownloader) isFlacAvailableContext(ctx context.Context, trackID string) (bool, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	reqBody := FlacAvailableRequest{ID: trackID}
	jsonData, err := json.Marshal(reqBody)
	if err != nil {
		return false, err
	}

	req, err := newHTTPRequestWithContext(ctx, "POST", spotidownloaderAPIBase+"/isFlacAvailable", bytes.NewBuffer(jsonData))
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
		return s.getDownloadLinkContext(context.Background(), trackID)
	})
}

func (s *SpotiDownloader) getDownloadLink(trackID string) (*DownloadResponse, error) {
	return s.getDownloadLinkContext(context.Background(), trackID)
}

func (s *SpotiDownloader) GetDownloadLinkContext(ctx context.Context, trackID string) (*DownloadResponse, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	result, err := s.getDownloadLinkContext(ctx, trackID)
	if !isSessionTokenError(err) {
		return result, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if refreshErr := s.refreshSessionToken(); refreshErr != nil {
		return nil, refreshErr
	}
	return s.getDownloadLinkContext(ctx, trackID)
}

func (s *SpotiDownloader) getDownloadLinkContextWithRetry(ctx context.Context, trackID string) (*DownloadResponse, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	result, err := s.getDownloadLinkContext(ctx, trackID)
	if !isSessionTokenError(err) {
		return result, err
	}
	if ctxErr := ctx.Err(); ctxErr != nil {
		return nil, ctxErr
	}
	if refreshErr := s.refreshSessionTokenForPipeline(); refreshErr != nil {
		return nil, refreshErr
	}
	return s.getDownloadLinkContext(ctx, trackID)
}

func (s *SpotiDownloader) getDownloadLinkContext(ctx context.Context, trackID string) (*DownloadResponse, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	reqBody := DownloadRequest{ID: trackID}
	jsonData, err := json.Marshal(reqBody)
	if err != nil {
		return nil, err
	}

	req, err := newHTTPRequestWithContext(ctx, "POST", spotidownloaderAPIBase+"/download", bytes.NewBuffer(jsonData))
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
		return s.downloadFileContext(context.Background(), downloadURL, outputBasePath, fallbackExt)
	})
}

func (s *SpotiDownloader) downloadFile(downloadURL, outputBasePath, fallbackExt string) (_ string, err error) {
	return s.downloadFileContext(context.Background(), downloadURL, outputBasePath, fallbackExt)
}

func (s *SpotiDownloader) DownloadFileContext(ctx context.Context, downloadURL, outputBasePath, fallbackExt string) (string, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	result, err := s.downloadFileContext(ctx, downloadURL, outputBasePath, fallbackExt)
	if !isSessionTokenError(err) {
		return result, err
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if refreshErr := s.refreshSessionToken(); refreshErr != nil {
		return "", refreshErr
	}
	return s.downloadFileContext(ctx, downloadURL, outputBasePath, fallbackExt)
}

func (s *SpotiDownloader) downloadFileContext(ctx context.Context, downloadURL, outputBasePath, fallbackExt string) (_ string, err error) {
	return s.downloadFileContextWithItemID(ctx, downloadURL, outputBasePath, fallbackExt, "")
}

// downloadFileContextWithItemID is the track-scoped variant used by the
// concurrent pipeline. Keeping the legacy wrapper above preserves the old
// 25-argument DownloadTrack contract while allowing each pipeline to report
// transfer progress against its own queue item.
func (s *SpotiDownloader) downloadFileContextWithItemID(ctx context.Context, downloadURL, outputBasePath, fallbackExt, itemID string) (_ string, err error) {
	if ctx == nil {
		ctx = context.Background()
	}
	req, err := newHTTPRequestWithContext(ctx, "GET", downloadURL, nil)
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
		if ctxErr := ctx.Err(); ctxErr != nil {
			return "", ctxErr
		}
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
		if err != nil || ctx.Err() != nil {
			_ = os.Remove(outputPath)
		}
	}()

	var progressWriter *ProgressWriter
	if itemID != "" {
		if resp.ContentLength >= 0 {
			progressWriter = NewProgressWriterWithID(out, itemID, resp.ContentLength)
		} else {
			progressWriter = NewProgressWriterWithID(out, itemID)
		}
	} else {
		progressWriter = NewProgressWriter(out, resp.ContentLength)
	}

	_, err = io.Copy(progressWriter, reader)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return "", ctxErr
		}
	}
	if ctxErr := ctx.Err(); ctxErr != nil {
		return "", ctxErr
	}

	if err == nil {
		mbDownloaded := float64(progressWriter.GetTotal()) / (1024 * 1024)
		fmt.Printf("\rDownloaded: %.2f MB - Complete\n", mbDownloaded)
	}

	if normalizedFallback := normalizeAudioExtension(fallbackExt); normalizedFallback != "" && normalizedFallback != actualExt {
		fmt.Printf("Detected downloaded audio container %s, saving with that extension instead of %s\n", actualExt, normalizedFallback)
	}

	return outputPath, err
}

func (s *SpotiDownloader) downloadFileContextWithItemIDRetry(ctx context.Context, downloadURL, outputBasePath, fallbackExt, itemID string) (string, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	result, err := s.downloadFileContextWithItemID(ctx, downloadURL, outputBasePath, fallbackExt, itemID)
	if !isSessionTokenError(err) {
		return result, err
	}
	if ctxErr := ctx.Err(); ctxErr != nil {
		return "", ctxErr
	}
	if refreshErr := s.refreshSessionTokenForPipeline(); refreshErr != nil {
		return "", refreshErr
	}
	return s.downloadFileContextWithItemID(ctx, downloadURL, outputBasePath, fallbackExt, itemID)
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
	return s.DownloadTrackContext(context.Background(), trackID, outputDir, audioFormat, filenameFormat,
		includeTrackNumber, position, trackName, artistName, albumName, albumArtist, releaseDate, coverURL,
		actualTrackNumber, discNumber, totalTracks, useAlbumTrackNumber, embedMaxQualityCover, totalDiscs,
		copyright, publisher, playlistName, playlistOwner, useFirstArtistOnly, useSingleGenre, embedGenre)
}

func (s *SpotiDownloader) DownloadTrackContext(
	ctx context.Context,
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
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}

	outputDir = NormalizePath(outputDir)

	metaChan := make(chan taggingMetadataResult, 1)
	go func() {
		result, warnings := fetchTaggingMetadata(ctx, s.metadataCache, TaggingMetadataRequest{
			TrackID: trackID, TrackName: trackName, ArtistName: artistName, AlbumName: albumName,
			ReleaseDate: releaseDate, UseSingleGenre: useSingleGenre, EmbedGenre: embedGenre,
		})
		for _, warning := range warnings {
			fmt.Printf("Warning: %s\n", warning)
		}
		metaChan <- result
	}()

	filenameArtist := artistName
	filenameAlbumArtist := albumArtist
	if useFirstArtistOnly {
		filenameArtist = GetFirstArtist(artistName)
		filenameAlbumArtist = GetFirstArtist(albumArtist)
	}

	resolveDownloadTarget := func() (string, string, error) {
		downloadResp, err := s.getDownloadLinkContext(ctx, trackID)
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
	var coverPath string
	outputCommitted := false
	defer func() {
		if ctx.Err() != nil && !outputCommitted {
			if outputPath != "" {
				_ = os.Remove(outputPath)
			}
			if coverPath != "" {
				_ = os.Remove(coverPath)
			}
		}
	}()
	var downloadCompleted bool
	for attempt := 0; attempt < 2; attempt++ {
		downloadURL, fileExt, err := resolveDownloadTarget()
		if err != nil {
			if ctxErr := ctx.Err(); ctxErr != nil {
				return "", ctxErr
			}
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

		outputPath, err = s.downloadFileContext(ctx, downloadURL, outputBasePath, fileExt)
		if err != nil {
			if ctxErr := ctx.Err(); ctxErr != nil {
				return "", ctxErr
			}
			if attempt == 0 && isSessionTokenError(err) {
				if refreshErr := s.refreshSessionToken(); refreshErr != nil {
					return "", fmt.Errorf("failed to download file: %v", refreshErr)
				}
				continue
			}
			return "", fmt.Errorf("failed to download file: %v", err)
		}

		outputPath, err = convertDownloadedAudioContext(ctx, outputPath, outputBasePath, audioFormat)
		if err != nil {
			if ctxErr := ctx.Err(); ctxErr != nil {
				return "", ctxErr
			}
			return "", fmt.Errorf("failed to convert downloaded file: %v", err)
		}

		downloadCompleted = true
		break
	}

	if !downloadCompleted {
		return "", fmt.Errorf("failed to download file: retry limit reached")
	}

	if coverURL != "" {
		var coverErr error
		coverPath, coverErr = s.downloadCoverImageContext(ctx, coverURL, outputDir, embedMaxQualityCover)
		if coverErr != nil {
			if ctxErr := ctx.Err(); ctxErr != nil {
				return "", ctxErr
			}
			fmt.Printf("Warning: Failed to download cover image: %v\n", coverErr)
			coverPath = ""
		}
	}

	var result taggingMetadataResult
	select {
	case result = <-metaChan:
	case <-ctx.Done():
		return "", ctx.Err()
	}
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

	if err := EmbedMetadataContext(ctx, outputPath, metadata, coverPath); err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return "", ctxErr
		}
		fmt.Printf("Warning: Failed to embed metadata: %v\n", err)
	}

	if finalizeErr := finalizeTaggedAudioFileForDownload(outputPath); finalizeErr != nil {
		fmt.Printf("Warning: Failed to finalize MP3 tag temp file: %v\n", finalizeErr)
	} else {
		outputCommitted = true
	}

	if coverPath != "" {
		os.Remove(coverPath)
	}

	return outputPath, nil
}

func (s *SpotiDownloader) downloadCoverImage(coverURL, outputDir string, embedMaxQualityCover bool) (string, error) {
	return s.downloadCoverImageContext(context.Background(), coverURL, outputDir, embedMaxQualityCover)
}

func (s *SpotiDownloader) downloadCoverImageContext(ctx context.Context, coverURL, outputDir string, embedMaxQualityCover bool) (coverPath string, err error) {
	_ = outputDir
	paths, pathErr := CurrentRuntimePaths()
	if pathErr != nil {
		return "", pathErr
	}
	return s.downloadCoverImageContextToDir(ctx, coverURL, paths.RunTempDir, embedMaxQualityCover, "")
}

// downloadCoverImageContextToDir stages artwork in outputDir. The pipeline
// passes its per-item runtime directory here so a cover can never leak into a
// process-wide or user-profile temp directory.
func (s *SpotiDownloader) downloadCoverImageContextToDir(ctx context.Context, coverURL, outputDir string, embedMaxQualityCover bool, _ string) (coverPath string, err error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}

	coverURL = convertSmallToMedium(coverURL)

	if embedMaxQualityCover {
		coverClient := NewCoverClient()
		coverURL = coverClient.getMaxResolutionURL(coverURL)
	}

	req, err := newHTTPRequestWithContext(ctx, http.MethodGet, coverURL, nil)
	if err != nil {
		return "", err
	}
	httpClient := s.httpClient
	if httpClient == nil {
		httpClient = newHTTPClient(60 * time.Second)
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return "", ctxErr
		}
		return "", fmt.Errorf("failed to download cover: status %d", resp.StatusCode)
	}

	_ = outputDir

	if strings.TrimSpace(outputDir) == "" {
		return "", fmt.Errorf("cover output directory is empty")
	}
	if err := os.MkdirAll(outputDir, 0755); err != nil {
		return "", err
	}
	out, err := os.CreateTemp(outputDir, "spotidownloader-cover-*.jpg")
	if err != nil {
		return "", err
	}
	defer func() {
		_ = out.Close()
		if err != nil || ctx.Err() != nil {
			_ = os.Remove(out.Name())
		}
	}()

	_, err = io.Copy(out, resp.Body)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return "", ctxErr
		}
		return "", err
	}
	if ctxErr := ctx.Err(); ctxErr != nil {
		return "", ctxErr
	}

	return out.Name(), nil
}

func fetchTrackTaggingMetadata(trackID string) (string, string, bool) {
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
	defer cancel()
	releaseDate, err := fetchTrackTaggingMetadataContext(ctx, CurrentMetadataCache(), trackID)
	if err != nil {
		return "", "", false
	}
	return releaseDate, "", false
}

func fetchTrackTaggingMetadataContext(ctx context.Context, cache *MetadataCache, trackID string) (string, error) {
	if strings.TrimSpace(trackID) == "" {
		return "", fmt.Errorf("empty track ID")
	}

	client := NewSpotifyMetadataClient()
	data, err := client.GetFilteredDataWithCache(ctx, cache, fmt.Sprintf("https://open.spotify.com/track/%s", trackID), false, 0)
	if err != nil {
		return "", err
	}

	switch resp := data.(type) {
	case TrackResponse:
		return strings.TrimSpace(resp.Track.ReleaseDate), nil
	case *TrackResponse:
		if resp != nil {
			return strings.TrimSpace(resp.Track.ReleaseDate), nil
		}
	}

	return "", fmt.Errorf("Spotify metadata response did not contain track data")
}

func fetchGenreFromDeezer(trackID string) (string, error) {
	return fetchGenreFromDeezerContext(context.Background(), trackID)
}

func fetchGenreFromDeezerContext(ctx context.Context, trackID string) (string, error) {
	if strings.TrimSpace(trackID) == "" {
		return "", fmt.Errorf("empty track ID")
	}
	client := metadataSongLinkClientFactory()
	genre, err := client.GetGenreContext(ctx, trackID)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(genre), nil
}
