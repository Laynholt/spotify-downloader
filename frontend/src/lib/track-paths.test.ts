// @vitest-environment jsdom
import { describe, expect, it, vi } from "vitest";
vi.mock("../../wailsjs/go/main/App", () => ({}));
import { DEFAULT_SETTINGS } from "./settings";
import { buildBatchTrackPathInfo } from "./track-paths";

describe("shared audio, cover and lyrics paths", () => {
    const track = { name: "Song/Part", artists: "Artist/Group", album_name: "Album/Deluxe",
        album_artist: "Artist/Group", release_date: "2024-03-20T00:00:00Z", disc_number: 2, track_number: 7 };
    it("uses sanitized metadata, dates, discs and album numbering in every directory component", () => {
        const info = buildBatchTrackPathInfo(track, { ...DEFAULT_SETTINGS, operatingSystem: "Windows",
            downloadPath: "C:\\Music", folderTemplate: "{artist}/{year}/{album}/{disc}/{track}", createPlaylistFolder: false }, "Playlist", true, 3);
        expect(info.targetOutputDir).toBe("C:\\Music\\Artist Group\\2024\\Album Deluxe\\2\\07");
        expect(info.trackPosition).toBe(7);
    });
    it("honors the playlist-folder setting and preserves source position when no template is used", () => {
        const settings = { ...DEFAULT_SETTINGS, operatingSystem: "linux/MacOS" as const, downloadPath: "/music", folderTemplate: "", createPlaylistFolder: false };
        expect(buildBatchTrackPathInfo(track, settings, "Playlist/Name", false, 3)).toMatchObject({ targetOutputDir: "/music", trackPosition: 3 });
        expect(buildBatchTrackPathInfo(track, { ...settings, createPlaylistFolder: true }, "Playlist/Name", false, 3).targetOutputDir).toBe("/music/Playlist Name");
    });
});
