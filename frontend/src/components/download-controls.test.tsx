// @vitest-environment jsdom
import { act } from "react";
import { createRoot } from "react-dom/client";
import { afterEach, expect, it, vi } from "vitest";
import { TrackInfo } from "./TrackInfo";
import { DownloadQueue } from "./DownloadQueue";
import type { backend } from "../../wailsjs/go/models";
import type { TrackMetadata } from "@/types/api";

vi.mock("@/hooks/usePreview", () => ({ usePreview: () => ({ playPreview: vi.fn(), loadingPreview: null, playingTrack: null }) }));
vi.mock("@/lib/toast-with-sound", () => ({ toastWithSound: { success: vi.fn(), error: vi.fn() } }));
afterEach(() => { document.body.innerHTML = ""; vi.unstubAllGlobals(); });
const track: TrackMetadata & { album_name: string; release_date: string } = {
    spotify_id: "one", name: "One", artists: "Artist", album_name: "Album", duration_ms: 1,
    images: "", release_date: "2026", track_number: 1, external_urls: "",
};

it("allows a single track while another queue downloads, and disables its own queued attempt", async () => {
    vi.stubGlobal("IS_REACT_ACT_ENVIRONMENT", true);
    const container = document.createElement("div");
    const root = createRoot(container);
    const props = { track, isDownloading: true, downloadingTrack: "other", isDownloaded: false, isFailed: false, isSkipped: false,
        onDownload: vi.fn(), onOpenFolder: vi.fn() };
    await act(async () => root.render(<TrackInfo {...props} />));
    expect(container.querySelector("button")!.disabled).toBe(false);
    await act(async () => root.render(<TrackInfo {...props} attempt={{ jobId: "job", status: "queued" }} />));
    expect(container.querySelector("button")!.disabled).toBe(true);
    await act(async () => root.unmount());
});

it("keeps the queue title inert and exposes explicit session, history and stop controls", async () => {
    vi.stubGlobal("IS_REACT_ACT_ENVIRONMENT", true);
    const container = document.createElement("div");
    document.body.append(container);
    const root = createRoot(container);
    const reset = vi.fn(async () => {});
    const clear = vi.fn(async () => {});
    const stop = vi.fn(async () => {});
    const stopQueue = vi.fn(async () => 1);
    await act(async () => root.render(<DownloadQueue isOpen onClose={() => {}} queueInfo={{ queue: [], completed_count: 1 } as unknown as backend.DownloadQueueInfo}
        schedulerSnapshot={{ queues: {}, activeCount: 1, peakActive: 1 }} onNewSession={reset} onClearCompleted={clear} onStopAll={stop} onStopQueue={stopQueue} />));
    const title = [...document.querySelectorAll("h2")].find(element => element.textContent === "Download Queue")!;
    await act(async () => title.click());
    expect(reset).not.toHaveBeenCalled();
    for (const [label, callback] of [["New Session", reset], ["Clear History", clear], ["Stop All", stop]] as const) {
        const button = [...document.querySelectorAll("button")].find(element => element.textContent?.includes(label))!;
        expect(button).toBeDefined();
        await act(async () => button.click());
        expect(callback).toHaveBeenCalledOnce();
    }
    await act(async () => root.unmount());
});
