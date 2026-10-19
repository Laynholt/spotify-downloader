import { useEffect, useMemo, useState } from "react";
import { X, Download, CheckCircle2, XCircle, Clock, FileCheck, Trash2, HardDrive, Zap, Timer, FileDown, } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Dialog, DialogContent, DialogHeader, DialogTitle, DialogDescription, } from "@/components/ui/dialog";
import { Badge } from "@/components/ui/badge";
import { ExportFailedDownloads } from "../../wailsjs/go/main/App";
import { toastWithSound as toast } from "@/lib/toast-with-sound";
import { formatAggregateProgress, formatPipelineTimings, formatTransferProgress, phaseLabel } from "@/lib/download-timings";
import type { backend } from "../../wailsjs/go/models";
import { mergeDownloadQueueInfo } from "@/lib/download-queue-selectors";
import type { DownloadSchedulerSnapshot } from "@/lib/download-queue-manager";
import type { MediaJobPayload, MediaJobResult } from "@/lib/download-jobs";
interface DownloadQueueProps {
    isOpen: boolean;
    onClose: () => void;
    queueInfo: Omit<backend.DownloadQueueInfo, "convertValues">;
    schedulerSnapshot: DownloadSchedulerSnapshot<MediaJobPayload, MediaJobResult>;
    onStopQueue: (queueId: string) => Promise<unknown>;
    onStopAll: () => Promise<void>;
    onClearCompleted: () => Promise<void>;
    onNewSession: () => Promise<void>;
}
export function DownloadQueue({ isOpen, onClose, queueInfo: backendInfo, schedulerSnapshot, onStopQueue, onStopAll, onClearCompleted, onNewSession }: DownloadQueueProps) {
    const queueInfo = useMemo(() => mergeDownloadQueueInfo(schedulerSnapshot, backendInfo), [schedulerSnapshot, backendInfo]);
    const [pendingAction, setPendingAction] = useState(false);
    const runAction = async (action: () => Promise<unknown>, success: string) => {
        setPendingAction(true);
        try { await action(); toast.success(success); }
        catch (error) { toast.error(`Queue action failed: ${error}`); }
        finally { setPendingAction(false); }
    };
    const [nowSeconds, setNowSeconds] = useState(() => Math.floor(Date.now() / 1000));
    useEffect(() => {
        if (!isOpen)
            return;
        const clockInterval = setInterval(() => {
            setNowSeconds(Math.floor(Date.now() / 1000));
        }, 1000);
        return () => clearInterval(clockInterval);
    }, [isOpen]);
    const handleExportFailed = async () => {
        try {
            const message = await ExportFailedDownloads();
            if (message.startsWith("Successfully")) {
                toast.success(message);
            }
            else if (message !== "Export cancelled") {
                toast.info(message);
            }
        }
        catch (error) {
            console.error("Failed to export:", error);
            toast.error(`Failed to export: ${error}`);
        }
    };
    const getStatusIcon = (status: string) => {
        switch (status) {
            case "downloading":
                return <Download className="h-4 w-4 text-blue-500 animate-bounce"/>;
            case "completed":
                return <CheckCircle2 className="h-4 w-4 text-green-500"/>;
            case "failed":
            case "unavailable":
                return <XCircle className="h-4 w-4 text-red-500"/>;
            case "suspicious":
                return <FileCheck className="h-4 w-4 text-yellow-500"/>;
            case "cancelled":
                return <XCircle className="h-4 w-4 text-orange-500"/>;
            case "skipped":
                return <FileCheck className="h-4 w-4 text-yellow-500"/>;
            case "queued":
                return <Clock className="h-4 w-4 text-muted-foreground"/>;
            default:
                return null;
        }
    };
    const getStatusBadge = (status: string, phase?: string) => {
        const variants: Record<string, "default" | "secondary" | "destructive" | "outline"> = {
            downloading: "default",
            completed: "outline",
            failed: "destructive",
            cancelled: "destructive",
            skipped: "secondary",
            queued: "outline",
        };
        return (<Badge variant={variants[status] || "outline"} className="text-xs">
        {phaseLabel(status === "downloading" || status === "queued" ? (phase || status) : status)}
      </Badge>);
    };
    const settledCount = queueInfo.completed_count + queueInfo.failed_count + queueInfo.skipped_count + (queueInfo.cancelled_count ?? 0);
    const activeCount = queueInfo.active_count ?? queueInfo.queue.filter((item) => item.status === "downloading").length;
    const formatDuration = (startTimestamp: number) => {
        if (startTimestamp === 0)
            return "вЂ”";
        const durationSeconds = nowSeconds - startTimestamp;
        const hours = Math.floor(durationSeconds / 3600);
        const minutes = Math.floor((durationSeconds % 3600) / 60);
        const seconds = durationSeconds % 60;
        if (hours > 0) {
            return `${hours}h ${minutes}m ${seconds}s`;
        }
        else if (minutes > 0) {
            return `${minutes}m ${seconds}s`;
        }
        else {
            return `${seconds}s`;
        }
    };
    const [filterStatus, setFilterStatus] = useState<string>("all");
    const toggleFilter = (status: string) => {
        setFilterStatus((prev) => (prev === status ? "all" : status));
    };
    const filteredQueue = queueInfo.queue.filter((item) => {
        if (filterStatus === "all")
            return true;
        return item.status === filterStatus;
    });
    return (<Dialog open={isOpen} onOpenChange={onClose}>
      <DialogContent aria-describedby={undefined} className="max-w-[1200px] w-[95vw] max-h-[80vh] flex flex-col p-0 gap-0 [&>button]:hidden">
        <DialogHeader className="px-6 pt-6 pb-4 border-b space-y-0">
          <div className="flex items-center justify-between mb-4">
            <DialogTitle className="text-lg font-semibold">
              Download Queue
            </DialogTitle>
            <DialogDescription className="sr-only">Download attempts in the current session, with controls to stop work and clear history.</DialogDescription>
            <div className="flex items-center gap-2">
              <Button variant="ghost" size="sm" disabled={pendingAction} onClick={() => void runAction(onNewSession, "New download session started")}>New Session</Button>
              {(schedulerSnapshot.activeCount > 0 || queueInfo.is_downloading) && <Button variant="ghost" size="sm" disabled={pendingAction} onClick={() => void runAction(onStopAll, "Download queues stopped")}>Stop All</Button>}
              <Button variant="ghost" size="sm" disabled={pendingAction} className="h-7 text-xs gap-1.5" onClick={() => void runAction(onClearCompleted, "Completed history cleared")}>
                  <Trash2 className="h-3 w-3"/>
                  Clear History
                </Button>
              {queueInfo.failed_count > 0 && (<Button variant="ghost" size="sm" className="h-7 text-xs gap-1.5" onClick={handleExportFailed}>
                  <FileDown className="h-3 w-3"/>
                  Export Failures
                </Button>)}
              <Button variant="ghost" size="icon" aria-label="Close download queue" className="h-7 w-7 rounded-full hover:bg-muted" onClick={onClose}>
                <X className="h-4 w-4"/>
              </Button>
            </div>
          </div>

          <div className="flex items-center gap-4 text-sm">
            <div className="flex items-center gap-1.5">
              <Download className="h-3.5 w-3.5 text-blue-500"/>
              <span className="font-semibold">{formatAggregateProgress(activeCount, settledCount, queueInfo.queue.length)}</span>
            </div>
            <button type="button" aria-pressed={filterStatus === "queued"} className={`flex items-center gap-1.5 cursor-pointer hover:opacity-80 transition-all select-none ${filterStatus === "queued" ? "bg-secondary px-2 py-0.5 rounded-md ring-1 ring-border" : ""}`} onClick={() => toggleFilter("queued")}>
              <Clock className="h-3.5 w-3.5 text-muted-foreground"/>
              <span className="text-muted-foreground">Queued:</span>
              <span className="font-semibold">{queueInfo.queued_count}</span>
            </button>
            <button type="button" aria-pressed={filterStatus === "completed"} className={`flex items-center gap-1.5 cursor-pointer hover:opacity-80 transition-all select-none ${filterStatus === "completed" ? "bg-green-500/10 px-2 py-0.5 rounded-md ring-1 ring-green-500/20" : ""}`} onClick={() => toggleFilter("completed")}>
              <CheckCircle2 className="h-3.5 w-3.5 text-green-500"/>
              <span className="text-muted-foreground">Completed:</span>
              <span className="font-semibold">{queueInfo.completed_count}</span>
            </button>
            <button type="button" aria-pressed={filterStatus === "skipped"} className={`flex items-center gap-1.5 cursor-pointer hover:opacity-80 transition-all select-none ${filterStatus === "skipped" ? "bg-yellow-500/10 px-2 py-0.5 rounded-md ring-1 ring-yellow-500/20" : ""}`} onClick={() => toggleFilter("skipped")}>
              <FileCheck className="h-3.5 w-3.5 text-yellow-500"/>
              <span className="text-muted-foreground">Skipped:</span>
              <span className="font-semibold">{queueInfo.skipped_count}</span>
            </button>
            <button type="button" aria-pressed={filterStatus === "failed"} className={`flex items-center gap-1.5 cursor-pointer hover:opacity-80 transition-all select-none ${filterStatus === "failed" ? "bg-red-500/10 px-2 py-0.5 rounded-md ring-1 ring-red-500/20" : ""}`} onClick={() => toggleFilter("failed")}>
              <XCircle className="h-3.5 w-3.5 text-red-500"/>
              <span className="text-muted-foreground">Failed:</span>
              <span className="font-semibold">{queueInfo.failed_count}</span>
            </button>
            <button type="button" aria-pressed={filterStatus === "cancelled"} className={`flex items-center gap-1.5 cursor-pointer hover:opacity-80 transition-all select-none bg-transparent p-0 text-sm ${filterStatus === "cancelled" ? "bg-orange-500/10 px-2 py-0.5 rounded-md ring-1 ring-orange-500/20" : ""}`} onClick={() => toggleFilter("cancelled")}>
              <XCircle className="h-3.5 w-3.5 text-orange-500"/>
              <span className="text-muted-foreground">Cancelled:</span>
              <span className="font-semibold">{queueInfo.cancelled_count ?? 0}</span>
            </button>
          </div>

          <div className="flex items-center gap-4 text-sm pt-3 mt-3 border-t">
            <div className="flex items-center gap-1.5">
              <HardDrive className="h-3.5 w-3.5 text-muted-foreground"/>
              <span className="text-muted-foreground">Downloaded:</span>
              <span className="font-semibold font-mono">
                {queueInfo.total_downloaded > 0
            ? `${queueInfo.total_downloaded.toFixed(2)} MB`
            : "0.00 MB"}
              </span>
            </div>
            <div className="flex items-center gap-1.5">
              <Zap className="h-3.5 w-3.5 text-muted-foreground"/>
              <span className="text-muted-foreground">Speed:</span>
              <span className="font-semibold font-mono">
                {queueInfo.current_speed > 0 && queueInfo.is_downloading
            ? `${queueInfo.current_speed.toFixed(2)} MB/s`
            : "вЂ”"}
              </span>
            </div>
            <div className="flex items-center gap-1.5">
              <Timer className="h-3.5 w-3.5 text-muted-foreground"/>
              <span className="text-muted-foreground">Duration:</span>
              <span className="font-semibold font-mono">
                {queueInfo.session_start_time > 0
            ? formatDuration(queueInfo.session_start_time)
            : "вЂ”"}
              </span>
            </div>
          </div>
        </DialogHeader>

        <div className="flex-1 overflow-y-auto px-6 custom-scrollbar">
          <div className="space-y-2 py-4">
            {Object.values(schedulerSnapshot.queues).map(queue => <div key={queue.id} className="flex items-center justify-between gap-2 text-sm">
                <span>{queue.title} В· {queue.counts.queued} queued В· {queue.counts.active} active</span>
                {(queue.counts.active > 0 || queue.counts.queued > 0) && <Button size="sm" variant="outline" disabled={pendingAction}
                    aria-label={`Stop ${queue.title}`} onClick={() => void runAction(() => onStopQueue(queue.id), "Queue stop requested")}>Stop Queue</Button>}
            </div>)}
            {queueInfo.queue.length === 0 ? (<div className="text-center py-12 text-muted-foreground">
                <Download className="h-12 w-12 mx-auto mb-3 opacity-20"/>
                <p>No downloads in queue</p>
              </div>) : filteredQueue.length === 0 ? (<div className="text-center py-12 text-muted-foreground">
                <p>No downloads with status "{filterStatus}"</p>
                <Button variant="link" onClick={() => setFilterStatus("all")}>
                  Clear filter
                </Button>
              </div>) : (filteredQueue.map((item) => (<div key={item.id} className="border rounded-lg p-3 hover:bg-muted/30 transition-colors">
                  <div className="flex items-start gap-3">
                    <div className="mt-1">{getStatusIcon(item.attempt_status || item.status)}</div>

                    <div className="flex-1 min-w-0">
                      <div className="flex items-start justify-between gap-2 mb-1">
                        <div className="flex-1 min-w-0">
                          <p className="font-medium truncate">
                            {item.track_name}
                          </p>
                          <p className="text-sm text-muted-foreground truncate">
                            {schedulerSnapshot.queues[item.queue_id]?.title && `${schedulerSnapshot.queues[item.queue_id]!.title} вЂў `}
                            {item.artist_name}
                            {item.album_name && ` вЂў ${item.album_name}`}
                          </p>
                        </div>
                        {getStatusBadge(item.attempt_status === "suspicious" || item.attempt_status === "unavailable" ? item.attempt_status : item.status, item.phase)}
                      </div>

                      {(item.status === "downloading" || item.status === "queued") && (<div className="mt-1.5 space-y-1 text-xs text-muted-foreground">
                          <div className="flex items-center justify-between gap-3">
                            <span className="font-medium text-foreground">{phaseLabel(item.phase || item.status)}</span>
                            {item.speed > 0 && <span className="font-mono">{item.speed.toFixed(2)} MB/s</span>}
                          </div>
                          {item.detail && <div className="truncate">{item.detail}</div>}
                          {formatTransferProgress(item.downloaded_bytes, item.total_bytes) && <div className="font-mono">{formatTransferProgress(item.downloaded_bytes, item.total_bytes)}</div>}
                          {item.warning && <div className="rounded bg-yellow-500/10 px-2 py-1 text-yellow-700 dark:text-yellow-400">{item.warning}</div>}
                        </div>)}

                      {item.status === "completed" && (<div className="mt-1.5 space-y-1 text-xs text-muted-foreground">
                          {formatTransferProgress(item.downloaded_bytes, item.total_bytes) && <div className="font-mono">{formatTransferProgress(item.downloaded_bytes, item.total_bytes)}</div>}
                          {item.timings && formatPipelineTimings(item.timings) && <div className="truncate font-mono">{formatPipelineTimings(item.timings)}</div>}
                          {item.warning && <div className="rounded bg-yellow-500/10 px-2 py-1 text-yellow-700 dark:text-yellow-400">{item.warning}</div>}
                        </div>)}

                      {item.status === "skipped" && (<div className="mt-1.5 text-xs text-muted-foreground">
                          File already exists
                        </div>)}

                      {item.status === "failed" && item.error_message && (<div className="mt-1.5 text-xs text-red-500 bg-red-50 dark:bg-red-950/20 rounded px-2 py-1">
                          {item.error_message}
                        </div>)}

                      {item.status === "cancelled" && (<div className="mt-1.5 text-xs text-orange-600 bg-orange-50 dark:bg-orange-950/20 rounded px-2 py-1">
                          {item.error_message || "Cancelled"}
                        </div>)}

                      {(item.status === "completed" ||
                item.status === "skipped") &&
                item.file_path && (<div className="mt-1.5 text-xs text-muted-foreground truncate font-mono">
                            {item.file_path}
                          </div>)}
                    </div>
                  </div>
                </div>)))}
          </div>
        </div>
      </DialogContent>
    </Dialog>);
}
