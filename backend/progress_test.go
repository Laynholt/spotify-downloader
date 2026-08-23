package backend

import (
	"bytes"
	"sync"
	"testing"
)

func resetProgressStateForTest(t *testing.T) {
	t.Helper()
	ClearAllDownloads()
	SetDownloading(false)
	downloadQueueLock.Lock()
	downloadQueue = nil
	downloadQueueLock.Unlock()
}

func TestConcurrentItemProgress(t *testing.T) {
	resetProgressStateForTest(t)
	defer resetProgressStateForTest(t)

	ids := []string{"item-a", "item-b", "item-c"}
	for _, id := range ids {
		AddToQueue(id, id, "artist", "album", id)
		StartDownloadItem(id)
	}

	phases := []DownloadPhase{PhaseDownloading, PhaseConverting, PhaseFinalizing}
	var wg sync.WaitGroup
	for i, id := range ids {
		wg.Add(1)
		go func(i int, id string) {
			defer wg.Done()
			UpdateItemPhase(id, phases[i], "branch detail")
			UpdateItemTransfer(id, int64((i+1)*1024), int64((i+1)*4096), float64(i+1))
			AddItemWarning(id, "warning-"+id)
		}(i, id)
	}
	wg.Wait()

	info := GetDownloadQueue()
	if info.ActiveCount != len(ids) {
		t.Fatalf("ActiveCount = %d, want %d", info.ActiveCount, len(ids))
	}
	for i, id := range ids {
		var item DownloadItem
		for _, candidate := range info.Queue {
			if candidate.ID == id {
				item = candidate
				break
			}
		}
		if item.Phase != phases[i] {
			t.Errorf("%s phase = %q, want %q", id, item.Phase, phases[i])
		}
		if item.DownloadedBytes != int64((i+1)*1024) {
			t.Errorf("%s downloaded bytes = %d, want %d", id, item.DownloadedBytes, (i+1)*1024)
		}
		if item.TotalBytes != int64((i+1)*4096) {
			t.Errorf("%s total bytes = %d, want %d", id, item.TotalBytes, (i+1)*4096)
		}
		if item.Speed != float64(i+1) {
			t.Errorf("%s speed = %f, want %d", id, item.Speed, i+1)
		}
		if item.Warning != "warning-"+id {
			t.Errorf("%s warning = %q", id, item.Warning)
		}
	}
}

func TestStartingItemDoesNotResetAnother(t *testing.T) {
	resetProgressStateForTest(t)
	defer resetProgressStateForTest(t)

	AddToQueue("item-a", "A", "artist", "album", "a")
	AddToQueue("item-b", "B", "artist", "album", "b")
	StartDownloadItem("item-a")
	UpdateItemPhase("item-a", PhaseDownloading, "audio")
	UpdateItemTransfer("item-a", 1234, 5678, 1.25)
	StartDownloadItem("item-b")

	info := GetDownloadQueue()
	items := make(map[string]DownloadItem, len(info.Queue))
	for _, item := range info.Queue {
		items[item.ID] = item
	}
	if got := items["item-a"].DownloadedBytes; got != 1234 {
		t.Fatalf("item A bytes after starting B = %d, want 1234", got)
	}
	if got := items["item-a"].Phase; got != PhaseDownloading {
		t.Fatalf("item A phase after starting B = %q, want %q", got, PhaseDownloading)
	}
	if got := items["item-a"].Speed; got != 1.25 {
		t.Fatalf("item A speed after starting B = %f, want 1.25", got)
	}
}

func TestQueueIDsAndAttemptsStayIsolated(t *testing.T) {
	ClearAllDownloads()
	t.Cleanup(ClearAllDownloads)

	if err := AddQueueItem("playlist:a", "job-a-1", "Track", "Artist", "Album", "spotify-1"); err != nil {
		t.Fatal(err)
	}
	if err := AddQueueItem("playlist:b", "job-b-1", "Track", "Artist", "Album", "spotify-1"); err != nil {
		t.Fatal(err)
	}
	StartDownloadItem("job-a-1")
	UpdateItemPhase("job-a-1", PhaseDownloading, "audio")

	queue := GetDownloadQueue()
	if queue.Queue[0].QueueID != "playlist:a" || queue.Queue[1].QueueID != "playlist:b" {
		t.Fatalf("queue IDs lost: %#v", queue.Queue)
	}
	if queue.Queue[1].Phase != PhaseQueued {
		t.Fatalf("second attempt mutated: %#v", queue.Queue[1])
	}
}

func TestAddQueueItemRejectsEmptyAndDuplicateIDs(t *testing.T) {
	ClearAllDownloads()
	t.Cleanup(ClearAllDownloads)

	if err := AddQueueItem("playlist:a", "", "Track", "Artist", "Album", "spotify-1"); err == nil {
		t.Fatal("AddQueueItem accepted an empty job ID")
	}
	if err := AddQueueItem("playlist:a", "job-a-1", "Track", "Artist", "Album", "spotify-1"); err != nil {
		t.Fatal(err)
	}
	if err := AddQueueItem("playlist:b", "job-a-1", "Track", "Artist", "Album", "spotify-1"); err == nil {
		t.Fatal("AddQueueItem accepted a duplicate job ID")
	}
}

func TestDownloadQueueAggregate(t *testing.T) {
	resetProgressStateForTest(t)
	defer resetProgressStateForTest(t)

	for _, id := range []string{"complete", "failed", "cancelled"} {
		AddToQueue(id, id, "artist", "album", id)
		StartDownloadItem(id)
	}
	UpdateItemTransfer("complete", 2*1024*1024, 2*1024*1024, 3.5)
	UpdateItemTransfer("failed", 1024, 4096, 2.0)
	UpdateItemTransfer("cancelled", 512, 4096, 1.0)

	timings := PipelineTimings{DownloadMS: 12, FinalizationMS: 4, TotalMS: 20}
	CompleteDownloadItem("complete", "complete.mp3", 2, timings)
	FailDownloadItem("failed", "network failed")
	CancelDownloadItem("cancelled")

	info := GetDownloadQueue()
	if info.ActiveCount != 0 {
		t.Fatalf("ActiveCount = %d, want 0", info.ActiveCount)
	}
	if info.CompletedCount != 1 || info.FailedCount != 1 || info.CancelledCount != 1 {
		t.Fatalf("terminal counts = completed:%d failed:%d cancelled:%d", info.CompletedCount, info.FailedCount, info.CancelledCount)
	}
	if info.CurrentSpeed != 0 {
		t.Fatalf("CurrentSpeed = %f, want 0 after terminal states", info.CurrentSpeed)
	}

	items := make(map[string]DownloadItem, len(info.Queue))
	for _, item := range info.Queue {
		items[item.ID] = item
	}
	if items["complete"].Phase != PhaseCompleted || items["complete"].Timings != timings {
		t.Fatalf("completed item = %#v", items["complete"])
	}
	if items["failed"].Phase != PhaseFailed || items["failed"].ErrorMessage != "network failed" {
		t.Fatalf("failed item = %#v", items["failed"])
	}
	if items["cancelled"].Phase != PhaseCancelled || items["cancelled"].ErrorMessage == "" {
		t.Fatalf("cancelled item = %#v", items["cancelled"])
	}
	if info.TotalDownloaded < 1.99 || info.TotalDownloaded > 2.01 {
		t.Fatalf("TotalDownloaded = %f MB, want approximately 2 MB", info.TotalDownloaded)
	}
}

func TestProgressWriterWithIDTracksOnlyItsItem(t *testing.T) {
	resetProgressStateForTest(t)
	defer resetProgressStateForTest(t)

	AddToQueue("writer-a", "A", "artist", "album", "a")
	AddToQueue("writer-b", "B", "artist", "album", "b")
	StartDownloadItem("writer-a")
	StartDownloadItem("writer-b")

	var destinationA, destinationB bytes.Buffer
	writerA := NewProgressWriterWithID(&destinationA, "writer-a", 4)
	writerB := NewProgressWriterWithID(&destinationB, "writer-b", 3)
	if _, err := writerA.Write(bytes.Repeat([]byte{'a'}, 4)); err != nil {
		t.Fatal(err)
	}
	if _, err := writerB.Write(bytes.Repeat([]byte{'b'}, 3)); err != nil {
		t.Fatal(err)
	}

	info := GetDownloadQueue()
	items := make(map[string]DownloadItem, len(info.Queue))
	for _, item := range info.Queue {
		items[item.ID] = item
	}
	if items["writer-a"].DownloadedBytes != 4 || items["writer-a"].TotalBytes != 4 {
		t.Fatalf("writer A item = %#v", items["writer-a"])
	}
	if items["writer-b"].DownloadedBytes != 3 || items["writer-b"].TotalBytes != 3 {
		t.Fatalf("writer B item = %#v", items["writer-b"])
	}
}

func TestTerminalItemsIgnoreLateProgressPhaseAndFailure(t *testing.T) {
	terminals := []struct {
		name string
		end  func(string)
	}{
		{name: "completed", end: func(id string) {
			CompleteDownloadItem(id, "completed.mp3", 1.5, PipelineTimings{TotalMS: 17})
		}},
		{name: "failed", end: func(id string) {
			FailDownloadItem(id, "initial failure")
		}},
		{name: "cancelled", end: func(id string) {
			CancelDownloadItem(id)
		}},
		{name: "skipped", end: func(id string) {
			SkipDownloadItem(id, "skipped.mp3")
		}},
	}

	for _, terminal := range terminals {
		t.Run(terminal.name, func(t *testing.T) {
			resetProgressStateForTest(t)
			defer resetProgressStateForTest(t)

			id := "terminal-" + terminal.name
			AddToQueue(id, terminal.name, "artist", "album", id)
			StartDownloadItem(id)
			UpdateItemTransfer(id, 100, 200, 4.5)
			terminal.end(id)

			before := queueItemForTest(t, id)
			lateWriter := NewProgressWriterWithID(&bytes.Buffer{}, id, 1000)
			if _, err := lateWriter.Write(bytes.Repeat([]byte{'x'}, 32)); err != nil {
				t.Fatal(err)
			}
			UpdateItemTransfer(id, 900, 1000, 99)
			UpdateItemProgress(id, 33, 88)
			UpdateItemPhase(id, PhaseDownloading, "late phase")
			AddItemWarning(id, "late warning")
			FailDownloadItem(id, "late failure")

			after := queueItemForTest(t, id)
			if after != before {
				t.Fatalf("terminal item changed after late writes:\n before=%#v\n after=%#v", before, after)
			}
		})
	}
}

func TestLegacyProgressFallsBackToInstallerForTerminalQueue(t *testing.T) {
	resetProgressStateForTest(t)
	defer resetProgressStateForTest(t)

	SetDownloadProgress(7.5)
	SetDownloadSpeed(2.25)
	SetDownloading(true)
	AddToQueue("stale", "stale", "artist", "album", "stale")
	StartDownloadItem("stale")
	CompleteDownloadItem("stale", "stale.mp3", 1)

	legacy := GetDownloadProgress()
	if legacy.MBDownloaded != 7.5 || legacy.SpeedMBps != 2.25 || !legacy.IsDownloading {
		t.Fatalf("legacy installer progress = %#v, want 7.5 MB at 2.25 MB/s while downloading", legacy)
	}

	AddToQueue("active", "active", "artist", "album", "active")
	StartDownloadItem("active")
	UpdateItemTransfer("active", 3*bytesPerMegabyte, 4*bytesPerMegabyte, 1.5)

	queueDerived := GetDownloadProgress()
	if queueDerived.MBDownloaded != 4 || queueDerived.SpeedMBps != 1.5 || !queueDerived.IsDownloading {
		t.Fatalf("active queue aggregate = %#v, want 4 MB at 1.5 MB/s", queueDerived)
	}
}

func queueItemForTest(t *testing.T, id string) DownloadItem {
	t.Helper()
	for _, item := range GetDownloadQueue().Queue {
		if item.ID == id {
			return item
		}
	}
	t.Fatalf("queue item %q not found", id)
	return DownloadItem{}
}
