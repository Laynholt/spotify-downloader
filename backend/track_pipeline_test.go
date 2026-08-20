package backend

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type trackPipelineRoundTripper func(*http.Request) (*http.Response, error)

func (f trackPipelineRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func tokenTestHTTPClient(server *httptest.Server) *http.Client {
	baseURL, err := url.Parse(server.URL)
	if err != nil {
		panic(err)
	}
	client := server.Client()
	transport := client.Transport
	client.Transport = trackPipelineRoundTripper(func(req *http.Request) (*http.Response, error) {
		clone := req.Clone(req.Context())
		clone.URL.Scheme = baseURL.Scheme
		clone.URL.Host = baseURL.Host
		return transport.RoundTrip(clone)
	})
	return client
}

func testTrackPipelineRequest(t *testing.T) (TrackPipelineRequest, RuntimePaths) {
	t.Helper()
	paths, err := ResolveRuntimePaths(filepath.Join(t.TempDir(), "SpotiDownloader.exe"), "run-test")
	if err != nil {
		t.Fatal(err)
	}
	if err := paths.Prepare(); err != nil {
		t.Fatal(err)
	}
	installRuntimePathsForTest(t, paths)
	outputDir := filepath.Join(t.TempDir(), "music")
	return TrackPipelineRequest{
		ItemID:         "item-1",
		TrackID:        "track-1",
		OutputDir:      outputDir,
		AudioFormat:    "mp3",
		FilenameFormat: "title-artist",
		EmbedLyrics:    true,
		EmbedGenre:     true,
		CoverURL:       "https://example.invalid/cover.jpg",
		BaseMetadata: Metadata{
			Title:       "Track",
			Artist:      "Artist",
			Album:       "Album",
			AlbumArtist: "Artist",
			Date:        "2024-01-01",
		},
	}, paths
}

func installPipelineDepsForTest(t *testing.T, deps trackPipelineDeps) {
	t.Helper()
	previous := pipelineDeps
	pipelineDeps = deps
	t.Cleanup(func() { pipelineDeps = previous })
}

func TestTrackPipelineBranchesOverlapBeforeAudioRelease(t *testing.T) {
	req, _ := testTrackPipelineRequest(t)
	started := map[string]chan struct{}{
		"audio":    make(chan struct{}),
		"cover":    make(chan struct{}),
		"metadata": make(chan struct{}),
		"lyrics":   make(chan struct{}),
	}
	releaseAudio := make(chan struct{})
	var starts atomic.Int32
	markStarted := func(name string) {
		select {
		case <-started[name]:
		default:
			close(started[name])
		}
		starts.Add(1)
	}
	installPipelineDepsForTest(t, trackPipelineDeps{
		resolveTarget: func(context.Context, *SpotiDownloader, TrackPipelineRequest) (string, string, error) {
			return "https://example.invalid/audio", ".mp3", nil
		},
		downloadAudio: func(ctx context.Context, _ *SpotiDownloader, _ string, base, ext, _ string) (string, error) {
			markStarted("audio")
			select {
			case <-releaseAudio:
			case <-ctx.Done():
				return "", ctx.Err()
			}
			path := base + ext
			if err := os.WriteFile(path, []byte("audio"), 0600); err != nil {
				return "", err
			}
			return path, nil
		},
		convertAudio: func(_ context.Context, path, _, _ string) (string, error) { return path, nil },
		downloadCover: func(context.Context, *SpotiDownloader, string, string, bool) (string, error) {
			markStarted("cover")
			return "", nil
		},
		metadata: func(context.Context, *MetadataCache, TaggingMetadataRequest) (taggingMetadataResult, []string) {
			markStarted("metadata")
			return taggingMetadataResult{}, nil
		},
		lyrics: func(context.Context, TrackPipelineRequest, int) (string, error) {
			markStarted("lyrics")
			return "[00:00.00]ready\n", nil
		},
		probeDuration: func(context.Context, string) (float64, error) { return 4, nil },
		finalizeMedia: func(context.Context, string, Metadata, string) error { return nil },
	})

	resultCh := make(chan struct {
		result TrackPipelineResult
		err    error
	}, 1)
	go func() {
		result, err := (&SpotiDownloader{}).RunTrackPipeline(context.Background(), req)
		resultCh <- struct {
			result TrackPipelineResult
			err    error
		}{result, err}
	}()

	for _, branch := range []string{"audio", "cover", "metadata", "lyrics"} {
		select {
		case <-started[branch]:
		case <-time.After(time.Second):
			t.Fatalf("%s did not start (started=%d)", branch, starts.Load())
		}
	}
	close(releaseAudio)
	select {
	case outcome := <-resultCh:
		if outcome.err != nil {
			t.Fatalf("pipeline: %v", outcome.err)
		}
		if outcome.result.File == "" {
			t.Fatal("pipeline returned empty file")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("pipeline did not complete")
	}
}

func TestTrackPipelineLeavesTerminalQueueStateToCaller(t *testing.T) {
	ClearAllDownloads()
	t.Cleanup(ClearAllDownloads)
	req, _ := testTrackPipelineRequest(t)
	AddToQueue(req.ItemID, req.BaseMetadata.Title, req.BaseMetadata.Artist, req.BaseMetadata.Album, req.TrackID)
	installPipelineDepsForTest(t, trackPipelineDeps{
		resolveTarget: func(context.Context, *SpotiDownloader, TrackPipelineRequest) (string, string, error) {
			return "source", ".mp3", nil
		},
		downloadAudio: func(_ context.Context, _ *SpotiDownloader, _ string, base, ext, _ string) (string, error) {
			path := base + ext
			return path, os.WriteFile(path, []byte("audio"), 0600)
		},
		convertAudio: func(_ context.Context, path, _, _ string) (string, error) { return path, nil },
		downloadCover: func(context.Context, *SpotiDownloader, string, string, bool) (string, error) {
			return "", nil
		},
		metadata: func(context.Context, *MetadataCache, TaggingMetadataRequest) (taggingMetadataResult, []string) {
			return taggingMetadataResult{}, nil
		},
		lyrics:        func(context.Context, TrackPipelineRequest, int) (string, error) { return "", nil },
		probeDuration: func(context.Context, string) (float64, error) { return 1, nil },
		finalizeMedia: func(context.Context, string, Metadata, string) error { return nil },
	})

	result, err := (&SpotiDownloader{}).RunTrackPipeline(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if result.File == "" {
		t.Fatal("pipeline returned empty file")
	}
	queue := GetDownloadQueue()
	if queue.CompletedCount != 0 || queue.FailedCount != 0 || queue.CancelledCount != 0 || queue.SkippedCount != 0 {
		t.Fatalf("pipeline terminal counts = completed:%d failed:%d cancelled:%d skipped:%d; caller must own terminal state", queue.CompletedCount, queue.FailedCount, queue.CancelledCount, queue.SkippedCount)
	}
}

func TestTrackPipelineLeavesFailureAndCancellationToCaller(t *testing.T) {
	t.Run("failure", func(t *testing.T) {
		ClearAllDownloads()
		t.Cleanup(ClearAllDownloads)
		req, _ := testTrackPipelineRequest(t)
		AddToQueue(req.ItemID, req.BaseMetadata.Title, req.BaseMetadata.Artist, req.BaseMetadata.Album, req.TrackID)
		installPipelineDepsForTest(t, trackPipelineDeps{
			resolveTarget: func(context.Context, *SpotiDownloader, TrackPipelineRequest) (string, string, error) {
				return "source", ".mp3", nil
			},
			downloadAudio: func(context.Context, *SpotiDownloader, string, string, string, string) (string, error) {
				return "", errors.New("audio failed")
			},
		})
		if _, err := (&SpotiDownloader{}).RunTrackPipeline(context.Background(), req); err == nil {
			t.Fatal("expected pipeline failure")
		}
		queue := GetDownloadQueue()
		if queue.CompletedCount != 0 || queue.FailedCount != 0 || queue.CancelledCount != 0 || queue.SkippedCount != 0 {
			t.Fatalf("failure terminal counts = completed:%d failed:%d cancelled:%d skipped:%d", queue.CompletedCount, queue.FailedCount, queue.CancelledCount, queue.SkippedCount)
		}
	})

	t.Run("cancellation", func(t *testing.T) {
		ClearAllDownloads()
		t.Cleanup(ClearAllDownloads)
		req, _ := testTrackPipelineRequest(t)
		AddToQueue(req.ItemID, req.BaseMetadata.Title, req.BaseMetadata.Artist, req.BaseMetadata.Album, req.TrackID)
		ctx, cancel := context.WithCancel(context.Background())
		started := make(chan struct{})
		installPipelineDepsForTest(t, trackPipelineDeps{
			resolveTarget: func(context.Context, *SpotiDownloader, TrackPipelineRequest) (string, string, error) {
				return "source", ".mp3", nil
			},
			downloadAudio: func(ctx context.Context, _ *SpotiDownloader, _ string, _ string, _ string, _ string) (string, error) {
				close(started)
				<-ctx.Done()
				return "", ctx.Err()
			},
		})
		resultCh := make(chan error, 1)
		go func() {
			_, err := (&SpotiDownloader{}).RunTrackPipeline(ctx, req)
			resultCh <- err
		}()
		select {
		case <-started:
		case <-time.After(time.Second):
			t.Fatal("audio did not start")
		}
		cancel()
		if err := <-resultCh; !errors.Is(err, context.Canceled) {
			t.Fatalf("error = %v, want cancellation", err)
		}
		queue := GetDownloadQueue()
		if queue.CompletedCount != 0 || queue.FailedCount != 0 || queue.CancelledCount != 0 || queue.SkippedCount != 0 {
			t.Fatalf("cancellation terminal counts = completed:%d failed:%d cancelled:%d skipped:%d", queue.CompletedCount, queue.FailedCount, queue.CancelledCount, queue.SkippedCount)
		}
	})
}

func TestTrackPipelineProbesDurationAndFinalizesOnce(t *testing.T) {
	req, _ := testTrackPipelineRequest(t)
	var probes atomic.Int32
	var finalizations atomic.Int32
	installPipelineDepsForTest(t, trackPipelineDeps{
		resolveTarget: func(context.Context, *SpotiDownloader, TrackPipelineRequest) (string, string, error) {
			return "source", ".mp3", nil
		},
		downloadAudio: func(context.Context, *SpotiDownloader, string, string, string, string) (string, error) {
			path := filepath.Join(req.OutputDir, "unused.mp3")
			return path, os.WriteFile(path, []byte("audio"), 0600)
		},
		convertAudio: func(_ context.Context, path, _, _ string) (string, error) { return path, nil },
		downloadCover: func(context.Context, *SpotiDownloader, string, string, bool) (string, error) {
			return "", nil
		},
		metadata: func(context.Context, *MetadataCache, TaggingMetadataRequest) (taggingMetadataResult, []string) {
			return taggingMetadataResult{}, nil
		},
		lyrics: func(context.Context, TrackPipelineRequest, int) (string, error) {
			return "[00:00.00]ok\n[99:00.00]late\n", nil
		},
		probeDuration: func(context.Context, string) (float64, error) {
			probes.Add(1)
			return 3, nil
		},
		finalizeMedia: func(_ context.Context, _ string, metadata Metadata, _ string) error {
			finalizations.Add(1)
			if !strings.Contains(metadata.Lyrics, "ok") || strings.Contains(metadata.Lyrics, "late") {
				return errors.New("lyrics were not validated before finalization")
			}
			return nil
		},
	})

	result, err := (&SpotiDownloader{}).RunTrackPipeline(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if probes.Load() != 1 {
		t.Fatalf("duration probes = %d, want 1", probes.Load())
	}
	if finalizations.Load() != 1 {
		t.Fatalf("finalizations = %d, want 1", finalizations.Load())
	}
	if result.DurationSeconds != 3 {
		t.Fatalf("duration = %v, want 3", result.DurationSeconds)
	}
}

func TestTrackPipelineLateLyricsAreDiscardedAtAbsoluteDeadline(t *testing.T) {
	req, _ := testTrackPipelineRequest(t)
	lyricsDone := make(chan struct{})
	var finalizations atomic.Int32
	startedAt := time.Now()
	var deadlinePassed atomic.Bool
	installPipelineDepsForTest(t, trackPipelineDeps{
		resolveTarget: func(context.Context, *SpotiDownloader, TrackPipelineRequest) (string, string, error) {
			return "source", ".mp3", nil
		},
		downloadAudio: func(context.Context, *SpotiDownloader, string, string, string, string) (string, error) {
			path := filepath.Join(req.OutputDir, "audio.mp3")
			return path, os.WriteFile(path, []byte("audio"), 0600)
		},
		convertAudio: func(_ context.Context, path, _, _ string) (string, error) { return path, nil },
		downloadCover: func(context.Context, *SpotiDownloader, string, string, bool) (string, error) {
			return "", nil
		},
		metadata: func(context.Context, *MetadataCache, TaggingMetadataRequest) (taggingMetadataResult, []string) {
			return taggingMetadataResult{}, nil
		},
		lyrics: func(ctx context.Context, _ TrackPipelineRequest, _ int) (string, error) {
			select {
			case <-time.After(150 * time.Millisecond):
				close(lyricsDone)
				return "[00:00.00]late", nil
			case <-ctx.Done():
				return "", ctx.Err()
			}
		},
		probeDuration: func(context.Context, string) (float64, error) {
			deadlinePassed.Store(true)
			return 1, nil
		},
		finalizeMedia: func(_ context.Context, _ string, metadata Metadata, _ string) error {
			finalizations.Add(1)
			if metadata.Lyrics != "" {
				return errors.New("late lyrics reopened finalization")
			}
			return nil
		},
		now: func() time.Time {
			if deadlinePassed.Load() {
				return startedAt.Add(6 * time.Second)
			}
			return startedAt
		},
	})

	result, err := (&SpotiDownloader{}).RunTrackPipeline(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if finalizations.Load() != 1 {
		t.Fatalf("finalizations = %d, want 1", finalizations.Load())
	}
	if len(result.Warnings) == 0 || !strings.Contains(strings.Join(result.Warnings, ";"), "lyrics deadline exceeded") {
		t.Fatalf("warnings = %#v", result.Warnings)
	}
	select {
	case <-lyricsDone:
	case <-time.After(time.Second):
		// Cancellation is allowed to stop the late branch before it returns.
	}
}

func TestTrackPipelineCleansTrackTempOnFailureAndCancellation(t *testing.T) {
	runFailureCase := func(t *testing.T, deps trackPipelineDeps) (TrackPipelineRequest, RuntimePaths, error) {
		t.Helper()
		req, paths := testTrackPipelineRequest(t)
		installPipelineDepsForTest(t, deps)
		_, err := (&SpotiDownloader{}).RunTrackPipeline(context.Background(), req)
		return req, paths, err
	}

	t.Run("failure", func(t *testing.T) {
		req, paths := testTrackPipelineRequest(t)
		installPipelineDepsForTest(t, trackPipelineDeps{
			resolveTarget: func(context.Context, *SpotiDownloader, TrackPipelineRequest) (string, string, error) {
				return "source", ".mp3", nil
			},
			downloadAudio: func(context.Context, *SpotiDownloader, string, string, string, string) (string, error) {
				return "", errors.New("audio failed")
			},
			downloadCover: func(context.Context, *SpotiDownloader, string, string, bool) (string, error) {
				return "", nil
			},
			metadata: func(context.Context, *MetadataCache, TaggingMetadataRequest) (taggingMetadataResult, []string) {
				return taggingMetadataResult{}, nil
			},
			lyrics: func(context.Context, TrackPipelineRequest, int) (string, error) {
				return "", nil
			},
		})
		if _, err := (&SpotiDownloader{}).RunTrackPipeline(context.Background(), req); err == nil {
			t.Fatal("expected audio failure")
		}
		if _, err := os.Stat(filepath.Join(paths.RunTempDir, SanitizeFilename(req.ItemID))); !os.IsNotExist(err) {
			t.Fatalf("track temp still exists: %v", err)
		}
	})

	t.Run("finalizer failure", func(t *testing.T) {
		var finalizations atomic.Int32
		req, paths, err := runFailureCase(t, trackPipelineDeps{
			resolveTarget: func(context.Context, *SpotiDownloader, TrackPipelineRequest) (string, string, error) {
				return "source", ".mp3", nil
			},
			downloadAudio: func(_ context.Context, _ *SpotiDownloader, _ string, base, ext, _ string) (string, error) {
				path := base + ext
				return path, os.WriteFile(path, []byte("audio"), 0600)
			},
			convertAudio: func(_ context.Context, path, _, _ string) (string, error) { return path, nil },
			downloadCover: func(context.Context, *SpotiDownloader, string, string, bool) (string, error) {
				return "", nil
			},
			metadata: func(context.Context, *MetadataCache, TaggingMetadataRequest) (taggingMetadataResult, []string) {
				return taggingMetadataResult{}, nil
			},
			lyrics:        func(context.Context, TrackPipelineRequest, int) (string, error) { return "", nil },
			probeDuration: func(context.Context, string) (float64, error) { return 1, nil },
			finalizeMedia: func(context.Context, string, Metadata, string) error {
				finalizations.Add(1)
				return errors.New("finalizer failed")
			},
		})
		if err == nil || !strings.Contains(err.Error(), "finalize media") {
			t.Fatalf("error = %v, want finalizer failure", err)
		}
		if finalizations.Load() != 1 {
			t.Fatalf("finalizations = %d, want 1", finalizations.Load())
		}
		if _, statErr := os.Stat(filepath.Join(paths.RunTempDir, SanitizeFilename(req.ItemID))); !os.IsNotExist(statErr) {
			t.Fatalf("track temp still exists: %v", statErr)
		}
	})

	t.Run("publication failure", func(t *testing.T) {
		var publications atomic.Int32
		req, paths, err := runFailureCase(t, trackPipelineDeps{
			resolveTarget: func(context.Context, *SpotiDownloader, TrackPipelineRequest) (string, string, error) {
				return "source", ".mp3", nil
			},
			downloadAudio: func(_ context.Context, _ *SpotiDownloader, _ string, base, ext, _ string) (string, error) {
				path := base + ext
				return path, os.WriteFile(path, []byte("audio"), 0600)
			},
			convertAudio: func(_ context.Context, path, _, _ string) (string, error) { return path, nil },
			downloadCover: func(context.Context, *SpotiDownloader, string, string, bool) (string, error) {
				return "", nil
			},
			metadata: func(context.Context, *MetadataCache, TaggingMetadataRequest) (taggingMetadataResult, []string) {
				return taggingMetadataResult{}, nil
			},
			lyrics:        func(context.Context, TrackPipelineRequest, int) (string, error) { return "", nil },
			probeDuration: func(context.Context, string) (float64, error) { return 1, nil },
			finalizeMedia: func(context.Context, string, Metadata, string) error { return nil },
			publish: func(context.Context, string, string) error {
				publications.Add(1)
				return errors.New("publication failed")
			},
		})
		if err == nil || !strings.Contains(err.Error(), "publish finalized media") {
			t.Fatalf("error = %v, want publication failure", err)
		}
		if publications.Load() != 1 {
			t.Fatalf("publications = %d, want 1", publications.Load())
		}
		if _, statErr := os.Stat(filepath.Join(paths.RunTempDir, SanitizeFilename(req.ItemID))); !os.IsNotExist(statErr) {
			t.Fatalf("track temp still exists: %v", statErr)
		}
	})

	t.Run("cancellation", func(t *testing.T) {
		req, paths := testTrackPipelineRequest(t)
		ctx, cancel := context.WithCancel(context.Background())
		started := make(chan struct{})
		coverStarted := make(chan struct{})
		coverDone := make(chan struct{})
		installPipelineDepsForTest(t, trackPipelineDeps{
			resolveTarget: func(context.Context, *SpotiDownloader, TrackPipelineRequest) (string, string, error) {
				return "source", ".mp3", nil
			},
			downloadAudio: func(ctx context.Context, _ *SpotiDownloader, _ string, _ string, _ string, _ string) (string, error) {
				close(started)
				<-ctx.Done()
				return "", ctx.Err()
			},
			downloadCover: func(ctx context.Context, _ *SpotiDownloader, _ string, dir string, _ bool) (string, error) {
				close(coverStarted)
				if err := os.WriteFile(filepath.Join(dir, "cover.partial"), []byte("cover"), 0600); err != nil {
					return "", err
				}
				<-ctx.Done()
				close(coverDone)
				return "", ctx.Err()
			},
			metadata: func(ctx context.Context, _ *MetadataCache, _ TaggingMetadataRequest) (taggingMetadataResult, []string) {
				<-ctx.Done()
				return taggingMetadataResult{}, nil
			},
			lyrics: func(ctx context.Context, _ TrackPipelineRequest, _ int) (string, error) {
				<-ctx.Done()
				return "", ctx.Err()
			},
		})
		resultCh := make(chan error, 1)
		go func() { _, err := (&SpotiDownloader{}).RunTrackPipeline(ctx, req); resultCh <- err }()
		select {
		case <-started:
		case <-time.After(time.Second):
			t.Fatal("audio did not start")
		}
		select {
		case <-coverStarted:
		case <-time.After(time.Second):
			t.Fatal("cover did not start")
		}
		cancel()
		if err := <-resultCh; !errors.Is(err, context.Canceled) {
			t.Fatalf("error = %v, want cancellation", err)
		}
		select {
		case <-coverDone:
		default:
			t.Fatal("cover branch was not complete before pipeline returned")
		}
		if _, err := os.Stat(filepath.Join(paths.RunTempDir, SanitizeFilename(req.ItemID))); !os.IsNotExist(err) {
			t.Fatalf("track temp still exists: %v", err)
		}
	})
}

func TestTrackPipelineExistingOutputSkipsOptionalBranches(t *testing.T) {
	ClearAllDownloads()
	t.Cleanup(ClearAllDownloads)
	req, _ := testTrackPipelineRequest(t)
	AddToQueue(req.ItemID, req.BaseMetadata.Title, req.BaseMetadata.Artist, req.BaseMetadata.Album, req.TrackID)
	if err := os.MkdirAll(req.OutputDir, 0755); err != nil {
		t.Fatal(err)
	}
	filename := SanitizeFilename(BuildFilename(req.BaseMetadata.Title, req.BaseMetadata.Artist, req.BaseMetadata.Album, req.BaseMetadata.AlbumArtist, req.BaseMetadata.Date, req.BaseMetadata.DiscNumber, req.FilenameFormat, req.IncludeTrackNumber, req.Position, req.UseAlbumTrackNumber, req.PlaylistName, req.PlaylistOwner))
	existing := filepath.Join(req.OutputDir, filename+".mp3")
	if err := os.WriteFile(existing, []byte("complete"), 0600); err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int32
	installPipelineDepsForTest(t, trackPipelineDeps{
		resolveTarget: func(context.Context, *SpotiDownloader, TrackPipelineRequest) (string, string, error) {
			calls.Add(1)
			return "source", ".mp3", nil
		},
	})
	result, err := (&SpotiDownloader{}).RunTrackPipeline(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if !result.AlreadyExists || result.File != existing {
		t.Fatalf("result = %#v", result)
	}
	if calls.Load() != 0 {
		t.Fatalf("source resolution calls = %d, want 0", calls.Load())
	}
	queue := GetDownloadQueue()
	if queue.CompletedCount != 0 || queue.FailedCount != 0 || queue.CancelledCount != 0 || queue.SkippedCount != 0 {
		t.Fatalf("existing output terminal counts = completed:%d failed:%d cancelled:%d skipped:%d", queue.CompletedCount, queue.FailedCount, queue.CancelledCount, queue.SkippedCount)
	}
}

func TestTrackPipelineDefaultMetadataPropagatesCacheOutcomes(t *testing.T) {
	cache, warnings := OpenMetadataCache("", CacheOptions{})
	if len(warnings) != 0 {
		t.Fatalf("cache warnings = %v", warnings)
	}
	defer cache.Close()

	trackID := "cache-track"
	now := time.Now()
	seed := func(key string, value any, ttl time.Duration) {
		t.Helper()
		encoded, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		cache.storeRaw(key, encoded, ttl, now)
	}
	seed(isrcCacheKey(trackID), cachedISRCMetadata{ISRC: "ISRC-1", Genres: []string{"electronic"}}, trackMetadataTTL)
	seed(chosicTrackCacheKey(trackID), "artist-1", trackMetadataTTL)
	seed(artistGenreCacheKey("artist-1"), []string{"ambient"}, artistGenreTTL)

	s := &SpotiDownloader{metadataCache: cache}
	result := trackPipelineMetadataCall(defaultTrackPipelineDeps, context.Background(), s, TrackPipelineRequest{
		TrackID:        trackID,
		EmbedGenre:     true,
		UseSingleGenre: true,
		BaseMetadata:   Metadata{Title: "Track", Artist: "Artist", Album: "Album", Date: "2024-01-01"},
	})
	if result.err != nil {
		t.Fatalf("metadata error = %v", result.err)
	}
	for _, key := range []string{isrcCacheKey(trackID), chosicTrackCacheKey(trackID), artistGenreCacheKey("artist-1")} {
		if got := result.cacheOutcomes[key]; got != CacheHit {
			t.Fatalf("cache outcome[%q] = %q, want %q (all=%v)", key, got, CacheHit, result.cacheOutcomes)
		}
	}
}

func TestTrackPipelineDefaultMetadataSpotifyCacheOutcomeMissThenHit(t *testing.T) {
	var spotifyCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || !strings.Contains(r.URL.Path, "pathfinder") {
			http.NotFound(w, r)
			return
		}
		spotifyCalls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":{"trackUnion":{"id":"spotify-outcome-track","name":"Track","trackNumber":1,"discNumber":1,"artists":{"items":[{"profile":{"name":"Artist"}}]},"duration":{"totalMilliseconds":180000},"albumOfTrack":{"name":"Album","artists":{"items":[{"profile":{"name":"Artist"}}]},"date":{"isoString":"2024-01-02"},"tracks":{"totalCount":1},"copyright":{"items":[]}}}}}`))
	}))
	defer server.Close()

	baseURL, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	client := server.Client()
	transport := client.Transport
	client.Transport = trackPipelineRoundTripper(func(req *http.Request) (*http.Response, error) {
		clone := req.Clone(req.Context())
		clone.URL.Scheme = baseURL.Scheme
		clone.URL.Host = baseURL.Host
		return transport.RoundTrip(clone)
	})
	previousFactory := metadataSpotifyClientFactory
	metadataSpotifyClientFactory = func() *SpotifyClient {
		return &SpotifyClient{client: client, accessToken: "access", clientToken: "client", clientVersion: "test", cookies: make(map[string]string)}
	}
	t.Cleanup(func() { metadataSpotifyClientFactory = previousFactory })

	cache, warnings := OpenMetadataCache("", CacheOptions{})
	if len(warnings) != 0 {
		t.Fatalf("cache warnings = %v", warnings)
	}
	defer cache.Close()
	s := &SpotiDownloader{metadataCache: cache}
	req := TrackPipelineRequest{
		TrackID:      "spotify-outcome-track",
		BaseMetadata: Metadata{Title: "Track", Artist: "Artist", Album: "Album"},
		EmbedGenre:   false,
	}

	first := trackPipelineMetadataCall(defaultTrackPipelineDeps, context.Background(), s, req)
	if first.err != nil {
		t.Fatalf("first metadata error = %v (warnings=%v)", first.err, first.warnings)
	}
	if got := first.tagging.ReleaseDate; got != "2024-01-02" {
		t.Fatalf("first release date = %q, want 2024-01-02", got)
	}
	if got := first.cacheOutcomes[spotifyCacheKey(req.TrackID)]; got != CacheMiss {
		t.Fatalf("first spotify outcome = %q, want %q (all=%v)", got, CacheMiss, first.cacheOutcomes)
	}

	second := trackPipelineMetadataCall(defaultTrackPipelineDeps, context.Background(), s, req)
	if second.err != nil {
		t.Fatalf("second metadata error = %v (warnings=%v)", second.err, second.warnings)
	}
	if got := second.cacheOutcomes[spotifyCacheKey(req.TrackID)]; got != CacheHit {
		t.Fatalf("second spotify outcome = %q, want %q (all=%v)", got, CacheHit, second.cacheOutcomes)
	}
	if got := spotifyCalls.Load(); got != 1 {
		t.Fatalf("spotify calls = %d, want one miss loader call", got)
	}
}

func TestTrackPipelineRejectsLyricsCompletedAtAbsoluteDeadline(t *testing.T) {
	req, _ := testTrackPipelineRequest(t)
	req.EmbedLyrics = true
	startedAt := time.Now()
	const lyricsBudget = 5 * time.Second
	deadline := startedAt.Add(lyricsBudget)
	var atDeadline atomic.Bool
	timerCreated := make(chan struct{})
	timerReady := make(chan time.Time, 1)
	releaseLyrics := make(chan struct{})
	lyricsCompleted := make(chan struct{})
	var finalizations atomic.Int32
	installPipelineDepsForTest(t, trackPipelineDeps{
		resolveTarget: func(context.Context, *SpotiDownloader, TrackPipelineRequest) (string, string, error) {
			return "source", ".mp3", nil
		},
		downloadAudio: func(_ context.Context, _ *SpotiDownloader, _ string, base, ext, _ string) (string, error) {
			path := base + ext
			return path, os.WriteFile(path, []byte("audio"), 0600)
		},
		convertAudio:  func(_ context.Context, path, _, _ string) (string, error) { return path, nil },
		downloadCover: func(context.Context, *SpotiDownloader, string, string, bool) (string, error) { return "", nil },
		metadata: func(context.Context, *MetadataCache, TaggingMetadataRequest) (taggingMetadataResult, []string) {
			return taggingMetadataResult{}, nil
		},
		lyrics: func(ctx context.Context, _ TrackPipelineRequest, _ int) (string, error) {
			select {
			case <-releaseLyrics:
				atDeadline.Store(true)
				close(lyricsCompleted)
				return "[00:00.00]late", nil
			case <-ctx.Done():
				return "", ctx.Err()
			}
		},
		probeDuration: func(context.Context, string) (float64, error) { return 1, nil },
		finalizeMedia: func(_ context.Context, _ string, metadata Metadata, _ string) error {
			finalizations.Add(1)
			if metadata.Lyrics != "" {
				return errors.New("lyrics completed at deadline were accepted")
			}
			return nil
		},
		now: func() time.Time {
			if atDeadline.Load() {
				return deadline
			}
			return startedAt
		},
		newTimer: func(time.Duration) <-chan time.Time {
			close(timerCreated)
			return timerReady
		},
	})

	resultCh := make(chan struct {
		result TrackPipelineResult
		err    error
	}, 1)
	go func() {
		result, err := (&SpotiDownloader{}).RunTrackPipeline(context.Background(), req)
		resultCh <- struct {
			result TrackPipelineResult
			err    error
		}{result, err}
	}()
	select {
	case <-timerCreated:
	case <-time.After(time.Second):
		t.Fatal("lyrics timer was not created")
	}
	close(releaseLyrics)
	select {
	case <-lyricsCompleted:
	case <-time.After(time.Second):
		t.Fatal("lyrics did not complete")
	}
	timerReady <- deadline
	outcome := <-resultCh
	if outcome.err != nil {
		t.Fatal(outcome.err)
	}
	if finalizations.Load() != 1 {
		t.Fatalf("finalizations = %d, want 1", finalizations.Load())
	}
	if outcome.result.Warnings == nil || !strings.Contains(strings.Join(outcome.result.Warnings, ";"), "lyrics deadline exceeded") {
		t.Fatalf("warnings = %#v", outcome.result.Warnings)
	}
}

func TestTrackPipelineSynthesizedTimeoutsRecordBranchTimings(t *testing.T) {
	req, _ := testTrackPipelineRequest(t)
	req.EmbedLyrics = false
	startedAt := time.Now()
	var timedOut atomic.Bool
	releaseBranches := make(chan struct{})
	finalizeStarted := make(chan struct{})
	installPipelineDepsForTest(t, trackPipelineDeps{
		resolveTarget: func(context.Context, *SpotiDownloader, TrackPipelineRequest) (string, string, error) {
			return "source", ".mp3", nil
		},
		downloadAudio: func(_ context.Context, _ *SpotiDownloader, _ string, base, ext, _ string) (string, error) {
			path := base + ext
			return path, os.WriteFile(path, []byte("audio"), 0600)
		},
		convertAudio: func(_ context.Context, path, _, _ string) (string, error) { return path, nil },
		downloadCover: func(ctx context.Context, _ *SpotiDownloader, _ string, _ string, _ bool) (string, error) {
			<-releaseBranches
			return "", ctx.Err()
		},
		metadata: func(ctx context.Context, _ *MetadataCache, _ TaggingMetadataRequest) (taggingMetadataResult, []string) {
			<-releaseBranches
			return taggingMetadataResult{}, nil
		},
		probeDuration: func(context.Context, string) (float64, error) { return 1, nil },
		finalizeMedia: func(context.Context, string, Metadata, string) error {
			close(finalizeStarted)
			close(releaseBranches)
			return nil
		},
		now: func() time.Time {
			if timedOut.Load() {
				return startedAt.Add(trackPipelineBranchBudget)
			}
			return startedAt
		},
		withTimeout: func(parent context.Context, _ time.Duration) (context.Context, context.CancelFunc) {
			timedOut.Store(true)
			child, cancel := context.WithCancel(parent)
			cancel()
			return child, func() {}
		},
	})

	resultCh := make(chan struct {
		result TrackPipelineResult
		err    error
	}, 1)
	go func() {
		result, err := (&SpotiDownloader{}).RunTrackPipeline(context.Background(), req)
		resultCh <- struct {
			result TrackPipelineResult
			err    error
		}{result, err}
	}()
	select {
	case <-finalizeStarted:
	case <-time.After(time.Second):
		t.Fatal("finalizer did not start")
	}
	outcome := <-resultCh
	if outcome.err != nil {
		t.Fatal(outcome.err)
	}
	if outcome.result.Timings.CoverMS != trackPipelineBranchBudget.Milliseconds() {
		t.Fatalf("cover timing = %dms, want %dms", outcome.result.Timings.CoverMS, trackPipelineBranchBudget.Milliseconds())
	}
	if outcome.result.Timings.MetadataMS != trackPipelineBranchBudget.Milliseconds() {
		t.Fatalf("metadata timing = %dms, want %dms", outcome.result.Timings.MetadataMS, trackPipelineBranchBudget.Milliseconds())
	}
}

func TestTrackPipelineLyricsDeadlineUsesInjectedContext(t *testing.T) {
	req, _ := testTrackPipelineRequest(t)
	req.EmbedGenre = false
	req.BaseMetadata.Date = "2024-01-01"
	req.BaseMetadata.ISRC = "ISRC-1"
	startedAt := time.Unix(0, 0)
	deadline := startedAt.Add(trackPipelineLyricsBudget)
	timerCreated := make(chan struct{})
	timerReady := make(chan time.Time, 1)
	lyricsStarted := make(chan struct{})
	lyricsCanceled := make(chan struct{})
	var finalizations atomic.Int32
	installPipelineDepsForTest(t, trackPipelineDeps{
		resolveTarget: func(context.Context, *SpotiDownloader, TrackPipelineRequest) (string, string, error) {
			return "source", ".mp3", nil
		},
		downloadAudio: func(_ context.Context, _ *SpotiDownloader, _ string, base, ext, _ string) (string, error) {
			path := base + ext
			return path, os.WriteFile(path, []byte("audio"), 0600)
		},
		convertAudio:  func(_ context.Context, path, _, _ string) (string, error) { return path, nil },
		downloadCover: func(context.Context, *SpotiDownloader, string, string, bool) (string, error) { return "", nil },
		lyrics: func(ctx context.Context, _ TrackPipelineRequest, _ int) (string, error) {
			close(lyricsStarted)
			select {
			case <-ctx.Done():
				close(lyricsCanceled)
				return "", ctx.Err()
			}
		},
		probeDuration: func(context.Context, string) (float64, error) { return 1, nil },
		finalizeMedia: func(context.Context, string, Metadata, string) error {
			finalizations.Add(1)
			return nil
		},
		now: func() time.Time { return startedAt },
		newTimer: func(time.Duration) <-chan time.Time {
			close(timerCreated)
			return timerReady
		},
		withDeadline: func(parent context.Context, gotDeadline time.Time) (context.Context, context.CancelFunc) {
			if !gotDeadline.Equal(deadline) {
				panic("unexpected lyrics deadline")
			}
			return context.WithCancel(parent)
		},
	})

	resultCh := make(chan struct {
		result TrackPipelineResult
		err    error
	}, 1)
	go func() {
		result, err := (&SpotiDownloader{}).RunTrackPipeline(context.Background(), req)
		resultCh <- struct {
			result TrackPipelineResult
			err    error
		}{result, err}
	}()
	select {
	case <-lyricsStarted:
	case <-time.After(time.Second):
		t.Fatal("lyrics did not start")
	}
	select {
	case <-lyricsCanceled:
		t.Fatal("lyrics context canceled before injected deadline")
	default:
	}
	select {
	case <-timerCreated:
	case <-time.After(time.Second):
		t.Fatal("lyrics timer was not created")
	}
	timerReady <- deadline
	outcome := <-resultCh
	if outcome.err != nil {
		t.Fatal(outcome.err)
	}
	if finalizations.Load() != 1 {
		t.Fatalf("finalizations = %d, want 1", finalizations.Load())
	}
	if outcome.result.Timings.LyricsMS != 0 {
		t.Fatalf("lyrics timing = %d, want context cancellation completion to be discarded", outcome.result.Timings.LyricsMS)
	}
	if !strings.Contains(strings.Join(outcome.result.Warnings, ";"), "lyrics deadline exceeded") {
		t.Fatalf("warnings = %#v", outcome.result.Warnings)
	}
}

func TestTrackPipelineRefreshCancellationDuringResolverAndAudio(t *testing.T) {
	tests := []struct {
		name  string
		setup func(*SpotiDownloader, *httptest.Server, TrackPipelineRequest)
	}{
		{name: "resolver", setup: func(s *SpotiDownloader, server *httptest.Server, _ TrackPipelineRequest) {
			s.pipelineDeps.resolveTarget = trackPipelineResolveTarget
		}},
		{name: "audio", setup: func(s *SpotiDownloader, server *httptest.Server, _ TrackPipelineRequest) {
			s.pipelineDeps.resolveTarget = func(context.Context, *SpotiDownloader, TrackPipelineRequest) (string, string, error) {
				return server.URL, ".mp3", nil
			}
			s.pipelineDeps.downloadAudio = trackPipelineDownloadAudio
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			req, paths := testTrackPipelineRequest(t)
			req.EmbedLyrics = false
			req.EmbedGenre = false
			req.CoverURL = ""
			req.BaseMetadata.Date = "2024-01-01"
			req.BaseMetadata.ISRC = "ISRC-1"
			var requests atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				requests.Add(1)
				w.WriteHeader(http.StatusUnauthorized)
				_, _ = w.Write([]byte("unauthorized"))
			}))
			defer server.Close()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			refreshStarted := make(chan struct{})
			refreshStopped := make(chan struct{})
			s := &SpotiDownloader{sessionToken: "expired", httpClient: tokenTestHTTPClient(server)}
			s.refreshToken = func(refreshCtx context.Context) error {
				close(refreshStarted)
				<-refreshCtx.Done()
				close(refreshStopped)
				return refreshCtx.Err()
			}
			test.setup(s, server, req)

			resultCh := make(chan error, 1)
			started := time.Now()
			go func() {
				_, err := s.RunTrackPipeline(ctx, req)
				resultCh <- err
			}()
			select {
			case <-refreshStarted:
			case <-time.After(time.Second):
				t.Fatal("refresh helper did not start")
			}
			cancel()
			select {
			case err := <-resultCh:
				if !errors.Is(err, context.Canceled) {
					t.Fatalf("error = %v, want cancellation", err)
				}
			case <-time.After(time.Second):
				t.Fatal("pipeline did not stop after refresh cancellation")
			}
			if elapsed := time.Since(started); elapsed >= time.Second {
				t.Fatalf("cancellation elapsed = %v, want <1s", elapsed)
			}
			select {
			case <-refreshStopped:
			default:
				t.Fatal("refresh helper did not observe cancellation before return")
			}
			if test.name == "audio" {
				if _, err := os.Stat(filepath.Join(paths.RunTempDir, SanitizeFilename(req.ItemID))); !os.IsNotExist(err) {
					t.Fatalf("audio track temp still exists: %v", err)
				}
			}
			if requests.Load() == 0 {
				t.Fatal("expected first token-error request")
			}
		})
	}
}

func TestTrackPipelineDefaultResolverRetriesSessionToken(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if requests.Add(1) == 1 {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte("unauthorized"))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"success":true,"link":"https://example.invalid/audio.mp3"}`))
	}))
	defer server.Close()
	var refreshes atomic.Int32
	s := &SpotiDownloader{sessionToken: "expired", httpClient: tokenTestHTTPClient(server)}
	s.refreshToken = func(context.Context) error {
		refreshes.Add(1)
		s.sessionToken = "fresh"
		return nil
	}
	_, _, err := trackPipelineResolveTargetCall(defaultTrackPipelineDeps, context.Background(), s, TrackPipelineRequest{TrackID: "track-1", AudioFormat: "mp3"})
	if err != nil {
		t.Fatal(err)
	}
	if refreshes.Load() != 1 {
		t.Fatalf("refreshes = %d, want 1", refreshes.Load())
	}
	if requests.Load() < 2 {
		t.Fatalf("requests = %d, want retry", requests.Load())
	}
}

func TestTrackPipelineDefaultAudioRetriesSessionToken(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if requests.Add(1) == 1 {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte("unauthorized"))
			return
		}
		w.Header().Set("Content-Type", "audio/mpeg")
		_, _ = w.Write([]byte("audio"))
	}))
	defer server.Close()
	var refreshes atomic.Int32
	s := &SpotiDownloader{sessionToken: "expired", httpClient: tokenTestHTTPClient(server)}
	s.refreshToken = func(context.Context) error {
		refreshes.Add(1)
		s.sessionToken = "fresh"
		return nil
	}
	path, err := trackPipelineDownloadAudio(context.Background(), s, server.URL, filepath.Join(t.TempDir(), "track"), ".mp3", "item-1")
	if err != nil {
		t.Fatal(err)
	}
	if refreshes.Load() != 1 {
		t.Fatalf("refreshes = %d, want 1", refreshes.Load())
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("audio path = %q: %v", path, err)
	}
}

func TestTrackPipelineResolverRetryStopsOnCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cancel()
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte("unauthorized"))
	}))
	defer server.Close()
	var refreshes atomic.Int32
	s := &SpotiDownloader{sessionToken: "expired", httpClient: tokenTestHTTPClient(server)}
	s.refreshToken = func(context.Context) error {
		refreshes.Add(1)
		return nil
	}
	_, _, err := trackPipelineResolveTargetCall(defaultTrackPipelineDeps, ctx, s, TrackPipelineRequest{TrackID: "track-1", AudioFormat: "mp3"})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want cancellation", err)
	}
	if refreshes.Load() != 0 {
		t.Fatalf("refreshes = %d, want 0", refreshes.Load())
	}
}
