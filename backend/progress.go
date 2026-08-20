package backend

import (
	"fmt"
	"io"
	"strings"
	"sync"
	"time"
)

const bytesPerMegabyte = int64(1024 * 1024)

type DownloadStatus string

const (
	StatusQueued      DownloadStatus = "queued"
	StatusDownloading DownloadStatus = "downloading"
	StatusCompleted   DownloadStatus = "completed"
	StatusFailed      DownloadStatus = "failed"
	StatusSkipped     DownloadStatus = "skipped"
	StatusCancelled   DownloadStatus = "cancelled"
)

type DownloadPhase string

const (
	PhaseQueued      DownloadPhase = "queued"
	PhasePreparing   DownloadPhase = "preparing"
	PhaseDownloading DownloadPhase = "downloading"
	PhaseConverting  DownloadPhase = "converting"
	PhaseFinalizing  DownloadPhase = "finalizing"
	PhaseCompleted   DownloadPhase = "completed"
	PhaseFailed      DownloadPhase = "failed"
	PhaseCancelled   DownloadPhase = "cancelled"
)

type PipelineTimings struct {
	PreparationMS   int64 `json:"preparation_ms"`
	DownloadMS      int64 `json:"download_ms"`
	ConversionMS    int64 `json:"conversion_ms"`
	CoverMS         int64 `json:"cover_ms"`
	MetadataMS      int64 `json:"metadata_ms"`
	LyricsMS        int64 `json:"lyrics_ms"`
	DurationProbeMS int64 `json:"duration_probe_ms"`
	FinalizationMS  int64 `json:"finalization_ms"`
	TotalMS         int64 `json:"total_ms"`
}

type DownloadItem struct {
	ID           string         `json:"id"`
	TrackName    string         `json:"track_name"`
	ArtistName   string         `json:"artist_name"`
	AlbumName    string         `json:"album_name"`
	SpotifyID    string         `json:"spotify_id"`
	Status       DownloadStatus `json:"status"`
	Progress     float64        `json:"progress"`
	TotalSize    float64        `json:"total_size"`
	Speed        float64        `json:"speed"`
	StartTime    int64          `json:"start_time"`
	EndTime      int64          `json:"end_time"`
	ErrorMessage string         `json:"error_message"`
	FilePath     string         `json:"file_path"`

	Phase           DownloadPhase   `json:"phase"`
	Detail          string          `json:"detail"`
	DownloadedBytes int64           `json:"downloaded_bytes"`
	TotalBytes      int64           `json:"total_bytes"`
	Warning         string          `json:"warning"`
	Timings         PipelineTimings `json:"timings"`
}

var (
	downloadQueue     []DownloadItem
	downloadQueueLock sync.RWMutex

	sessionStartTime int64
	sessionStartLock sync.RWMutex

	// Installer progress is intentionally separate from queue-item progress. The
	// FFmpeg installer still uses the legacy setters, while track writers update
	// their item directly by ID.
	installerProgress    float64
	installerSpeed       float64
	installerDownloading bool
	installerStateLock   sync.RWMutex
)

type ProgressInfo struct {
	IsDownloading bool    `json:"is_downloading"`
	MBDownloaded  float64 `json:"mb_downloaded"`
	SpeedMBps     float64 `json:"speed_mbps"`
}

type DownloadQueueInfo struct {
	IsDownloading    bool           `json:"is_downloading"`
	Queue            []DownloadItem `json:"queue"`
	CurrentSpeed     float64        `json:"current_speed"`
	TotalDownloaded  float64        `json:"total_downloaded"`
	SessionStartTime int64          `json:"session_start_time"`
	QueuedCount      int            `json:"queued_count"`
	CompletedCount   int            `json:"completed_count"`
	FailedCount      int            `json:"failed_count"`
	SkippedCount     int            `json:"skipped_count"`
	ActiveCount      int            `json:"active_count"`
	CancelledCount   int            `json:"cancelled_count"`
}

func GetDownloadProgress() ProgressInfo {
	queue := GetDownloadQueue()
	if queue.ActiveCount == 0 && queue.QueuedCount == 0 {
		installerStateLock.RLock()
		installer := ProgressInfo{
			IsDownloading: installerDownloading,
			MBDownloaded:  installerProgress,
			SpeedMBps:     installerSpeed,
		}
		installerStateLock.RUnlock()
		return installer
	}
	return ProgressInfo{
		IsDownloading: queue.IsDownloading,
		MBDownloaded:  queue.TotalDownloaded,
		SpeedMBps:     queue.CurrentSpeed,
	}
}

// SetDownloadSpeed and SetDownloadProgress remain for the FFmpeg installer
// and other legacy callers that do not own a queue item. Track-scoped writers
// never call these setters.
func SetDownloadSpeed(mbps float64) {
	installerStateLock.Lock()
	installerSpeed = mbps
	installerStateLock.Unlock()
}

func SetDownloadProgress(mbDownloaded float64) {
	installerStateLock.Lock()
	installerProgress = mbDownloaded
	installerStateLock.Unlock()
}

func SetDownloading(downloading bool) {
	installerStateLock.Lock()
	installerDownloading = downloading
	if !downloading {
		installerProgress = 0
		installerSpeed = 0
	}
	installerStateLock.Unlock()
}

type ProgressWriter struct {
	writer      io.Writer
	total       int64
	totalBytes  int64
	lastPrinted int64
	startTime   int64
	lastTime    int64
	lastBytes   int64
	itemID      string
	mu          sync.Mutex
}

func NewProgressWriter(writer io.Writer, totalBytes ...int64) *ProgressWriter {
	total := int64(0)
	if len(totalBytes) > 0 {
		total = totalBytes[0]
	}
	now := getCurrentTimeMillis()
	return &ProgressWriter{
		writer:     writer,
		totalBytes: total,
		startTime:  now,
		lastTime:   now,
	}
}

func NewProgressWriterWithID(writer io.Writer, itemID string, totalBytes ...int64) *ProgressWriter {
	pw := NewProgressWriter(writer, totalBytes...)
	pw.itemID = itemID
	return pw
}

func getCurrentTimeMillis() int64 {
	return time.Now().UnixMilli()
}

func (pw *ProgressWriter) Write(p []byte) (int, error) {
	pw.mu.Lock()
	defer pw.mu.Unlock()

	n, err := pw.writer.Write(p)
	if n < 0 {
		n = 0
	}
	pw.total += int64(n)

	now := getCurrentTimeMillis()
	elapsed := float64(now-pw.lastTime) / 1000
	bytesDiff := pw.total - pw.lastBytes
	speedMBps := float64(0)
	if elapsed > 0 {
		speedMBps = (float64(bytesDiff) / float64(bytesPerMegabyte)) / elapsed
	}

	if pw.itemID != "" {
		UpdateItemTransfer(pw.itemID, pw.total, pw.totalBytes, speedMBps)
	} else {
		SetDownloadProgress(float64(pw.total) / float64(bytesPerMegabyte))
		if speedMBps > 0 {
			SetDownloadSpeed(speedMBps)
		}
	}

	if pw.total-pw.lastPrinted >= 256*1024 {
		mbDownloaded := float64(pw.total) / float64(bytesPerMegabyte)
		if elapsed > 0 {
			fmt.Printf("\rDownloaded: %.2f MB (%.2f MB/s)", mbDownloaded, speedMBps)
		} else {
			fmt.Printf("\rDownloaded: %.2f MB", mbDownloaded)
		}
		pw.lastPrinted = pw.total
		pw.lastTime = now
		pw.lastBytes = pw.total
	}

	return n, err
}

func (pw *ProgressWriter) GetTotal() int64 {
	pw.mu.Lock()
	defer pw.mu.Unlock()
	return pw.total
}

// GetCurrentItemID is retained for old bindings. It derives the first active
// item from the queue snapshot and does not maintain process-global item state.
func GetCurrentItemID() string {
	downloadQueueLock.RLock()
	defer downloadQueueLock.RUnlock()
	for _, item := range downloadQueue {
		if item.Status == StatusDownloading {
			return item.ID
		}
	}
	return ""
}

func AddToQueue(id, trackName, artistName, albumName, spotifyID string) {
	downloadQueueLock.Lock()
	downloadQueue = append(downloadQueue, DownloadItem{
		ID:         id,
		TrackName:  trackName,
		ArtistName: artistName,
		AlbumName:  albumName,
		SpotifyID:  spotifyID,
		Status:     StatusQueued,
		Phase:      PhaseQueued,
	})
	downloadQueueLock.Unlock()

	sessionStartLock.Lock()
	if sessionStartTime == 0 {
		sessionStartTime = time.Now().Unix()
	}
	sessionStartLock.Unlock()
}

func StartDownloadItem(id string) {
	downloadQueueLock.Lock()
	defer downloadQueueLock.Unlock()

	for i := range downloadQueue {
		if downloadQueue[i].ID != id {
			continue
		}
		if isTerminalStatus(downloadQueue[i].Status) {
			return
		}
		downloadQueue[i].Status = StatusDownloading
		downloadQueue[i].Phase = PhasePreparing
		downloadQueue[i].Detail = ""
		downloadQueue[i].StartTime = time.Now().Unix()
		downloadQueue[i].Progress = 0
		downloadQueue[i].DownloadedBytes = 0
		downloadQueue[i].Speed = 0
		return
	}
}

func UpdateItemTransfer(id string, downloaded, total int64, speedMBps float64) {
	if downloaded < 0 {
		downloaded = 0
	}
	if total < 0 {
		total = 0
	}

	downloadQueueLock.Lock()
	defer downloadQueueLock.Unlock()
	for i := range downloadQueue {
		if downloadQueue[i].ID != id {
			continue
		}
		if isTerminalStatus(downloadQueue[i].Status) {
			return
		}
		downloadQueue[i].DownloadedBytes = downloaded
		downloadQueue[i].TotalBytes = total
		downloadQueue[i].Progress = float64(downloaded) / float64(bytesPerMegabyte)
		downloadQueue[i].TotalSize = float64(total) / float64(bytesPerMegabyte)
		downloadQueue[i].Speed = speedMBps
		return
	}
}

// UpdateItemProgress is the pre-item-bytes compatibility API. New callers
// should use UpdateItemTransfer so totals remain lossless.
func UpdateItemProgress(id string, progress, speed float64) {
	if progress < 0 {
		progress = 0
	}
	downloaded := int64(progress * float64(bytesPerMegabyte))
	downloadQueueLock.Lock()
	defer downloadQueueLock.Unlock()
	for i := range downloadQueue {
		if downloadQueue[i].ID != id {
			continue
		}
		if isTerminalStatus(downloadQueue[i].Status) {
			return
		}
		downloadQueue[i].DownloadedBytes = downloaded
		downloadQueue[i].Progress = progress
		downloadQueue[i].Speed = speed
		return
	}
}

func UpdateItemPhase(id string, phase DownloadPhase, detail string) {
	downloadQueueLock.Lock()
	defer downloadQueueLock.Unlock()
	for i := range downloadQueue {
		if downloadQueue[i].ID != id || isTerminalStatus(downloadQueue[i].Status) {
			continue
		}
		downloadQueue[i].Phase = phase
		downloadQueue[i].Detail = detail
		return
	}
}

func AddItemWarning(id, warning string) {
	warning = strings.TrimSpace(warning)
	if warning == "" {
		return
	}
	downloadQueueLock.Lock()
	defer downloadQueueLock.Unlock()
	for i := range downloadQueue {
		if downloadQueue[i].ID != id || isTerminalStatus(downloadQueue[i].Status) {
			continue
		}
		if downloadQueue[i].Warning == "" {
			downloadQueue[i].Warning = warning
		} else if !strings.Contains(downloadQueue[i].Warning, warning) {
			downloadQueue[i].Warning += "; " + warning
		}
		return
	}
}

func CompleteDownloadItem(id, filePath string, finalSize float64, timings ...PipelineTimings) {
	var pipelineTimings PipelineTimings
	if len(timings) > 0 {
		pipelineTimings = timings[0]
	}

	downloadQueueLock.Lock()
	defer downloadQueueLock.Unlock()
	for i := range downloadQueue {
		item := &downloadQueue[i]
		if item.ID != id || isTerminalStatus(item.Status) {
			continue
		}
		item.Status = StatusCompleted
		item.Phase = PhaseCompleted
		item.Detail = ""
		item.EndTime = time.Now().Unix()
		item.FilePath = filePath
		item.Progress = finalSize
		item.Speed = 0
		item.Timings = pipelineTimings
		if item.DownloadedBytes == 0 && finalSize > 0 {
			item.DownloadedBytes = int64(finalSize * float64(bytesPerMegabyte))
		}
		if item.TotalBytes == 0 {
			item.TotalBytes = item.DownloadedBytes
		}
		if item.TotalSize == 0 && item.TotalBytes > 0 {
			item.TotalSize = float64(item.TotalBytes) / float64(bytesPerMegabyte)
		}
		return
	}
}

func SkipDownloadItem(id, filePath string) {
	downloadQueueLock.Lock()
	defer downloadQueueLock.Unlock()
	for i := range downloadQueue {
		item := &downloadQueue[i]
		if item.ID != id || isTerminalStatus(item.Status) {
			continue
		}
		item.Status = StatusSkipped
		item.EndTime = time.Now().Unix()
		item.FilePath = filePath
		item.Speed = 0
		item.Phase = PhaseCompleted
		return
	}
}

func FailDownloadItem(id, errorMsg string) {
	downloadQueueLock.Lock()
	defer downloadQueueLock.Unlock()
	for i := range downloadQueue {
		item := &downloadQueue[i]
		if item.ID != id || isTerminalStatus(item.Status) {
			continue
		}
		item.Status = StatusFailed
		item.Phase = PhaseFailed
		item.EndTime = time.Now().Unix()
		item.ErrorMessage = errorMsg
		item.Speed = 0
		return
	}
}

func CancelDownloadItem(id string) {
	downloadQueueLock.Lock()
	defer downloadQueueLock.Unlock()
	for i := range downloadQueue {
		item := &downloadQueue[i]
		if item.ID != id || isTerminalStatus(item.Status) {
			continue
		}
		item.Status = StatusCancelled
		item.Phase = PhaseCancelled
		item.EndTime = time.Now().Unix()
		if item.ErrorMessage == "" {
			item.ErrorMessage = "Cancelled"
		}
		item.Speed = 0
		return
	}
}

func GetDownloadQueue() DownloadQueueInfo {
	installerStateLock.RLock()
	installer := struct {
		downloading bool
		progress    float64
		speed       float64
	}{installerDownloading, installerProgress, installerSpeed}
	installerStateLock.RUnlock()

	sessionStartLock.RLock()
	sessionStart := sessionStartTime
	sessionStartLock.RUnlock()

	downloadQueueLock.RLock()
	queueCopy := make([]DownloadItem, len(downloadQueue))
	copy(queueCopy, downloadQueue)

	var queued, completed, failed, skipped, cancelled, active int
	var speed float64
	var downloadedBytes int64
	for _, item := range downloadQueue {
		switch item.Status {
		case StatusQueued:
			queued++
		case StatusCompleted:
			completed++
		case StatusFailed:
			failed++
		case StatusSkipped:
			skipped++
		case StatusCancelled:
			cancelled++
		case StatusDownloading:
			active++
			speed += item.Speed
		}
		downloadedBytes += item.DownloadedBytes
		if item.DownloadedBytes == 0 && item.Progress > 0 {
			downloadedBytes += int64(item.Progress * float64(bytesPerMegabyte))
		}
	}
	downloadQueueLock.RUnlock()

	totalDownloaded := float64(downloadedBytes) / float64(bytesPerMegabyte)
	currentSpeed := speed

	return DownloadQueueInfo{
		IsDownloading:    active > 0 || installer.downloading,
		Queue:            queueCopy,
		CurrentSpeed:     currentSpeed,
		TotalDownloaded:  totalDownloaded,
		SessionStartTime: sessionStart,
		QueuedCount:      queued,
		CompletedCount:   completed,
		FailedCount:      failed,
		SkippedCount:     skipped,
		ActiveCount:      active,
		CancelledCount:   cancelled,
	}
}

func ClearDownloadQueue() {
	downloadQueueLock.Lock()
	defer downloadQueueLock.Unlock()
	newQueue := make([]DownloadItem, 0, len(downloadQueue))
	for _, item := range downloadQueue {
		if item.Status == StatusQueued || item.Status == StatusDownloading {
			newQueue = append(newQueue, item)
		}
	}
	downloadQueue = newQueue
}

func ClearAllDownloads() {
	downloadQueueLock.Lock()
	downloadQueue = nil
	downloadQueueLock.Unlock()

	sessionStartLock.Lock()
	sessionStartTime = 0
	sessionStartLock.Unlock()

	installerStateLock.Lock()
	installerProgress = 0
	installerSpeed = 0
	installerDownloading = false
	installerStateLock.Unlock()
}

func CancelAllQueuedItems() {
	downloadQueueLock.Lock()
	defer downloadQueueLock.Unlock()
	for i := range downloadQueue {
		if downloadQueue[i].Status == StatusQueued {
			downloadQueue[i].Status = StatusCancelled
			downloadQueue[i].Phase = PhaseCancelled
			downloadQueue[i].EndTime = time.Now().Unix()
			downloadQueue[i].ErrorMessage = "Cancelled"
		}
	}
}

func ResetSessionIfComplete() {
	downloadQueueLock.RLock()
	hasActiveOrQueued := false
	for _, item := range downloadQueue {
		if item.Status == StatusQueued || item.Status == StatusDownloading {
			hasActiveOrQueued = true
			break
		}
	}
	downloadQueueLock.RUnlock()
	if hasActiveOrQueued {
		return
	}

	sessionStartLock.Lock()
	sessionStartTime = 0
	sessionStartLock.Unlock()
}

func isTerminalStatus(status DownloadStatus) bool {
	switch status {
	case StatusCompleted, StatusFailed, StatusSkipped, StatusCancelled:
		return true
	default:
		return false
	}
}
