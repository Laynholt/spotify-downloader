import { useDownloadProgress } from "@/hooks/useDownloadProgress";
import { formatAggregateProgress, formatTransferProgress, phaseLabel } from "@/lib/download-timings";
import { Download, ChevronRight } from "lucide-react";
import { Button } from "@/components/ui/button";
interface DownloadProgressToastProps {
    onClick: () => void;
}
export function DownloadProgressToast({ onClick }: DownloadProgressToastProps) {
    const progress = useDownloadProgress();
    const activeItems = progress.queue.filter((item) => item.status === "queued" || item.status === "downloading");
    const hasActiveDownloads = activeItems.length > 0;
    if (!hasActiveDownloads) {
        return null;
    }
    const settled = progress.settled_count;
    const aggregate = formatAggregateProgress(progress.active_count, settled, progress.total_count);
    const primaryItem = activeItems.find((item) => item.status === "downloading") || activeItems[0];
    const bytes = formatTransferProgress(primaryItem.downloaded_bytes, primaryItem.total_bytes) || "Preparing";
    return (<div className="fixed bottom-4 left-[calc(56px+1rem)] z-50 animate-in slide-in-from-bottom-5 data-[state=closed]:animate-out data-[state=closed]:slide-out-to-bottom-5">
      <Button variant="outline" className="bg-background border rounded-lg shadow-lg p-3 h-auto hover:bg-muted/50 transition-colors cursor-pointer" onClick={onClick}>
        <div className="flex items-center gap-3">
          <Download className={`h-4 w-4 text-primary ${progress.is_downloading ? 'animate-bounce' : ''}`}/>
          <div className="flex min-w-[170px] flex-col items-start">
            <p className="text-sm font-medium tabular-nums">{aggregate}</p>
            <p className="max-w-[220px] truncate text-xs text-muted-foreground">
              {phaseLabel(primaryItem.phase)}{primaryItem.detail ? ` · ${primaryItem.detail}` : ""}
            </p>
            <p className="text-xs text-muted-foreground font-mono tabular-nums">
              {bytes}
              {primaryItem.speed > 0 && ` · ${primaryItem.speed.toFixed(2)} MB/s`}
            </p>
            {primaryItem.warning && <p className="max-w-[220px] truncate text-xs text-yellow-600 dark:text-yellow-400">{primaryItem.warning}</p>}
          </div>
          <ChevronRight className="h-4 w-4 text-muted-foreground ml-1"/>
        </div>
      </Button>
    </div>);
}
