// @vitest-environment jsdom
import { act, useLayoutEffect } from "react";
import { createRoot } from "react-dom/client";
import { expect, it, vi } from "vitest";
import type { DownloadQueueHarnessOptions } from "./useDownloadQueues";
import type { DownloadQueueSnapshot } from "@/lib/download-queue-manager";
import type { MediaJobPayload, MediaJobResult } from "@/lib/download-jobs";
import type { Settings } from "@/lib/settings";
import { useDownload } from "./useDownload";
import { logger } from "@/lib/logger";

const harness = vi.hoisted(() => ({ options: undefined as DownloadQueueHarnessOptions | undefined,
    snapshot: { queues: {}, activeCount: 0, peakActive: 0 }, clearCompletedQueues: vi.fn(async () => {}), startNewSession: vi.fn(async () => {}),
    createM3u8: vi.fn(async () => "created") }));
vi.mock("./useDownloadQueues", () => ({ useDownloadQueues: (options: DownloadQueueHarnessOptions) => {
    harness.options = options;
    return harness;
}, applyYouTubeResultToSummary: vi.fn() }));
vi.mock("../../wailsjs/go/main/App", () => ({ CreateM3U8File: harness.createM3u8 }));
vi.mock("@/lib/toast-with-sound", () => ({ toastWithSound: { success: vi.fn() } }));

function completedQueue(id: string, sourceIndex: number, file: string): DownloadQueueSnapshot<MediaJobPayload, MediaJobResult> {
    return { id: "playlist:one", title: "Playlist", progress: 1,
        counts: { total: 1, terminal: 1, completed: 1, failed: 0, active: 0, queued: 0 },
        jobs: [{ id, queueId: "playlist:one", queueTitle: "Playlist", trackKey: `spotify:${id}`, sourceIndex,
            kind: "spotify-download", status: "completed", force: false, enqueuedAt: 1, finishedAt: 2,
            result: { success: true, message: "Downloaded", file },
            payload: { track: { spotify_id: id, name: id, artists: "Artist", album_name: "Album", duration_ms: 1,
                images: "", release_date: "2026", track_number: 1, external_urls: "" },
                settings: {} as Settings, queue: { id: "playlist:one", title: "Playlist", kind: "playlist" }, isAlbum: false, position: sourceIndex + 1 },
        }],
    };
}

it("logs all diagnostics once as soon as a track completes while the queue is still busy", async () => {
    vi.stubGlobal("IS_REACT_ACT_ENVIRONMENT", true);
    logger.clear();
    const queue = completedQueue("done", 0, "done.mp3");
    const warnings = ["lyrics unavailable: Spotify returned 503; LRCLIB returned 404", "Chosic track lookup failed: request timed out"];
    queue.jobs[0]!.result = { success: true, message: "Downloaded", warnings, validation_warning: "duration check unavailable" };
    const active = { ...completedQueue("active", 1, "").jobs[0]!, status: "active" as const, result: undefined };
    const busy = { ...queue, jobs: [...queue.jobs, active] };
    harness.snapshot = { queues: { [queue.id]: busy }, activeCount: 1, peakActive: 1 };
    function Probe() { useDownload(); return null; }
    const root = createRoot(document.createElement("div"));
    try {
        await act(async () => root.render(<Probe />));
        const entries = logger.getLogs().filter(log => log.message.startsWith("downloaded: done"));
        expect(entries).toHaveLength(1);
        expect(entries[0]!.level).toBe("warning");
        for (const warning of [...warnings, "duration check unavailable"]) {
            expect(entries[0]!.message).toContain(warning);
        }
        harness.snapshot = { ...harness.snapshot, queues: { [queue.id]: { ...busy } } };
        await act(async () => root.render(<Probe />));
        expect(logger.getLogs().filter(log => log.message.startsWith("downloaded: done"))).toHaveLength(1);
    } finally {
        await act(async () => root.unmount());
        harness.snapshot = { queues: {}, activeCount: 0, peakActive: 0 };
        vi.unstubAllGlobals();
    }
});

it("keeps previous playlist paths after history clears, ordered with appended paths, until a new session", async () => {
    vi.stubGlobal("IS_REACT_ACT_ENVIRONMENT", true);
    let download!: ReturnType<typeof useDownload>;
    function Probe() {
        const current = useDownload();
        useLayoutEffect(() => { download = current; });
        return null;
    }
    const root = createRoot(document.createElement("div"));
    await act(async () => root.render(<Probe />));
    const settle = async (queue: DownloadQueueSnapshot<MediaJobPayload, MediaJobResult>) => await act(async () => {
        await harness.options!.onQueueIdle!(queue, { queues: { [queue.id]: queue }, activeCount: 0, peakActive: 1 }, {
            settings: { createM3u8File: true } as Settings, playlistName: "Playlist", m3u8OutputDir: "output",
        });
    });
    await settle(completedQueue("later", 2, "later.mp3"));
    await act(async () => download.clearCompletedQueues());
    await settle(completedQueue("earlier", 0, "earlier.mp3"));
    expect(harness.createM3u8).toHaveBeenLastCalledWith("Playlist", "output", ["earlier.mp3", "later.mp3"]);
    await act(async () => download.startNewSession());
    expect(download.batchSummary).toBeNull();
    expect(download.downloadedTracks.size).toBe(0);
    await settle(completedQueue("new", 1, "new.mp3"));
    expect(harness.createM3u8).toHaveBeenLastCalledWith("Playlist", "output", ["new.mp3"]);
    await act(async () => root.unmount());
    vi.unstubAllGlobals();
});
