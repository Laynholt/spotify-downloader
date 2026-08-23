import { describe, expect, it, vi } from "vitest";
import {
    createDynamicDownloadScheduler,
    type DownloadJobCandidate,
    type DownloadSchedulerSnapshot,
} from "./download-queue-manager";

interface JobPayload {
    value: string;
}

interface Result {
    status: "completed" | "skipped" | "failed" | "suspicious";
    value?: string;
}

function deferred<T>() {
    let resolve!: (value: T | PromiseLike<T>) => void;
    let reject!: (reason?: unknown) => void;
    const promise = new Promise<T>((resolvePromise, rejectPromise) => {
        resolve = resolvePromise;
        reject = rejectPromise;
    });
    return { promise, resolve, reject };
}

function monotonicTestClock() {
    let tick = 0;
    return () => ++tick;
}

function candidate(
    queueId: string,
    id: string,
    overrides: Partial<DownloadJobCandidate<JobPayload>> = {},
): DownloadJobCandidate<JobPayload> {
    return {
        id,
        queueId,
        queueTitle: queueId,
        trackKey: id,
        sourceIndex: 0,
        kind: "spotify-download",
        payload: { value: id },
        ...overrides,
    };
}

function enqueueMany(
    scheduler: ReturnType<typeof createDynamicDownloadScheduler<JobPayload, Result>>,
    queueId: string,
    ids: string[],
) {
    for (const id of ids) {
        expect(scheduler.enqueue(candidate(queueId, id), "unseen-only").added).toBe(true);
    }
}

async function flushPromises() {
    await Promise.resolve();
    await Promise.resolve();
}

describe("createDynamicDownloadScheduler", () => {
    it("round-robins dynamically appended queues with a global peak of three", async () => {
        const started: string[] = [];
        const gates = new Map<string, ReturnType<typeof deferred<Result>>>();
        const scheduler = createDynamicDownloadScheduler<JobPayload, Result>({
            concurrency: 3,
            execute: async (job) => {
                started.push(job.id);
                const gate = deferred<Result>();
                gates.set(job.id, gate);
                return gate.promise;
            },
            classifyResult: (result) => result.status,
            cancelBackendQueue: vi.fn(async () => 0),
            now: monotonicTestClock(),
        });

        enqueueMany(scheduler, "playlist:a", ["a1", "a2", "a3", "a4"]);
        enqueueMany(scheduler, "playlist:b", ["b1", "b2"]);
        expect(started).toEqual(["a1", "a2", "a3"]);
        gates.get("a1")!.resolve({ status: "completed" });
        await flushPromises();
        expect(started[3]).toBe("b1");
        expect(scheduler.getSnapshot().peakActive).toBe(3);

        gates.get("a2")!.resolve({ status: "completed" });
        gates.get("a3")!.resolve({ status: "completed" });
        gates.get("b1")!.resolve({ status: "completed" });
        await flushPromises();
        await scheduler.dispose();
    });

    it("preserves FIFO within a queue while rotating between runnable queues", async () => {
        const started: string[] = [];
        const gates = new Map<string, ReturnType<typeof deferred<Result>>>();
        const scheduler = createDynamicDownloadScheduler<JobPayload, Result>({
            concurrency: 1,
            execute: async (job) => {
                started.push(job.id);
                const gate = deferred<Result>();
                gates.set(job.id, gate);
                return gate.promise;
            },
            classifyResult: (result) => result.status,
            cancelBackendQueue: vi.fn(async () => 0),
            now: monotonicTestClock(),
        });

        enqueueMany(scheduler, "a", ["a1", "a2"]);
        enqueueMany(scheduler, "b", ["b1", "b2"]);
        expect(started).toEqual(["a1"]);

        gates.get("a1")!.resolve({ status: "completed" });
        await flushPromises();
        expect(started).toEqual(["a1", "b1"]);
        gates.get("b1")!.resolve({ status: "completed" });
        await flushPromises();
        expect(started).toEqual(["a1", "b1", "a2"]);
        gates.get("a2")!.resolve({ status: "completed" });
        await flushPromises();
        expect(started).toEqual(["a1", "b1", "a2", "b2"]);
        gates.get("b2")!.resolve({ status: "completed" });
        await flushPromises();
        await scheduler.dispose();
    });

    it("applies unseen-only and retry-terminal enqueue policies", async () => {
        const scheduler = createDynamicDownloadScheduler<JobPayload, Result>({
            concurrency: 1,
            execute: async () => ({ status: "failed" }),
            classifyResult: (result) => result.status,
            cancelBackendQueue: vi.fn(async () => 0),
            now: monotonicTestClock(),
        });
        const first = candidate("a", "same", { trackKey: "track" });
        const duplicate = scheduler.enqueue(first, "unseen-only");
        expect(duplicate.added).toBe(true);
        expect(scheduler.enqueue({ ...first, id: "different" }, "unseen-only")).toEqual({
            added: false,
            reason: "already-represented",
        });

        expect(scheduler.enqueue({ ...first, id: "retry-before-settlement" }, "retry-terminal")).toEqual({
            added: false,
            reason: "already-in-flight",
        });
        await flushPromises();
        const retry = scheduler.enqueue({ ...first, id: "retry-after-failure" }, "retry-terminal");
        expect(retry.added).toBe(true);
        expect(retry.jobId).not.toBe(duplicate.jobId);
        const retryJob = scheduler.getSnapshot().queues.a.jobs.find(job => job.id === retry.jobId);
        expect(retryJob?.force).toBe(true);
        await scheduler.dispose();
    });

    it("records unavailable candidates as terminal without executing them", async () => {
        const execute = vi.fn(async () => ({ status: "completed" as const }));
        const scheduler = createDynamicDownloadScheduler<JobPayload, Result>({
            concurrency: 2,
            execute,
            classifyResult: (result) => result.status,
            cancelBackendQueue: vi.fn(async () => 0),
            now: monotonicTestClock(),
        });

        const decision = scheduler.enqueue(candidate("a", "missing", { unavailableReason: "not found" }), "unseen-only");
        expect(decision.added).toBe(true);
        expect(execute).not.toHaveBeenCalled();
        const queue = scheduler.getSnapshot().queues.a;
        expect(queue.jobs[0]).toMatchObject({
            id: decision.jobId,
            status: "unavailable",
            error: "not found",
        });
        expect(queue.counts).toEqual({ total: 1, terminal: 1, active: 0, queued: 0, completed: 0, failed: 0 });
        expect(queue.progress).toBe(1);
        await scheduler.dispose();
    });

    it("cancels only the selected queue, aborts active work, and protects cancellation state", async () => {
        const started: string[] = [];
        const signals = new Map<string, AbortSignal>();
        const gates = new Map<string, ReturnType<typeof deferred<Result>>>();
        const backendGate = deferred<number>();
        const cancelBackendQueue = vi.fn(async () => backendGate.promise);
        const scheduler = createDynamicDownloadScheduler<JobPayload, Result>({
            concurrency: 2,
            execute: async (job, signal) => {
                started.push(job.id);
                signals.set(job.id, signal);
                const gate = deferred<Result>();
                gates.set(job.id, gate);
                return gate.promise;
            },
            classifyResult: (result) => result.status,
            cancelBackendQueue,
            now: monotonicTestClock(),
        });
        expect(scheduler.enqueue(candidate("a", "a1"), "unseen-only").added).toBe(true);
        expect(scheduler.enqueue(candidate("b", "b1"), "unseen-only").added).toBe(true);
        expect(scheduler.enqueue(candidate("a", "a2"), "unseen-only").added).toBe(true);
        expect(scheduler.enqueue(candidate("b", "b2"), "unseen-only").added).toBe(true);
        expect(started).toEqual(["a1", "b1"]);

        const cancellation = scheduler.cancelQueue("a");
        await flushPromises();
        expect(started).toEqual(["a1", "b1", "b2"]);
        expect(signals.get("a1")!.aborted).toBe(true);
        expect(cancelBackendQueue).toHaveBeenCalledTimes(1);
        expect(cancelBackendQueue).toHaveBeenCalledWith("a");
        expect(scheduler.getSnapshot().queues.a.jobs.map(job => job.status)).toEqual(["cancelled", "cancelled"]);

        gates.get("a1")!.resolve({ status: "completed" });
        await flushPromises();
        expect(scheduler.getSnapshot().queues.a.jobs.map(job => job.status)).toEqual(["cancelled", "cancelled"]);

        backendGate.resolve(99);
        expect(await cancellation).toBe(2);
        gates.get("b1")!.resolve({ status: "completed" });
        await flushPromises();
        expect(started).toEqual(["a1", "b1", "b2"]);
        gates.get("b2")!.resolve({ status: "completed" });
        await flushPromises();
        expect(await scheduler.cancelQueue("a")).toBe(0);
        expect(cancelBackendQueue).toHaveBeenCalledTimes(1);
        await scheduler.dispose();
    });

    it("updates only the requested attempt and publishes immutable snapshots", async () => {
        const gate = deferred<Result>();
        const scheduler = createDynamicDownloadScheduler<JobPayload, Result>({
            concurrency: 2,
            execute: async () => gate.promise,
            classifyResult: (result) => result.status,
            cancelBackendQueue: vi.fn(async () => 0),
            now: monotonicTestClock(),
        });
        const listener = vi.fn();
        const unsubscribe = scheduler.subscribe(listener);
        const first = scheduler.enqueue(candidate("a", "first", { trackKey: "same" }), "unseen-only");
        gate.resolve({ status: "failed" });
        await flushPromises();
        const second = scheduler.enqueue(candidate("a", "second", { trackKey: "same" }), "retry-terminal");
        expect(second.added).toBe(true);
        scheduler.setBackendItemId(second.jobId!, "backend-2");
        const snapshot = scheduler.getSnapshot();
        expect(snapshot.queues.a.jobs.find(job => job.id === first.jobId)?.backendItemId).toBeUndefined();
        expect(snapshot.queues.a.jobs.find(job => job.id === second.jobId)?.backendItemId).toBe("backend-2");
        expect(Object.isFrozen(snapshot)).toBe(true);
        expect(Object.isFrozen(snapshot.queues)).toBe(true);
        expect(Object.isFrozen(snapshot.queues.a)).toBe(true);
        expect(Object.isFrozen(snapshot.queues.a.jobs)).toBe(true);
        expect(Object.isFrozen(snapshot.queues.a.jobs[0])).toBe(true);
        expect(listener).toHaveBeenCalled();
        unsubscribe();
        await scheduler.dispose();
    });

    it("keeps snapshot counts derived from job statuses", async () => {
        const gate = deferred<Result>();
        const scheduler = createDynamicDownloadScheduler<JobPayload, Result>({
            concurrency: 1,
            execute: async () => gate.promise,
            classifyResult: (result) => result.status,
            cancelBackendQueue: vi.fn(async () => 0),
            now: monotonicTestClock(),
        });
        scheduler.enqueue(candidate("a", "a1"), "unseen-only");
        expect(scheduler.getSnapshot().queues.a.counts).toEqual({
            total: 1,
            terminal: 0,
            active: 1,
            queued: 0,
            completed: 0,
            failed: 0,
        });
        gate.resolve({ status: "skipped" });
        await flushPromises();
        expect(scheduler.getSnapshot().queues.a.counts).toEqual({
            total: 1,
            terminal: 1,
            active: 0,
            queued: 0,
            completed: 0,
            failed: 0,
        });
        expect(scheduler.getSnapshot().queues.a.progress).toBe(1);
        await scheduler.dispose();
    });

    it("rejects invalid concurrency", () => {
        expect(() => createDynamicDownloadScheduler<JobPayload, Result>({
            concurrency: 0,
            execute: async () => ({ status: "completed" }),
            classifyResult: (result) => result.status,
            cancelBackendQueue: vi.fn(async () => 0),
            now: monotonicTestClock(),
        })).toThrow(RangeError);
    });

    it("rejects a concurrency above the hard global peak of three", () => {
        expect(() => createDynamicDownloadScheduler<JobPayload, Result>({
            concurrency: 4,
            execute: async () => ({ status: "completed" }),
            classifyResult: (result) => result.status,
            cancelBackendQueue: vi.fn(async () => 0),
            now: monotonicTestClock(),
        })).toThrow(RangeError);
    });

    it("preserves waiting queue order when A is active and B then C are appended", async () => {
        const started: string[] = [];
        const gates = new Map<string, ReturnType<typeof deferred<Result>>>();
        const scheduler = createDynamicDownloadScheduler<JobPayload, Result>({
            concurrency: 1,
            execute: async (job) => {
                started.push(job.id);
                const gate = deferred<Result>();
                gates.set(job.id, gate);
                return gate.promise;
            },
            classifyResult: (result) => result.status,
            cancelBackendQueue: vi.fn(async () => 0),
            now: monotonicTestClock(),
        });

        enqueueMany(scheduler, "playlist:a", ["a1"]);
        enqueueMany(scheduler, "playlist:b", ["b1"]);
        enqueueMany(scheduler, "playlist:c", ["c1"]);
        expect(started).toEqual(["a1"]);

        gates.get("a1")!.resolve({ status: "completed" });
        await flushPromises();
        expect(started).toEqual(["a1", "b1"]);

        gates.get("b1")!.resolve({ status: "completed" });
        await flushPromises();
        expect(started).toEqual(["a1", "b1", "c1"]);
        gates.get("c1")!.resolve({ status: "completed" });
        await flushPromises();
        await scheduler.dispose();
    });
});

export type SchedulerTestSnapshot = DownloadSchedulerSnapshot<JobPayload, Result>;
