// @vitest-environment jsdom
import { StrictMode, act, useLayoutEffect } from "react";
import { createRoot } from "react-dom/client";
import { afterEach, expect, it, vi } from "vitest";
import { useDownloadQueues, type UseDownloadQueuesResult } from "./useDownloadQueues";
import type { Settings } from "@/lib/settings";

afterEach(() => vi.unstubAllGlobals());

it("keeps the scheduler usable after StrictMode effect replay and cancels on real unmount", async () => {
    vi.stubGlobal("IS_REACT_ACT_ENVIRONMENT", true);
    const cancel = vi.fn(async () => 1);
    let queues!: UseDownloadQueuesResult;
    function Probe() {
        const current = useDownloadQueues({ executeSpotify: async (_job, signal) => await new Promise(resolve => {
            signal.addEventListener("abort", () => resolve({ success: false, message: "cancelled" }), { once: true });
        }), api: { cancelDownloadQueue: cancel } });
        useLayoutEffect(() => { queues = current; });
        return <div>{current.snapshot.activeCount}</div>;
    }
    const container = document.createElement("div");
    const root = createRoot(container);
    await act(async () => root.render(<StrictMode><Probe /></StrictMode>));
    await act(async () => {
        await queues.retryTrack({ id: "adhoc:single", title: "Downloads", kind: "adhoc" }, {
            spotify_id: "one", name: "One", artists: "Artist", album_name: "Album", duration_ms: 1,
            images: "", release_date: "2026", track_number: 1, external_urls: "",
        }, { settings: {} as Settings });
    });
    expect(container.textContent).toBe("1");
    expect(cancel).not.toHaveBeenCalled();
    await act(async () => root.unmount());
    expect(cancel).toHaveBeenCalledWith("adhoc:single");
});
