import { Button } from "@/components/ui/button";
import { useDownloadQueueData } from "@/hooks/useDownloadQueueData";
import { formatAggregateProgress, formatTransferProgress, phaseLabel } from "@/lib/download-timings";
import { StopCircle } from "lucide-react";
interface DownloadProgressProps {
    progress: number;
    currentTrack: {
        name: string;
        artists: string;
    } | null;
    onStop: () => void;
}
export function DownloadProgress({ currentTrack, onStop }: DownloadProgressProps) {
    const queueInfo = useDownloadQueueData();
    const activeItems = queueInfo.queue.filter((item) => item.status === "downloading" || item.status === "queued");
    const activeCount = queueInfo.active_count ?? queueInfo.queue.filter((item) => item.status === "downloading").length;
    const settledCount = queueInfo.completed_count + queueInfo.failed_count + queueInfo.skipped_count + (queueInfo.cancelled_count ?? 0);
    const aggregate = formatAggregateProgress(activeCount, settledCount, queueInfo.queue.length);
    return (<div className="w-full space-y-2 mt-4">
      <div className="flex items-center gap-2">
        <p className="flex-1 text-sm font-medium tabular-nums">{aggregate}</p>
        <Button variant="destructive" size="sm" onClick={onStop} className="gap-1.5">
          <StopCircle className="h-4 w-4"/>
          Stop
        </Button>
      </div>
      <p className="text-xs text-muted-foreground">
        {currentTrack
            ? `${currentTrack.name} - ${currentTrack.artists}`
            : "Preparing download..."}
      </p>
      {activeItems.length > 0 && (<div className="space-y-1.5 rounded-md border border-border/60 px-3 py-2">
        {activeItems.slice(0, 3).map((item) => (<div key={item.id} className="space-y-0.5 text-xs">
          <div className="flex items-center justify-between gap-2">
            <span className="truncate font-medium">{phaseLabel(item.phase)}</span>
            {item.speed > 0 && <span className="shrink-0 font-mono text-muted-foreground">{item.speed.toFixed(2)} MB/s</span>}
          </div>
          <div className="truncate text-muted-foreground">{item.detail || item.track_name}</div>
          {formatTransferProgress(item.downloaded_bytes, item.total_bytes) && <div className="font-mono text-muted-foreground">{formatTransferProgress(item.downloaded_bytes, item.total_bytes)}</div>}
          {item.warning && <div className="truncate text-yellow-600 dark:text-yellow-400">{item.warning}</div>}
        </div>))}
        {activeItems.length > 3 && <div className="text-xs text-muted-foreground">+{activeItems.length - 3} more active tracks</div>}
      </div>)}
    </div>);
}
