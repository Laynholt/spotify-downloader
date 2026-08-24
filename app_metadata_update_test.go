package main

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/afkarxyz/SpotiDownloader/backend"
)

func TestRedownloadTrackFromYouTubeQueueCancellationMapsCancelled(t *testing.T) {
	backend.ClearAllDownloads()
	t.Cleanup(backend.ClearAllDownloads)
	if err := backend.AddQueueItem("playlist:a", "youtube-job", "Song", "Artist", "Album", "spotify-track"); err != nil {
		t.Fatal(err)
	}

	started := make(chan context.Context, 1)
	app := &App{
		ctx: context.Background(),
		redownloadTrackFromYouTube: func(ctx context.Context, _ backend.SuspiciousRedownloadRequest) backend.SuspiciousRedownloadResult {
			started <- ctx
			<-ctx.Done()
			return backend.SuspiciousRedownloadResult{Status: "failed", Error: ctx.Err().Error()}
		},
	}
	req := backend.SuspiciousRedownloadRequest{
		QueueID: "playlist:a", JobID: "youtube-job",
		Metadata: backend.TrackMetadataUpdateRequest{TrackID: "spotify-track"},
	}

	var wg sync.WaitGroup
	wg.Add(1)
	resultCh := make(chan backend.SuspiciousRedownloadResult, 1)
	errorCh := make(chan error, 1)
	go func() {
		defer wg.Done()
		result, err := app.RedownloadTrackFromYouTube(req)
		resultCh <- result
		errorCh <- err
	}()

	var activeCtx context.Context
	select {
	case activeCtx = <-started:
	case <-time.After(time.Second):
		t.Fatal("YouTube redownload did not start")
	}
	app.activeMu.Lock()
	active := app.activeDownloads[req.JobID]
	app.activeMu.Unlock()
	if active.Context != activeCtx || active.QueueID != req.QueueID {
		t.Fatalf("active redownload = %#v", active)
	}
	if got := app.CancelDownloadQueue(req.QueueID); got != 1 {
		t.Fatalf("CancelDownloadQueue() = %d, want 1", got)
	}

	wg.Wait()
	if err := <-errorCh; err != nil {
		t.Fatalf("RedownloadTrackFromYouTube returned error: %v", err)
	}
	result := <-resultCh
	if result.Success || result.Status != "cancelled" {
		t.Fatalf("result = %#v", result)
	}
	queue := backend.GetDownloadQueue()
	if queue.CancelledCount != 1 || queue.FailedCount != 0 {
		t.Fatalf("queue terminal counts = cancelled:%d failed:%d", queue.CancelledCount, queue.FailedCount)
	}
}

func TestRedownloadTrackFromYouTubeCompletesRegisteredJob(t *testing.T) {
	backend.ClearAllDownloads()
	t.Cleanup(backend.ClearAllDownloads)
	if err := backend.AddQueueItem("playlist:a", "youtube-job", "Song", "Artist", "Album", "spotify-track"); err != nil {
		t.Fatal(err)
	}

	app := &App{
		ctx: context.Background(),
		redownloadTrackFromYouTube: func(context.Context, backend.SuspiciousRedownloadRequest) backend.SuspiciousRedownloadResult {
			return backend.SuspiciousRedownloadResult{Success: true, Status: "replaced", ReplacementPath: "replacement.mp3"}
		},
	}
	result, err := app.RedownloadTrackFromYouTube(backend.SuspiciousRedownloadRequest{QueueID: "playlist:a", JobID: "youtube-job"})
	if err != nil || !result.Success {
		t.Fatalf("result = %#v, err = %v", result, err)
	}
	queue := backend.GetDownloadQueue()
	if queue.CompletedCount != 1 || queue.FailedCount != 0 || queue.CancelledCount != 0 {
		t.Fatalf("queue terminal counts = completed:%d failed:%d cancelled:%d", queue.CompletedCount, queue.FailedCount, queue.CancelledCount)
	}
}
