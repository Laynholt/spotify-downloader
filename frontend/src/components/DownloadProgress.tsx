import { Button } from "@/components/ui/button";
import { Progress } from "@/components/ui/progress";
import type { QueueStatusView } from "@/lib/download-queue-selectors";
import { StopCircle } from "lucide-react";
interface DownloadProgressProps {
    status: QueueStatusView;
    onStop: () => void;
}
export function DownloadProgress({ status, onStop }: DownloadProgressProps) {
    if (status.total === 0) {
        return null;
    }
    return (<div className="w-full space-y-2 mt-4">
      <div className="flex items-center gap-2">
        <p className="flex-1 text-sm font-medium tabular-nums">{status.label}</p>
        <Button variant="destructive" size="sm" onClick={onStop} disabled={!status.busy} className="gap-1.5">
          <StopCircle className="h-4 w-4"/>
          Stop this queue
        </Button>
      </div>
      <Progress value={status.progress} aria-label={`${status.progress}% settled`}/>
    </div>);
}
