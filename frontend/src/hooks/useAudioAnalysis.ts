import { useState, useCallback, useMemo } from "react";
import { AnalyzeTrack } from "../../wailsjs/go/main/App";
import type { AnalysisResult } from "@/types/api";
import { logger } from "@/lib/logger";
import { toastWithSound as toast } from "@/lib/toast-with-sound";

export type AudioAnalysisStatus = "pending" | "analyzing" | "success" | "error";

export interface AudioAnalysisItem {
    path: string;
    name: string;
    format: string;
    status: AudioAnalysisStatus;
    result?: AnalysisResult;
    error?: string;
}

const SUPPORTED_EXTENSIONS = [".mp3", ".flac"];

function getFileName(path: string): string {
    return path.split(/[/\\]/).pop() || path;
}

function getExtension(path: string): string {
    const name = getFileName(path);
    const index = name.lastIndexOf(".");
    return index >= 0 ? name.slice(index).toLowerCase() : "";
}

function createAnalysisItem(path: string): AudioAnalysisItem {
    const ext = getExtension(path);
    return {
        path,
        name: getFileName(path),
        format: ext.replace(".", "").toUpperCase(),
        status: "pending",
    };
}

export function useAudioAnalysis() {
    const [items, setItems] = useState<AudioAnalysisItem[]>([]);
    const [selectedFilePath, setSelectedFilePath] = useState("");
    const [analyzing, setAnalyzing] = useState(false);

    const selectedItem = useMemo(() => {
        if (!selectedFilePath) {
            return items.find((item) => item.status === "success") || items[0] || null;
        }
        return items.find((item) => item.path === selectedFilePath) || null;
    }, [items, selectedFilePath]);

    const selectedResult = selectedItem?.result || null;

    const analyzeFiles = useCallback(async (paths: string[]) => {
        const validPaths = paths.filter((path) => SUPPORTED_EXTENSIONS.includes(getExtension(path)));
        const skipped = paths.length - validPaths.length;

        if (skipped > 0) {
            toast.info("Some files skipped", {
                description: `${skipped} file(s) were skipped. Supported formats: MP3, FLAC.`,
            });
        }

        const existing = new Set(items.map((item) => item.path.toLowerCase()));
        const uniqueNewItems = validPaths
            .filter((path, index) => validPaths.indexOf(path) === index)
            .map(createAnalysisItem)
            .filter((item) => !existing.has(item.path.toLowerCase()));

        if (uniqueNewItems.length === 0) {
            if (paths.length > 0 && skipped === 0) {
                toast.info("No new files added", {
                    description: "All selected files are already in the analyzer list.",
                });
            }
            return;
        }

        const queuedItems = uniqueNewItems;
        setItems((prev) => [...prev, ...queuedItems]);

        if (queuedItems.length === 0) {
            toast.info("No new files added", {
                description: "All selected files are already in the analyzer list.",
            });
            return;
        }

        if (!selectedFilePath) {
            setSelectedFilePath(queuedItems[0].path);
        }

        setAnalyzing(true);
        try {
            for (const item of queuedItems) {
                setItems((prev) => prev.map((current) => current.path === item.path
                    ? { ...current, status: "analyzing", error: undefined }
                    : current));

                try {
                    logger.info(`Analyzing audio file: ${item.path}`);
                    const startTime = Date.now();
                    const response = await AnalyzeTrack(item.path);
                    const result: AnalysisResult = JSON.parse(response);
                    const elapsed = ((Date.now() - startTime) / 1000).toFixed(2);
                    logger.success(`Audio analysis completed in ${elapsed}s`);

                    setItems((prev) => prev.map((current) => current.path === item.path
                        ? { ...current, status: "success", result, error: undefined }
                        : current));
                }
                catch (err) {
                    const errorMessage = err instanceof Error ? err.message : "Failed to analyze audio file";
                    logger.error(`Analysis error: ${errorMessage}`);
                    setItems((prev) => prev.map((current) => current.path === item.path
                        ? { ...current, status: "error", error: errorMessage }
                        : current));
                }
            }
        }
        finally {
            setAnalyzing(false);
        }
    }, [items, selectedFilePath]);

    const clearResult = useCallback(() => {
        setItems([]);
        setSelectedFilePath("");
    }, []);

    const removeFile = useCallback((path: string) => {
        setItems((prev) => prev.filter((item) => item.path !== path));
        setSelectedFilePath((current) => current === path ? "" : current);
    }, []);

    const selectFile = useCallback((path: string) => {
        setSelectedFilePath(path);
    }, []);

    return {
        analyzing,
        items,
        selectedItem,
        selectedResult,
        selectedFilePath: selectedItem?.path || "",
        spectrumLoading: false,
        analyzeFiles,
        clearResult,
        removeFile,
        selectFile,
    };
}
