import { Button } from "@/components/ui/button";
import { Progress } from "@/components/ui/progress";
import { useDownloadQueueData } from "@/hooks/useDownloadQueueData";
import { phaseLabel } from "@/lib/download-timings";
import { StopCircle } from "lucide-react";
interface DownloadProgressProps {
    progress: number;
    currentTrack: {
        name: string;
        artists: string;
    } | null;
    onStop: () => void;
}
export function DownloadProgress({ progress, currentTrack, onStop }: DownloadProgressProps) {
    const queueInfo = useDownloadQueueData();
    const clampedProgress = Math.min(100, Math.max(0, progress));
    const activeItems = queueInfo.queue.filter((item) => item.status === "downloading" || item.status === "queued");
    const formatBytes = (bytes: number) => {
        if (!Number.isFinite(bytes) || bytes <= 0) {
            return "0 B";
        }
        if (bytes < 1024 * 1024) {
            return `${(bytes / 1024).toFixed(0)} KB`;
        }
        return `${(bytes / (1024 * 1024)).toFixed(2)} MB`;
    };
    return (<div className="w-full space-y-2 mt-4">
      <div className="flex items-center gap-2">
        <Progress value={clampedProgress} className="h-2 flex-1"/>
        <Button variant="destructive" size="sm" onClick={onStop} className="gap-1.5">
          <StopCircle className="h-4 w-4"/>
          Stop
        </Button>
      </div>
      <p className="text-xs text-muted-foreground">
        {clampedProgress}% -{" "}
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
          <div className="flex items-center justify-between gap-2 font-mono text-muted-foreground">
            <span>
              {item.total_bytes > 0
                  ? `${formatBytes(item.downloaded_bytes)} / ${formatBytes(item.total_bytes)}`
                  : item.downloaded_bytes > 0
                      ? `${formatBytes(item.downloaded_bytes)} downloaded`
                      : "Waiting for byte progress"}
            </span>
            {item.warning && <span className="truncate text-yellow-600 dark:text-yellow-400">{item.warning}</span>}
          </div>
        </div>))}
        {activeItems.length > 3 && <div className="text-xs text-muted-foreground">+{activeItems.length - 3} more active tracks</div>}
      </div>)}
    </div>);
}
