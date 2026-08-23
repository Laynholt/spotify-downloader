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
    if (!Number.isFinite(concurrency) || !Number.isInteger(concurrency) || concurrency <= 0) {
        throw new RangeError("concurrency must be a positive integer");
    }

    const now = options.now ?? Date.now;
    const queues = new Map<string, MutableQueue<TPayload, TResult>>();
    const queueOrder: string[] = [];
    const listeners = new Set<() => void>();
    const usedJobIds = new Set<string>();
    const attemptsByKey = new Map<string, number>();
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
        queues.set(queue.id, queue);
        queueOrder.push(queue.id);
        rotationCursor = queueOrder.length - 1;
        return queue;
    };

    const matchingKey = (candidate: DownloadJobCandidate<TPayload>): string =>
        `${candidate.queueId}\u0000${candidate.trackKey}\u0000${candidate.kind}`;

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
                rotationCursor = (index + 1) % queueOrder.length;
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
            Promise.resolve(execution).then(
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
                    job.status = "failed";
                    job.error = errorMessage(reason);
                    job.finishedAt = now();
                    job.controller = undefined;
                    publish();
                    pump();
                },
            );
        }
    };

    const enqueue = (candidate: DownloadJobCandidate<TPayload>, policy: EnqueuePolicy): EnqueueDecision => {
        if (disposed) {
            throw new Error("scheduler has been disposed");
        }
        const queue = queueFor(candidate);
        const matchingJobs = queue.jobs.filter(job =>
            job.trackKey === candidate.trackKey && job.kind === candidate.kind,
        );
        if (policy === "unseen-only" && matchingJobs.length > 0) {
            return { added: false, reason: "already-represented" };
        }
        const latest = matchingJobs[matchingJobs.length - 1];
        if (policy === "retry-terminal" && latest && !isTerminal(latest.status)) {
            return { added: false, reason: "already-in-flight" };
        }

        const jobId = createJobId(candidate);
        const timestamp = now();
        const unavailable = candidate.unavailableReason !== undefined;
        const job: MutableDownloadJob<TPayload, TResult> = {
            ...candidate,
            id: jobId,
            status: unavailable || queue.cancelled ? (unavailable ? "unavailable" : "cancelled") : "queued",
            force: policy === "retry-terminal" && matchingJobs.length > 0,
            enqueuedAt: timestamp,
            ...(unavailable ? { error: candidate.unavailableReason, finishedAt: timestamp } : {}),
            ...(queue.cancelled && !unavailable ? { finishedAt: timestamp } : {}),
            settled: unavailable || queue.cancelled,
        };
        queue.jobs.push(job);
        publish();
        if (!job.settled) {
            pump();
        }
        return { added: true, jobId };
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
                    job.status = "cancelled";
                    job.finishedAt = now();
                    job.settled = true;
                    job.controller = undefined;
                    activeCount--;
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

    return { enqueue, setBackendItemId, cancelQueue, getSnapshot, subscribe, dispose };
}
