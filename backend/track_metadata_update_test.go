package backend

import "testing"

func TestBuildTrackMetadataPayloadMapsRequestFields(t *testing.T) {
	req := TrackMetadataUpdateRequest{
		TrackID:      "spotify-track",
		TrackName:    "Title",
		ArtistName:   "Artist",
		AlbumName:    "Album",
		AlbumArtist:  "Album Artist",
		ReleaseDate:  "2026-06-09",
		TrackNumber:  7,
		TotalTracks:  12,
		DiscNumber:   2,
		TotalDiscs:   3,
		Copyright:    "Copyright",
		Publisher:    "Publisher",
		ISRC:         "ISRC123",
		Genre:        "Pop",
	}

	got := BuildTrackMetadataPayload(req)

	if got.Title != req.TrackName || got.Artist != req.ArtistName || got.Album != req.AlbumName {
		t.Fatalf("basic metadata mismatch: %#v", got)
	}
	if got.URL != "https://open.spotify.com/track/spotify-track" {
		t.Fatalf("URL = %q", got.URL)
	}
	if got.TrackNumber != 7 || got.TotalTracks != 12 || got.DiscNumber != 2 || got.TotalDiscs != 3 {
		t.Fatalf("number metadata mismatch: %#v", got)
	}
	if got.ISRC != req.ISRC || got.Genre != req.Genre {
		t.Fatalf("extended metadata mismatch: %#v", got)
	}
}

func TestBuildYtDlpSearchQueryUsesArtistAndTitle(t *testing.T) {
	got := BuildYtDlpSearchQuery("Artist", "Song")
	want := "ytsearch1:Artist - Song audio"
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}
