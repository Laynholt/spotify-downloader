import { useEffect, useState } from "react";
import { GetDownloadQueue } from "../../wailsjs/go/main/App";
import { backend } from "../../wailsjs/go/models";
export function useDownloadQueueData() {
    const [queueInfo, setQueueInfo] = useState<backend.DownloadQueueInfo>(new backend.DownloadQueueInfo({
        is_downloading: false,
        queue: [],
        current_speed: 0,
        total_downloaded: 0,
        session_start_time: 0,
        queued_count: 0,
        completed_count: 0,
        failed_count: 0,
        skipped_count: 0,
        active_count: 0,
        cancelled_count: 0,
    }));
    useEffect(() => {
        const fetchQueue = async () => {
            try {
                const info = await GetDownloadQueue();
                setQueueInfo(info);
            }
            catch (error) {
                console.error("Failed to get download queue:", error);
            }
        };
        fetchQueue();
        const interval = setInterval(fetchQueue, 200);
        return () => clearInterval(interval);
    }, []);
    const settledCount = queueInfo.completed_count + queueInfo.failed_count + queueInfo.skipped_count + (queueInfo.cancelled_count ?? 0);
    return {
        ...queueInfo,
        settled_count: settledCount,
        total_count: queueInfo.queue.length,
    };
}
