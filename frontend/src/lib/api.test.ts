import { afterEach, expect, it, vi } from "vitest";
import {
    AddDownloadJob,
    CancelDownloadQueue,
    RedownloadTrackFromYouTube,
} from "../../wailsjs/go/main/App";
import { backend } from "../../wailsjs/go/models";

afterEach(() => vi.unstubAllGlobals());

it("generated bindings preserve queue and job IDs", async () => {
    vi.stubGlobal("window", {
        go: {
            main: {
                App: {
                    AddDownloadJob: (_queueId: string, jobId: string) => Promise.resolve(jobId),
                    CancelDownloadQueue: (queueId: string) => Promise.resolve(queueId.length),
                    RedownloadTrackFromYouTube: (request: backend.SuspiciousRedownloadRequest) =>
                        Promise.resolve({ success: true, status: `${request.queue_id}:${request.job_id}` }),
                },
            },
        },
    });

    expect(await AddDownloadJob("playlist:mix", "job-7", "track-1", "Song", "Artist", "Album")).toBe("job-7");
    expect(await CancelDownloadQueue("playlist:mix")).toBe(12);
    expect(
        await RedownloadTrackFromYouTube(
            backend.SuspiciousRedownloadRequest.createFrom({
                queue_id: "playlist:mix",
                job_id: "job-8",
                original_file_path: "bad.mp3",
                metadata: {},
            }),
        ),
    ).toMatchObject({ success: true, status: "playlist:mix:job-8" });
});
