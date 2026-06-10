import { useState, useCallback, useEffect } from "react";
import { Button } from "@/components/ui/button";
import { Badge } from "@/components/ui/badge";
import { Upload, ArrowLeft, Trash2, CheckCircle2, AlertTriangle, Loader2, X, FileAudio } from "lucide-react";
import { AudioAnalysis } from "@/components/AudioAnalysis";
import { SpectrumVisualization } from "@/components/SpectrumVisualization";
import { useAudioAnalysis, type AudioAnalysisItem } from "@/hooks/useAudioAnalysis";
import { SelectAudioFiles } from "../../wailsjs/go/main/App";
import { toastWithSound as toast } from "@/lib/toast-with-sound";
import { OnFileDrop, OnFileDropOff } from "../../wailsjs/runtime/runtime";
import { cn } from "@/lib/utils";

interface AudioAnalysisPageProps {
    onBack?: () => void;
}

function getStatusIcon(item: AudioAnalysisItem) {
    if (item.status === "analyzing") {
        return <Loader2 className="h-4 w-4 animate-spin text-primary"/>;
    }
    if (item.status === "success") {
        if (item.result?.quality?.verdict_code === "suspicious" || item.result?.quality?.verdict_code === "spectrum_limited") {
            return <AlertTriangle className="h-4 w-4 text-amber-500"/>;
        }
        return <CheckCircle2 className="h-4 w-4 text-emerald-500"/>;
    }
    if (item.status === "error") {
        return <AlertTriangle className="h-4 w-4 text-destructive"/>;
    }
    return <FileAudio className="h-4 w-4 text-muted-foreground"/>;
}

function getVerdictBadgeClass(item: AudioAnalysisItem): string {
    switch (item.result?.quality?.verdict_code) {
        case "likely_genuine":
        case "lossless_container":
            return "border-emerald-500/40 bg-emerald-500/10 text-emerald-600 dark:text-emerald-300";
        case "suspicious":
        case "spectrum_limited":
            return "border-amber-500/40 bg-amber-500/10 text-amber-700 dark:text-amber-300";
        default:
            return "border-muted-foreground/30 text-muted-foreground";
    }
}

function formatCutoff(item: AudioAnalysisItem): string {
    const cutoff = item.result?.quality?.cutoff_frequency_hz;
    return cutoff ? `${(cutoff / 1000).toFixed(1)} kHz` : "";
}

export function AudioAnalysisPage({ onBack }: AudioAnalysisPageProps) {
    const {
        analyzing,
        items,
        selectedItem,
        selectedResult,
        analyzeFiles,
        clearResult,
        selectedFilePath,
        selectFile,
        removeFile,
        spectrumLoading,
    } = useAudioAnalysis();
    const [isDragging, setIsDragging] = useState(false);

    const handleSelectFiles = async () => {
        try {
            const filePaths = await SelectAudioFiles();
            if (filePaths && filePaths.length > 0) {
                await analyzeFiles(filePaths);
            }
        }
        catch (err) {
            toast.error("File Selection Failed", {
                description: err instanceof Error ? err.message : "Failed to select files",
            });
        }
    };

    const handleFileDrop = useCallback(async (_x: number, _y: number, paths: string[]) => {
        setIsDragging(false);
        if (paths.length === 0)
            return;
        await analyzeFiles(paths);
    }, [analyzeFiles]);

    useEffect(() => {
        OnFileDrop((x, y, paths) => {
            handleFileDrop(x, y, paths);
        }, true);
        return () => {
            OnFileDropOff();
        };
    }, [handleFileDrop]);

    const successCount = items.filter((item) => item.status === "success").length;
    const warningCount = items.filter((item) => item.result?.quality?.verdict_code === "suspicious" || item.result?.quality?.verdict_code === "spectrum_limited").length;

    return (<div className="space-y-6">
      <div className="flex flex-wrap items-center justify-between gap-3">
        <div className="flex items-center gap-4">
          {onBack && (<Button variant="ghost" size="icon" onClick={onBack}>
              <ArrowLeft className="h-5 w-5"/>
            </Button>)}
          <div>
            <h1 className="text-2xl font-bold">Audio Quality Analyzer</h1>
            <p className="text-sm text-muted-foreground">Batch MP3 and FLAC spectrum inspection</p>
          </div>
        </div>
        {items.length > 0 && (<div className="flex flex-wrap gap-2">
            <Button onClick={handleSelectFiles} variant="outline" size="sm" disabled={analyzing}>
              <Upload className="h-4 w-4"/>
              Add Files
            </Button>
            <Button onClick={clearResult} variant="outline" size="sm" disabled={analyzing}>
              <Trash2 className="h-4 w-4"/>
              Clear All
            </Button>
          </div>)}
      </div>

      {items.length === 0 && !analyzing && (<div className={`flex flex-col items-center justify-center h-[400px] border-2 border-dashed rounded-lg transition-colors ${isDragging
                ? "border-primary bg-primary/10"
                : "border-muted-foreground/30"}`} onDragOver={(e) => {
                e.preventDefault();
                setIsDragging(true);
            }} onDragLeave={(e) => {
                e.preventDefault();
                setIsDragging(false);
            }} onDrop={(e) => {
                e.preventDefault();
                setIsDragging(false);
            }} style={{ "--wails-drop-target": "drop" } as React.CSSProperties}>
          <div className="mb-4 flex h-16 w-16 items-center justify-center rounded-full bg-muted">
            <Upload className="h-8 w-8 text-primary"/>
          </div>
          <p className="text-sm text-muted-foreground mb-4 text-center">
            {isDragging
                ? "Drop your MP3 or FLAC files here"
                : "Drag and drop MP3 or FLAC files here, or select a batch"}
          </p>
          <Button onClick={handleSelectFiles} size="lg">
            <Upload className="h-5 w-5"/>
            Select Files
          </Button>
          <p className="text-xs text-muted-foreground mt-4 text-center">
            Supported formats: MP3, FLAC
          </p>
        </div>)}

      {items.length > 0 && (<div className="grid gap-4 lg:grid-cols-[360px_minmax(0,1fr)]">
          <div className="rounded-lg border">
            <div className="flex items-center justify-between border-b p-3">
              <div>
                <p className="text-sm font-medium">{items.length} file(s)</p>
                <p className="text-xs text-muted-foreground">{successCount} analyzed • {warningCount} warning(s)</p>
              </div>
              {analyzing && <Loader2 className="h-4 w-4 animate-spin text-primary"/>}
            </div>
            <div className="max-h-[640px] overflow-y-auto p-2">
              {items.map((item) => (<div key={item.path} role="button" tabIndex={0} onClick={() => selectFile(item.path)} onKeyDown={(event) => {
                    if (event.key === "Enter" || event.key === " ") {
                        event.preventDefault();
                        selectFile(item.path);
                    }
                }} className={cn("mb-2 flex w-full items-start gap-3 rounded-md border p-3 text-left transition-colors hover:bg-muted/60", selectedFilePath === item.path && "border-primary bg-primary/5")}>
                  <span className="mt-0.5 shrink-0">{getStatusIcon(item)}</span>
                  <span className="min-w-0 flex-1">
                    <span className="block truncate text-sm font-medium">{item.name}</span>
                    <span className="mt-1 flex flex-wrap items-center gap-2">
                      <Badge variant="outline" className="text-[10px]">{item.format}</Badge>
                      {item.result?.quality && (<Badge variant="outline" className={cn("text-[10px]", getVerdictBadgeClass(item))}>
                          {item.result.quality.verdict}
                        </Badge>)}
                      {formatCutoff(item) && <span className="text-[11px] text-muted-foreground">{formatCutoff(item)}</span>}
                    </span>
                    {item.error && <span className="mt-1 block truncate text-xs text-destructive">{item.error}</span>}
                  </span>
                  {item.status !== "analyzing" && (<button type="button" onClick={(event) => {
                        event.stopPropagation();
                        removeFile(item.path);
                    }} className="rounded p-1 text-muted-foreground hover:bg-muted hover:text-foreground">
                      <X className="h-4 w-4"/>
                    </button>)}
                </div>))}
            </div>
          </div>

          <div className="min-w-0 space-y-4">
            {selectedResult && (<>
                <AudioAnalysis result={selectedResult} analyzing={false} showAnalyzeButton={false} filePath={selectedFilePath}/>
                {spectrumLoading ? (<div className="flex flex-col items-center justify-center py-16 border rounded-lg">
                    <Loader2 className="h-8 w-8 animate-spin text-primary mb-2"/>
                    <p className="text-sm text-muted-foreground">Loading spectrum data...</p>
                  </div>) : (<SpectrumVisualization sampleRate={selectedResult.sample_rate} bitsPerSample={selectedResult.bits_per_sample} duration={selectedResult.duration} spectrumData={selectedResult.spectrum}/>)}
              </>)}

            {!selectedResult && selectedItem?.status === "analyzing" && (<div className="flex flex-col items-center justify-center py-16 border rounded-lg">
                <Loader2 className="h-10 w-10 animate-spin text-primary mb-3"/>
                <p className="text-sm text-muted-foreground">Analyzing {selectedItem.name}...</p>
              </div>)}

            {!selectedResult && selectedItem?.status === "error" && (<div className="flex flex-col items-center justify-center py-16 border rounded-lg text-center">
                <AlertTriangle className="h-10 w-10 text-destructive mb-3"/>
                <p className="font-medium">Analysis failed</p>
                <p className="max-w-xl text-sm text-muted-foreground">{selectedItem.error}</p>
              </div>)}
          </div>
        </div>)}
    </div>);
}
