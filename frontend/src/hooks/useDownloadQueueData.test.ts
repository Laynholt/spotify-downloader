import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

const api = vi.hoisted(() => ({ queue: vi.fn(), progress: vi.fn() }));
vi.mock("../../wailsjs/go/main/App", () => ({ GetDownloadQueue: api.queue, GetDownloadProgress: api.progress }));

const idle = { queue: [], is_downloading: false, queued_count: 0, active_count: 0,
    completed_count: 0, failed_count: 0, skipped_count: 0, cancelled_count: 0,
    current_speed: 0, total_downloaded: 0, session_start_time: 0 };

describe("shared queue polling", () => {
    beforeEach(() => {
        vi.resetModules();
        vi.useFakeTimers();
        api.queue.mockReset().mockResolvedValue(idle);
        api.progress.mockReset().mockResolvedValue({ is_downloading: false, mb_downloaded: 0, speed_mbps: 0 });
    });
    afterEach(() => { vi.useRealTimers(); });

    it("shares idle polling across subscribers and stops after the last one leaves", async () => {
        const { downloadQueueStore } = await import("./useDownloadQueueData");
        const stopA = downloadQueueStore.subscribe(() => {});
        const stopB = downloadQueueStore.subscribe(() => {});
        await vi.advanceTimersByTimeAsync(0);
        expect(api.queue).toHaveBeenCalledTimes(1);
        await vi.advanceTimersByTimeAsync(999);
        expect(api.queue).toHaveBeenCalledTimes(1);
        stopA();
        await vi.advanceTimersByTimeAsync(1);
        expect(api.queue).toHaveBeenCalledTimes(2);
        stopB();
        await vi.advanceTimersByTimeAsync(2000);
        expect(api.queue).toHaveBeenCalledTimes(2);
    });

    it("does not overlap slow requests, even when a subscription remounts", async () => {
        let resolve!: (value: typeof idle) => void;
        api.queue.mockReturnValueOnce(new Promise<typeof idle>(done => { resolve = done; }));
        const { downloadQueueStore } = await import("./useDownloadQueueData");
        const stop = downloadQueueStore.subscribe(() => {});
        stop();
        const cleanup = downloadQueueStore.subscribe(() => {});
        await vi.advanceTimersByTimeAsync(3000);
        expect(api.queue).toHaveBeenCalledTimes(1);
        resolve(idle);
        await vi.advanceTimersByTimeAsync(0);
        expect(downloadQueueStore.getSnapshot().total_count).toBe(0);
        cleanup();
    });

    it("polls active work quickly and preserves installer progress", async () => {
        api.queue.mockResolvedValue({ ...idle, is_downloading: true });
        api.progress.mockResolvedValue({ is_downloading: true, mb_downloaded: 12, speed_mbps: 3 });
        const { downloadQueueStore } = await import("./useDownloadQueueData");
        const cleanup = downloadQueueStore.subscribe(() => {});
        await vi.advanceTimersByTimeAsync(0);
        expect(downloadQueueStore.getSnapshot().mb_downloaded).toBe(12);
        await vi.advanceTimersByTimeAsync(200);
        expect(api.queue).toHaveBeenCalledTimes(2);
        cleanup();
    });
});
