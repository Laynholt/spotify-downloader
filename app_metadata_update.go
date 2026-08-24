package main

import "github.com/afkarxyz/SpotiDownloader/backend"

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
	trackCtx, cleanup := a.beginTrack(req.JobID, req.QueueID)
	defer cleanup()
	backend.StartDownloadItem(req.JobID)

	run := a.redownloadTrackFromYouTube
	if run == nil {
		run = backend.RedownloadSuspiciousTrackFromYouTubeContext
	}
	result := run(trackCtx, req)
	if err := trackCtx.Err(); err != nil {
		result.Success = false
		result.Status = "cancelled"
		if result.Error == "" {
			result.Error = err.Error()
		}
	}

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
