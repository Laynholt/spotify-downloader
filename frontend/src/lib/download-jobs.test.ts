import { describe, expect, it } from "vitest";
import {
    buildSpotifyCandidate,
    makeQueueId,
    makeTrackKey,
    parseSpotifyCollectionURL,
} from "./download-jobs";
import type { TrackMetadata } from "@/types/api";
import type { Settings } from "./settings";

const settings = {} as Settings;

describe("download job builders", () => {
    it("turns a missing Spotify ID into a stable unavailable candidate", () => {
        const candidate = buildSpotifyCandidate({
            queue: { id: "playlist:p1", title: "Mix", kind: "playlist" },
            track: { name: "Local", artists: "Artist" } as TrackMetadata,
            sourceIndex: 4,
            settings,
        });

        expect(candidate.trackKey).toBe("source:4");
        expect(candidate.unavailableReason).toBe("Spotify track ID is missing");
        expect(candidate.kind).toBe("spotify-download");
    });

    it("builds stable queue IDs from a source ID or supported Spotify collection URL", () => {
        expect(makeQueueId("playlist", "p1")).toBe("playlist:p1");
        expect(makeQueueId("album", "https://open.spotify.com/album/a1?si=test")).toBe("album:a1");
        expect(makeQueueId("artist", "spotify:artist:ar1")).toBe("artist:ar1");
        expect(makeQueueId("adhoc")).toBe("adhoc:single");
        expect(parseSpotifyCollectionURL("https://open.spotify.com/playlist/p1?si=test")).toEqual({
            kind: "playlist",
            id: "p1",
        });
        expect(parseSpotifyCollectionURL("https://example.com/playlist/p1")).toBeUndefined();
        expect(() => makeQueueId("playlist")).toThrow("playlist source ID is required");
        expect(() => makeQueueId("playlist", "https://open.spotify.com/artist/ar1")).toThrow("playlist URL");
        expect(() => makeQueueId("playlist", "https://example.com/playlist/p1")).toThrow("invalid playlist Spotify URL");
    });

    it("uses the Spotify ID for represented-track identity and carries resolved payload inputs", () => {
        const track = {
            spotify_id: "track-1",
            name: "Song",
            artists: "Artist",
            album_name: "Album",
        } as TrackMetadata;
        const queue = { id: "playlist:p1", title: "Mix", kind: "playlist" as const };
        const candidate = buildSpotifyCandidate({
            queue,
            track,
            sourceIndex: 2,
            settings,
            playlistOwner: "Owner",
        });

        expect(makeTrackKey(track, 2)).toBe("spotify:track-1");
        expect(candidate.trackKey).toBe("spotify:track-1");
        expect(candidate.id).toEqual(expect.any(String));
        expect(candidate.payload).toMatchObject({
            track,
            settings,
            queue,
            playlistName: "Mix",
            playlistOwner: "Owner",
            isAlbum: false,
            position: 3,
        });
    });
});
