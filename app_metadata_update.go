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
	return backend.EnsureYtDlpInstalledOrUpdated()
}

func (a *App) RedownloadSuspiciousTracksFromYouTube(reqs []backend.SuspiciousRedownloadRequest) ([]backend.SuspiciousRedownloadResult, error) {
	return backend.RedownloadSuspiciousTracksFromYouTube(reqs), nil
}
