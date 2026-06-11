import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Tooltip, TooltipContent, TooltipTrigger, } from "@/components/ui/tooltip";
import { openExternal } from "@/lib/utils";
import { formatRelativeTime } from "@/lib/relative-time";
import { DownloadCloud, Loader2 } from "lucide-react";
interface HeaderProps {
    version: string;
    hasUpdate: boolean;
    releaseDate?: string | null;
    updateAssetName?: string;
    isUpdating?: boolean;
    updateProgress?: number;
    onApplyUpdate?: () => void;
}
export function Header({ version, hasUpdate, releaseDate, updateAssetName, isUpdating = false, updateProgress = 0, onApplyUpdate }: HeaderProps) {
    return (<div className="relative">
      <div className="text-center space-y-2">
        <div className="flex items-center justify-center gap-3">
          <img src="/icon.svg" alt="SpotiDownloader" className="w-12 h-12 cursor-pointer" onClick={() => window.location.reload()}/>
          <h1 className="text-4xl font-bold cursor-pointer" onClick={() => window.location.reload()}>
            SpotiDownloader
          </h1>
          <div className="relative">
            <Tooltip>
              <TooltipTrigger asChild>
                <Badge variant="default" asChild>
                  <button type="button" onClick={() => openExternal("https://github.com/Laynholt/spotify-downloader/releases")} className="cursor-pointer hover:opacity-80 transition-opacity">
                    v{version}
                  </button>
                </Badge>
              </TooltipTrigger>
              {hasUpdate && releaseDate && (<TooltipContent>
                  <p>{formatRelativeTime(releaseDate)}{updateAssetName ? ` · ${updateAssetName}` : ""}</p>
                </TooltipContent>)}
            </Tooltip>
            {hasUpdate && (<span className="absolute -top-1 -right-1 flex h-3 w-3">
                <span className="animate-ping absolute inline-flex h-full w-full rounded-full bg-green-400 opacity-75"></span>
                <span className="relative inline-flex rounded-full h-3 w-3 bg-green-500"></span>
              </span>)}
          </div>
          {hasUpdate && onApplyUpdate && (<Tooltip>
              <TooltipTrigger asChild>
                <Button type="button" size="icon" variant="outline" className="h-7 w-7" onClick={onApplyUpdate} disabled={isUpdating} aria-label="Download and apply update">
                  {isUpdating ? <Loader2 className="h-3.5 w-3.5 animate-spin"/> : <DownloadCloud className="h-3.5 w-3.5"/>}
                </Button>
              </TooltipTrigger>
              <TooltipContent>
                <p>{isUpdating ? `Downloading update ${updateProgress}%` : "Download and apply update"}</p>
              </TooltipContent>
            </Tooltip>)}
        </div>
        <p className="text-muted-foreground">
          Get Spotify tracks in MP3 and FLAC via spotidownloader.com
        </p>
      </div>
    </div>);
}
