import { useState, useEffect, useRef } from "react";
import { GetPreviewURL } from "@/../wailsjs/go/main/App";
import { toast } from "sonner";
export function usePreview() {
    const [loadingPreview, setLoadingPreview] = useState<string | null>(null);
    const currentAudioRef = useRef<HTMLAudioElement | null>(null);
    const [playingTrack, setPlayingTrack] = useState<string | null>(null);
    useEffect(() => {
        return () => {
            if (currentAudioRef.current) {
                currentAudioRef.current.pause();
                currentAudioRef.current.currentTime = 0;
            }
        };
    }, []);
    const playPreview = async (trackId: string, trackName: string) => {
        try {
            const currentAudio = currentAudioRef.current;
            if (playingTrack === trackId && currentAudio) {
                currentAudio.pause();
                currentAudio.currentTime = 0;
                setPlayingTrack(null);
                currentAudioRef.current = null;
                return;
            }
            if (currentAudio) {
                currentAudio.pause();
                currentAudio.currentTime = 0;
                currentAudioRef.current = null;
                setPlayingTrack(null);
            }
            setLoadingPreview(trackId);
            const previewURL = await GetPreviewURL(trackId);
            if (!previewURL) {
                toast.error("Preview not available", {
                    description: `No preview found for "${trackName}"`,
                });
                setLoadingPreview(null);
                return;
            }
            const audio = new Audio(previewURL);
            audio.addEventListener("loadeddata", () => {
                setLoadingPreview(null);
                setPlayingTrack(trackId);
            });
            audio.addEventListener("ended", () => {
                setPlayingTrack(null);
                currentAudioRef.current = null;
            });
            audio.addEventListener("error", () => {
                toast.error("Failed to play preview", {
                    description: `Could not play preview for "${trackName}"`,
                });
                setLoadingPreview(null);
                setPlayingTrack(null);
                currentAudioRef.current = null;
            });
            currentAudioRef.current = audio;
            await audio.play();
        }
        catch (error) {
            console.error("Preview error:", error);
            toast.error("Preview not available", {
                description: error instanceof Error ? error.message : `Could not load preview for "${trackName}"`,
            });
            setLoadingPreview(null);
            setPlayingTrack(null);
        }
    };
    const stopPreview = () => {
        const currentAudio = currentAudioRef.current;
        if (currentAudio) {
            currentAudio.pause();
            currentAudio.currentTime = 0;
            currentAudioRef.current = null;
            setPlayingTrack(null);
        }
    };
    return {
        playPreview,
        stopPreview,
        loadingPreview,
        playingTrack,
    };
}
