package main

import "github.com/afkarxyz/SpotiDownloader/backend"

func (a *App) UpdateTrackMetadata(req backend.TrackMetadataUpdateRequest) (backend.TrackMetadataUpdateResult, error) {
	return backend.UpdateTrackMetadata(req), nil
}

func (a *App) UpdateTracksMetadata(reqs []backend.TrackMetadataUpdateRequest) ([]backend.TrackMetadataUpdateResult, error) {
	return backend.UpdateTracksMetadata(reqs), nil
}
