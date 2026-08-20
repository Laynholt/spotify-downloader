package backend

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

var AppVersion = "7.2.0"

var musicBrainzHTTPClientFactory = func(timeout time.Duration) *http.Client {
	return metadataHTTPClientFactory(timeout)
}

const musicBrainzAPIBase = "https://musicbrainz.org/ws/2"

type MusicBrainzRecordingResponse struct {
	Recordings []struct {
		ID       string `json:"id"`
		Title    string `json:"title"`
		Length   int    `json:"length"`
		Releases []struct {
			ID           string `json:"id"`
			Title        string `json:"title"`
			Status       string `json:"status"`
			ReleaseGroup struct {
				ID          string `json:"id"`
				Title       string `json:"title"`
				PrimaryType string `json:"primary-type"`
			} `json:"release-group"`
			Date    string `json:"date"`
			Country string `json:"country"`
			Media   []struct {
				Format string `json:"format"`
			} `json:"media"`
			LabelInfo []struct {
				Label struct {
					Name string `json:"name"`
				} `json:"label"`
			} `json:"label-info"`
		} `json:"releases"`
		ArtistCredit []struct {
			Artist struct {
				ID   string `json:"id"`
				Name string `json:"name"`
			} `json:"artist"`
		} `json:"artist-credit"`
		Tags []struct {
			Count int    `json:"count"`
			Name  string `json:"name"`
		} `json:"tags"`
	} `json:"recordings"`
}

func FetchMusicBrainzMetadata(isrc, title, artist, album string, useSingleGenre bool, embedGenre bool) (Metadata, error) {
	return FetchMusicBrainzMetadataContext(context.Background(), isrc, title, artist, album, useSingleGenre, embedGenre)
}

func FetchMusicBrainzMetadataContext(ctx context.Context, isrc, title, artist, album string, useSingleGenre bool, embedGenre bool) (Metadata, error) {
	var meta Metadata
	if !embedGenre {
		return meta, nil
	}
	genres, err := fetchMusicBrainzGenresContext(ctx, isrc, title, artist, album)
	if err != nil {
		return meta, err
	}
	if useSingleGenre {
		meta.Genre = genres[0]
	} else {
		meta.Genre = strings.Join(genres, ", ")
	}
	return meta, nil
}

func fetchMusicBrainzGenresContext(ctx context.Context, isrc, title, artist, album string) ([]string, error) {
	if ctx == nil {
		ctx = context.Background()
	}

	client := musicBrainzHTTPClientFactory(10 * time.Second)

	queries := make([]string, 0, 2)
	if strings.TrimSpace(isrc) != "" {
		queries = append(queries, fmt.Sprintf("isrc:%s", strings.TrimSpace(isrc)))
	}
	titleTrimmed := strings.TrimSpace(title)
	artistTrimmed := strings.TrimSpace(artist)
	if titleTrimmed != "" && artistTrimmed != "" {
		queries = append(queries, fmt.Sprintf("recording:\"%s\" AND artist:\"%s\"", escapeMusicBrainzQuery(titleTrimmed), escapeMusicBrainzQuery(artistTrimmed)))
	} else if titleTrimmed != "" {
		queries = append(queries, fmt.Sprintf("recording:\"%s\"", escapeMusicBrainzQuery(titleTrimmed)))
	}

	if len(queries) == 0 {
		return nil, fmt.Errorf("no query source available for genre lookup")
	}

	var recordingResponse *MusicBrainzRecordingResponse
	recordingIndex := -1

	for _, query := range queries {
		mbResp, err := queryMusicBrainzRecordingsContext(ctx, client, query)
		if err != nil {
			continue
		}
		if len(mbResp.Recordings) == 0 {
			continue
		}

		bestIdx := -1
		bestTagCount := -1
		for i := range mbResp.Recordings {
			tagCount := len(mbResp.Recordings[i].Tags)
			if tagCount > bestTagCount {
				bestTagCount = tagCount
				bestIdx = i
			}
		}
		if bestIdx >= 0 {
			recordingResponse = mbResp
			recordingIndex = bestIdx
			if len(mbResp.Recordings[bestIdx].Tags) > 0 {
				break
			}
		}
	}

	if recordingResponse == nil || recordingIndex < 0 {
		return nil, fmt.Errorf("no recordings found for provided query")
	}

	var genres []string
	recording := &recordingResponse.Recordings[recordingIndex]
	seen := make(map[string]struct{}, len(recording.Tags))

	for _, tag := range recording.Tags {
		name := strings.TrimSpace(tag.Name)
		if name == "" {
			continue
		}
		key := strings.ToLower(name)
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		genres = append(genres, name)
		if len(genres) >= 5 {
			break
		}
	}

	if len(genres) == 0 {
		return nil, fmt.Errorf("no genre tags found in MusicBrainz")
	}

	return genres, nil
}

func escapeMusicBrainzQuery(s string) string {
	return strings.ReplaceAll(s, "\"", "\\\"")
}

func queryMusicBrainzRecordings(client *http.Client, query string) (*MusicBrainzRecordingResponse, error) {
	return queryMusicBrainzRecordingsContext(context.Background(), client, query)
}

func queryMusicBrainzRecordingsContext(ctx context.Context, client *http.Client, query string) (*MusicBrainzRecordingResponse, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	reqURL := fmt.Sprintf("%s/recording?query=%s&fmt=json&inc=releases+artist-credits+tags+media+release-groups+labels", musicBrainzAPIBase, url.QueryEscape(query))

	req, err := http.NewRequestWithContext(ctx, "GET", reqURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", fmt.Sprintf("SpotiDownloader/%s ( support@exyezed.cc )", AppVersion))

	var resp *http.Response
	var lastErr error
	for attempt, delay := 1, 500*time.Millisecond; attempt <= 3; attempt++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		resp, lastErr = client.Do(req)
		if lastErr == nil && resp.StatusCode == http.StatusOK {
			break
		}
		if resp != nil {
			statusCode := resp.StatusCode
			_ = resp.Body.Close()
			resp = nil
			lastErr = fmt.Errorf("MusicBrainz API returned status: %d", statusCode)
		}
		if attempt == 3 {
			return nil, lastErr
		}
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, ctx.Err()
		case <-timer.C:
		}
		if delay < 2*time.Second {
			delay *= 2
		}
	}
	if resp == nil {
		return nil, lastErr
	}
	defer resp.Body.Close()

	var mbResp MusicBrainzRecordingResponse
	if err := json.NewDecoder(resp.Body).Decode(&mbResp); err != nil {
		return nil, err
	}
	return &mbResp, nil
}
