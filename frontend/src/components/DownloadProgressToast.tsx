import { formatAggregateProgress, formatTransferProgress, phaseLabel } from "@/lib/download-timings";
import { selectLatestTrackAttempt } from "@/lib/download-queue-selectors";
import type { MediaJobPayload, MediaJobResult } from "@/lib/download-jobs";
import type { DownloadSchedulerSnapshot } from "@/lib/download-queue-manager";
import type { backend } from "../../wailsjs/go/models";
import { Download, ChevronRight } from "lucide-react";
import { Button } from "@/components/ui/button";
interface DownloadProgressToastProps {
    onClick: () => void;
    snapshot: DownloadSchedulerSnapshot<MediaJobPayload, MediaJobResult>;
    itemsByJobId: ReadonlyMap<string, backend.DownloadItem>;
}
export function DownloadProgressToast({ onClick, snapshot, itemsByJobId }: DownloadProgressToastProps) {
    const queues = Object.values(snapshot.queues);
    const jobs = queues.flatMap(queue => queue.jobs);
    const activeJobs = jobs.filter(job => job.status === "queued" || job.status === "active");
    if (activeJobs.length === 0) {
        return null;
    }
    const aggregate = formatAggregateProgress(
        jobs.filter(job => job.status === "active").length,
        jobs.length - activeJobs.length,
        jobs.length,
    );
    const primaryJob = activeJobs.find(job => job.status === "active") || activeJobs[0];
    const primaryQueue = queues.find(queue => queue.id === primaryJob.queueId);
    if (!primaryQueue) return null;

    const attempt = selectLatestTrackAttempt(primaryQueue, primaryJob.trackKey, itemsByJobId);
    if (!attempt) return null;

    const speedMbps = attempt.speedMbps ?? 0;
    const bytes = attempt.totalBytes
        ? formatTransferProgress(attempt.downloadedBytes ?? 0, attempt.totalBytes)
        : "Preparing";
    return (<div className="fixed bottom-4 left-[calc(56px+1rem)] z-50 animate-in slide-in-from-bottom-5 data-[state=closed]:animate-out data-[state=closed]:slide-out-to-bottom-5">
      <Button variant="outline" className="bg-background border rounded-lg shadow-lg p-3 h-auto hover:bg-muted/50 transition-colors cursor-pointer" onClick={onClick}>
        <div className="flex items-center gap-3">
          <Download className={`h-4 w-4 text-primary ${primaryJob.status === "active" ? 'animate-bounce' : ''}`}/>
          <div className="flex min-w-[170px] flex-col items-start">
            <p className="text-sm font-medium tabular-nums">{aggregate}</p>
            <p className="max-w-[220px] truncate text-xs text-muted-foreground">
              {phaseLabel(attempt.phase || primaryJob.status)}{attempt.detail ? ` · ${attempt.detail}` : ""}
            </p>
            <p className="text-xs text-muted-foreground font-mono tabular-nums">
              {bytes}
              {speedMbps > 0 && ` · ${speedMbps.toFixed(2)} MB/s`}
            </p>
            {attempt.warning && <p className="max-w-[220px] truncate text-xs text-yellow-600 dark:text-yellow-400">{attempt.warning}</p>}
          </div>
          <ChevronRight className="h-4 w-4 text-muted-foreground ml-1"/>
        </div>
      </Button>
    </div>);
}
