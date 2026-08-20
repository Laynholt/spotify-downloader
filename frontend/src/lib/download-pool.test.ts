import { describe, expect, it } from "vitest";
import { runBoundedPool, type PoolResult } from "./download-pool";

describe("runBoundedPool", () => {
    it.each([0, -1, Number.NaN, Number.POSITIVE_INFINITY, Number.NEGATIVE_INFINITY, 0.5, 1.5])(
        "rejects a non-positive, non-finite, or fractional concurrency (%s)",
        async (concurrency) => {
            let workerCalls = 0;
            let shouldStopCalls = 0;
            const settled: PoolResult<string>[] = [];

            await expect(runBoundedPool<number, string>({
                items: [1, 2, 3],
                concurrency,
                shouldStop: () => {
                    shouldStopCalls++;
                    return false;
                },
                worker: async () => {
                    workerCalls++;
                    return "unexpected";
                },
                onSettled: result => settled.push(result),
            })).rejects.toThrow(RangeError);

            expect(workerCalls).toBe(0);
            expect(shouldStopCalls).toBe(0);
            expect(settled).toHaveLength(0);
        },
    );

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

    it("cancels unclaimed indices after three active tasks settle", async () => {
        let stop = false;
        let started = 0;
        let releaseWorkers!: () => void;
        const workerGate = new Promise<void>(resolve => {
            releaseWorkers = resolve;
        });
        const settled: PoolResult<string>[] = [];

        const poolPromise = runBoundedPool<string, string>({
            items: ["first", "second", "third", "fourth", "fifth"],
            concurrency: 3,
            shouldStop: () => stop,
            onSettled: result => settled.push(result),
            worker: async (item, index) => {
                started++;
                if (index === 2) {
                    stop = true;
                }
                await workerGate;
                return item;
            },
        });

        expect(started).toBe(3);
        expect(stop).toBe(true);
        releaseWorkers();
        const results = await poolPromise;

        expect(results).toEqual([
            { index: 0, status: "fulfilled", value: "first" },
            { index: 1, status: "fulfilled", value: "second" },
            { index: 2, status: "fulfilled", value: "third" },
            { index: 3, status: "cancelled" },
            { index: 4, status: "cancelled" },
        ]);
        expect(settled).toHaveLength(5);
        expect(settled.map(result => result.index).sort()).toEqual([0, 1, 2, 3, 4]);
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
