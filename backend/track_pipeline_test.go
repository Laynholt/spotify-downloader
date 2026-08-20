package backend

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

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
	req, _ := testTrackPipelineRequest(t)
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
}
