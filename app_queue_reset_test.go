package main

import (
	"context"
	"testing"
	"time"

	"github.com/afkarxyz/SpotiDownloader/backend"
)

func TestClearAllDownloadsCancelsAndWaitsForActiveWork(t *testing.T) {
	backend.ClearAllDownloads()
	t.Cleanup(backend.ClearAllDownloads)
	started, finished := make(chan struct{}), make(chan struct{})
	app := &App{ctx: context.Background(), runTrackPipeline: func(ctx context.Context, _ backend.TrackPipelineRequest) (backend.TrackPipelineResult, error) {
		close(started)
		<-ctx.Done()
		return backend.TrackPipelineResult{}, ctx.Err()
	}}
	t.Cleanup(func() { app.CancelActiveDownloads() })
	go func() {
		defer close(finished)
		_, _ = app.DownloadTrack(DownloadRequest{TrackID: "reset-test", SessionToken: "token", OutputDir: t.TempDir()})
	}()
	<-started
	app.ClearAllDownloads()
	select {
	case <-finished:
	case <-time.After(time.Second):
		t.Fatal("reset left the active download running")
	}
	if queue := backend.GetDownloadQueue(); len(queue.Queue) != 0 {
		t.Fatalf("reset queue = %#v, want empty", queue.Queue)
	}
}
