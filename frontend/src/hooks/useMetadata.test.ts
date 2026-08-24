import { afterAll, beforeAll, beforeEach, describe, expect, it, vi } from "vitest";
import type { SpotifyMetadataResponse } from "@/types/api";
import type { LoadedSpotifyMetadata } from "@/lib/download-queue-selectors";

const hookState = vi.hoisted(() => {
    let state: unknown[] = [];
    let refs: Array<{ current: unknown }> = [];
    let stateIndex = 0;
    let refIndex = 0;

    return {
        reset() {
            state = [];
            refs = [];
            stateIndex = 0;
            refIndex = 0;
        },
        beginRender() {
            stateIndex = 0;
            refIndex = 0;
        },
        useState<T>(initial: T | (() => T)) {
            const index = stateIndex++;
            if (index >= state.length) {
                state[index] = typeof initial === "function"
                    ? (initial as () => T)()
                    : initial;
            }
            const setState = (next: T | ((current: T) => T)) => {
                state[index] = typeof next === "function"
                    ? (next as (current: T) => T)(state[index] as T)
                    : next;
            };
            return [state[index] as T, setState] as const;
        },
        useRef<T>(initial: T) {
            const index = refIndex++;
            if (index >= refs.length) {
                refs[index] = { current: initial };
            }
            return refs[index] as { current: T };
        },
    };
});

const doubles = vi.hoisted(() => ({
    fetchSpotifyMetadata: vi.fn(),
    addFetchHistory: vi.fn(),
    toastSuccess: vi.fn(),
    toastError: vi.fn(),
}));

vi.mock("react", async (importOriginal) => ({
    ...await importOriginal<typeof import("react")>(),
    useState: hookState.useState,
    useRef: hookState.useRef,
}));
vi.mock("@/lib/api", () => ({ fetchSpotifyMetadata: doubles.fetchSpotifyMetadata }));
vi.mock("@/lib/toast-with-sound", () => ({
    toastWithSound: { success: doubles.toastSuccess, error: doubles.toastError },
}));
vi.mock("@/lib/logger", () => ({
    logger: {
        info: vi.fn(),
        debug: vi.fn(),
        success: vi.fn(),
        warning: vi.fn(),
        error: vi.fn(),
    },
}));
vi.mock("@/lib/duplicate-tracks", () => ({ logDuplicateTracks: vi.fn() }));
vi.mock("../../wailsjs/go/main/App", () => ({ AddFetchHistory: doubles.addFetchHistory }));

let loadedMetadataAfterAttempt: typeof import("./useMetadata")["loadedMetadataAfterAttempt"];
let useMetadata: typeof import("./useMetadata")["useMetadata"];

beforeAll(async () => {
    vi.stubGlobal("window", { navigator: { platform: "Win32" } });
    ({ loadedMetadataAfterAttempt, useMetadata } = await import("./useMetadata"));
});

afterAll(() => vi.unstubAllGlobals());

beforeEach(() => {
    hookState.reset();
    doubles.fetchSpotifyMetadata.mockReset();
    doubles.addFetchHistory.mockReset().mockResolvedValue(undefined);
    doubles.toastSuccess.mockReset();
    doubles.toastError.mockReset();
});

function playlistMetadata(title: string): SpotifyMetadataResponse {
    return {
        playlist_info: {
            name: "",
            tracks: { total: 0 },
            followers: { total: 0 },
            owner: { display_name: "Owner", name: title, images: "" },
        },
        track_list: [],
    };
}

function deferred<T>() {
    let resolve!: (value: T) => void;
    let reject!: (reason: unknown) => void;
    const promise = new Promise<T>((resolvePromise, rejectPromise) => {
        resolve = resolvePromise;
        reject = rejectPromise;
    });
    return { promise, resolve, reject };
}

function MetadataHookHarness() {
    hookState.beginRender();
    return useMetadata();
}

describe("loaded metadata source identity", () => {
    it("retains rendered collection A when fetching draft URL B fails", () => {
        const loadedA: LoadedSpotifyMetadata = {
            data: playlistMetadata("Collection A"),
            sourceUrl: "https://open.spotify.com/playlist/a",
        };

        expect(loadedMetadataAfterAttempt(
            loadedA,
            "https://open.spotify.com/album/b",
            undefined,
        )).toBe(loadedA);
    });

    it("replaces payload and source URL together for cached history B", () => {
        const loadedA: LoadedSpotifyMetadata = {
            data: playlistMetadata("Collection A"),
            sourceUrl: "https://open.spotify.com/playlist/a",
        };
        const metadataB = playlistMetadata("Collection B");

        expect(loadedMetadataAfterAttempt(
            loadedA,
            "https://open.spotify.com/playlist/b",
            metadataB,
        )).toEqual({
            data: metadataB,
            sourceUrl: "https://open.spotify.com/playlist/b",
        });
    });
});

describe("useMetadata async source identity", () => {
    const urlA = "https://open.spotify.com/playlist/a";
    const urlB = "https://open.spotify.com/playlist/b";

    it("keeps B when request A resolves after request B", async () => {
        const requestAResult = deferred<SpotifyMetadataResponse>();
        const requestBResult = deferred<SpotifyMetadataResponse>();
        const metadataA = playlistMetadata("Collection A");
        const metadataB = playlistMetadata("Collection B");
        doubles.fetchSpotifyMetadata.mockImplementation((url: string) =>
            url === urlA ? requestAResult.promise : requestBResult.promise);

        let hook = MetadataHookHarness();
        const requestA = hook.handleFetchMetadata(urlA);
        hook = MetadataHookHarness();
        const requestB = hook.handleFetchMetadata(urlB);
        requestBResult.resolve(metadataB);
        await requestB;

        hook = MetadataHookHarness();
        expect(hook.loadedMetadata).toEqual({ data: metadataB, sourceUrl: urlB });

        requestAResult.resolve(metadataA);
        await requestA;
        hook = MetadataHookHarness();
        expect(hook.loadedMetadata).toEqual({ data: metadataB, sourceUrl: urlB });
        expect(hook.loading).toBe(false);
    });

    it("keeps cached history B when request A completes later", async () => {
        const requestAResult = deferred<SpotifyMetadataResponse>();
        const metadataA = playlistMetadata("Collection A");
        const metadataB = playlistMetadata("Cached B");
        doubles.fetchSpotifyMetadata.mockReturnValue(requestAResult.promise);

        let hook = MetadataHookHarness();
        const requestA = hook.handleFetchMetadata(urlA);
        hook.loadFromCache(JSON.stringify(metadataB), urlB);

        hook = MetadataHookHarness();
        expect(hook.loadedMetadata).toEqual({ data: metadataB, sourceUrl: urlB });
        expect(hook.loading).toBe(false);

        requestAResult.resolve(metadataA);
        await requestA;
        hook = MetadataHookHarness();
        expect(hook.loadedMetadata).toEqual({ data: metadataB, sourceUrl: urlB });
    });

    it("does not repopulate metadata after reset while a request is pending", async () => {
        const requestAResult = deferred<SpotifyMetadataResponse>();
        doubles.fetchSpotifyMetadata.mockReturnValue(requestAResult.promise);

        let hook = MetadataHookHarness();
        const requestA = hook.handleFetchMetadata(urlA);
        hook.resetMetadata();

        hook = MetadataHookHarness();
        expect(hook.loadedMetadata).toBeNull();
        expect(hook.loading).toBe(false);

        requestAResult.resolve(playlistMetadata("Collection A"));
        await requestA;
        hook = MetadataHookHarness();
        expect(hook.loadedMetadata).toBeNull();
    });

    it("ignores loading and error updates from a stale rejected request", async () => {
        const requestAResult = deferred<SpotifyMetadataResponse>();
        const requestBResult = deferred<SpotifyMetadataResponse>();
        doubles.fetchSpotifyMetadata.mockImplementation((url: string) =>
            url === urlA ? requestAResult.promise : requestBResult.promise);

        let hook = MetadataHookHarness();
        const requestA = hook.handleFetchMetadata(urlA);
        hook = MetadataHookHarness();
        const requestB = hook.handleFetchMetadata(urlB);
        requestAResult.reject(new Error("stale A failed"));
        await requestA;
        const afterStaleFailure = MetadataHookHarness();

        requestBResult.resolve(playlistMetadata("Collection B"));
        await requestB;
        hook = MetadataHookHarness();
        expect(afterStaleFailure.loading).toBe(true);
        expect(doubles.toastError).not.toHaveBeenCalled();
        expect(hook.loadedMetadata?.sourceUrl).toBe(urlB);
    });

    it("retains the prior successful pair when the current sequential request fails", async () => {
        const metadataA = playlistMetadata("Collection A");
        doubles.fetchSpotifyMetadata.mockRejectedValue(new Error("B failed"));

        let hook = MetadataHookHarness();
        hook.loadFromCache(JSON.stringify(metadataA), urlA);
        hook = MetadataHookHarness();
        await hook.handleFetchMetadata(urlB);
        hook = MetadataHookHarness();

        expect(hook.loadedMetadata).toEqual({ data: metadataA, sourceUrl: urlA });
        expect(hook.loading).toBe(false);
        expect(doubles.toastError).toHaveBeenCalledWith("B failed");
    });

    it("retains the prior successful pair when a current album request fails", async () => {
        const metadataA = playlistMetadata("Collection A");
        const albumUrl = "https://open.spotify.com/album/b";
        doubles.fetchSpotifyMetadata.mockRejectedValue(new Error("album B failed"));

        let hook = MetadataHookHarness();
        hook.loadFromCache(JSON.stringify(metadataA), urlA);
        hook = MetadataHookHarness();
        hook.handleAlbumClick({ id: "b", name: "Album B", external_urls: albumUrl });
        hook = MetadataHookHarness();
        await hook.handleConfirmAlbumFetch();
        hook = MetadataHookHarness();

        expect(hook.loadedMetadata).toEqual({ data: metadataA, sourceUrl: urlA });
        expect(hook.loading).toBe(false);
        expect(doubles.toastError).toHaveBeenCalledWith("album B failed");
    });
});
