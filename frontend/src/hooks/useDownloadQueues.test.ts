import { describe, expect, it, vi } from "vitest";
import type { DownloadResponse, DownloadRequest, TrackMetadata } from "@/types/api";
import type { Settings } from "@/lib/settings";
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

function backendApi(overrides: Partial<DownloadQueueApi> = {}): DownloadQueueApi {
    return {
        addDownloadJob: async (_queueId, jobId) => `backend:${jobId}`,
        cancelDownloadQueue: async () => 0,
        downloadTrack: async () => ({ success: true, message: "Downloaded" }),
        ensureValidToken: async () => "token",
        ...overrides,
    };
}

describe("createDownloadQueueHarness", () => {
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
