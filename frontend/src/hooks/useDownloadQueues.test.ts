import { describe, expect, it, vi } from "vitest";
import type { DownloadResponse, DownloadRequest, TrackMetadata } from "@/types/api";
import type { Settings } from "@/lib/settings";
import { createEmptyBatchSummary } from "@/lib/batch-summary";
import {
    createDownloadQueueHarness,
    type CollectionDownloadArgs,
    type DownloadQueueApi,
} from "./useDownloadQueues";

function deferred<T>() {
    let resolve!: (value: T | PromiseLike<T>) => void;
    let reject!: (reason?: unknown) => void;
    const promise = new Promise<T>((resolvePromise, rejectPromise) => {
        resolve = resolvePromise;
        reject = rejectPromise;
    });
    return { promise, resolve, reject };
}

async function flushPromises() {
    for (let index = 0; index < 8; index++) {
        await Promise.resolve();
    }
}

async function waitFor(condition: () => boolean) {
    for (let index = 0; index < 20; index++) {
        if (condition()) return;
        await Promise.resolve();
    }
    expect(condition()).toBe(true);
}

function track(id?: string, index = 0): TrackMetadata {
    return {
        spotify_id: id,
        name: `Track ${index}`,
        artists: "Artist",
        album_name: "Album",
        duration_ms: 1,
        images: "",
        release_date: "2024-01-01",
        track_number: index + 1,
        external_urls: "",
    };
}

const playlistA = { id: "playlist:a", title: "Playlist A", kind: "playlist" as const };
const playlistB = { id: "playlist:b", title: "Playlist B", kind: "playlist" as const };
const args: CollectionDownloadArgs = { settings: {} as Settings };

it("calculates queue peak from actual overlaps", async () => {
    vi.stubGlobal("window", { navigator: { platform: "Win32" } });
    const { queuePeakActive } = await import("./useDownload");
    try {
        expect(queuePeakActive([
            { startedAt: 0, finishedAt: 10 },
            { startedAt: 0, finishedAt: 20 },
            { startedAt: 0, finishedAt: 30 },
            { startedAt: 10, finishedAt: 40 },
        ])).toBe(3);
        expect(queuePeakActive([
            { finishedAt: 0 },
            { startedAt: 1, finishedAt: 2 },
        ])).toBe(1);
        expect(queuePeakActive([{ startedAt: 0, finishedAt: 0 }])).toBe(1);
    }
    finally {
        vi.unstubAllGlobals();
    }
});

it("returns every newly started job exactly once even when only its terminal snapshot is observed", async () => {
    vi.stubGlobal("window", { navigator: { platform: "Win32" } });
    const { takeUnloggedStartedJobs } = await import("./useDownload");
    try {
        const loggedIds = new Set<string>();
        const jobs = [
            { id: "job-a", status: "active", startedAt: 0 },
            { id: "job-b", status: "active", startedAt: 1 },
            { id: "job-c", status: "active", startedAt: 2 },
            { id: "job-d", status: "queued" },
            { id: "job-e", status: "completed", startedAt: 3 },
        ];

        expect(takeUnloggedStartedJobs(jobs, loggedIds).map((job) => job.id)).toEqual(["job-a", "job-b", "job-c", "job-e"]);
        expect(takeUnloggedStartedJobs(jobs, loggedIds)).toEqual([]);
    }
    finally {
        vi.unstubAllGlobals();
    }
});

it("formats every terminal queue outcome and batch timing for scalar logs", async () => {
    vi.stubGlobal("window", { navigator: { platform: "Win32" } });
    const { batchCompletionLog, queueTerminalLog } = await import("./useDownload");
    try {
        const outcomes = [
            ["completed", "info", "downloaded"],
            ["skipped", "info", "skipped"],
            ["failed", "error", "failed"],
            ["unavailable", "error", "unavailable"],
            ["suspicious", "warning", "suspicious"],
            ["cancelled", "info", "cancelled"],
        ] as const;

        for (const [status, level, label] of outcomes) {
            expect(queueTerminalLog({
                status,
                error: `${status} detail`,
                payload: { track: track("one", 0) },
            } as Parameters<typeof queueTerminalLog>[0])).toMatchObject({
                level,
                message: expect.stringContaining(label),
            });
        }

        const message = batchCompletionLog({
            ...createEmptyBatchSummary("summary"),
            downloaded: 1,
            cancelled: 1,
            elapsedMs: 1250,
            peakActive: 3,
        });
        expect(message).toContain("elapsed 1.25s");
        expect(message).toContain("peak active 3");
    }
    finally {
        vi.unstubAllGlobals();
    }
});

it("carries the owning queue on an opened batch summary", async () => {
    vi.stubGlobal("window", { navigator: { platform: "Win32" } });
    const { withBatchSummaryQueue } = await import("./useDownload");
    try {
        const latestEnqueuedQueueId = playlistB.id;
        const openedSummary = withBatchSummaryQueue(createEmptyBatchSummary("Playlist A"), playlistA.id);

        expect(openedSummary.queueId).toBe(playlistA.id);
        expect(openedSummary.queueId).not.toBe(latestEnqueuedQueueId);
    }
    finally {
        vi.unstubAllGlobals();
    }
});

function backendApi(overrides: Partial<DownloadQueueApi> = {}): DownloadQueueApi {
    return {
        addDownloadJob: async (_queueId, jobId) => `backend:${jobId}`,
        cancelDownloadItem: async () => undefined,
        cancelDownloadQueue: async () => 0,
        downloadTrack: async () => ({ success: true, message: "Downloaded" }),
        ensureValidToken: async () => "token",
        markDownloadItemFailed: async () => undefined,
        skipDownloadItem: async () => undefined,
        ...overrides,
    };
}

describe("createDownloadQueueHarness", () => {
    it("does not append a playlist name to an already resolved output directory", async () => {
        const requests: DownloadRequest[] = [];
        const harness = createDownloadQueueHarness({
            api: backendApi({
                downloadTrack: async (request) => {
                    requests.push(request);
                    return { success: true, message: "Downloaded" };
                },
            }),
        });

        await harness.enqueueAll(playlistA, [track("one", 0)], {
            settings: { downloadPath: "C:/Music" } as Settings,
            playlistName: "Playlist A",
        });
        await waitFor(() => harness.snapshot().queues[playlistA.id].jobs[0]?.status === "completed");

        expect(requests[0]).toMatchObject({ output_dir: "C:/Music" });
        expect(requests[0]).not.toHaveProperty("playlist_name");
        await harness.dispose();
    });

    it("does not start DownloadTrack after stopping during backend registration", async () => {
        const registration = deferred<string>();
        const downloadTrack = vi.fn(async () => ({ success: true, message: "Downloaded" }));
        const markDownloadItemFailed = vi.fn(async () => undefined);
        const cancelDownloadItem = vi.fn(async () => undefined);
        const harness = createDownloadQueueHarness({
            api: backendApi({
                addDownloadJob: async () => registration.promise,
                cancelDownloadItem,
                downloadTrack,
                markDownloadItemFailed,
            }),
        });

        await harness.enqueueAll(playlistA, [track("one", 0)], args);
        await flushPromises();
        const stopped = harness.stopQueue(playlistA.id);
        registration.resolve("backend-item");
        await stopped;
        await flushPromises();

        expect(downloadTrack).not.toHaveBeenCalled();
        expect(cancelDownloadItem).toHaveBeenCalledWith("backend-item");
        expect(markDownloadItemFailed).not.toHaveBeenCalled();
        await harness.dispose();
    });

    it("does not start DownloadTrack after stopping during the initial token wait", async () => {
        const initialToken = deferred<string>();
        const downloadTrack = vi.fn(async () => ({ success: true, message: "Downloaded" }));
        const markDownloadItemFailed = vi.fn(async () => undefined);
        const cancelDownloadItem = vi.fn(async () => undefined);
        const harness = createDownloadQueueHarness({
            api: backendApi({
                cancelDownloadItem,
                ensureValidToken: async () => initialToken.promise,
                downloadTrack,
                markDownloadItemFailed,
            }),
        });

        await harness.enqueueAll(playlistA, [track("one", 0)], args);
        await flushPromises();
        const stopped = harness.stopQueue(playlistA.id);
        initialToken.resolve("token");
        await stopped;
        await flushPromises();

        expect(downloadTrack).not.toHaveBeenCalled();
        expect(cancelDownloadItem).toHaveBeenCalledWith(expect.any(String));
        expect(markDownloadItemFailed).not.toHaveBeenCalled();
        await harness.dispose();
    });

    it("marks a registered backend item failed when the initial token cannot be obtained", async () => {
        const markDownloadItemFailed = vi.fn(async () => undefined);
        const cancelDownloadItem = vi.fn(async () => undefined);
        const harness = createDownloadQueueHarness({
            api: backendApi({
                cancelDownloadItem,
                ensureValidToken: async () => { throw new Error("token unavailable"); },
                markDownloadItemFailed,
            }),
        });

        await harness.enqueueAll(playlistA, [track("one", 0)], args);
        await waitFor(() => harness.snapshot().queues[playlistA.id].jobs[0]?.status === "failed");

        expect(markDownloadItemFailed).toHaveBeenCalledWith(expect.any(String), "token unavailable");
        expect(cancelDownloadItem).not.toHaveBeenCalled();
        await harness.dispose();
    });

    it("cancels the backend item before an in-flight fallback failure mark can terminalize it", async () => {
        const markStarted = deferred<void>();
        const releaseMark = deferred<void>();
        let backendStatus = "queued";
        const cancelDownloadItem = vi.fn(async () => {
            if (backendStatus === "queued") backendStatus = "cancelled";
        });
        const markDownloadItemFailed = vi.fn(async () => {
            markStarted.resolve(undefined);
            await releaseMark.promise;
            if (backendStatus === "queued") backendStatus = "failed";
        });
        const harness = createDownloadQueueHarness({
            api: backendApi({
                cancelDownloadItem,
                ensureValidToken: async () => { throw new Error("token unavailable"); },
                markDownloadItemFailed,
            }),
        });

        await harness.enqueueAll(playlistA, [track("one", 0)], args);
        await markStarted.promise;
        await harness.stopQueue(playlistA.id);
        await flushPromises();

        expect(cancelDownloadItem).toHaveBeenCalledTimes(1);
        expect(backendStatus).toBe("cancelled");
        releaseMark.resolve(undefined);
        await flushPromises();
        expect(markDownloadItemFailed).toHaveBeenCalledTimes(1);
        expect(cancelDownloadItem).toHaveBeenCalledTimes(1);
        expect(backendStatus).toBe("cancelled");
        expect(harness.snapshot().queues[playlistA.id].jobs[0].status).toBe("cancelled");
        await harness.dispose();
    });

    it("notifies idle for a queue whose only candidate is immediately unavailable", async () => {
        const onQueueIdle = vi.fn();
        const harness = createDownloadQueueHarness({ onQueueIdle });

        await harness.enqueueAll(playlistA, [track(undefined, 0)], args);
        await flushPromises();

        expect(onQueueIdle).toHaveBeenCalledTimes(1);
        await harness.dispose();
    });

    it("notifies idle when Download All is repeated after the queue is terminal", async () => {
        const onQueueIdle = vi.fn();
        const harness = createDownloadQueueHarness({ onQueueIdle, api: backendApi() });
        const onlyTrack = track("one", 0);

        await harness.enqueueAll(playlistA, [onlyTrack], args);
        await waitFor(() => harness.snapshot().queues[playlistA.id].jobs[0]?.status === "completed");
        await flushPromises();
        await harness.enqueueAll(playlistA, [onlyTrack], args);
        await flushPromises();

        expect(onQueueIdle).toHaveBeenCalledTimes(2);
        await harness.dispose();
    });

    it("preflights existing files and updates metadata instead of downloading them", async () => {
        const downloadTrack = vi.fn(async () => ({ success: true, message: "Downloaded" }));
        const skipDownloadItem = vi.fn(async () => undefined);
        const updateExistingFile = vi.fn(async () => ({
            success: true,
            message: "Metadata updated",
            file: "existing.mp3",
            already_exists: true,
            metadata_updated: true,
        }));
        const harness = createDownloadQueueHarness({
            api: backendApi({
                downloadTrack,
                skipDownloadItem,
            }),
        });
        const existingArgs = {
            settings: { updateMetadataForExistingFiles: true } as Settings,
            existingFileFor: () => "existing.mp3",
            updateExistingFile,
        } as CollectionDownloadArgs;

        await harness.enqueueAll(playlistA, [track("one", 0)], existingArgs);
        await waitFor(() => harness.snapshot().queues[playlistA.id].jobs[0]?.status === "skipped");

        expect(downloadTrack).not.toHaveBeenCalled();
        expect(updateExistingFile).toHaveBeenCalledWith(expect.objectContaining({ spotify_id: "one" }), 0, "existing.mp3");
        expect(skipDownloadItem).toHaveBeenCalledWith(expect.any(String), "existing.mp3");
        await harness.dispose();
    });

    it("cancels the backend item before an in-flight existing-file failure can terminalize it", async () => {
        const markStarted = deferred<void>();
        const releaseMark = deferred<void>();
        let backendStatus = "queued";
        const cancelDownloadItem = vi.fn(async () => {
            if (backendStatus === "queued") backendStatus = "cancelled";
        });
        const markDownloadItemFailed = vi.fn(async () => {
            markStarted.resolve(undefined);
            await releaseMark.promise;
            if (backendStatus === "queued") backendStatus = "failed";
        });
        const harness = createDownloadQueueHarness({
            api: backendApi({ cancelDownloadItem, markDownloadItemFailed }),
        });
        const existingArgs: CollectionDownloadArgs = {
            settings: { updateMetadataForExistingFiles: true } as Settings,
            existingFileFor: () => "existing.mp3",
            updateExistingFile: async () => ({
                success: false,
                message: "Metadata update failed",
                error: "metadata failure",
            }),
        };

        await harness.enqueueAll(playlistA, [track("one", 0)], existingArgs);
        await markStarted.promise;
        await harness.stopQueue(playlistA.id);
        await flushPromises();

        expect(cancelDownloadItem).toHaveBeenCalledTimes(1);
        expect(backendStatus).toBe("cancelled");
        releaseMark.resolve(undefined);
        await flushPromises();
        expect(markDownloadItemFailed).toHaveBeenCalledTimes(1);
        expect(backendStatus).toBe("cancelled");
        expect(harness.snapshot().queues[playlistA.id].jobs[0].status).toBe("cancelled");
        await harness.dispose();
    });

    it("cancels the backend item before an in-flight existing-file skip can terminalize it", async () => {
        const skipStarted = deferred<void>();
        const releaseSkip = deferred<void>();
        let backendStatus = "queued";
        const cancelDownloadItem = vi.fn(async () => {
            if (backendStatus === "queued") backendStatus = "cancelled";
        });
        const skipDownloadItem = vi.fn(async () => {
            skipStarted.resolve(undefined);
            await releaseSkip.promise;
            if (backendStatus === "queued") backendStatus = "skipped";
        });
        const harness = createDownloadQueueHarness({
            api: backendApi({ cancelDownloadItem, skipDownloadItem }),
        });
        const existingArgs: CollectionDownloadArgs = {
            settings: {} as Settings,
            existingFileFor: () => "existing.mp3",
        };

        await harness.enqueueAll(playlistA, [track("one", 0)], existingArgs);
        await skipStarted.promise;
        await harness.stopQueue(playlistA.id);
        await flushPromises();

        expect(cancelDownloadItem).toHaveBeenCalledTimes(1);
        expect(backendStatus).toBe("cancelled");
        releaseSkip.resolve(undefined);
        await flushPromises();
        expect(skipDownloadItem).toHaveBeenCalledTimes(1);
        expect(backendStatus).toBe("cancelled");
        expect(harness.snapshot().queues[playlistA.id].jobs[0].status).toBe("cancelled");
        await harness.dispose();
    });

    it("uses the original position for a single-track preflight", async () => {
        const observedSourceIndices: number[] = [];
        const indexedArgs: CollectionDownloadArgs = {
            settings: {} as Settings,
            sourceIndexFor: () => 4,
            existingFileFor: (_track: TrackMetadata, sourceIndex: number) => {
                observedSourceIndices.push(sourceIndex);
                return "existing.mp3";
            },
        };
        const harness = createDownloadQueueHarness({ api: backendApi() });

        await harness.retryTrack(playlistA, track("five", 4), indexedArgs);
        await waitFor(() => harness.snapshot().queues[playlistA.id].jobs[0]?.status === "skipped");

        expect(harness.snapshot().queues[playlistA.id].jobs[0].sourceIndex).toBe(4);
        expect(observedSourceIndices).toEqual([4]);
        await harness.dispose();
    });

    it("preserves selected tracks' original playlist source indices", async () => {
        const selectedTracks = [track("two", 1), track("four", 3)];
        const requestedSourceIndices: number[] = [];
        const indexedArgs: CollectionDownloadArgs = {
            settings: {} as Settings,
            sourceIndexFor: (_track: TrackMetadata, selectedIndex: number) => [1, 3][selectedIndex],
            requestFor: (_track: TrackMetadata, sourceIndex: number) => {
                requestedSourceIndices.push(sourceIndex);
                return { position: sourceIndex + 1 };
            },
        };
        const harness = createDownloadQueueHarness({ api: backendApi() });

        await harness.enqueueSelected(playlistA, selectedTracks, ["two", "four"], indexedArgs);
        await waitFor(() => harness.snapshot().queues[playlistA.id].counts.completed === 2);

        expect(harness.snapshot().queues[playlistA.id].jobs.map((job) => job.sourceIndex)).toEqual([1, 3]);
        expect(requestedSourceIndices).toEqual([1, 3]);
        await harness.dispose();
    });

    it("keeps a playlist queue live while more tracks are appended", async () => {
        const gates = new Map<string, ReturnType<typeof deferred<DownloadResponse>>>();
        const harness = createDownloadQueueHarness({
            executeSpotify: async (job) => {
                const gate = deferred<DownloadResponse>();
                gates.set(job.id, gate);
                return gate.promise;
            },
        });
        const firstThree = [track("one", 0), track("two", 1), track("three", 2)];
        const fourth = track("four", 3);

        await harness.enqueueAll(playlistA, firstThree, args);
        expect(harness.snapshot().queues[playlistA.id].counts.active).toBe(3);

        await harness.retryTrack(playlistA, fourth, { ...args, sourceIndex: 3 });
        expect(harness.snapshot().queues[playlistA.id].counts.total).toBe(4);
        expect(harness.snapshot().queues[playlistA.id].counts.queued).toBe(1);

        for (const gate of gates.values()) {
            gate.resolve({ success: true, message: "Downloaded" });
        }
        await flushPromises();
        await harness.dispose();
    });

    it("does not duplicate terminal tracks when Download All is repeated", async () => {
        const harness = createDownloadQueueHarness({ api: backendApi() });
        const onlyTrack = track("one", 0);

        await harness.enqueueAll(playlistA, [onlyTrack], args);
        await flushPromises();
        const secondEnqueue = await harness.enqueueAll(playlistA, [onlyTrack], args);

        expect(secondEnqueue).toEqual([{ added: false, reason: "already-represented" }]);
        expect(harness.snapshot().queues[playlistA.id].counts.total).toBe(1);
        await harness.dispose();
    });

    it("creates a forced attempt when an individual terminal track is retried", async () => {
        const harness = createDownloadQueueHarness({ api: backendApi() });
        const onlyTrack = track("one", 0);

        await harness.enqueueAll(playlistA, [onlyTrack], args);
        await flushPromises();
        const retry = await harness.retryTrack(playlistA, onlyTrack, { ...args, sourceIndex: 0 });
        await flushPromises();

        expect(retry.added).toBe(true);
        const jobs = harness.snapshot().queues[playlistA.id].jobs;
        expect(jobs).toHaveLength(2);
        expect(jobs[1]).toMatchObject({ force: true, status: "completed" });
        await harness.dispose();
    });

    it("keeps every candidate in the queue, including a missing Spotify ID", async () => {
        const gate = deferred<DownloadResponse>();
        const executeSpotify = vi.fn(async () => gate.promise);
        const harness = createDownloadQueueHarness({ executeSpotify });

        await harness.enqueueAll(playlistA, [track("one", 0), track(undefined, 1)], args);
        const jobs = harness.snapshot().queues[playlistA.id].jobs;

        expect(jobs).toHaveLength(2);
        expect(jobs[1]).toMatchObject({ status: "unavailable", error: "Spotify track ID is missing" });
        expect(executeSpotify).toHaveBeenCalledTimes(1);
        gate.resolve({ success: true, message: "Downloaded" });
        await flushPromises();
        await harness.dispose();
    });

    it("cancels only the requested queue", async () => {
        const signals = new Map<string, AbortSignal>();
        const gate = deferred<DownloadResponse>();
        const cancelDownloadQueue = vi.fn(async () => 1);
        const harness = createDownloadQueueHarness({
            api: backendApi({ cancelDownloadQueue }),
            executeSpotify: async (job, signal) => {
                signals.set(job.queueId, signal);
                return gate.promise;
            },
        });

        await harness.enqueueAll(playlistA, [track("one", 0)], args);
        await harness.enqueueAll(playlistB, [track("two", 0)], args);
        await harness.stopQueue(playlistA.id);

        expect(cancelDownloadQueue).toHaveBeenCalledWith(playlistA.id);
        expect(signals.get(playlistA.id)?.aborted).toBe(true);
        expect(signals.get(playlistB.id)?.aborted).toBe(false);
        expect(harness.snapshot().queues[playlistA.id].jobs[0].status).toBe("cancelled");
        expect(harness.snapshot().queues[playlistB.id].jobs[0].status).toBe("active");
        gate.resolve({ success: true, message: "Downloaded" });
        await flushPromises();
        await harness.dispose();
    });

    it("registers the backend job and passes its queue, item, and force fields to DownloadTrack", async () => {
        const requests: DownloadRequest[] = [];
        const addDownloadJob = vi.fn(async () => "backend-item");
        const harness = createDownloadQueueHarness({
            api: backendApi({
                addDownloadJob,
                downloadTrack: async (request) => {
                    requests.push(request);
                    return { success: true, message: "Downloaded" };
                },
            }),
        });
        const onlyTrack = track("one", 0);

        await harness.enqueueAll(playlistA, [onlyTrack], args);
        await flushPromises();
        await harness.retryTrack(playlistA, onlyTrack, { ...args, sourceIndex: 0 });
        await flushPromises();

        expect(addDownloadJob).toHaveBeenCalledWith(
            playlistA.id,
            expect.any(String),
            "one",
            "Track 0",
            "Artist",
            "Album",
        );
        expect(requests).toEqual([
            expect.objectContaining({ queue_id: playlistA.id, item_id: "backend-item", force: false }),
            expect.objectContaining({ queue_id: playlistA.id, item_id: "backend-item", force: true }),
        ]);
        expect(harness.snapshot().queues[playlistA.id].jobs[0].backendItemId).toBe("backend-item");
        await harness.dispose();
    });

    it("refreshes once after an unauthorized response", async () => {
        const tokens: string[] = [];
        const ensureValidToken = vi.fn(async (force?: boolean) => force ? "fresh" : "stale");
        const harness = createDownloadQueueHarness({
            api: backendApi({
                ensureValidToken,
                downloadTrack: async (request) => {
                    tokens.push(request.session_token);
                    return tokens.length === 1
                        ? { success: false, message: "Unauthorized", error: "401 unauthorized" }
                        : { success: true, message: "Downloaded" };
                },
            }),
        });

        await harness.enqueueAll(playlistA, [track("one", 0)], args);
        await waitFor(() => harness.snapshot().queues[playlistA.id].jobs[0]?.status === "completed");

        expect(tokens).toEqual(["stale", "fresh"]);
        expect(ensureValidToken).toHaveBeenNthCalledWith(1, false);
        expect(ensureValidToken).toHaveBeenNthCalledWith(2, true);
        expect(harness.snapshot().queues[playlistA.id].jobs[0].status).toBe("completed");
        await harness.dispose();
    });

    it("does not start an authorized retry after its queue is stopped during refresh", async () => {
        const refresh = deferred<string>();
        const downloadTrack = vi.fn(async (request: DownloadRequest) => {
            if (request.session_token === "stale") {
                return { success: false, message: "Unauthorized", error: "401 unauthorized" };
            }
            return { success: true, message: "Downloaded" };
        });
        const harness = createDownloadQueueHarness({
            api: backendApi({
                ensureValidToken: async (force?: boolean) => force ? refresh.promise : "stale",
                downloadTrack,
            }),
        });

        await harness.enqueueAll(playlistA, [track("one", 0)], args);
        await flushPromises();
        await harness.stopQueue(playlistA.id);
        refresh.resolve("fresh");
        await flushPromises();

        expect(downloadTrack).toHaveBeenCalledTimes(1);
        expect(harness.snapshot().queues[playlistA.id].jobs[0].status).toBe("cancelled");
        await harness.dispose();
    });
});
