import { useSyncExternalStore } from "react";
import { GetDownloadQueue, GetDownloadProgress } from "../../wailsjs/go/main/App";
import type { backend } from "../../wailsjs/go/models";

interface QueueData extends Omit<backend.DownloadQueueInfo, "convertValues"> {
    mb_downloaded: number;
    speed_mbps: number;
    settled_count: number;
    total_count: number;
    itemsByJobId: ReadonlyMap<string, backend.DownloadItem>;
}

let snapshot: QueueData = {
    is_downloading: false, queue: [], current_speed: 0, total_downloaded: 0,
    session_start_time: 0, queued_count: 0, completed_count: 0, failed_count: 0,
    skipped_count: 0, active_count: 0, cancelled_count: 0,
    mb_downloaded: 0, speed_mbps: 0, settled_count: 0, total_count: 0,
    itemsByJobId: new Map(),
};
const listeners = new Set<() => void>();
let timer: ReturnType<typeof setTimeout> | undefined;
let polling = false;

async function poll() {
    if (polling || listeners.size === 0) return;
    polling = true;
    try {
        const queue = await GetDownloadQueue();
        // Only the installer needs the legacy endpoint; track totals are already in the queue.
        const installer = queue.is_downloading && !queue.active_count && !queue.queued_count
            ? await GetDownloadProgress() : undefined;
        if (listeners.size === 0) return;
        snapshot = {
            ...queue,
            mb_downloaded: installer?.mb_downloaded ?? queue.total_downloaded,
            speed_mbps: installer?.speed_mbps ?? queue.current_speed,
            settled_count: queue.completed_count + queue.failed_count + queue.skipped_count + (queue.cancelled_count ?? 0),
            total_count: queue.queue.length,
            itemsByJobId: new Map(queue.queue.map(item => [item.id, item])),
        };
        listeners.forEach(listener => listener());
    } catch (error) {
        console.error("Failed to get download queue:", error);
    } finally {
        polling = false;
        if (listeners.size > 0) {
            timer = setTimeout(() => { timer = undefined; void poll(); },
                snapshot.is_downloading || snapshot.queued_count > 0 ? 200 : 1000);
        }
    }
}

export const downloadQueueStore = {
    getSnapshot: () => snapshot,
    subscribe(listener: () => void) {
        listeners.add(listener);
        if (listeners.size === 1 && !timer) void poll();
        return () => {
            listeners.delete(listener);
            if (listeners.size === 0 && timer) {
                clearTimeout(timer);
                timer = undefined;
            }
        };
    },
};

export function useDownloadQueueData() {
    return useSyncExternalStore(downloadQueueStore.subscribe, downloadQueueStore.getSnapshot, downloadQueueStore.getSnapshot);
}
