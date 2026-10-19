package backend

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type SongLinkClient struct {
	client           *http.Client
	minCallDelay     time.Duration
	lastAPICallTime  time.Time
	apiCallCount     int
	apiCallResetTime time.Time
}

const (
	songLinkMaxCallsPerMinute = 9
	songLinkMinCallDelay      = 7 * time.Second
	songLinkRetryDelay        = 15 * time.Second
	songLinkMaxRetries        = 3
)

type songLinkLinksResponse struct {
	LinksByPlatform map[string]struct {
		URL string `json:"url"`
	} `json:"linksByPlatform"`
}

type SongLinkURLs struct {
	TidalURL  string `json:"tidal_url"`
	AmazonURL string `json:"amazon_url"`
	ISRC      string `json:"isrc"`
}

type TrackAvailability struct {
	SpotifyID string `json:"spotify_id"`
	Tidal     bool   `json:"tidal"`
	Amazon    bool   `json:"amazon"`
	Qobuz     bool   `json:"qobuz"`
	TidalURL  string `json:"tidal_url,omitempty"`
	AmazonURL string `json:"amazon_url,omitempty"`
	QobuzURL  string `json:"qobuz_url,omitempty"`
}

func NewSongLinkClient() *SongLinkClient {
	return &SongLinkClient{
		client:           newHTTPClient(30 * time.Second),
		minCallDelay:     songLinkMinCallDelay,
		apiCallResetTime: time.Now(),
	}
}

func (s *SongLinkClient) waitForRequestSlot() {
	s.waitForRequestSlotContext(context.Background())
}

func (s *SongLinkClient) waitForRequestSlotContext(ctx context.Context) {
	if ctx == nil {
		ctx = context.Background()
	}
	now := time.Now()
	if now.Sub(s.apiCallResetTime) >= time.Minute {
		s.apiCallCount = 0
		s.apiCallResetTime = now
	}

	if s.apiCallCount >= songLinkMaxCallsPerMinute {
		waitTime := time.Minute - now.Sub(s.apiCallResetTime)
		if waitTime > 0 {
			fmt.Printf("Rate limit reached, waiting %v...\n", waitTime.Round(time.Second))
			timer := time.NewTimer(waitTime)
			select {
			case <-ctx.Done():
				timer.Stop()
				return
			case <-timer.C:
			}
			now = time.Now()
		}
		s.apiCallCount = 0
		s.apiCallResetTime = now
	}

	if !s.lastAPICallTime.IsZero() {
		timeSinceLastCall := now.Sub(s.lastAPICallTime)
		minCallDelay := s.minCallDelay
		if minCallDelay <= 0 {
			minCallDelay = songLinkMinCallDelay
		}
		if timeSinceLastCall < minCallDelay {
			waitTime := minCallDelay - timeSinceLastCall
			fmt.Printf("Rate limiting: waiting %v...\n", waitTime.Round(time.Second))
			timer := time.NewTimer(waitTime)
			select {
			case <-ctx.Done():
				timer.Stop()
				return
			case <-timer.C:
			}
		}
	}
}

func (s *SongLinkClient) getLinksResponse(spotifyTrackID, region, action string) (*songLinkLinksResponse, error) {
	return s.getLinksResponseContext(context.Background(), spotifyTrackID, region, action)
}

func (s *SongLinkClient) getLinksResponseContext(ctx context.Context, spotifyTrackID, region, action string) (*songLinkLinksResponse, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	s.waitForRequestSlotContext(ctx)
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	spotifyBase, _ := base64.StdEncoding.DecodeString("aHR0cHM6Ly9vcGVuLnNwb3RpZnkuY29tL3RyYWNrLw==")
	spotifyURL := fmt.Sprintf("%s%s", string(spotifyBase), spotifyTrackID)

	apiBase, _ := base64.StdEncoding.DecodeString("aHR0cHM6Ly9hcGkuc29uZy5saW5rL3YxLWFscGhhLjEvbGlua3M/dXJsPQ==")
	apiURL := fmt.Sprintf("%s%s", string(apiBase), url.QueryEscape(spotifyURL))
	if region != "" {
		apiURL += fmt.Sprintf("&userCountry=%s", region)
	}

	req, err := http.NewRequestWithContext(ctx, "GET", apiURL, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	fmt.Println(action)

	var resp *http.Response
	var lastErr error
	for attempt, delay := 1, songLinkRetryDelay; attempt <= songLinkMaxRetries; attempt++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		resp, err = s.client.Do(req)
		if err == nil {
			s.lastAPICallTime = time.Now()
			s.apiCallCount++
			if resp.StatusCode == http.StatusOK {
				break
			}
			statusCode := resp.StatusCode
			_ = resp.Body.Close()
			resp = nil
			if statusCode == http.StatusTooManyRequests {
				fmt.Printf("Rate limited by API, waiting %v before retry...\n", songLinkRetryDelay)
				lastErr = fmt.Errorf("song.link API rate limited request")
			} else {
				lastErr = fmt.Errorf("API returned status %d", statusCode)
				if statusCode < http.StatusInternalServerError {
					// Retrying cannot fix a client error; the public API now answers 401 to everyone.
					return nil, lastErr
				}
			}
		} else {
			lastErr = err
		}
		if attempt == songLinkMaxRetries {
			return nil, lastErr
		}
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response body: %w", err)
	}
	if len(body) == 0 {
		return nil, fmt.Errorf("API returned empty response")
	}

	var songLinkResp songLinkLinksResponse
	if err := json.Unmarshal(body, &songLinkResp); err != nil {
		bodyStr := string(body)
		if len(bodyStr) > 200 {
			bodyStr = bodyStr[:200] + "..."
		}
		return nil, fmt.Errorf("failed to decode response: %w (response: %s)", err, bodyStr)
	}

	return &songLinkResp, nil
}

func (s *SongLinkClient) GetAllURLsFromSpotify(spotifyTrackID string, region string) (*SongLinkURLs, error) {
	songLinkResp, err := s.getLinksResponse(spotifyTrackID, region, "Getting streaming URLs from song.link...")
	if err != nil {
		return nil, fmt.Errorf("failed to get URLs: %w", err)
	}

	urls := &SongLinkURLs{}

	if tidalLink, ok := songLinkResp.LinksByPlatform["tidal"]; ok && tidalLink.URL != "" {
		urls.TidalURL = tidalLink.URL
		fmt.Printf("✓ Tidal URL found\n")
	}

	if amazonLink, ok := songLinkResp.LinksByPlatform["amazonMusic"]; ok && amazonLink.URL != "" {
		amazonURL := amazonLink.URL

		if len(amazonURL) > 0 {
			urls.AmazonURL = amazonURL
			fmt.Printf("✓ Amazon URL found\n")
		}
	}

	if deezerLink, ok := songLinkResp.LinksByPlatform["deezer"]; ok && deezerLink.URL != "" {
		if isrc, err := getDeezerISRC(deezerLink.URL); err == nil && isrc != "" {
			urls.ISRC = isrc
		}
	}

	if urls.TidalURL == "" && urls.AmazonURL == "" {
		return nil, fmt.Errorf("no streaming URLs found")
	}

	return urls, nil
}

func (s *SongLinkClient) CheckTrackAvailability(spotifyTrackID string) (*TrackAvailability, error) {
	songLinkResp, err := s.getLinksResponse(spotifyTrackID, "", fmt.Sprintf("Checking availability for track: %s", spotifyTrackID))
	if err != nil {
		return nil, fmt.Errorf("failed to check availability: %w", err)
	}

	availability := &TrackAvailability{
		SpotifyID: spotifyTrackID,
	}

	if tidalLink, ok := songLinkResp.LinksByPlatform["tidal"]; ok && tidalLink.URL != "" {
		availability.Tidal = true
		availability.TidalURL = tidalLink.URL
	}

	if amazonLink, ok := songLinkResp.LinksByPlatform["amazonMusic"]; ok && amazonLink.URL != "" {
		availability.Amazon = true
		availability.AmazonURL = amazonLink.URL
	}

	if deezerLink, ok := songLinkResp.LinksByPlatform["deezer"]; ok && deezerLink.URL != "" {
		deezerURL := deezerLink.URL

		deezerISRC, err := getDeezerISRC(deezerURL)
		if err == nil && deezerISRC != "" {
			qobuzAvailable := checkQobuzAvailability(deezerISRC)
			availability.Qobuz = qobuzAvailable
		}
	}

	return availability, nil
}

func checkQobuzAvailability(isrc string) bool {
	client := newHTTPClient(10 * time.Second)
	appID := "798273057"

	apiBase, _ := base64.StdEncoding.DecodeString("aHR0cHM6Ly93d3cucW9idXouY29tL2FwaS5qc29uLzAuMi90cmFjay9zZWFyY2g/cXVlcnk9")
	searchURL := fmt.Sprintf("%s%s&limit=1&app_id=%s", string(apiBase), isrc, appID)

	resp, err := client.Get(searchURL)
	if err != nil {
		return false
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return false
	}

	var searchResp struct {
		Tracks struct {
			Total int `json:"total"`
		} `json:"tracks"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&searchResp); err != nil {
		return false
	}

	return searchResp.Tracks.Total > 0
}

func (s *SongLinkClient) GetDeezerURLFromSpotify(spotifyTrackID string) (string, error) {
	return s.GetDeezerURLFromSpotifyContext(context.Background(), spotifyTrackID)
}

func (s *SongLinkClient) GetDeezerURLFromSpotifyContext(ctx context.Context, spotifyTrackID string) (string, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	songLinkResp, err := s.getLinksResponseContext(ctx, spotifyTrackID, "", "Getting Deezer URL from song.link...")
	if err != nil {
		return "", fmt.Errorf("failed to get Deezer URL: %w", err)
	}

	deezerLink, ok := songLinkResp.LinksByPlatform["deezer"]
	if !ok || deezerLink.URL == "" {
		return "", fmt.Errorf("deezer link not found")
	}

	deezerURL := deezerLink.URL
	fmt.Printf("Found Deezer URL: %s\n", deezerURL)
	return deezerURL, nil
}

func getDeezerISRC(deezerURL string) (string, error) {
	return getDeezerISRCContext(context.Background(), deezerURL)
}

func getDeezerISRCContext(ctx context.Context, deezerURL string) (string, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	trackID := parseDeezerTrackID(deezerURL)
	if trackID == "" {
		return "", fmt.Errorf("could not extract track ID from Deezer URL: %s", deezerURL)
	}

	apiURL := fmt.Sprintf("https://api.deezer.com/track/%s", trackID)

	client := metadataHTTPClientFactory(10 * time.Second)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, apiURL, nil)
	if err != nil {
		return "", fmt.Errorf("failed to create Deezer API request: %w", err)
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("failed to call Deezer API: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return "", fmt.Errorf("Deezer API returned status %d", resp.StatusCode)
	}

	var deezerTrack struct {
		ID    int64  `json:"id"`
		ISRC  string `json:"isrc"`
		Title string `json:"title"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&deezerTrack); err != nil {
		return "", fmt.Errorf("failed to decode Deezer API response: %w", err)
	}

	if deezerTrack.ISRC == "" {
		return "", fmt.Errorf("ISRC not found in Deezer API response for track %s", trackID)
	}

	fmt.Printf("Found ISRC from Deezer: %s (track: %s)\n", deezerTrack.ISRC, deezerTrack.Title)
	return deezerTrack.ISRC, nil
}

func parseDeezerTrackID(deezerURL string) string {
	if !strings.Contains(deezerURL, "/track/") {
		return ""
	}
	parts := strings.Split(deezerURL, "/track/")
	if len(parts) <= 1 {
		return ""
	}
	trackID := strings.Split(parts[1], "?")[0]
	return strings.TrimSpace(trackID)
}

func getDeezerGenreByID(genreID int64) (string, error) {
	return getDeezerGenreByIDContext(context.Background(), genreID)
}

func getDeezerGenreByIDContext(ctx context.Context, genreID int64) (string, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if genreID <= 0 {
		return "", fmt.Errorf("invalid Deezer genre ID: %d", genreID)
	}

	apiURL := fmt.Sprintf("https://api.deezer.com/genre/%d", genreID)
	client := metadataHTTPClientFactory(10 * time.Second)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, apiURL, nil)
	if err != nil {
		return "", fmt.Errorf("failed to create Deezer genre API request: %w", err)
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("failed to call Deezer genre API: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return "", fmt.Errorf("Deezer genre API returned status %d", resp.StatusCode)
	}

	var deezerGenre struct {
		Name string `json:"name"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&deezerGenre); err != nil {
		return "", fmt.Errorf("failed to decode Deezer genre response: %w", err)
	}

	if strings.TrimSpace(deezerGenre.Name) == "" {
		return "", fmt.Errorf("genre name is empty for Deezer genre ID %d", genreID)
	}

	return strings.TrimSpace(deezerGenre.Name), nil
}

func getDeezerGenreFromTrackURL(deezerURL string) (string, error) {
	return getDeezerGenreFromTrackURLContext(context.Background(), deezerURL)
}

func getDeezerGenreFromTrackURLContext(ctx context.Context, deezerURL string) (string, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	trackID := parseDeezerTrackID(deezerURL)
	if trackID == "" {
		return "", fmt.Errorf("could not extract track ID from Deezer URL: %s", deezerURL)
	}

	apiURL := fmt.Sprintf("https://api.deezer.com/track/%s", trackID)
	client := metadataHTTPClientFactory(10 * time.Second)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, apiURL, nil)
	if err != nil {
		return "", fmt.Errorf("failed to create Deezer track API request: %w", err)
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("failed to call Deezer track API: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return "", fmt.Errorf("Deezer track API returned status %d", resp.StatusCode)
	}

	var deezerTrack struct {
		Title   string `json:"title"`
		GenreID int64  `json:"genre_id"`
		Album   struct {
			ID      int64 `json:"id"`
			GenreID int64 `json:"genre_id"`
		} `json:"album"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&deezerTrack); err != nil {
		return "", fmt.Errorf("failed to decode Deezer track response: %w", err)
	}

	if deezerTrack.GenreID > 0 {
		if genre, err := getDeezerGenreByIDContext(ctx, deezerTrack.GenreID); err == nil && genre != "" {
			return genre, nil
		}
	}

	if deezerTrack.Album.GenreID > 0 {
		if genre, err := getDeezerGenreByIDContext(ctx, deezerTrack.Album.GenreID); err == nil && genre != "" {
			return genre, nil
		}
	}

	if deezerTrack.Album.ID > 0 {
		albumURL := fmt.Sprintf("https://api.deezer.com/album/%d", deezerTrack.Album.ID)
		albumReq, reqErr := http.NewRequestWithContext(ctx, http.MethodGet, albumURL, nil)
		if reqErr == nil {
			albumResp, err := client.Do(albumReq)
			if err == nil {
				defer albumResp.Body.Close()
				if albumResp.StatusCode == 200 {
					var deezerAlbum struct {
						GenreID int64 `json:"genre_id"`
						Genres  struct {
							Data []struct {
								Name string `json:"name"`
							} `json:"data"`
						} `json:"genres"`
					}
					if json.NewDecoder(albumResp.Body).Decode(&deezerAlbum) == nil {
						for _, g := range deezerAlbum.Genres.Data {
							name := strings.TrimSpace(g.Name)
							if name != "" {
								return name, nil
							}
						}
						if deezerAlbum.GenreID > 0 {
							if genre, err := getDeezerGenreByIDContext(ctx, deezerAlbum.GenreID); err == nil && genre != "" {
								return genre, nil
							}
						}
					}
				}
			}
		}
	}

	return "", fmt.Errorf("genre not found in Deezer track/album response for track %s", deezerTrack.Title)
}

func (s *SongLinkClient) GetGenre(spotifyID string) (string, error) {
	return s.GetGenreContext(context.Background(), spotifyID)
}

func (s *SongLinkClient) GetGenreContext(ctx context.Context, spotifyID string) (string, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	deezerURL, err := s.GetDeezerURLFromSpotifyContext(ctx, spotifyID)
	if err != nil {
		return "", err
	}
	return getDeezerGenreFromTrackURLContext(ctx, deezerURL)
}

func (s *SongLinkClient) GetISRC(spotifyID string) (string, error) {
	return s.GetISRCContext(context.Background(), spotifyID)
}

func (s *SongLinkClient) GetISRCContext(ctx context.Context, spotifyID string) (string, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	deezerURL, err := s.GetDeezerURLFromSpotifyContext(ctx, spotifyID)
	if err != nil {
		return "", err
	}
	return getDeezerISRCContext(ctx, deezerURL)
}
