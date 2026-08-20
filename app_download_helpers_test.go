package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/afkarxyz/SpotiDownloader/backend"
)

func TestCancelActiveDownloadsCancelsEveryTrackContext(t *testing.T) {
	backend.ClearAllDownloads()
	t.Cleanup(backend.ClearAllDownloads)

	var started atomic.Int32
	app := &App{
		ctx: context.Background(),
		runTrackPipeline: func(ctx context.Context, _ backend.TrackPipelineRequest) (backend.TrackPipelineResult, error) {
			started.Add(1)
			<-ctx.Done()
			return backend.TrackPipelineResult{}, ctx.Err()
		},
	}

	var wg sync.WaitGroup
	for i := 0; i < 3; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, _ = app.DownloadTrack(DownloadRequest{
				TrackID:      fmt.Sprintf("track-%d", i),
				SessionToken: "session-token",
				TrackName:    fmt.Sprintf("Track %d", i),
				ArtistName:   "Artist",
				OutputDir:    t.TempDir(),
			})
		}(i)
	}

	deadline := time.Now().Add(time.Second)
	for started.Load() != 3 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if started.Load() != 3 {
		t.Fatalf("started pipelines = %d, want 3", started.Load())
	}

	if got := app.CancelActiveDownloads(); got != 3 {
		t.Fatalf("CancelActiveDownloads() = %d, want 3", got)
	}
	wg.Wait()

	queue := backend.GetDownloadQueue()
	if queue.CancelledCount != 3 {
		t.Fatalf("cancelled queue items = %d, want 3", queue.CancelledCount)
	}
}

func TestDownloadCompletesAfterFinalization(t *testing.T) {
	backend.ClearAllDownloads()
	t.Cleanup(backend.ClearAllDownloads)

	finalizationReleased := make(chan struct{})
	pipelineStarted := make(chan struct{})
	app := &App{
		ctx: context.Background(),
		runTrackPipeline: func(ctx context.Context, req backend.TrackPipelineRequest) (backend.TrackPipelineResult, error) {
			close(pipelineStarted)
			select {
			case <-finalizationReleased:
			case <-ctx.Done():
				return backend.TrackPipelineResult{}, ctx.Err()
			}
			return backend.TrackPipelineResult{File: filepath.Join(req.OutputDir, "track.mp3")}, nil
		},
	}

	resultCh := make(chan struct {
		response DownloadResponse
		err      error
	}, 1)
	go func() {
		response, err := app.DownloadTrack(DownloadRequest{
			TrackID:      "track-finalize",
			SessionToken: "session-token",
			TrackName:    "Track",
			ArtistName:   "Artist",
			OutputDir:    t.TempDir(),
		})
		resultCh <- struct {
			response DownloadResponse
			err      error
		}{response, err}
	}()

	select {
	case <-pipelineStarted:
	case <-time.After(time.Second):
		t.Fatal("pipeline did not start")
	}

	select {
	case outcome := <-resultCh:
		t.Fatalf("download returned before finalization release: %#v", outcome)
	case <-time.After(25 * time.Millisecond):
	}
	queue := backend.GetDownloadQueue()
	if queue.CompletedCount != 0 {
		t.Fatalf("completed queue items before finalization = %d, want 0", queue.CompletedCount)
	}

	close(finalizationReleased)
	outcome := <-resultCh
	if outcome.err != nil {
		t.Fatalf("DownloadTrack returned error: %v", outcome.err)
	}
	if !outcome.response.Success {
		t.Fatalf("DownloadTrack response unsuccessful: %#v", outcome.response)
	}
	queue = backend.GetDownloadQueue()
	if queue.CompletedCount != 1 {
		t.Fatalf("completed queue items after finalization = %d, want 1", queue.CompletedCount)
	}
}

func TestDownloadTrackReturnsPipelineDiagnosticsAndBuildsRequest(t *testing.T) {
	backend.ClearAllDownloads()
	t.Cleanup(backend.ClearAllDownloads)
	outputDir := t.TempDir()
	finalPath := filepath.Join(outputDir, "track.mp3")
	if err := os.WriteFile(finalPath, []byte("audio"), 0o644); err != nil {
		t.Fatal(err)
	}

	var captured backend.TrackPipelineRequest
	app := &App{
		ctx: context.Background(),
		runTrackPipeline: func(_ context.Context, req backend.TrackPipelineRequest) (backend.TrackPipelineResult, error) {
			captured = req
			return backend.TrackPipelineResult{
				File:            finalPath,
				DurationSeconds: 180,
				Warnings:        []string{"cover unavailable"},
				CacheOutcomes:   map[string]backend.CacheOutcome{"spotify:track": backend.CacheHit},
				Timings:         backend.PipelineTimings{DownloadMS: 12, FinalizationMS: 4, TotalMS: 20},
			}, nil
		},
	}

	response, err := app.DownloadTrack(DownloadRequest{
		TrackID:             "track-1",
		SessionToken:        "session-token",
		TrackName:           "Title",
		ArtistName:          "Artist",
		AlbumName:           "Album",
		AlbumArtist:         "Album Artist",
		ReleaseDate:         "2024-01-02",
		OutputDir:           outputDir,
		AudioFormat:         "flac",
		FilenameFormat:      "title-artist",
		TrackNumber:         true,
		Position:            3,
		UseAlbumTrackNumber: true,
		TotalTracks:         9,
		DiscNumber:          2,
		SpotifyTotalDiscs:   3,
		Duration:            180000,
		EmbedLyrics:         true,
		EmbedGenre:          true,
		UseSingleGenre:      true,
		CoverURL:            "https://example.invalid/cover.jpg",
	})
	if err != nil {
		t.Fatalf("DownloadTrack returned error: %v", err)
	}
	if !response.Success || response.File != finalPath {
		t.Fatalf("unexpected response: %#v", response)
	}
	if len(response.Warnings) != 1 || response.Warnings[0] != "cover unavailable" {
		t.Fatalf("response warnings = %#v", response.Warnings)
	}
	if response.CacheOutcomes["spotify:track"] != backend.CacheHit {
		t.Fatalf("response cache outcomes = %#v", response.CacheOutcomes)
	}
	if response.Timings.TotalMS != 20 {
		t.Fatalf("response timings = %#v", response.Timings)
	}
	if captured.ItemID == "" || captured.TrackID != "track-1" || captured.ExpectedDurationMS != 180000 {
		t.Fatalf("captured request identity/duration = %#v", captured)
	}
	if captured.BaseMetadata.TrackNumber != 1 || captured.BaseMetadata.TotalTracks != 9 || captured.BaseMetadata.TotalDiscs != 3 {
		t.Fatalf("captured base metadata = %#v", captured.BaseMetadata)
	}
	queue := backend.GetDownloadQueue()
	if queue.CompletedCount != 1 {
		t.Fatalf("completed queue items = %d, want 1", queue.CompletedCount)
	}
	if got := queue.Queue[0].Timings; got != response.Timings {
		t.Fatalf("queue timings = %#v, want %#v", got, response.Timings)
	}
}

func TestDownloadTrackMapsPipelineFailureToQueueTerminalState(t *testing.T) {
	backend.ClearAllDownloads()
	t.Cleanup(backend.ClearAllDownloads)
	app := &App{
		ctx: context.Background(),
		runTrackPipeline: func(context.Context, backend.TrackPipelineRequest) (backend.TrackPipelineResult, error) {
			return backend.TrackPipelineResult{Warnings: []string{"metadata unavailable"}}, errors.New("audio failed")
		},
	}

	response, err := app.DownloadTrack(DownloadRequest{
		TrackID:      "track-failure",
		SessionToken: "session-token",
		TrackName:    "Track",
		ArtistName:   "Artist",
		OutputDir:    t.TempDir(),
	})
	if err == nil || response.Success {
		t.Fatalf("expected failed response, got response=%#v err=%v", response, err)
	}
	queue := backend.GetDownloadQueue()
	if queue.FailedCount != 1 || queue.CancelledCount != 0 {
		t.Fatalf("queue terminal counts = failed:%d cancelled:%d", queue.FailedCount, queue.CancelledCount)
	}
	if queue.Queue[0].Warning != "metadata unavailable" {
		t.Fatalf("queue warning = %q", queue.Queue[0].Warning)
	}
}

func TestCancelActiveDownloadsIsIdempotentAfterTrackCleanup(t *testing.T) {
	backend.ClearAllDownloads()
	t.Cleanup(backend.ClearAllDownloads)
	app := &App{ctx: context.Background()}
	ctx, cleanup := app.beginTrack("item-cleanup")
	if ctx == nil {
		t.Fatal("beginTrack returned nil context")
	}
	cleanup()
	cleanup()
	if got := app.CancelActiveDownloads(); got != 0 {
		t.Fatalf("CancelActiveDownloads after cleanup = %d, want 0", got)
	}
}

func TestNormalizeDownloadRequestSetsDefaults(t *testing.T) {
	t.Parallel()

	req := DownloadRequest{
		TrackID:      "track123",
		SessionToken: "session-token",
		OutputDir:    t.TempDir(),
	}

	if err := normalizeDownloadRequest(&req); err != nil {
		t.Fatalf("normalizeDownloadRequest returned error: %v", err)
	}
	if req.AudioFormat != "mp3" {
		t.Fatalf("expected default audio format mp3, got %q", req.AudioFormat)
	}
	if req.FilenameFormat != "title-artist" {
		t.Fatalf("expected default filename format title-artist, got %q", req.FilenameFormat)
	}
}

func TestBuildDownloadValidationResultMarksSuspiciousDelta(t *testing.T) {
	got := buildDownloadValidationResult(180000, 184.25, nil)

	if !got.Suspicious {
		t.Fatalf("expected suspicious result for >3s delta: %#v", got)
	}
	if got.ExpectedDurationSeconds != 180 {
		t.Fatalf("expected duration = %v, want 180", got.ExpectedDurationSeconds)
	}
	if got.ActualDurationSeconds != 184.25 {
		t.Fatalf("actual duration = %v, want 184.25", got.ActualDurationSeconds)
	}
	if got.DurationDeltaSeconds != 4.25 {
		t.Fatalf("delta = %v, want 4.25", got.DurationDeltaSeconds)
	}
}

func TestBuildDownloadValidationResultKeepsProbeFailureAsWarning(t *testing.T) {
	got := buildDownloadValidationResult(180000, 0, errors.New("ffprobe failed"))

	if got.Suspicious {
		t.Fatalf("probe failure should not be suspicious: %#v", got)
	}
	if got.ExpectedDurationSeconds != 180 {
		t.Fatalf("expected duration = %v, want 180", got.ExpectedDurationSeconds)
	}
	if got.ValidationWarning == "" {
		t.Fatalf("expected validation warning")
	}
}

func TestMoveSuspiciousDownloadIfNeededMovesFileToSuspiciousDir(t *testing.T) {
	dir := t.TempDir()
	collectionDir := filepath.Join(dir, "Playlist")
	if err := os.MkdirAll(collectionDir, 0o755); err != nil {
		t.Fatal(err)
	}
	originalPath := filepath.Join(collectionDir, "Song.mp3")
	if err := os.WriteFile(originalPath, []byte("audio"), 0o644); err != nil {
		t.Fatal(err)
	}

	validation := DownloadResponse{
		Suspicious:              true,
		ExpectedDurationSeconds: 180,
		ActualDurationSeconds:   184.5,
		DurationDeltaSeconds:    4.5,
	}

	got, err := moveSuspiciousDownloadIfNeeded(originalPath, collectionDir, validation)
	if err != nil {
		t.Fatal(err)
	}

	wantMovedPath := filepath.Join(collectionDir, "Suspicious", "Song.mp3")
	if got.File != wantMovedPath {
		t.Fatalf("File = %q, want moved suspicious path %q", got.File, wantMovedPath)
	}
	if got.MovedOriginalPath != wantMovedPath {
		t.Fatalf("MovedOriginalPath = %q, want %q", got.MovedOriginalPath, wantMovedPath)
	}
	if got.ReplacementPath != originalPath {
		t.Fatalf("ReplacementPath = %q, want original path %q", got.ReplacementPath, originalPath)
	}
	if _, err := os.Stat(originalPath); !os.IsNotExist(err) {
		t.Fatalf("original path still exists or stat failed unexpectedly: %v", err)
	}
	if _, err := os.Stat(wantMovedPath); err != nil {
		t.Fatalf("moved suspicious file missing: %v", err)
	}
}
