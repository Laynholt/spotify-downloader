package main

import (
	"fmt"
	"strings"
	"sync/atomic"
	"time"

	"github.com/afkarxyz/SpotiDownloader/backend"
)

var youtubeJobSequence uint64

func (a *App) UpdateTrackMetadata(req backend.TrackMetadataUpdateRequest) (backend.TrackMetadataUpdateResult, error) {
	return backend.UpdateTrackMetadata(req), nil
}

func (a *App) UpdateTracksMetadata(reqs []backend.TrackMetadataUpdateRequest) ([]backend.TrackMetadataUpdateResult, error) {
	return backend.UpdateTracksMetadata(reqs), nil
}

func (a *App) CheckYtDlpInstalled() (backend.YtDlpStatus, error) {
	return backend.CheckYtDlpInstalled()
}

func (a *App) EnsureYtDlpInstalledOrUpdated() (backend.YtDlpStatus, error) {
	return backend.EnsureYtDlpInstalledOrUpdatedContext(a.ctx)
}

func (a *App) RedownloadSuspiciousTracksFromYouTube(reqs []backend.SuspiciousRedownloadRequest) ([]backend.SuspiciousRedownloadResult, error) {
	return backend.RedownloadSuspiciousTracksFromYouTube(reqs), nil
}

func (a *App) RedownloadTrackFromYouTube(req backend.SuspiciousRedownloadRequest) (backend.SuspiciousRedownloadResult, error) {
	req.JobID = normalizeYouTubeJobID(req.JobID, req.Metadata.TrackID)
	trackCtx, cleanup := a.beginTrack(req.JobID, req.QueueID)
	defer cleanup()
	backend.StartDownloadItem(req.JobID)

	run := a.redownloadTrackFromYouTube
	if run == nil {
		run = backend.RedownloadSuspiciousTrackFromYouTubeContext
	}
	result := run(trackCtx, req)

	switch {
	case result.Status == "cancelled":
		backend.CancelDownloadItem(req.JobID)
	case result.Success:
		completeDownloadTracking(req.JobID, result.ReplacementPath, backend.PipelineTimings{})
	default:
		backend.FailDownloadItem(req.JobID, result.Error)
	}
	return result, nil
}

func normalizeYouTubeJobID(jobID, trackID string) string {
	if jobID = strings.TrimSpace(jobID); jobID != "" {
		return jobID
	}
	prefix := backend.SanitizeFilename(strings.TrimSpace(trackID))
	if prefix == "" || prefix == "Unknown" {
		prefix = "youtube"
	}
	sequence := atomic.AddUint64(&youtubeJobSequence, 1)
	return fmt.Sprintf("%s-youtube-%d-%d", prefix, time.Now().UnixNano(), sequence)
}
