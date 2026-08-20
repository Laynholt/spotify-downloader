import { useState, useEffect, useRef } from "react";
import { GetDownloadProgress } from "../../wailsjs/go/main/App";
import { useDownloadQueueData } from "@/hooks/useDownloadQueueData";
import type { backend } from "../../wailsjs/go/models";
export interface DownloadProgressInfo {
    is_downloading: boolean;
    mb_downloaded: number;
    speed_mbps: number;
    active_count: number;
    settled_count: number;
    total_count: number;
    queue: backend.DownloadItem[];
}
export function useDownloadProgress() {
    const queueInfo = useDownloadQueueData();
    const [progress, setProgress] = useState<DownloadProgressInfo>({
        is_downloading: false,
        mb_downloaded: 0,
        speed_mbps: 0,
        active_count: 0,
        settled_count: 0,
        total_count: 0,
        queue: [],
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
        active_count: queueInfo.active_count ?? queueInfo.queue.filter((item) => item.status === "downloading").length,
        settled_count: queueInfo.settled_count,
        total_count: queueInfo.total_count,
        queue: queueInfo.queue,
    };
}
