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
			return backend.SuspiciousRedownloadResult{Status: "cancelled", Error: ctx.Err().Error()}
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

func TestRedownloadTrackFromYouTubeKeepsCommittedSuccessAfterLateCancellation(t *testing.T) {
	backend.ClearAllDownloads()
	t.Cleanup(backend.ClearAllDownloads)
	const queueID = "playlist:late-success"
	const jobID = "youtube-late-success"
	if err := backend.AddQueueItem(queueID, jobID, "Song", "Artist", "Album", "spotify-track"); err != nil {
		t.Fatal(err)
	}

	app := &App{ctx: context.Background()}
	app.redownloadTrackFromYouTube = func(context.Context, backend.SuspiciousRedownloadRequest) backend.SuspiciousRedownloadResult {
		if got := app.CancelDownloadQueue(queueID); got != 1 {
			t.Errorf("CancelDownloadQueue() = %d, want 1", got)
		}
		return backend.SuspiciousRedownloadResult{Success: true, Status: "replaced", ReplacementPath: "replacement.mp3"}
	}

	result, err := app.RedownloadTrackFromYouTube(backend.SuspiciousRedownloadRequest{QueueID: queueID, JobID: jobID})
	if err != nil || !result.Success || result.Status != "replaced" {
		t.Fatalf("result = %#v, err = %v", result, err)
	}
	queue := backend.GetDownloadQueue()
	if queue.CompletedCount != 1 || queue.CancelledCount != 0 || queue.FailedCount != 0 {
		t.Fatalf("queue terminal counts = completed:%d failed:%d cancelled:%d", queue.CompletedCount, queue.FailedCount, queue.CancelledCount)
	}
}

func TestRedownloadTrackFromYouTubeKeepsRollbackFailureAfterLateCancellation(t *testing.T) {
	backend.ClearAllDownloads()
	t.Cleanup(backend.ClearAllDownloads)
	const queueID = "playlist:late-rollback"
	const jobID = "youtube-late-rollback"
	if err := backend.AddQueueItem(queueID, jobID, "Song", "Artist", "Album", "spotify-track"); err != nil {
		t.Fatal(err)
	}

	want := backend.SuspiciousRedownloadResult{Status: "failed", Stage: "rollback", Error: "publish failed; restore denied"}
	app := &App{ctx: context.Background()}
	app.redownloadTrackFromYouTube = func(context.Context, backend.SuspiciousRedownloadRequest) backend.SuspiciousRedownloadResult {
		if got := app.CancelDownloadQueue(queueID); got != 1 {
			t.Errorf("CancelDownloadQueue() = %d, want 1", got)
		}
		return want
	}

	result, err := app.RedownloadTrackFromYouTube(backend.SuspiciousRedownloadRequest{QueueID: queueID, JobID: jobID})
	if err != nil || result != want {
		t.Fatalf("result = %#v, want %#v; err = %v", result, want, err)
	}
	queue := backend.GetDownloadQueue()
	if queue.FailedCount != 1 || queue.CancelledCount != 0 || queue.CompletedCount != 0 {
		t.Fatalf("queue terminal counts = completed:%d failed:%d cancelled:%d", queue.CompletedCount, queue.FailedCount, queue.CancelledCount)
	}
}

func TestRedownloadTrackFromYouTubeRegistersConcurrentMissingIDsSeparately(t *testing.T) {
	backend.ClearAllDownloads()
	t.Cleanup(backend.ClearAllDownloads)
	type startedCall struct {
		jobID string
		ctx   context.Context
	}
	started := make(chan startedCall, 2)
	release := make(chan struct{})
	app := &App{ctx: context.Background()}
	app.redownloadTrackFromYouTube = func(ctx context.Context, req backend.SuspiciousRedownloadRequest) backend.SuspiciousRedownloadResult {
		started <- startedCall{jobID: req.JobID, ctx: ctx}
		<-release
		return backend.SuspiciousRedownloadResult{Success: true, Status: "replaced", ReplacementPath: req.JobID + ".mp3"}
	}
	req := backend.SuspiciousRedownloadRequest{
		QueueID:  "playlist:missing-ids",
		Metadata: backend.TrackMetadataUpdateRequest{TrackID: "spotify-track"},
	}

	var wg sync.WaitGroup
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := app.RedownloadTrackFromYouTube(req); err != nil {
				t.Errorf("RedownloadTrackFromYouTube: %v", err)
			}
		}()
	}
	calls := []startedCall{waitForStartedRedownload(t, started), waitForStartedRedownload(t, started)}
	idsValid := calls[0].jobID != "" && calls[1].jobID != "" && calls[0].jobID != calls[1].jobID
	app.activeMu.Lock()
	activeCount := len(app.activeDownloads)
	registryValid := activeCount == 2
	for _, call := range calls {
		active, ok := app.activeDownloads[call.jobID]
		if !ok || active.Context != call.ctx || active.QueueID != req.QueueID {
			registryValid = false
		}
	}
	app.activeMu.Unlock()
	close(release)
	wg.Wait()
	if !idsValid {
		t.Fatalf("forwarded job IDs = %q and %q", calls[0].jobID, calls[1].jobID)
	}
	if !registryValid {
		t.Fatalf("active registry did not contain both forwarded jobs; size was %d", activeCount)
	}
}

func TestRedownloadTrackFromYouTubeCancelsBothConcurrentMissingIDJobs(t *testing.T) {
	backend.ClearAllDownloads()
	t.Cleanup(backend.ClearAllDownloads)
	started := make(chan string, 2)
	results := make(chan backend.SuspiciousRedownloadResult, 2)
	app := &App{ctx: context.Background()}
	app.redownloadTrackFromYouTube = func(ctx context.Context, req backend.SuspiciousRedownloadRequest) backend.SuspiciousRedownloadResult {
		started <- req.JobID
		<-ctx.Done()
		return backend.SuspiciousRedownloadResult{Status: "cancelled", Error: ctx.Err().Error()}
	}
	req := backend.SuspiciousRedownloadRequest{
		QueueID:  "playlist:missing-id-cancel",
		Metadata: backend.TrackMetadataUpdateRequest{TrackID: "spotify-track"},
	}
	start := func() {
		go func() {
			result, _ := app.RedownloadTrackFromYouTube(req)
			results <- result
		}()
	}

	start()
	firstID := waitForStartedRedownload(t, started)
	start()
	secondID := waitForStartedRedownload(t, started)
	targeted := app.CancelDownloadQueue(req.QueueID)
	for range 2 {
		select {
		case result := <-results:
			if result.Status != "cancelled" || result.Success {
				t.Fatalf("result = %#v", result)
			}
		case <-time.After(time.Second):
			t.Fatal("timed out waiting for cancelled missing-ID job")
		}
	}
	if firstID == "" || secondID == "" || firstID == secondID {
		t.Fatalf("forwarded job IDs = %q and %q", firstID, secondID)
	}
	if targeted != 2 {
		t.Fatalf("CancelDownloadQueue() = %d, want 2", targeted)
	}
}

func waitForStartedRedownload[T any](t *testing.T, started <-chan T) T {
	t.Helper()
	select {
	case call := <-started:
		return call
	case <-time.After(time.Second):
		var zero T
		t.Fatal("timed out waiting for YouTube redownload")
		return zero
	}
}
