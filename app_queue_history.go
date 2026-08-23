package main

import (
	"context"
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/afkarxyz/SpotiDownloader/backend"
	"github.com/wailsapp/wails/v2/pkg/runtime"
)

const defaultAppQueueID = "adhoc:single"

func normalizeAppQueueID(queueID string) string {
	if queueID == "" {
		return defaultAppQueueID
	}
	return queueID
}

func (a *App) beginTrack(itemID, queueID string) (context.Context, func()) {
	queueID = normalizeAppQueueID(queueID)
	parent := a.ctx
	if parent == nil {
		parent = context.Background()
	}
	trackCtx, cancel := context.WithCancel(parent)

	a.activeMu.Lock()
	if a.activeDownloads == nil {
		a.activeDownloads = make(map[string]activeDownload)
	}
	previous, hadPrevious := a.activeDownloads[itemID]
	a.activeDownloads[itemID] = activeDownload{
		QueueID: queueID,
		Context: trackCtx,
		Cancel:  cancel,
	}
	a.activeMu.Unlock()
	if hadPrevious && previous.Cancel != nil {
		previous.Cancel()
	}

	var cleanupOnce sync.Once
	cleanup := func() {
		cleanupOnce.Do(func() {
			cancel()
			a.activeMu.Lock()
			if current, ok := a.activeDownloads[itemID]; ok && current.Context == trackCtx {
				delete(a.activeDownloads, itemID)
			}
			a.activeMu.Unlock()
		})
	}
	return trackCtx, cleanup
}

// CancelDownloadQueue cancels every active track context owned by queueID.
// Each DownloadTrack call maps its resulting cancellation to the queue exactly
// once after the pipeline returns.
func (a *App) CancelDownloadQueue(queueID string) int {
	queueID = normalizeAppQueueID(queueID)

	a.activeMu.Lock()
	cancels := make([]context.CancelFunc, 0)
	targeted := 0
	for itemID, active := range a.activeDownloads {
		if active.QueueID != queueID {
			continue
		}
		delete(a.activeDownloads, itemID)
		targeted++
		if active.Cancel != nil {
			cancels = append(cancels, active.Cancel)
		}
	}
	a.activeMu.Unlock()

	for _, cancel := range cancels {
		cancel()
	}
	return targeted
}

// CancelActiveDownloads cancels every track context currently owned by the
// application. Each DownloadTrack call maps its resulting cancellation to the
// queue exactly once after the pipeline returns.
func (a *App) CancelActiveDownloads() int {
	a.activeMu.Lock()
	cancels := make([]context.CancelFunc, 0, len(a.activeDownloads))
	targeted := len(a.activeDownloads)
	for itemID, active := range a.activeDownloads {
		delete(a.activeDownloads, itemID)
		if active.Cancel != nil {
			cancels = append(cancels, active.Cancel)
		}
	}
	a.activeMu.Unlock()

	for _, cancel := range cancels {
		cancel()
	}
	return targeted
}

func (a *App) GetDownloadProgress() backend.ProgressInfo {
	return backend.GetDownloadProgress()
}

func (a *App) GetDownloadQueue() backend.DownloadQueueInfo {
	return backend.GetDownloadQueue()
}

func (a *App) ClearAllDownloads() {
	backend.ClearAllDownloads()
}

func (a *App) AddToDownloadQueue(trackID, trackName, artistName, albumName string) string {
	itemID := fmt.Sprintf("%s-%d", trackID, time.Now().UnixNano())
	backend.AddToQueue(itemID, trackName, artistName, albumName, trackID)
	return itemID
}

func (a *App) ClearCompletedDownloads() {
	backend.ClearDownloadQueue()
}

func (a *App) MarkDownloadItemFailed(itemID, errorMsg string) {
	backend.FailDownloadItem(itemID, errorMsg)
}

func (a *App) CancelDownloadItem(itemID string) {
	backend.CancelDownloadItem(itemID)
}

func (a *App) SkipDownloadItem(itemID, filePath string) {
	backend.SkipDownloadItem(itemID, filePath)
}

func (a *App) ExportFailedDownloads() (string, error) {
	queueInfo := backend.GetDownloadQueue()
	content, count, hasFailed := backend.BuildFailedDownloadsReport(queueInfo, time.Now())
	if !hasFailed {
		return "No failed downloads to export.", nil
	}

	defaultFilename := fmt.Sprintf("SpotiDownloader_%s_Failed.txt", time.Now().Format("20060102_150405"))
	path, err := runtime.SaveFileDialog(a.ctx, runtime.SaveDialogOptions{
		DefaultFilename: defaultFilename,
		Title:           "Export Failed Downloads",
		Filters: []runtime.FileFilter{{
			DisplayName: "Text Files (*.txt)",
			Pattern:     "*.txt",
		}},
	})
	if err != nil {
		return "", fmt.Errorf("failed to open save dialog: %v", err)
	}
	if path == "" {
		return "Export cancelled", nil
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		return "", fmt.Errorf("failed to write file: %v", err)
	}

	return fmt.Sprintf("Successfully exported %d failed downloads to %s", count, path), nil
}

func (a *App) GetDownloadHistory() ([]backend.HistoryItem, error) {
	return backend.GetHistoryItems("SpotiDownloader")
}

func (a *App) ClearDownloadHistory() error {
	return backend.ClearHistory("SpotiDownloader")
}

func (a *App) GetFetchHistory() ([]backend.FetchHistoryItem, error) {
	return backend.GetFetchHistoryItems("SpotiDownloader")
}

func (a *App) AddFetchHistory(item backend.FetchHistoryItem) error {
	return backend.AddFetchHistoryItem(item, "SpotiDownloader")
}

func (a *App) ClearFetchHistory() error {
	return backend.ClearFetchHistory("SpotiDownloader")
}

func (a *App) DeleteDownloadHistoryItem(id string) error {
	return backend.DeleteHistoryItem(id, "SpotiDownloader")
}

func (a *App) DeleteFetchHistoryItem(id string) error {
	return backend.DeleteFetchHistoryItem(id, "SpotiDownloader")
}

func (a *App) ClearFetchHistoryByType(itemType string) error {
	return backend.ClearFetchHistoryByType(itemType, "SpotiDownloader")
}
