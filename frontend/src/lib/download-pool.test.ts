import { describe, expect, it } from "vitest";
import { runBoundedPool, type PoolResult } from "./download-pool";

describe("runBoundedPool", () => {
    it("runs at most three tasks and preserves indexed results", async () => {
        let active = 0;
        let peak = 0;
        const results = await runBoundedPool<number, string>({
            items: [40, 10, 30, 5, 20],
            concurrency: 3,
            shouldStop: () => false,
            worker: async (delay, index) => {
                active++;
                peak = Math.max(peak, active);
                await new Promise(resolve => setTimeout(resolve, delay));
                active--;
                return `track-${index}`;
            },
        });

        expect(peak).toBe(3);
        expect(results.map(result => result.index)).toEqual([0, 1, 2, 3, 4]);
        expect(results.map(result => result.status)).toEqual([
            "fulfilled",
            "fulfilled",
            "fulfilled",
            "fulfilled",
            "fulfilled",
        ]);
        expect(results.map(result => result.status === "fulfilled" ? result.value : undefined)).toEqual([
            "track-0",
            "track-1",
            "track-2",
            "track-3",
            "track-4",
        ]);
    });

    it("cancels unclaimed indices after stop while active tasks settle", async () => {
        let stop = false;
        let releaseWorker!: () => void;
        const workerGate = new Promise<void>(resolve => {
            releaseWorker = resolve;
        });

        const poolPromise = runBoundedPool({
            items: ["first", "second", "third", "fourth"],
            concurrency: 3,
            shouldStop: () => stop,
            worker: async (item) => {
                stop = true;
                await workerGate;
                return item;
            },
        });

        expect(stop).toBe(true);
        releaseWorker();
        const results = await poolPromise;

        expect(results).toEqual([
            { index: 0, status: "fulfilled", value: "first" },
            { index: 1, status: "cancelled" },
            { index: 2, status: "cancelled" },
            { index: 3, status: "cancelled" },
        ]);
    });

    it("isolates worker rejection and settles other workers", async () => {
        const failure = new Error("download failed");
        const settled: PoolResult<string>[] = [];
        const results = await runBoundedPool<string, string>({
            items: ["bad", "good"],
            concurrency: 2,
            shouldStop: () => false,
            onSettled: result => settled.push(result),
            worker: async (item) => {
                if (item === "bad") {
                    throw failure;
                }
                await Promise.resolve();
                return "downloaded";
            },
        });

        expect(results[0]).toEqual({ index: 0, status: "rejected", reason: failure });
        expect(results[1]).toEqual({ index: 1, status: "fulfilled", value: "downloaded" });
        expect(settled).toHaveLength(2);
        expect(settled.map(result => result.index).sort()).toEqual([0, 1]);
    });
});
