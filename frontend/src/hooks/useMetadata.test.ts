import { afterAll, beforeAll, describe, expect, it, vi } from "vitest";
import type { SpotifyMetadataResponse } from "@/types/api";
import type { LoadedSpotifyMetadata } from "@/lib/download-queue-selectors";

let loadedMetadataAfterAttempt: typeof import("./useMetadata")["loadedMetadataAfterAttempt"];

beforeAll(async () => {
    vi.stubGlobal("window", { navigator: { platform: "Win32" } });
    ({ loadedMetadataAfterAttempt } = await import("./useMetadata"));
});

afterAll(() => vi.unstubAllGlobals());

function playlistMetadata(title: string): SpotifyMetadataResponse {
    return {
        playlist_info: {
            name: "",
            tracks: { total: 0 },
            followers: { total: 0 },
            owner: { display_name: "Owner", name: title, images: "" },
        },
        track_list: [],
    };
}

describe("loaded metadata source identity", () => {
    it("retains rendered collection A when fetching draft URL B fails", () => {
        const loadedA: LoadedSpotifyMetadata = {
            data: playlistMetadata("Collection A"),
            sourceUrl: "https://open.spotify.com/playlist/a",
        };

        expect(loadedMetadataAfterAttempt(
            loadedA,
            "https://open.spotify.com/album/b",
            undefined,
        )).toBe(loadedA);
    });

    it("replaces payload and source URL together for cached history B", () => {
        const loadedA: LoadedSpotifyMetadata = {
            data: playlistMetadata("Collection A"),
            sourceUrl: "https://open.spotify.com/playlist/a",
        };
        const metadataB = playlistMetadata("Collection B");

        expect(loadedMetadataAfterAttempt(
            loadedA,
            "https://open.spotify.com/playlist/b",
            metadataB,
        )).toEqual({
            data: metadataB,
            sourceUrl: "https://open.spotify.com/playlist/b",
        });
    });
});
