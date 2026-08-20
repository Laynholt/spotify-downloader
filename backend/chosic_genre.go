package backend

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strings"
	"time"
)

const (
	chosicBaseURL    = "https://www.chosic.com"
	chosicToolsAPI   = chosicBaseURL + "/api/tools"
	chosicFinderPage = chosicBaseURL + "/music-genre-finder/"
)

type chosicClient struct {
	httpClient *http.Client
}

func newChosicClient() (*chosicClient, error) {
	jar, err := cookiejar.New(nil)
	if err != nil {
		return nil, fmt.Errorf("failed to initialize cookie jar: %w", err)
	}

	client := metadataHTTPClientFactory(20 * time.Second)
	if client == nil {
		client = newHTTPClient(20 * time.Second)
	}
	client.Jar = jar
	return &chosicClient{httpClient: client}, nil
}

func (c *chosicClient) handshake() error {
	return c.handshakeContext(context.Background())
}

func (c *chosicClient) handshakeContext(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if c == nil || c.httpClient == nil {
		return fmt.Errorf("chosic client is not initialized")
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, chosicToolsAPI+"/handshake/", nil)
	if err != nil {
		return fmt.Errorf("failed to create handshake request: %w", err)
	}
	setChosicHeaders(req)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("chosic handshake request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("chosic handshake failed: HTTP %d", resp.StatusCode)
	}

	var payload struct {
		Success bool `json:"success"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return fmt.Errorf("failed to decode chosic handshake response: %w", err)
	}
	if !payload.Success {
		return fmt.Errorf("chosic handshake returned unsuccessful response")
	}

	return nil
}

func (c *chosicClient) getTrack(trackID string) (*chosicTrackResponse, error) {
	return c.getTrackContext(context.Background(), trackID)
}

func (c *chosicClient) getTrackContext(ctx context.Context, trackID string) (*chosicTrackResponse, error) {
	var payload chosicTrackResponse
	if err := c.getJSONContext(ctx, "/tracks/"+url.PathEscape(trackID), &payload); err != nil {
		return nil, err
	}
	return &payload, nil
}

func (c *chosicClient) getArtist(artistID string) (*chosicArtistResponse, error) {
	return c.getArtistContext(context.Background(), artistID)
}

func (c *chosicClient) getArtistContext(ctx context.Context, artistID string) (*chosicArtistResponse, error) {
	var payload chosicArtistResponse
	if err := c.getJSONContext(ctx, "/artists/"+url.PathEscape(artistID), &payload); err != nil {
		return nil, err
	}
	return &payload, nil
}

func (c *chosicClient) getJSON(path string, out interface{}) error {
	return c.getJSONContext(context.Background(), path, out)
}

func (c *chosicClient) getJSONContext(ctx context.Context, path string, out interface{}) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if c == nil || c.httpClient == nil {
		return fmt.Errorf("chosic client is not initialized")
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, chosicToolsAPI+path, nil)
	if err != nil {
		return fmt.Errorf("failed to create chosic request: %w", err)
	}
	setChosicHeaders(req)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("chosic request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("chosic request failed: HTTP %d (%s)", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return fmt.Errorf("failed to decode chosic response: %w", err)
	}
	return nil
}

func setChosicHeaders(req *http.Request) {
	req.Header.Set("Accept", "application/json, text/plain, */*")
	req.Header.Set("Origin", chosicBaseURL)
	req.Header.Set("Referer", chosicFinderPage)
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/145.0.0.0 Safari/537.36")
}

type chosicTrackResponse struct {
	Artists []struct {
		ID string `json:"id"`
	} `json:"artists"`
}

type chosicArtistResponse struct {
	Genres []string `json:"genres"`
}

func fetchGenresFromChosic(trackID string) ([]string, error) {
	return fetchGenresFromChosicContext(context.Background(), trackID)
}

func fetchGenresFromChosicContext(ctx context.Context, trackID string) ([]string, error) {
	trackID = strings.TrimSpace(trackID)
	if trackID == "" {
		return nil, fmt.Errorf("empty track ID")
	}

	client, err := metadataChosicClientFactory()
	if err != nil {
		return nil, err
	}
	if err := client.handshakeContext(ctx); err != nil {
		return nil, err
	}

	track, err := client.getTrackContext(ctx, trackID)
	if err != nil {
		return nil, err
	}

	artistID := ""
	for _, artist := range track.Artists {
		id := strings.TrimSpace(artist.ID)
		if id != "" {
			artistID = id
			break
		}
	}
	if artistID == "" {
		return nil, fmt.Errorf("chosic track response did not contain artist ID")
	}

	artist, err := client.getArtistContext(ctx, artistID)
	if err != nil {
		return nil, err
	}

	genres := make([]string, 0, len(artist.Genres))
	seen := make(map[string]struct{}, len(artist.Genres))
	for _, genre := range artist.Genres {
		genre = strings.TrimSpace(genre)
		if genre != "" {
			key := strings.ToLower(genre)
			if _, exists := seen[key]; exists {
				continue
			}
			seen[key] = struct{}{}
			genres = append(genres, genre)
			if len(genres) >= 5 {
				break
			}
		}
	}
	if len(genres) == 0 {
		return nil, fmt.Errorf("chosic artist response did not contain genres")
	}

	return genres, nil
}

func (c *chosicClient) fetchArtistIDContext(ctx context.Context, trackID string) (string, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := c.handshakeContext(ctx); err != nil {
		return "", err
	}
	track, err := c.getTrackContext(ctx, trackID)
	if err != nil {
		return "", err
	}
	for _, artist := range track.Artists {
		if artistID := strings.TrimSpace(artist.ID); artistID != "" {
			return artistID, nil
		}
	}
	return "", fmt.Errorf("chosic track response did not contain artist ID")
}

func (c *chosicClient) fetchArtistGenresContext(ctx context.Context, artistID string) ([]string, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := c.handshakeContext(ctx); err != nil {
		return nil, err
	}
	artist, err := c.getArtistContext(ctx, artistID)
	if err != nil {
		return nil, err
	}
	genres := make([]string, 0, len(artist.Genres))
	seen := make(map[string]struct{}, len(artist.Genres))
	for _, genre := range artist.Genres {
		genre = strings.TrimSpace(genre)
		if genre == "" {
			continue
		}
		key := strings.ToLower(genre)
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		genres = append(genres, genre)
		if len(genres) >= 5 {
			break
		}
	}
	if len(genres) == 0 {
		return nil, fmt.Errorf("chosic artist response did not contain genres")
	}
	return genres, nil
}
