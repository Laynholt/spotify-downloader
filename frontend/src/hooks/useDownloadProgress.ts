import { useDownloadQueueData } from "@/hooks/useDownloadQueueData";

export type DownloadProgressInfo = ReturnType<typeof useDownloadQueueData>;

export function useDownloadProgress(): DownloadProgressInfo {
    return useDownloadQueueData();
}
