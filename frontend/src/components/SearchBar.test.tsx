// @vitest-environment jsdom
import { act } from "react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { SearchBar } from "./SearchBar";
import { TooltipProvider } from "./ui/tooltip";
import { SearchSpotify, SearchSpotifyByType } from "../../wailsjs/go/main/App";
import { backend } from "../../wailsjs/go/models";
import { mount, change, button, deferred } from "../test/render";

vi.mock("../../wailsjs/go/main/App", () => ({ SearchSpotify: vi.fn(), SearchSpotifyByType: vi.fn() }));
vi.mock("@/hooks/useTypingEffect", () => ({ useTypingEffect: () => "Search" }));

function result(name: string) {
    return new backend.SearchResult({ id: name, name, type: "track", artists: "Artist", images: "", external_urls: "https://open.spotify.com/track/1", duration_ms: 1000, is_explicit: false, release_date: "", owner: "" });
}
function response(name: string, count = 1, albums = false) {
    return new backend.SearchResponse({ tracks: Array.from({ length: count }, (_, index) => result(`${name} ${index}`)), albums: albums ? [result("Album")] : [], artists: [], playlists: [] });
}
const props = { url: "", loading: false, onUrlChange: vi.fn(), onFetch: vi.fn(), onFetchUrl: vi.fn(), history: [], onHistorySelect: vi.fn(), onHistoryRemove: vi.fn(), hasResult: false, searchMode: true, onSearchModeChange: vi.fn() };
let view: Awaited<ReturnType<typeof mount>>;
beforeEach(async () => {
    vi.resetAllMocks();
    vi.useFakeTimers();
    localStorage.clear();
    vi.stubGlobal("IS_REACT_ACT_ENVIRONMENT", true);
    view = await mount(<TooltipProvider><SearchBar {...props}/></TooltipProvider>);
});
afterEach(async () => { await view.unmount(); vi.useRealTimers(); vi.unstubAllGlobals(); });
async function query(value: string) {
    await act(async () => change(view.container.querySelector<HTMLInputElement>("#spotify-search")!, value));
}
async function start(value: string) {
    await query(value);
    await act(async () => vi.advanceTimersByTimeAsync(400));
}

describe("search request lifecycle", () => {
    it("shows waiting during debounce and empty only after a successful empty response", async () => {
        vi.mocked(SearchSpotify).mockResolvedValue(response("", 0));
        await query("empty");
        expect(view.container.textContent).not.toContain("No results found");
        expect(view.container.textContent).toContain("Waiting");
        await act(async () => vi.advanceTimersByTimeAsync(400));
        expect(view.container.textContent).toContain("No results found");
    });

    it.each(["resolve", "reject"])("ignores an old %s as soon as the query changes, before the next debounce", async (outcome) => {
        const old = deferred<backend.SearchResponse>();
        vi.mocked(SearchSpotify).mockReturnValueOnce(old.promise).mockResolvedValueOnce(response("Fresh"));
        await start("old");
        await act(async () => {
            change(view.container.querySelector<HTMLInputElement>("#spotify-search")!, "new");
            if (outcome === "resolve") old.resolve(response("Stale"));
            else old.reject(new Error("stale network error"));
            await Promise.resolve();
        });
        expect(view.container.textContent).not.toContain("Stale");
        expect(view.container.textContent).not.toContain("stale network error");
        expect(view.container.textContent).toContain("Waiting");
        expect(localStorage.getItem("spotidownloader_recent_searches") || "").not.toContain("old");
        await act(async () => vi.advanceTimersByTimeAsync(400));
        expect(view.container.textContent).toContain("Fresh");
    });

    it.each(["clear", "mode"])("ignores a response after %s", async (action) => {
        const old = deferred<backend.SearchResponse>();
        vi.mocked(SearchSpotify).mockReturnValue(old.promise);
        await start("old");
        if (action === "clear") await query("");
        else await view.render(<TooltipProvider><SearchBar {...props} searchMode={false}/></TooltipProvider>);
        await act(async () => old.resolve(response("Stale")));
        if (action === "mode") await view.render(<TooltipProvider><SearchBar {...props}/></TooltipProvider>);
        expect(view.container.textContent).not.toContain("Stale");
        expect(localStorage.getItem("spotidownloader_recent_searches") || "").not.toContain("old");
    });

    it("shows a service error separately from empty results and retries through the button", async () => {
        vi.mocked(SearchSpotify).mockRejectedValueOnce(new Error("service offline")).mockResolvedValueOnce(response("Recovered"));
        await start("retry");
        expect(view.container.textContent).toContain("service offline");
        expect(view.container.textContent).not.toContain("No results found");
        await act(async () => button(view.container, "Retry search").click());
        await act(async () => vi.advanceTimersByTimeAsync(400));
        expect(view.container.textContent).toContain("Recovered");
    });

    it.each(["resolve", "reject"])("preserves a newer successful search when the older request later %ss", async (outcome) => {
        const old = deferred<backend.SearchResponse>();
        vi.mocked(SearchSpotify).mockReturnValueOnce(old.promise).mockResolvedValueOnce(response("Latest"));
        await start("old");
        await start("new");
        await act(async () => {
            if (outcome === "resolve") old.resolve(response("Stale"));
            else old.reject(new Error("stale failure"));
        });
        expect(view.container.textContent).toContain("Latest");
        expect(view.container.textContent).not.toContain("Stale");
        expect(view.container.textContent).not.toContain("stale failure");
    });

    it("retains results on pagination error and retries the same page through the button", async () => {
        vi.mocked(SearchSpotify).mockResolvedValue(response("Initial", 50));
        vi.mocked(SearchSpotifyByType).mockRejectedValueOnce(new Error("page offline")).mockResolvedValueOnce([result("Recovered page")]);
        await start("first");
        await act(async () => button(view.container, "Load More").click());
        expect(view.container.textContent).toContain("Initial");
        expect(view.container.textContent).toContain("page offline");
        await act(async () => button(view.container, "Retry loading more").click());
        expect(view.container.textContent).toContain("Recovered page");
        expect(view.container.textContent).not.toContain("page offline");
        expect(SearchSpotifyByType).toHaveBeenLastCalledWith({ query: "first", search_type: "track", limit: 50, offset: 50 });
    });

    it("ignores a pagination rejection from the previous tab", async () => {
        const page = deferred<backend.SearchResult[]>();
        vi.mocked(SearchSpotify).mockResolvedValue(response("Initial", 50, true));
        vi.mocked(SearchSpotifyByType).mockReturnValue(page.promise);
        await start("first");
        await act(async () => button(view.container, "Load More").click());
        await act(async () => button(view.container, "Albums (1)").click());
        await act(async () => page.reject(new Error("stale page error")));
        expect(view.container.textContent).toContain("Album");
        expect(view.container.textContent).not.toContain("stale page error");
        await act(async () => button(view.container, "Tracks (50)").click());
        expect(button(view.container, "Load More").disabled).toBe(false);
    });

    it.each(["query", "tab"])("discards pagination after a %s change", async (action) => {
        const page = deferred<backend.SearchResult[]>();
        vi.mocked(SearchSpotify).mockResolvedValue(response("Initial", 50, true));
        vi.mocked(SearchSpotifyByType).mockReturnValue(page.promise);
        await start("first");
        await act(async () => button(view.container, "Load More").click());
        if (action === "query") {
            vi.mocked(SearchSpotify).mockResolvedValue(response("Replacement"));
            await start("second");
        } else await act(async () => button(view.container, "Albums (1)").click());
        await act(async () => page.resolve([result("Stale page")]));
        if (action === "tab") {
            expect(view.container.textContent).toContain("Tracks (50)");
            await act(async () => button(view.container, "Tracks (50)").click());
        }
        expect(view.container.textContent).not.toContain("Stale page");
        expect(view.container.textContent).toContain(action === "query" ? "Replacement" : "Initial");
    });
});
