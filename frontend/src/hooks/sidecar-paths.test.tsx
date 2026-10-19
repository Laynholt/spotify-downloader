// @vitest-environment jsdom
import { act, useLayoutEffect } from "react";
import { afterEach, expect, it, vi } from "vitest";
import { mount } from "@/test/render";
import { useCover } from "./useCover";
import { useLyrics } from "./useLyrics";

const api = vi.hoisted(() => ({
    cover: vi.fn<(request: unknown) => Promise<{ success: boolean }>>(async () => ({ success: true })),
    lyrics: vi.fn<(request: unknown) => Promise<{ success: boolean }>>(async () => ({ success: true })),
}));
vi.mock("../../wailsjs/go/main/App", () => ({ DownloadCover: api.cover, DownloadLyrics: api.lyrics }));
vi.mock("sonner", () => ({ toast: { success: vi.fn(), info: vi.fn(), error: vi.fn() } }));
vi.mock("@/lib/logger", () => ({ logger: { info: vi.fn(), error: vi.fn() } }));
vi.mock("@/lib/settings", async importOriginal => {
    const settings = await importOriginal<typeof import("@/lib/settings")>();
    return { ...settings, getSettingsWithDefaults: vi.fn(async () => ({ ...settings.DEFAULT_SETTINGS,
        operatingSystem: "Windows", downloadPath: "C:\\Music", createPlaylistFolder: false, folderTemplate: "{album}/{disc}/{track}" })) };
});

afterEach(() => { vi.unstubAllGlobals(); vi.clearAllMocks(); });

it("single and bulk cover/lyrics downloads honor the same playlist setting, disc and track folders as audio", async () => {
    vi.stubGlobal("IS_REACT_ACT_ENVIRONMENT", true);
    let cover!: ReturnType<typeof useCover>;
    let lyrics!: ReturnType<typeof useLyrics>;
    function Probe() {
        const c = useCover(), l = useLyrics();
        useLayoutEffect(() => { cover = c; lyrics = l; });
        return null;
    }
    const view = await mount(<Probe />);
    try {
        await act(async () => {
            await cover.handleDownloadCover("https://example.com/cover.jpg", "Song", "Artist", "Album/Deluxe", "Playlist", false, 7, "id", "Artist", "2024-03-20", 2, true);
            await lyrics.handleDownloadLyrics("id", "Song", "Artist", "Album/Deluxe", "Playlist", false, 7, "Artist", "2024-03-20", 2, true);
            const tracks = [{ name: "Song", artists: "Artist", album_name: "Album/Deluxe", images: "https://example.com/cover.jpg",
                spotify_id: "id", track_number: 7, disc_number: 2, release_date: "2024-03-20", external_urls: "", duration_ms: 1000 }];
            await cover.handleDownloadAllCovers(tracks, "Playlist", false, true);
            await lyrics.handleDownloadAllLyrics(tracks, "Playlist", false, true);
        });
        expect(api.cover).toHaveBeenCalledTimes(2);
        expect(api.lyrics).toHaveBeenCalledTimes(2);
        for (const [request] of [...api.cover.mock.calls, ...api.lyrics.mock.calls]) {
            expect(request).toMatchObject({ output_dir: "C:\\Music\\Album Deluxe\\2\\07", position: 7, disc_number: 2 });
        }
    } finally { await view.unmount(); }
});
