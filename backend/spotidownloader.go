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
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/sync/singleflight"
)

const (
	spotidownloaderAPIBase = "https://api.spotidownloader.com"
)

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
	// refreshToken replaces refreshSessionToken in pipeline retry tests.
	refreshToken func(context.Context) error
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
	TrackNumber int
	TotalTracks int
}

type TaggingMetadataRequest struct {
	TrackID        string
	TrackName      string
	ArtistName     string
	AlbumName      string
	ReleaseDate    string
	TrackNumber    int
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
			switch resp.StatusCode {
			case http.StatusInternalServerError, http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout:
				if attempt == networkRetryAttempts || !canReplayRequest(req) {
					return resp, nil
				}
				_ = resp.Body.Close()
			default:
				return resp, nil
			}
		} else if !isTransientNetworkError(err) || attempt == networkRetryAttempts || !canReplayRequest(req) {
			return nil, err
		}
		finalErr = err

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

	if result.ReleaseDate == "" || req.TrackNumber <= 0 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			track, outcome, err := fetchTrackTaggingMetadataContextWithOutcome(ctx, cache, req.TrackID)
			resultMu.Lock()
			outcomes[spotifyCacheKey(req.TrackID)] = outcome
			resultMu.Unlock()
			if err != nil {
				addWarning("Spotify metadata lookup failed: %v", err)
				return
			}
			resultMu.Lock()
			if result.ReleaseDate == "" {
				result.ReleaseDate = strings.TrimSpace(track.ReleaseDate)
			}
			result.TrackNumber = track.TrackNumber
			result.TotalTracks = track.TotalTracks
			resultMu.Unlock()
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

func isHLSPlaylist(contentType string, sniff []byte) bool {
	return bytes.HasPrefix(bytes.TrimLeft(sniff, "\ufeff \t\r\n"), []byte("#EXTM3U")) ||
		strings.Contains(strings.ToLower(contentType), "mpegurl")
}

func isSpotiDownloaderURL(rawURL string) bool {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return false
	}
	host := strings.ToLower(parsed.Hostname())
	return host == "spotidownloader.com" || strings.HasSuffix(host, ".spotidownloader.com")
}

// hlsDurationSeconds sums the #EXTINF segment durations of a media playlist.
func hlsDurationSeconds(playlist []byte) float64 {
	total := 0.0
	for _, line := range strings.Split(string(playlist), "\n") {
		value, ok := strings.CutPrefix(strings.TrimSpace(line), "#EXTINF:")
		if !ok {
			continue
		}
		value, _, _ = strings.Cut(value, ",")
		if seconds, err := strconv.ParseFloat(strings.TrimSpace(value), 64); err == nil && seconds > 0 {
			total += seconds
		}
	}
	return total
}

// hlsProgressReporter turns FFmpeg "-progress" key=value lines into queue
// item transfer updates.
// shortcut: the total size is extrapolated from output size and stream
// position, so it settles only as the download goes on; exact once it ends.
type hlsProgressReporter struct {
	itemID       string
	totalSeconds float64
	size, outUS  int64
	lastSize     int64
	lastAt       time.Time
}

func (r *hlsProgressReporter) line(line string) {
	key, value, _ := strings.Cut(strings.TrimSpace(line), "=")
	switch key {
	case "total_size":
		if size, err := strconv.ParseInt(value, 10, 64); err == nil {
			r.size = size
		}
	case "out_time_us":
		if outUS, err := strconv.ParseInt(value, 10, 64); err == nil {
			r.outUS = outUS
		}
	case "progress":
		total := int64(0)
		if value == "end" {
			total = r.size
		} else if r.totalSeconds > 0 && r.outUS > 0 {
			total = max(r.size, int64(float64(r.size)*r.totalSeconds*1e6/float64(r.outUS)))
		}
		now := time.Now()
		speed := 0.0
		if elapsed := now.Sub(r.lastAt).Seconds(); !r.lastAt.IsZero() && elapsed > 0 {
			speed = float64(r.size-r.lastSize) / float64(bytesPerMegabyte) / elapsed
		}
		r.lastSize, r.lastAt = r.size, now
		UpdateItemTransfer(r.itemID, r.size, total, max(speed, 0))
	}
}

// ffmpegHTTPProxy returns the proxy our own HTTP client uses for rawURL. A child
// FFmpeg only reads proxy environment variables, which a GUI app started from
// the desktop does not have, so it would bypass the Windows system proxy.
// shortcut: FFmpeg tunnels only through HTTP proxies; a SOCKS-only proxy is skipped.
func ffmpegHTTPProxy(rawURL string) string {
	req, err := http.NewRequest(http.MethodGet, rawURL, nil)
	if err != nil {
		return ""
	}
	proxyURL, err := proxyFromEnvironmentOrSystem(req)
	if err != nil || proxyURL == nil || proxyURL.Scheme != "http" {
		return ""
	}
	return proxyURL.String()
}

// fetchHLSAudio lets FFmpeg fetch every segment of an HLS stream and remux the
// audio, without re-encoding, into one .m4a file.
func fetchHLSAudio(ctx context.Context, playlistURL string, totalSeconds float64, outputBasePath, itemID string) (string, error) {
	ffmpegPath, err := GetFFmpegPathContext(ctx)
	if err != nil {
		return "", fmt.Errorf("track is an HLS stream and FFmpeg is required to download it: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(outputBasePath), 0755); err != nil {
		return "", err
	}
	outputPath := outputBasePath + ".m4a"
	tempPath := outputBasePath + ".hls.m4a"
	_ = os.Remove(tempPath)
	args := []string{"-nostdin", "-v", "error", "-nostats", "-progress", "pipe:1", "-y",
		// Without it a stalled CDN connection blocks the track forever.
		"-rw_timeout", "30000000"}
	if proxy := ffmpegHTTPProxy(playlistURL); proxy != "" {
		args = append(args, "-http_proxy", proxy)
	}
	args = append(args, "-i", playlistURL,
		"-vn", "-map", "0:a:0", "-c:a", "copy",
		"-movflags", "+faststart", "-f", "ipod",
		tempPath)
	cmd := newManagedCommand(ctx, ffmpegPath, args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	stdout, err := cmd.StdoutPipe()
	if err == nil {
		err = cmd.Start()
	}
	if err == nil {
		reporter := hlsProgressReporter{itemID: itemID, totalSeconds: totalSeconds}
		scanner := bufio.NewScanner(stdout)
		for scanner.Scan() {
			if itemID != "" {
				reporter.line(scanner.Text())
			}
		}
		err = cmd.Wait()
	}
	if err != nil {
		_ = os.Remove(tempPath)
		if ctxErr := ctx.Err(); ctxErr != nil {
			return "", ctxErr
		}
		return "", fmt.Errorf("HLS download failed: %s - %w", strings.TrimSpace(stderr.String()), err)
	}
	if err := MoveFileWithFallback(tempPath, outputPath); err != nil {
		_ = os.Remove(tempPath)
		return "", err
	}
	if err := ctx.Err(); err != nil {
		_ = os.Remove(outputPath)
		return "", err
	}
	return outputPath, nil
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

	ffmpegPath, err := GetFFmpegPathContext(ctx)
	if err != nil {
		return "", fmt.Errorf("downloaded format mismatch (%s -> %s), and ffmpeg is unavailable: %w", currentExt, desiredExt, err)
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

var (
	sessionTokenRefresh singleflight.Group
	fetchSessionToken   = FetchSessionTokenWithParamsContext
)

// refreshSessionToken replaces a token the API rejected. Concurrent workers
// share one helper run (it starts a browser), and a worker whose request still
// carried a token another worker already replaced just adopts the new one.
func (s *SpotiDownloader) refreshSessionToken(ctx context.Context) error {
	if s.refreshToken != nil {
		return s.refreshToken(ctx)
	}
	if current := getPreferredSessionToken(""); current != "" && current != s.sessionToken {
		s.sessionToken = current
		return nil
	}
	ch := sessionTokenRefresh.DoChan("session-token", func() (any, error) {
		fmt.Println("Session token expired, fetching a fresh token...")
		// Detached from this worker's cancellation because other workers wait
		// on the same run; the helper has its own timeout.
		token, err := fetchSessionToken(context.WithoutCancel(ctx), 15, 1)
		if err == nil {
			RememberSessionToken(token)
		}
		return token, err
	})
	select {
	case <-ctx.Done():
		return ctx.Err()
	case res := <-ch:
		if res.Err != nil {
			return fmt.Errorf("failed to refresh session token: %w", res.Err)
		}
		s.sessionToken = res.Val.(string)
		return nil
	}
}

// withSessionToken runs call and, if the API rejected the session token,
// refreshes it once and runs call again.
func withSessionToken[T any](ctx context.Context, s *SpotiDownloader, call func() (T, error)) (T, error) {
	result, err := call()
	if !isSessionTokenError(err) {
		return result, err
	}
	var zero T
	if ctxErr := ctx.Err(); ctxErr != nil {
		return zero, ctxErr
	}
	if refreshErr := s.refreshSessionToken(ctx); refreshErr != nil {
		return zero, refreshErr
	}
	return call()
}

func (s *SpotiDownloader) getDownloadLink(ctx context.Context, trackID string) (*DownloadResponse, error) {
	return withSessionToken(ctx, s, func() (*DownloadResponse, error) { return s.requestDownloadLink(ctx, trackID) })
}

func (s *SpotiDownloader) requestDownloadLink(ctx context.Context, trackID string) (*DownloadResponse, error) {
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
		return nil, fmt.Errorf("download request failed: %s", formatStatusBody(body))
	}

	return &result, nil
}

// fetchAudio streams one audio file, reporting progress against its queue item.
func (s *SpotiDownloader) fetchAudio(ctx context.Context, downloadURL, outputBasePath, fallbackExt, itemID string) (_ string, err error) {
	if ctx == nil {
		ctx = context.Background()
	}
	req, err := newHTTPRequestWithContext(ctx, "GET", downloadURL, nil)
	if err != nil {
		return "", err
	}

	// Links often point at third-party CDNs; an Azure-backed one rejects the
	// whole request when it carries our session token.
	apiHost := isSpotiDownloaderURL(downloadURL)
	if apiHost {
		req.Header.Set("Authorization", "Bearer "+s.sessionToken)
	}
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
		if !apiHost {
			// A CDN never saw our token, so its rejection must not trigger a token refresh.
			return "", fmt.Errorf("failed to download file: %v", err)
		}
		if isSessionTokenError(err) {
			return "", err
		}
		return "", fmt.Errorf("failed to download file: %w", err)
	}

	reader := bufio.NewReader(resp.Body)
	sniff, _ := reader.Peek(32)
	if isHLSPlaylist(resp.Header.Get("Content-Type"), sniff) {
		// Some tracks are served as an HLS stream (SoundCloud AAC) instead of a file.
		playlist, _ := io.ReadAll(io.LimitReader(reader, 1<<20))
		_ = resp.Body.Close()
		return fetchHLSAudio(ctx, downloadURL, hlsDurationSeconds(playlist), outputBasePath, itemID)
	}
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

func (s *SpotiDownloader) downloadAudio(ctx context.Context, downloadURL, outputBasePath, fallbackExt, itemID string) (string, error) {
	return withSessionToken(ctx, s, func() (string, error) {
		return s.fetchAudio(ctx, downloadURL, outputBasePath, fallbackExt, itemID)
	})
}

// downloadCover stages artwork in outputDir. Callers pass a runtime temp
// directory so a cover never leaks into the music folder.
func (s *SpotiDownloader) downloadCover(ctx context.Context, coverURL, outputDir string, embedMaxQualityCover bool) (coverPath string, err error) {
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
	track, _, err := fetchTrackTaggingMetadataContextWithOutcome(ctx, cache, trackID)
	return strings.TrimSpace(track.ReleaseDate), err
}

func fetchTrackTaggingMetadataContextWithOutcome(ctx context.Context, cache *MetadataCache, trackID string) (TrackMetadata, CacheOutcome, error) {
	if strings.TrimSpace(trackID) == "" {
		return TrackMetadata{}, CacheMiss, fmt.Errorf("empty track ID")
	}

	client := NewSpotifyMetadataClient()
	data, outcome, err := client.GetFilteredDataWithCacheOutcome(ctx, cache, fmt.Sprintf("https://open.spotify.com/track/%s", trackID), false, 0)
	if err != nil {
		return TrackMetadata{}, outcome, err
	}

	switch resp := data.(type) {
	case TrackResponse:
		return resp.Track, outcome, nil
	case *TrackResponse:
		if resp != nil {
			return resp.Track, outcome, nil
		}
	}

	return TrackMetadata{}, outcome, fmt.Errorf("Spotify metadata response did not contain track data")
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
