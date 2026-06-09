import { AlertTriangle, CheckCircle, Download, FileCheck, RefreshCw, XCircle } from "lucide-react";
import type { ReactNode } from "react";
import { Button } from "@/components/ui/button";
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { ScrollArea } from "@/components/ui/scroll-area";
import { Spinner } from "@/components/ui/spinner";
import type { BatchDownloadSummary, BatchTrackResult } from "@/lib/batch-summary";
import { hasProblemDetails } from "@/lib/batch-summary";

interface BatchDownloadSummaryDialogProps {
    open: boolean;
    summary: BatchDownloadSummary | null;
    isRedownloadingSuspicious?: boolean;
    onOpenChange: (open: boolean) => void;
    onRedownloadSuspicious?: () => void;
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
        <div className="rounded-md border px-3 py-2">
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
        <details className="rounded-md border p-3" open>
            <summary className="flex cursor-pointer list-none items-center gap-2 text-sm font-medium">
                {icon}
                {title} ({results.length})
            </summary>
            <div className="mt-3 space-y-2">
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

export function BatchDownloadSummaryDialog({ open, summary, isRedownloadingSuspicious = false, onOpenChange, onRedownloadSuspicious }: BatchDownloadSummaryDialogProps) {
    const failed = summary?.results.filter((result) => result.status === "failed") || [];
    const suspicious = summary?.results.filter((result) => result.status === "suspicious") || [];
    const duplicateSkipped = summary?.results.filter((result) => result.status === "duplicate_skipped") || [];
    const metadataUpdated = summary?.results.filter((result) => result.status === "metadata_updated") || [];
    const youtubeResults = summary?.results.filter((result) => result.status.startsWith("youtube_")) || [];
    const canRedownloadSuspicious = suspicious.length > 0 && !!onRedownloadSuspicious;

    return (
        <Dialog open={open} onOpenChange={onOpenChange}>
            <DialogContent className="max-h-[85vh] sm:max-w-[720px] [&>button]:hidden">
                <DialogHeader>
                    <DialogTitle>{summary?.title || "Download Summary"}</DialogTitle>
                    <DialogDescription>
                        Batch operation completed. Review suspicious and failed tracks before closing.
                    </DialogDescription>
                </DialogHeader>

                {summary && (
                    <div className="space-y-4">
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

                        <ScrollArea className="max-h-[42vh] pr-3">
                            {hasProblemDetails(summary) || metadataUpdated.length > 0 || youtubeResults.length > 0 ? (
                                <div className="space-y-3">
                                    <ResultSection title="Suspicious Tracks" icon={<AlertTriangle className="h-4 w-4 text-yellow-500" />} results={suspicious} />
                                    <ResultSection title="Failed Tracks" icon={<XCircle className="h-4 w-4 text-red-500" />} results={failed} />
                                    <ResultSection title="Duplicate Skips" icon={<FileCheck className="h-4 w-4 text-yellow-500" />} results={duplicateSkipped} />
                                    <ResultSection title="Metadata Updates" icon={<RefreshCw className="h-4 w-4 text-blue-500" />} results={metadataUpdated} />
                                    <ResultSection title="YouTube Redownloads" icon={<Download className="h-4 w-4 text-green-500" />} results={youtubeResults} />
                                </div>
                            ) : (
                                <div className="flex items-center gap-2 rounded-md border px-3 py-4 text-sm text-muted-foreground">
                                    <CheckCircle className="h-4 w-4 text-green-500" />
                                    No failed or suspicious tracks.
                                </div>
                            )}
                        </ScrollArea>
                    </div>
                )}

                <DialogFooter>
                    {canRedownloadSuspicious && (
                        <Button onClick={onRedownloadSuspicious} disabled={isRedownloadingSuspicious}>
                            {isRedownloadingSuspicious ? <Spinner /> : <Download className="h-4 w-4" />}
                            Redownload suspicious from YouTube
                        </Button>
                    )}
                    <Button variant="outline" onClick={() => onOpenChange(false)}>
                        Close
                    </Button>
                </DialogFooter>
            </DialogContent>
        </Dialog>
    );
}
