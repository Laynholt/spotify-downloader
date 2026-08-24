import { useState, useEffect, useRef } from "react";
import { GetDownloadProgress } from "../../wailsjs/go/main/App";
import { useDownloadQueueData } from "@/hooks/useDownloadQueueData";
import type { backend } from "../../wailsjs/go/models";
export interface DownloadProgressInfo {
    is_downloading: boolean;
    mb_downloaded: number;
    speed_mbps: number;
    current_speed: number;
    total_downloaded: number;
    session_start_time: number;
    queued_count: number;
    completed_count: number;
    failed_count: number;
    skipped_count: number;
    cancelled_count: number;
    active_count: number;
    settled_count: number;
    total_count: number;
    queue: backend.DownloadItem[];
    itemsByJobId: ReadonlyMap<string, backend.DownloadItem>;
}
export function useDownloadProgress() {
    const queueInfo = useDownloadQueueData();
    const [progress, setProgress] = useState<DownloadProgressInfo>({
        is_downloading: false,
        mb_downloaded: 0,
        speed_mbps: 0,
        current_speed: 0,
        total_downloaded: 0,
        session_start_time: 0,
        queued_count: 0,
        completed_count: 0,
        failed_count: 0,
        skipped_count: 0,
        cancelled_count: 0,
        active_count: 0,
        settled_count: 0,
        total_count: 0,
        queue: [],
        itemsByJobId: new Map(),
    });
    const intervalRef = useRef<number | null>(null);
    useEffect(() => {
        const pollProgress = async () => {
            try {
                const progressInfo = await GetDownloadProgress();
                setProgress((previous) => ({
                    ...previous,
                    ...progressInfo,
                }));
            }
            catch (error) {
                console.error("Failed to get download progress:", error);
            }
        };
        intervalRef.current = window.setInterval(pollProgress, 200);
        pollProgress();
        return () => {
            if (intervalRef.current) {
                clearInterval(intervalRef.current);
            }
        };
    }, []);
    return {
        ...progress,
        current_speed: queueInfo.current_speed,
        total_downloaded: queueInfo.total_downloaded,
        session_start_time: queueInfo.session_start_time,
        queued_count: queueInfo.queued_count,
        completed_count: queueInfo.completed_count,
        failed_count: queueInfo.failed_count,
        skipped_count: queueInfo.skipped_count,
        cancelled_count: queueInfo.cancelled_count ?? 0,
        active_count: queueInfo.active_count ?? queueInfo.queue.filter((item) => item.status === "downloading").length,
        settled_count: queueInfo.settled_count,
        total_count: queueInfo.total_count,
        queue: queueInfo.queue,
        itemsByJobId: queueInfo.itemsByJobId,
    };
}
