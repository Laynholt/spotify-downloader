export type PoolResult<R> =
    | { index: number; status: "fulfilled"; value: R }
    | { index: number; status: "rejected"; reason: unknown }
    | { index: number; status: "cancelled" };

export interface PoolOptions<T, R> {
    items: readonly T[];
    concurrency: number;
    shouldStop: () => boolean;
    worker: (item: T, index: number) => Promise<R>;
    onSettled?: (result: PoolResult<R>) => void;
}

export async function runBoundedPool<T, R>(options: PoolOptions<T, R>): Promise<PoolResult<R>[]> {
    const { items, concurrency, shouldStop, worker, onSettled } = options;
    if (!Number.isFinite(concurrency) || !Number.isInteger(concurrency) || concurrency <= 0) {
        throw new RangeError("concurrency must be a positive integer");
    }
    const results: Array<PoolResult<R> | undefined> = new Array(items.length);
    let nextIndex = 0;
    const workerCount = Math.min(concurrency, items.length);

    const runWorker = async (): Promise<void> => {
        while (nextIndex < items.length) {
            if (shouldStop()) {
                return;
            }

            const index = nextIndex++;
            const item = items[index];
            let result: PoolResult<R>;
            try {
                const value = await worker(item, index);
                result = { index, status: "fulfilled", value };
            }
            catch (reason) {
                result = { index, status: "rejected", reason };
            }
            results[index] = result;
            onSettled?.(result);
        }
    };

    await Promise.all(Array.from({ length: workerCount }, () => runWorker()));

    for (let index = 0; index < items.length; index++) {
        if (results[index]) {
            continue;
        }
        const result: PoolResult<R> = { index, status: "cancelled" };
        results[index] = result;
        onSettled?.(result);
    }

    return results as PoolResult<R>[];
}
