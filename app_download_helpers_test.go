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

func TestEnsureDownloadQueueItemPreservesRequestQueueID(t *testing.T) {
	backend.ClearAllDownloads()
	t.Cleanup(backend.ClearAllDownloads)

	req := DownloadRequest{
		QueueID:    "playlist:a",
		TrackID:    "track-a",
		TrackName:  "Track A",
		ArtistName: "Artist",
		AlbumName:  "Album",
	}
	itemID := ensureDownloadQueueItem(req)
	queue := backend.GetDownloadQueue()
	if len(queue.Queue) != 1 || queue.Queue[0].ID != itemID || queue.Queue[0].QueueID != "playlist:a" {
		t.Fatalf("implicit queue item = %#v, want queue playlist:a", queue.Queue)
	}
}

func TestCancelDownloadQueueCancelsOnlySelectedQueue(t *testing.T) {
	backend.ClearAllDownloads()
	t.Cleanup(backend.ClearAllDownloads)

	started := make(chan string, 3)
	var queueBContext context.Context
	app := &App{
		ctx: context.Background(),
		runTrackPipeline: func(ctx context.Context, req backend.TrackPipelineRequest) (backend.TrackPipelineResult, error) {
			if req.ItemID == "job-b-1" {
				queueBContext = ctx
			}
			started <- req.ItemID
			<-ctx.Done()
			return backend.TrackPipelineResult{}, ctx.Err()
		},
	}

	var wg sync.WaitGroup
	for _, req := range []DownloadRequest{
		{QueueID: "playlist:a", ItemID: "job-a-1", TrackID: "track-a-1", SessionToken: "session-token", TrackName: "Track A1", OutputDir: t.TempDir()},
		{QueueID: "playlist:a", ItemID: "job-a-2", TrackID: "track-a-2", SessionToken: "session-token", TrackName: "Track A2", OutputDir: t.TempDir()},
		{QueueID: "playlist:b", ItemID: "job-b-1", TrackID: "track-b-1", SessionToken: "session-token", TrackName: "Track B1", OutputDir: t.TempDir()},
	} {
		if err := backend.AddQueueItem(req.QueueID, req.ItemID, req.TrackName, req.ArtistName, req.AlbumName, req.TrackID); err != nil {
			t.Fatal(err)
		}
		wg.Add(1)
		go func(req DownloadRequest) {
			defer wg.Done()
			_, _ = app.DownloadTrack(req)
		}(req)
	}

	for i := 0; i < 3; i++ {
		select {
		case <-started:
		case <-time.After(time.Second):
			t.Fatal("pipeline did not start all active jobs")
		}
	}

	if got := app.CancelDownloadQueue("playlist:a"); got != 2 {
		t.Fatalf("CancelDownloadQueue(playlist:a) = %d, want 2", got)
	}
	if queueBContext == nil {
		t.Fatal("queue B pipeline context was not captured")
	}
	if err := queueBContext.Err(); err != nil {
		t.Fatalf("queue B context ended after cancelling queue A: %v", err)
	}
	if got := app.CancelActiveDownloads(); got != 1 {
		t.Fatalf("CancelActiveDownloads() = %d, want 1 remaining queue B job", got)
	}
	wg.Wait()

	queue := backend.GetDownloadQueue()
	if queue.CancelledCount != 3 {
		t.Fatalf("cancelled queue items = %d, want 3", queue.CancelledCount)
	}
}

func TestAddDownloadJobUsesProvidedIdentity(t *testing.T) {
	backend.ClearAllDownloads()
	t.Cleanup(backend.ClearAllDownloads)

	app := &App{}
	jobID, err := app.AddDownloadJob("playlist:a", "frontend-job-1", "spotify-1", "Track", "Artist", "Album")
	if err != nil {
		t.Fatal(err)
	}
	if jobID != "frontend-job-1" {
		t.Fatalf("AddDownloadJob returned %q, want frontend-job-1", jobID)
	}
	queue := backend.GetDownloadQueue()
	if len(queue.Queue) != 1 || queue.Queue[0].ID != "frontend-job-1" || queue.Queue[0].QueueID != "playlist:a" {
		t.Fatalf("queued job = %#v", queue.Queue)
	}
	if _, err := app.AddDownloadJob("playlist:b", "frontend-job-1", "spotify-1", "Track", "Artist", "Album"); err == nil {
		t.Fatal("AddDownloadJob accepted a duplicate frontend job ID")
	}
}

func TestCancelDownloadItemMarksRegisteredJobCancelled(t *testing.T) {
	backend.ClearAllDownloads()
	t.Cleanup(backend.ClearAllDownloads)

	app := &App{}
	jobID, err := app.AddDownloadJob("playlist:a", "frontend-job-1", "spotify-1", "Track", "Artist", "Album")
	if err != nil {
		t.Fatal(err)
	}

	app.CancelDownloadItem(jobID)

	queue := backend.GetDownloadQueue()
	if queue.CancelledCount != 1 || len(queue.Queue) != 1 || queue.Queue[0].Status != backend.StatusCancelled {
		t.Fatalf("cancelled job = %#v, cancelled count = %d", queue.Queue, queue.CancelledCount)
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

func TestDownloadDoesNotCompleteBeforeSuspiciousMoveFinishes(t *testing.T) {
	backend.ClearAllDownloads()
	t.Cleanup(backend.ClearAllDownloads)
	outputDir := t.TempDir()
	originalPath := filepath.Join(outputDir, "track.mp3")
	if err := os.WriteFile(originalPath, []byte("audio"), 0o644); err != nil {
		t.Fatal(err)
	}
	moveStarted := make(chan struct{})
	releaseMove := make(chan struct{})
	app := &App{
		ctx: context.Background(),
		runTrackPipeline: func(_ context.Context, _ backend.TrackPipelineRequest) (backend.TrackPipelineResult, error) {
			return backend.TrackPipelineResult{File: originalPath, DurationSeconds: 10}, nil
		},
		moveSuspiciousDownload: func(filename, destination string) (string, error) {
			close(moveStarted)
			<-releaseMove
			return filename, nil
		},
	}

	resultCh := make(chan error, 1)
	go func() {
		_, err := app.DownloadTrack(DownloadRequest{
			TrackID:      "track-suspicious-blocked",
			SessionToken: "session-token",
			TrackName:    "Track",
			ArtistName:   "Artist",
			Duration:     1000,
			OutputDir:    outputDir,
		})
		resultCh <- err
	}()
	select {
	case <-moveStarted:
	case <-time.After(time.Second):
		t.Fatal("suspicious move did not start")
	}
	queue := backend.GetDownloadQueue()
	if queue.CompletedCount != 0 || queue.FailedCount != 0 || queue.CancelledCount != 0 {
		t.Fatalf("queue became terminal before suspicious move release: %#v", queue)
	}
	close(releaseMove)
	if err := <-resultCh; err != nil {
		t.Fatalf("DownloadTrack returned error: %v", err)
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

func TestDownloadTrackPropagatesQueueAndForce(t *testing.T) {
	backend.ClearAllDownloads()
	t.Cleanup(backend.ClearAllDownloads)
	outputDir := t.TempDir()
	finalPath := filepath.Join(outputDir, "track.mp3")
	if err := os.WriteFile(finalPath, []byte("audio"), 0o644); err != nil {
		t.Fatal(err)
	}

	var captured backend.TrackPipelineRequest
	var activeQueueID string
	app := &App{ctx: context.Background()}
	app.runTrackPipeline = func(_ context.Context, req backend.TrackPipelineRequest) (backend.TrackPipelineResult, error) {
		captured = req
		app.activeMu.Lock()
		activeQueueID = app.activeDownloads["job-1"].QueueID
		app.activeMu.Unlock()
		return backend.TrackPipelineResult{File: finalPath}, nil
	}
	_, err := app.DownloadTrack(DownloadRequest{
		QueueID:      "playlist:p1",
		Force:        true,
		ItemID:       "job-1",
		TrackID:      "track-1",
		SessionToken: "session-token",
		TrackName:    "Track",
		ArtistName:   "Artist",
		OutputDir:    outputDir,
	})
	if err != nil {
		t.Fatalf("DownloadTrack returned error: %v", err)
	}
	if !captured.Force {
		t.Fatalf("captured pipeline request Force = false, want true")
	}
	if activeQueueID != "playlist:p1" {
		t.Fatalf("active queue ID = %q, want playlist:p1", activeQueueID)
	}
	if got := app.CancelDownloadQueue("playlist:p1"); got != 0 {
		t.Fatalf("queue cancellation after completed download = %d, want 0", got)
	}
}

func TestDownloadTrackSuspiciousMoveOwnsTerminalQueuePath(t *testing.T) {
	backend.ClearAllDownloads()
	t.Cleanup(backend.ClearAllDownloads)
	outputDir := t.TempDir()
	collectionDir := filepath.Join(outputDir, "collection")
	if err := os.MkdirAll(collectionDir, 0o755); err != nil {
		t.Fatal(err)
	}
	originalPath := filepath.Join(collectionDir, "track.mp3")
	if err := os.WriteFile(originalPath, []byte("audio"), 0o644); err != nil {
		t.Fatal(err)
	}
	movedPath := filepath.Join(collectionDir, backend.SuspiciousTracksDirName, "track.mp3")
	app := &App{
		ctx: context.Background(),
		runTrackPipeline: func(_ context.Context, _ backend.TrackPipelineRequest) (backend.TrackPipelineResult, error) {
			return backend.TrackPipelineResult{File: originalPath, DurationSeconds: 10}, nil
		},
		moveSuspiciousDownload: func(filename, destination string) (string, error) {
			if filename != originalPath || destination != filepath.Join(collectionDir, backend.SuspiciousTracksDirName) {
				t.Fatalf("move request = (%q, %q)", filename, destination)
			}
			if err := os.MkdirAll(destination, 0o755); err != nil {
				return "", err
			}
			if err := os.Rename(filename, movedPath); err != nil {
				return "", err
			}
			return movedPath, nil
		},
	}

	response, err := app.DownloadTrack(DownloadRequest{
		TrackID:       "track-suspicious",
		SessionToken:  "session-token",
		TrackName:     "Track",
		ArtistName:    "Artist",
		Duration:      1000,
		OutputDir:     outputDir,
		CollectionDir: collectionDir,
	})
	if err != nil {
		t.Fatalf("DownloadTrack returned error: %v", err)
	}
	if !response.Success || response.File != movedPath || !response.Suspicious {
		t.Fatalf("response = %#v", response)
	}
	queue := backend.GetDownloadQueue()
	if queue.CompletedCount != 1 || queue.FailedCount != 0 {
		t.Fatalf("queue terminal counts = completed:%d failed:%d", queue.CompletedCount, queue.FailedCount)
	}
	if got := queue.Queue[0].FilePath; got != movedPath {
		t.Fatalf("queue file path = %q, want moved path %q", got, movedPath)
	}
}

func TestDownloadTrackSuspiciousMoveFailureFailsQueue(t *testing.T) {
	backend.ClearAllDownloads()
	t.Cleanup(backend.ClearAllDownloads)
	outputDir := t.TempDir()
	originalPath := filepath.Join(outputDir, "track.mp3")
	if err := os.WriteFile(originalPath, []byte("audio"), 0o644); err != nil {
		t.Fatal(err)
	}
	app := &App{
		ctx: context.Background(),
		runTrackPipeline: func(_ context.Context, _ backend.TrackPipelineRequest) (backend.TrackPipelineResult, error) {
			return backend.TrackPipelineResult{File: originalPath, DurationSeconds: 10}, nil
		},
		moveSuspiciousDownload: func(string, string) (string, error) {
			return "", errors.New("move failed")
		},
	}

	response, err := app.DownloadTrack(DownloadRequest{
		TrackID:      "track-suspicious-failure",
		SessionToken: "session-token",
		TrackName:    "Track",
		ArtistName:   "Artist",
		Duration:     1000,
		OutputDir:    outputDir,
	})
	if err == nil || response.Success {
		t.Fatalf("expected suspicious move failure, response=%#v err=%v", response, err)
	}
	queue := backend.GetDownloadQueue()
	if queue.FailedCount != 1 || queue.CompletedCount != 0 || queue.CancelledCount != 0 {
		t.Fatalf("queue terminal counts = failed:%d completed:%d cancelled:%d", queue.FailedCount, queue.CompletedCount, queue.CancelledCount)
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
	ctx, cleanup := app.beginTrack("item-cleanup", "")
	if ctx == nil {
		t.Fatal("beginTrack returned nil context")
	}
	cleanup()
	cleanup()
	if got := app.CancelActiveDownloads(); got != 0 {
		t.Fatalf("CancelActiveDownloads after cleanup = %d, want 0", got)
	}
}

func TestShutdownWaitsForTrackCleanupBeforeReturning(t *testing.T) {
	app := &App{ctx: context.Background(), runtimeInitErr: errors.New("skip runtime cleanup")}
	trackCtx, cleanup := app.beginTrack("shutdown-item", "playlist:a")
	cancelled := make(chan struct{})
	releaseCleanup := make(chan struct{})
	cleanupReleased := false
	defer func() {
		if !cleanupReleased {
			close(releaseCleanup)
		}
	}()
	go func() {
		<-trackCtx.Done()
		close(cancelled)
		<-releaseCleanup
		cleanup()
	}()

	shutdownDone := make(chan struct{})
	go func() {
		app.shutdown(context.Background())
		close(shutdownDone)
	}()

	select {
	case <-cancelled:
	case <-time.After(time.Second):
		t.Fatal("shutdown did not cancel the registered track")
	}
	select {
	case <-shutdownDone:
		t.Fatal("shutdown returned before the registered track cleaned up")
	case <-time.After(50 * time.Millisecond):
	}
	close(releaseCleanup)
	cleanupReleased = true
	select {
	case <-shutdownDone:
	case <-time.After(time.Second):
		t.Fatal("shutdown did not return after track cleanup")
	}
}

func TestBeginTrackRejectsRegistrationAfterShutdownStarts(t *testing.T) {
	app := &App{ctx: context.Background(), runtimeInitErr: errors.New("skip runtime cleanup")}
	trackCtx, cleanup := app.beginTrack("shutdown-item", "playlist:a")
	cancelled := make(chan struct{})
	releaseCleanup := make(chan struct{})
	cleanupReleased := false
	defer func() {
		if !cleanupReleased {
			close(releaseCleanup)
		}
	}()
	go func() {
		<-trackCtx.Done()
		close(cancelled)
		<-releaseCleanup
		cleanup()
	}()

	shutdownDone := make(chan struct{})
	go func() {
		app.shutdown(context.Background())
		close(shutdownDone)
	}()
	select {
	case <-cancelled:
	case <-time.After(time.Second):
		t.Fatal("shutdown did not start cancellation")
	}

	lateCtx, lateCleanup := app.beginTrack("late-item", "playlist:a")
	defer lateCleanup()
	if !errors.Is(lateCtx.Err(), context.Canceled) {
		t.Fatalf("late track context error = %v, want context.Canceled", lateCtx.Err())
	}

	close(releaseCleanup)
	cleanupReleased = true
	select {
	case <-shutdownDone:
	case <-time.After(time.Second):
		t.Fatal("shutdown did not return after track cleanup")
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
