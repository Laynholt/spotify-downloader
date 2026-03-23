package main

import (
	"errors"

	"github.com/afkarxyz/SpotiDownloader/backend"
)

type LyricsDownloadRequest backend.LyricsDownloadRequest
type CoverDownloadRequest backend.CoverDownloadRequest
type HeaderDownloadRequest backend.HeaderDownloadRequest
type GalleryImageDownloadRequest backend.GalleryImageDownloadRequest
type AvatarDownloadRequest backend.AvatarDownloadRequest

func requireNonEmpty(value, errMsg string) error {
	if value == "" {
		return errors.New(errMsg)
	}
	return nil
}

func (a *App) DownloadLyrics(req LyricsDownloadRequest) (backend.LyricsDownloadResponse, error) {
	if err := requireNonEmpty(req.SpotifyID, "spotify ID is required"); err != nil {
		return backend.LyricsDownloadResponse{Success: false, Error: err.Error()}, err
	}

	resp, err := backend.NewLyricsClient().DownloadLyrics(backend.LyricsDownloadRequest(req))
	if err != nil {
		return backend.LyricsDownloadResponse{Success: false, Error: err.Error()}, err
	}
	return *resp, nil
}

func (a *App) DownloadCover(req CoverDownloadRequest) (backend.CoverDownloadResponse, error) {
	if err := requireNonEmpty(req.CoverURL, "cover URL is required"); err != nil {
		return backend.CoverDownloadResponse{Success: false, Error: err.Error()}, err
	}

	resp, err := backend.NewCoverClient().DownloadCover(backend.CoverDownloadRequest(req))
	if err != nil {
		return backend.CoverDownloadResponse{Success: false, Error: err.Error()}, err
	}
	return *resp, nil
}

func (a *App) DownloadHeader(req HeaderDownloadRequest) (backend.HeaderDownloadResponse, error) {
	if err := requireNonEmpty(req.HeaderURL, "header URL is required"); err != nil {
		return backend.HeaderDownloadResponse{Success: false, Error: err.Error()}, err
	}
	if err := requireNonEmpty(req.ArtistName, "artist name is required"); err != nil {
		return backend.HeaderDownloadResponse{Success: false, Error: err.Error()}, err
	}

	resp, err := backend.NewCoverClient().DownloadHeader(backend.HeaderDownloadRequest(req))
	if err != nil {
		return backend.HeaderDownloadResponse{Success: false, Error: err.Error()}, err
	}
	return *resp, nil
}

func (a *App) DownloadGalleryImage(req GalleryImageDownloadRequest) (backend.GalleryImageDownloadResponse, error) {
	if err := requireNonEmpty(req.ImageURL, "image URL is required"); err != nil {
		return backend.GalleryImageDownloadResponse{Success: false, Error: err.Error()}, err
	}
	if err := requireNonEmpty(req.ArtistName, "artist name is required"); err != nil {
		return backend.GalleryImageDownloadResponse{Success: false, Error: err.Error()}, err
	}

	resp, err := backend.NewCoverClient().DownloadGalleryImage(backend.GalleryImageDownloadRequest(req))
	if err != nil {
		return backend.GalleryImageDownloadResponse{Success: false, Error: err.Error()}, err
	}
	return *resp, nil
}

func (a *App) DownloadAvatar(req AvatarDownloadRequest) (backend.AvatarDownloadResponse, error) {
	if err := requireNonEmpty(req.AvatarURL, "avatar URL is required"); err != nil {
		return backend.AvatarDownloadResponse{Success: false, Error: err.Error()}, err
	}
	if err := requireNonEmpty(req.ArtistName, "artist name is required"); err != nil {
		return backend.AvatarDownloadResponse{Success: false, Error: err.Error()}, err
	}

	resp, err := backend.NewCoverClient().DownloadAvatar(backend.AvatarDownloadRequest(req))
	if err != nil {
		return backend.AvatarDownloadResponse{Success: false, Error: err.Error()}, err
	}
	return *resp, nil
}
