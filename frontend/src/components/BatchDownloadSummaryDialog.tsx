import { AlertTriangle, CheckCircle, Download, FileCheck, RefreshCw, XCircle } from "lucide-react";
import { useState, type ReactNode } from "react";
import { Button } from "@/components/ui/button";
import { Checkbox } from "@/components/ui/checkbox";
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { Progress } from "@/components/ui/progress";
import { ScrollArea } from "@/components/ui/scroll-area";
import { Spinner } from "@/components/ui/spinner";
import type { BatchDownloadSummary, BatchTrackResult } from "@/lib/batch-summary";
import { hasProblemDetails } from "@/lib/batch-summary";

interface BatchDownloadSummaryDialogProps {
    open: boolean;
    summary: BatchDownloadSummary | null;
    isRedownloadingSuspicious?: boolean;
    redownloadProgress?: number;
    redownloadCurrentTrack?: {
        name: string;
        artists: string;
    } | null;
    onMinimize: () => void;
    onClose: () => void;
    onRedownloadSuspicious?: (includeFailedTracks?: boolean) => void;
}

function formatDuration(seconds?: number): string {
    if (seconds === undefined || Number.isNaN(seconds)) {
        return "";
    }
    const sign = seconds < 0 ? "-" : "";
    const abs = Math.abs(seconds);
    const minutes = Math.floor(abs / 60);
    const rest = Math.round(abs % 60).toString().padStart(2, "0");
    return `${sign}${minutes}:${rest}`;
}

function detailText(result: BatchTrackResult): string {
    const parts = [
        result.reason,
        result.error,
        result.validationWarning,
        result.file ? `File: ${result.file}` : "",
        result.movedOriginalPath ? `Moved: ${result.movedOriginalPath}` : "",
        result.replacementPath ? `Replacement: ${result.replacementPath}` : "",
    ].filter(Boolean);

    const durations = [
        result.expectedDurationSeconds !== undefined ? `expected ${formatDuration(result.expectedDurationSeconds)}` : "",
        result.actualDurationSeconds !== undefined ? `actual ${formatDuration(result.actualDurationSeconds)}` : "",
        result.durationDeltaSeconds !== undefined ? `delta ${result.durationDeltaSeconds.toFixed(1)}s` : "",
    ].filter(Boolean);
    if (durations.length > 0) {
        parts.push(durations.join(", "));
    }

    return parts.join(" • ");
}

function SummaryCount({ label, value }: { label: string; value: number }) {
    return (
        <div className="rounded-md border border-border/60 px-3 py-2">
            <div className="text-lg font-semibold tabular-nums">{value}</div>
            <div className="text-xs text-muted-foreground">{label}</div>
        </div>
    );
}

function ResultSection({ title, icon, results }: { title: string; icon: ReactNode; results: BatchTrackResult[] }) {
    if (results.length === 0) {
        return null;
    }

    return (
        <details className="rounded-md border border-border/60 p-3" open>
            <summary className="flex cursor-pointer list-none items-center gap-2 text-sm font-medium">
                {icon}
                {title} ({results.length})
            </summary>
            <div className="mt-3 max-h-48 space-y-2 overflow-y-auto pr-1">
                {results.map((result) => (
                    <div key={`${result.status}-${result.id}-${result.file || result.replacementPath || ""}`} className="rounded-md bg-muted/40 px-3 py-2">
                        <div className="text-sm font-medium">{result.name}</div>
                        <div className="text-xs text-muted-foreground">{result.artists}</div>
                        {detailText(result) && <div className="mt-1 text-xs text-muted-foreground">{detailText(result)}</div>}
                    </div>
                ))}
            </div>
        </details>
    );
}

export function BatchDownloadSummaryDialog({ open, summary, isRedownloadingSuspicious = false, redownloadProgress = 0, redownloadCurrentTrack = null, onMinimize, onClose, onRedownloadSuspicious }: BatchDownloadSummaryDialogProps) {
    const [includeFailedTracks, setIncludeFailedTracks] = useState(false);
    const failed = summary?.results.filter((result) => result.status === "failed") || [];
    const suspicious = summary?.results.filter((result) => result.status === "suspicious") || [];
    const duplicateSkipped = summary?.results.filter((result) => result.status === "duplicate_skipped") || [];
    const metadataUpdated = summary?.results.filter((result) => result.status === "metadata_updated") || [];
    const youtubeSucceeded = summary?.results.filter((result) => result.status === "youtube_replaced") || [];
    const youtubeFailed = summary?.results.filter((result) => result.status === "youtube_failed" || result.status === "youtube_still_suspicious") || [];
    const canRedownloadFromYouTube = (suspicious.length > 0 || failed.length > 0) && !!onRedownloadSuspicious;

    return (
        <Dialog open={open} onOpenChange={(nextOpen) => {
            if (!nextOpen) {
                onMinimize();
            }
        }}>
            <DialogContent className="flex max-h-[calc(100vh-2rem)] w-[calc(100vw-2rem)] flex-col overflow-hidden border-zinc-700/80 sm:max-w-[720px] [&>button]:hidden">
                <DialogHeader className="shrink-0">
                    <DialogTitle>{summary?.title || "Download Summary"}</DialogTitle>
                    <DialogDescription>
                        Batch operation completed. Review suspicious and failed tracks before closing.
                    </DialogDescription>
                </DialogHeader>

                {summary && (
                    <div className="flex min-h-0 flex-1 flex-col gap-4 overflow-hidden">
                        <div className="grid grid-cols-2 gap-2 md:grid-cols-4">
                            <SummaryCount label="Downloaded" value={summary.downloaded} />
                            <SummaryCount label="Skipped" value={summary.skipped} />
                            <SummaryCount label="Metadata" value={summary.metadataUpdated} />
                            <SummaryCount label="Failed" value={summary.failed} />
                            <SummaryCount label="Duplicates" value={summary.duplicateSkipped} />
                            <SummaryCount label="Suspicious" value={summary.suspicious} />
                            <SummaryCount label="YT Replaced" value={summary.youtubeReplaced} />
                            <SummaryCount label="YT Failed" value={summary.youtubeFailed + summary.youtubeStillSuspicious} />
                        </div>

                        <ScrollArea className="min-h-0 flex-1 pr-3">
                            {hasProblemDetails(summary) || metadataUpdated.length > 0 || youtubeSucceeded.length > 0 ? (
                                <div className="space-y-3">
                                    <ResultSection title="Suspicious Tracks" icon={<AlertTriangle className="h-4 w-4 text-yellow-500" />} results={suspicious} />
                                    <ResultSection title="Failed Tracks" icon={<XCircle className="h-4 w-4 text-red-500" />} results={failed} />
                                    <ResultSection title="Duplicate Tracks" icon={<FileCheck className="h-4 w-4 text-yellow-500" />} results={duplicateSkipped} />
                                    <ResultSection title="Metadata Updates" icon={<RefreshCw className="h-4 w-4 text-blue-500" />} results={metadataUpdated} />
                                    <ResultSection title="YouTube Downloads" icon={<Download className="h-4 w-4 text-green-500" />} results={youtubeSucceeded} />
                                    <ResultSection title="YouTube Failed" icon={<XCircle className="h-4 w-4 text-red-500" />} results={youtubeFailed} />
                                </div>
                            ) : (
                                <div className="flex items-center gap-2 rounded-md border border-border/60 px-3 py-4 text-sm text-muted-foreground">
                                    <CheckCircle className="h-4 w-4 text-green-500" />
                                    No failed or suspicious tracks.
                                </div>
                            )}
                        </ScrollArea>

                        {isRedownloadingSuspicious && (
                            <div className="space-y-2 rounded-md border border-border/60 px-3 py-3">
                                <div className="flex items-center justify-between gap-3 text-xs text-muted-foreground">
                                    <span>
                                        {redownloadCurrentTrack
                                            ? `${redownloadCurrentTrack.name}${redownloadCurrentTrack.artists ? ` - ${redownloadCurrentTrack.artists}` : ""}`
                                            : "Preparing YouTube redownload..."}
                                    </span>
                                    <span className="tabular-nums">{Math.min(100, Math.max(0, redownloadProgress))}%</span>
                                </div>
                                <Progress value={Math.min(100, Math.max(0, redownloadProgress))} className="h-2" />
                            </div>
                        )}
                    </div>
                )}

                <DialogFooter className="shrink-0 gap-2">
                    {canRedownloadFromYouTube && (
                        <div className="flex flex-1 flex-wrap items-center justify-end gap-3">
                            {failed.length > 0 && (
                                <label className="flex items-center gap-2 text-sm text-muted-foreground">
                                    <Checkbox checked={includeFailedTracks} onCheckedChange={(checked) => setIncludeFailedTracks(checked === true)} />
                                    Include failed tracks
                                </label>
                            )}
                            <Button onClick={() => onRedownloadSuspicious?.(includeFailedTracks)} disabled={isRedownloadingSuspicious}>
                                {isRedownloadingSuspicious ? <Spinner /> : <Download className="h-4 w-4" />}
                                {suspicious.length > 0 ? "Redownload suspicious from YouTube" : "Download failed from YouTube"}
                            </Button>
                        </div>
                    )}
                    <Button variant="outline" onClick={onClose}>
                        Close
                    </Button>
                </DialogFooter>
            </DialogContent>
        </Dialog>
    );
}
