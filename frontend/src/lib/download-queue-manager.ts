export type DownloadJobKind = "spotify-download" | "youtube-redownload";

export type DownloadJobStatus =
    | "queued"
    | "active"
    | "completed"
    | "skipped"
    | "failed"
    | "suspicious"
    | "cancelled"
    | "unavailable";

export type EnqueuePolicy = "unseen-only" | "retry-terminal";

export interface DownloadJobCandidate<TPayload> {
    id: string;
    queueId: string;
    queueTitle: string;
    trackKey: string;
    sourceIndex: number;
    kind: DownloadJobKind;
    payload: TPayload;
    unavailableReason?: string;
}

export interface DownloadJob<TPayload, TResult> extends DownloadJobCandidate<TPayload> {
    status: DownloadJobStatus;
    force: boolean;
    backendItemId?: string;
    result?: TResult;
    error?: string;
    enqueuedAt: number;
    startedAt?: number;
    finishedAt?: number;
}

export interface QueueCounts {
    total: number;
    terminal: number;
    active: number;
    queued: number;
    completed: number;
    failed: number;
}

export interface DownloadQueueSnapshot<TPayload, TResult> {
    id: string;
    title: string;
    jobs: ReadonlyArray<DownloadJob<TPayload, TResult>>;
    counts: QueueCounts;
    progress: number;
}

export interface DownloadSchedulerSnapshot<TPayload, TResult> {
    queues: Readonly<Record<string, DownloadQueueSnapshot<TPayload, TResult>>>;
    activeCount: number;
    peakActive: number;
}

export interface EnqueueDecision {
    added: boolean;
    jobId?: string;
    reason?: "already-represented" | "already-in-flight";
}

export interface DynamicDownloadScheduler<TPayload, TResult> {
    enqueue(candidate: DownloadJobCandidate<TPayload>, policy: EnqueuePolicy): EnqueueDecision;
    enqueueMany(candidates: DownloadJobCandidate<TPayload>[], policy: EnqueuePolicy): EnqueueDecision[];
    waitForIdle(): Promise<void>;
    clearCompleted(): void;
    reset(): void;
    setBackendItemId(jobId: string, backendItemId: string): void;
    cancelQueue(queueId: string): Promise<number>;
    getSnapshot(): DownloadSchedulerSnapshot<TPayload, TResult>;
    subscribe(listener: () => void): () => void;
    dispose(): Promise<void>;
}

export interface DynamicDownloadSchedulerOptions<TPayload, TResult> {
    concurrency: number;
    execute: (job: DownloadJob<TPayload, TResult>, signal: AbortSignal) => Promise<TResult>;
    classifyResult: (result: TResult) => DownloadJobStatus;
    cancelBackendQueue: (queueId: string) => Promise<number>;
    now?: () => number;
}

interface MutableDownloadJob<TPayload, TResult> extends DownloadJob<TPayload, TResult> {
    settled: boolean;
    controller?: AbortController;
}

interface MutableQueue<TPayload, TResult> {
    id: string;
    title: string;
    jobs: MutableDownloadJob<TPayload, TResult>[];
    cancelled: boolean;
    cancelPromise?: Promise<number>;
}

function isTerminal(status: DownloadJobStatus): boolean {
    return status === "completed" || status === "skipped" || status === "failed" ||
        status === "suspicious" || status === "cancelled" || status === "unavailable";
}

function errorMessage(reason: unknown): string {
    if (reason instanceof Error) {
        return reason.message;
    }
    return String(reason);
}

export function createDynamicDownloadScheduler<TPayload, TResult>(
    options: DynamicDownloadSchedulerOptions<TPayload, TResult>,
): DynamicDownloadScheduler<TPayload, TResult> {
    const { concurrency, execute, classifyResult, cancelBackendQueue } = options;
    if (!Number.isFinite(concurrency) || !Number.isInteger(concurrency) || concurrency <= 0 || concurrency > 3) {
        throw new RangeError("concurrency must be an integer from 1 through 3");
    }

    const now = options.now ?? Date.now;
    const queues = new Map<string, MutableQueue<TPayload, TResult>>();
    const queueOrder: string[] = [];
    const listeners = new Set<() => void>();
    const usedJobIds = new Set<string>();
    const attemptsByKey = new Map<string, number>();
    const latestByKey = new Map<string, MutableDownloadJob<TPayload, TResult>>();
    const executions = new Set<Promise<void>>();
    let rotationCursor = 0;
    let activeCount = 0;
    let peakActive = 0;
    let disposed = false;
    let snapshot: DownloadSchedulerSnapshot<TPayload, TResult>;

    const createSnapshot = (): DownloadSchedulerSnapshot<TPayload, TResult> => {
        const queueSnapshots: Record<string, DownloadQueueSnapshot<TPayload, TResult>> = {};
        for (const [queueId, queue] of queues) {
            const jobs = queue.jobs.map(job => {
                const { settled: _settled, controller: _controller, ...publicJob } = job;
                void _settled;
                void _controller;
                return Object.freeze({ ...publicJob });
            });
            const counts: QueueCounts = {
                total: jobs.length,
                terminal: jobs.filter(job => isTerminal(job.status)).length,
                active: jobs.filter(job => job.status === "active").length,
                queued: jobs.filter(job => job.status === "queued").length,
                completed: jobs.filter(job => job.status === "completed").length,
                failed: jobs.filter(job => job.status === "failed").length,
            };
            const queueSnapshot: DownloadQueueSnapshot<TPayload, TResult> = {
                id: queue.id,
                title: queue.title,
                jobs: Object.freeze(jobs),
                counts: Object.freeze(counts),
                progress: counts.total === 0 ? 0 : counts.terminal / counts.total,
            };
            queueSnapshots[queueId] = Object.freeze(queueSnapshot);
        }
        return Object.freeze({
            queues: Object.freeze(queueSnapshots),
            activeCount,
            peakActive,
        });
    };

    const publish = (): void => {
        snapshot = createSnapshot();
        for (const listener of listeners) {
            listener();
        }
    };

    snapshot = createSnapshot();

    const queueFor = (candidate: DownloadJobCandidate<TPayload>): MutableQueue<TPayload, TResult> => {
        const existing = queues.get(candidate.queueId);
        if (existing) {
            return existing;
        }
        const queue: MutableQueue<TPayload, TResult> = {
            id: candidate.queueId,
            title: candidate.queueTitle,
            jobs: [],
            cancelled: false,
        };
        const isFirstQueue = queueOrder.length === 0;
        queues.set(queue.id, queue);
        queueOrder.push(queue.id);
        if (isFirstQueue) {
            rotationCursor = 0;
        }
        return queue;
    };

    const matchingKey = (candidate: DownloadJobCandidate<TPayload>): string =>
        `${candidate.queueId}\u0000${candidate.trackKey}`;

    const createJobId = (candidate: DownloadJobCandidate<TPayload>): string => {
        if (!usedJobIds.has(candidate.id)) {
            usedJobIds.add(candidate.id);
            return candidate.id;
        }
        const key = matchingKey(candidate);
        let attempt = (attemptsByKey.get(key) ?? 1) + 1;
        let jobId = `${candidate.id}#${attempt}`;
        while (usedJobIds.has(jobId)) {
            attempt++;
            jobId = `${candidate.id}#${attempt}`;
        }
        attemptsByKey.set(key, attempt);
        usedJobIds.add(jobId);
        return jobId;
    };

    const nextRunnableQueue = (): MutableQueue<TPayload, TResult> | undefined => {
        for (let checked = 0; checked < queueOrder.length; checked++) {
            const index = (rotationCursor + checked) % queueOrder.length;
            const queueId = queueOrder[index];
            const queue = queueId === undefined ? undefined : queues.get(queueId);
            if (queue && !queue.cancelled && queue.jobs.some(job => job.status === "queued")) {
                // Keep the cursor as the next absolute queue position. Keeping a
                // value equal to the old queue count lets an appended queue take
                // the next slot without resetting fairness to the newest queue.
                rotationCursor = index + 1;
                return queue;
            }
        }
        return undefined;
    };

    const pump = (): void => {
        while (!disposed && activeCount < concurrency) {
            const queue = nextRunnableQueue();
            if (!queue) {
                return;
            }
            const job = queue.jobs.find(candidateJob => candidateJob.status === "queued");
            if (!job) {
                continue;
            }
            job.status = "active";
            job.startedAt = now();
            job.controller = new AbortController();
            activeCount++;
            peakActive = Math.max(peakActive, activeCount);
            publish();

            let execution: Promise<TResult>;
            try {
                execution = execute(job, job.controller.signal);
            }
            catch (reason) {
                execution = Promise.reject(reason);
            }
            const completion = Promise.resolve(execution).then(
                result => {
                    if (job.settled) {
                        return;
                    }
                    job.settled = true;
                    activeCount--;
                    job.result = result;
                    try {
                        job.status = classifyResult(result);
                    }
                    catch (reason) {
                        job.status = "failed";
                        job.error = errorMessage(reason);
                    }
                    if (!isTerminal(job.status)) {
                        job.status = "failed";
                        job.error = "classifyResult returned a non-terminal status";
                    }
                    job.finishedAt = now();
                    job.controller = undefined;
                    publish();
                    pump();
                },
                reason => {
                    if (job.settled) {
                        return;
                    }
                    job.settled = true;
                    activeCount--;
                    job.status = job.controller?.signal.aborted ? "cancelled" : "failed";
                    job.error = errorMessage(reason);
                    job.finishedAt = now();
                    job.controller = undefined;
                    publish();
                    pump();
                },
            );
            executions.add(completion);
            void completion.then(() => executions.delete(completion));
        }
    };

    const addJob = (candidate: DownloadJobCandidate<TPayload>, policy: EnqueuePolicy): EnqueueDecision => {
        if (disposed) {
            throw new Error("scheduler has been disposed");
        }
        const queue = queueFor(candidate);
        const latest = latestByKey.get(matchingKey(candidate));
        if (policy === "unseen-only" && latest) {
            return { added: false, reason: "already-represented" };
        }
        if (policy === "retry-terminal" && latest && !isTerminal(latest.status)) {
            return { added: false, reason: "already-in-flight" };
        }

        queue.cancelled = false;
        queue.cancelPromise = undefined;

        const jobId = createJobId(candidate);
        const timestamp = now();
        const unavailable = candidate.unavailableReason !== undefined;
        const job: MutableDownloadJob<TPayload, TResult> = {
            ...candidate,
            id: jobId,
            status: unavailable ? "unavailable" : "queued",
            force: policy === "retry-terminal" && latest !== undefined,
            enqueuedAt: timestamp,
            ...(unavailable ? { error: candidate.unavailableReason, finishedAt: timestamp } : {}),
            settled: unavailable,
        };
        queue.jobs.push(job);
        latestByKey.set(matchingKey(candidate), job);
        return { added: true, jobId };
    };

    const enqueueMany = (candidates: DownloadJobCandidate<TPayload>[], policy: EnqueuePolicy): EnqueueDecision[] => {
        const decisions = candidates.map(candidate => addJob(candidate, policy));
        if (decisions.some(decision => decision.added)) {
            publish();
            pump();
        }
        return decisions;
    };
    const enqueue = (candidate: DownloadJobCandidate<TPayload>, policy: EnqueuePolicy): EnqueueDecision =>
        enqueueMany([candidate], policy)[0]!;

    const clearCompleted = (): void => {
        for (const queue of queues.values()) queue.jobs = queue.jobs.filter(job => !isTerminal(job.status));
        publish();
    };
    const reset = (): void => {
        if (activeCount > 0) throw new Error("Stop active downloads before starting a new session");
        queues.clear();
        queueOrder.length = 0;
        latestByKey.clear();
        usedJobIds.clear();
        attemptsByKey.clear();
        rotationCursor = 0;
        peakActive = 0;
        publish();
    };

    const setBackendItemId = (jobId: string, backendItemId: string): void => {
        for (const queue of queues.values()) {
            const job = queue.jobs.find(candidateJob => candidateJob.id === jobId);
            if (!job) {
                continue;
            }
            job.backendItemId = backendItemId;
            publish();
            return;
        }
    };

    const cancelQueue = async (queueId: string): Promise<number> => {
        const queue = queues.get(queueId);
        if (!queue) {
            return 0;
        }
        let targeted = 0;
        if (!queue.cancelled) {
            queue.cancelled = true;
            for (const job of queue.jobs) {
                if (job.status === "queued") {
                    job.status = "cancelled";
                    job.finishedAt = now();
                    job.settled = true;
                    targeted++;
                }
                else if (job.status === "active") {
                    job.controller?.abort();
                    targeted++;
                }
            }
            publish();
            pump();
        }
        if (!queue.cancelPromise) {
            let backendResult: Promise<number>;
            try {
                backendResult = cancelBackendQueue(queueId);
            }
            catch (reason) {
                backendResult = Promise.reject(reason);
            }
            queue.cancelPromise = Promise.resolve(backendResult);
        }
        await queue.cancelPromise;
        return targeted;
    };

    const getSnapshot = (): DownloadSchedulerSnapshot<TPayload, TResult> => snapshot;

    const subscribe = (listener: () => void): (() => void) => {
        listeners.add(listener);
        return () => listeners.delete(listener);
    };

    const dispose = async (): Promise<void> => {
        if (disposed) {
            return;
        }
        disposed = true;
        for (const queue of queues.values()) {
            queue.cancelled = true;
            for (const job of queue.jobs) {
                if (job.status === "active") {
                    job.controller?.abort();
                    job.controller = undefined;
                    activeCount--;
                    job.status = "cancelled";
                    job.finishedAt = now();
                    job.settled = true;
                }
                else if (job.status === "queued") {
                    job.status = "cancelled";
                    job.finishedAt = now();
                    job.settled = true;
                }
            }
        }
        publish();
        listeners.clear();
    };

    return { enqueue, enqueueMany, waitForIdle: async () => { await Promise.all(executions); }, clearCompleted, reset,
        setBackendItemId, cancelQueue, getSnapshot, subscribe, dispose };
}
