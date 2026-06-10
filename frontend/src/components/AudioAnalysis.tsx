import { Card, CardContent, CardHeader } from "@/components/ui/card";
import { Spinner } from "@/components/ui/spinner";
import { Button } from "@/components/ui/button";
import { Badge } from "@/components/ui/badge";
import { Activity, Waves, Radio, TrendingUp, FileAudio, Clock, Gauge, HardDrive, ShieldCheck, AlertTriangle, HelpCircle } from "lucide-react";
import type { AnalysisResult } from "@/types/api";
interface AudioAnalysisProps {
    result: AnalysisResult | null;
    analyzing: boolean;
    onAnalyze?: () => void;
    showAnalyzeButton?: boolean;
    filePath?: string;
}
export function AudioAnalysis({ result, analyzing, onAnalyze, showAnalyzeButton = true, filePath }: AudioAnalysisProps) {
    if (analyzing) {
        return (<Card>
        <CardContent className="px-6">
          <div className="flex items-center justify-center py-8 gap-3">
            <Spinner />
            <span className="text-muted-foreground">Analyzing audio quality...</span>
          </div>
        </CardContent>
      </Card>);
    }
    if (!result && showAnalyzeButton) {
        return (<Card>
        <CardContent className="px-6">
          <div className="flex flex-col items-center justify-center py-8 gap-4">
            <Activity className="h-12 w-12 text-primary"/>
            <div className="text-center space-y-2">
              <p className="font-medium">Audio Quality Analysis</p>
              <p className="text-sm text-muted-foreground">
                Verify the true lossless quality of downloaded files
              </p>
            </div>
            {onAnalyze && (<Button onClick={onAnalyze}>
                <Activity className="h-4 w-4"/>
                Analyze Audio
              </Button>)}
          </div>
        </CardContent>
      </Card>);
    }
    if (!result)
        return null;
    const formatDuration = (seconds: number) => {
        const mins = Math.floor(seconds / 60);
        const secs = Math.floor(seconds % 60);
        return `${mins}:${secs.toString().padStart(2, '0')}`;
    };
    const formatNumber = (num: number) => num.toFixed(2);
    const formatBitrate = (bitsPerSecond?: number): string => {
        if (!bitsPerSecond || bitsPerSecond <= 0)
            return "Unknown";
        return `${Math.round(bitsPerSecond / 1000)} kbps`;
    };
    const formatFileSize = (bytes: number): string => {
        if (bytes === 0)
            return "0 B";
        const k = 1024;
        const sizes = ["B", "KB", "MB", "GB"];
        const i = Math.floor(Math.log(bytes) / Math.log(k));
        return parseFloat((bytes / Math.pow(k, i)).toFixed(1)) + " " + sizes[i];
    };
    const getQualityIcon = () => {
        switch (result.quality?.verdict_code) {
            case "likely_genuine":
            case "lossless_container":
                return <ShieldCheck className="h-3.5 w-3.5"/>;
            case "suspicious":
            case "spectrum_limited":
                return <AlertTriangle className="h-3.5 w-3.5"/>;
            default:
                return <HelpCircle className="h-3.5 w-3.5"/>;
        }
    };
    const getQualityClassName = () => {
        switch (result.quality?.verdict_code) {
            case "likely_genuine":
            case "lossless_container":
                return "border-emerald-500/40 bg-emerald-500/10 text-emerald-600 dark:text-emerald-300";
            case "suspicious":
            case "spectrum_limited":
                return "border-amber-500/40 bg-amber-500/10 text-amber-700 dark:text-amber-300";
            default:
                return "border-muted-foreground/30 bg-muted text-muted-foreground";
        }
    };
    const nyquistFreq = result.sample_rate / 2;
    return (<Card className="gap-2">
      <CardHeader>
        <div className="flex flex-col gap-2">
          {filePath && (<p className="text-sm font-mono break-all">{filePath}</p>)}
          {result.quality && (<div className="flex flex-wrap items-center gap-2">
              <Badge variant="outline" className={getQualityClassName()}>
                {getQualityIcon()}
                {result.quality.verdict}
              </Badge>
              <span className="text-xs text-muted-foreground">
                Confidence {Math.round(result.quality.confidence * 100)}%
              </span>
            </div>)}
        </div>
      </CardHeader>

      <CardContent className="space-y-2">
        
        <div className="flex flex-wrap items-center gap-x-3 gap-y-1 text-xs">
          <div className="flex items-center gap-1">
            <Radio className="h-3 w-3 text-muted-foreground"/>
            <span className="text-muted-foreground">Sample Rate:</span>
            <span className="font-semibold">{(result.sample_rate / 1000).toFixed(1)} kHz</span>
          </div>
          <div className="flex items-center gap-1">
            <FileAudio className="h-3 w-3 text-muted-foreground"/>
            <span className="text-muted-foreground">Bit Depth:</span>
            <span className="font-semibold">{result.bit_depth}</span>
          </div>
          <div className="flex items-center gap-1">
            <Waves className="h-3 w-3 text-muted-foreground"/>
            <span className="text-muted-foreground">Channels:</span>
            <span className="font-semibold">{result.channels === 2 ? "Stereo" : result.channels === 1 ? "Mono" : `${result.channels}`}</span>
          </div>
          <div className="flex items-center gap-1">
            <Clock className="h-3 w-3 text-muted-foreground"/>
            <span className="text-muted-foreground">Duration:</span>
            <span className="font-semibold">{formatDuration(result.duration)}</span>
          </div>
          <div className="flex items-center gap-1">
            <Gauge className="h-3 w-3 text-muted-foreground"/>
            <span className="text-muted-foreground">Nyquist:</span>
            <span className="font-semibold">{(nyquistFreq / 1000).toFixed(1)} kHz</span>
          </div>
          <div className="flex items-center gap-1">
            <span className="text-muted-foreground">Declared Bitrate:</span>
            <span className="font-semibold">{formatBitrate(result.bit_rate)}</span>
          </div>
          {result.file_size > 0 && (<div className="flex items-center gap-1">
              <HardDrive className="h-3 w-3 text-muted-foreground"/>
              <span className="text-muted-foreground">Size:</span>
              <span className="font-semibold">{formatFileSize(result.file_size)}</span>
            </div>)}
        </div>

        {result.quality && (<div className="grid gap-2 rounded-md border bg-muted/30 p-3 text-xs sm:grid-cols-3">
          <div>
            <p className="text-muted-foreground">Estimated Source Tier</p>
            <p className="font-semibold">{result.quality.estimated_bitrate_kbps ? `${result.quality.estimated_bitrate_kbps} kbps` : "Unknown"}</p>
          </div>
          <div>
            <p className="text-muted-foreground">High-Frequency Cutoff</p>
            <p className="font-semibold">{result.quality.cutoff_frequency_hz ? `${(result.quality.cutoff_frequency_hz / 1000).toFixed(1)} kHz` : "Unknown"}</p>
          </div>
          <div>
            <p className="text-muted-foreground">Format</p>
            <p className="font-semibold uppercase">{result.quality.format || "Unknown"}</p>
          </div>
          <p className="text-muted-foreground sm:col-span-3">{result.quality.details}</p>
        </div>)}

        
        <div className="flex flex-wrap items-center gap-x-3 gap-y-1 text-xs border-t pt-2">
          <div className="flex items-center gap-1">
            <TrendingUp className="h-3 w-3 text-muted-foreground"/>
            <span className="text-muted-foreground">Dynamic Range:</span>
            <span className="font-semibold">{formatNumber(result.dynamic_range)} dB</span>
          </div>
          <div className="flex items-center gap-1">
            <span className="text-muted-foreground">Peak:</span>
            <span className="font-semibold">{formatNumber(result.peak_amplitude)} dB</span>
          </div>
          <div className="flex items-center gap-1">
            <span className="text-muted-foreground">RMS:</span>
            <span className="font-semibold">{formatNumber(result.rms_level)} dB</span>
          </div>
          <div className="flex items-center gap-1 ml-auto">
            <span className="text-muted-foreground">Samples:</span>
            <span className="font-semibold">{result.total_samples.toLocaleString()}</span>
          </div>
        </div>
      </CardContent>
    </Card>);
}
