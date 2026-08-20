import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

vi.mock("../../wailsjs/go/main/App", () => ({
    FetchSessionTokenWithParams: vi.fn(),
}));

vi.mock("./settings", () => ({
    MIN_TOKEN_TIMEOUT: 120,
    getSettings: () => ({ tokenRetry: 0, tokenTimeout: 120 }),
    isLikelySessionToken: (token: string | null | undefined) => Boolean(token && token.startsWith("eyJ") && token.length > 20),
}));

type TokenResponse = { token: string; expires_at: number };

async function loadTokenManager() {
    const app = await import("../../wailsjs/go/main/App");
    const manager = await import("./token-manager");
    return {
        ensureValidToken: manager.ensureValidToken,
        fetchToken: vi.mocked(app.FetchSessionTokenWithParams),
    };
}

function token(suffix: string): string {
    return "eyJ" + suffix.repeat(40);
}

function response(value: string): TokenResponse {
    return { token: value, expires_at: Math.floor(Date.now() / 1000) + 3600 };
}

describe("ensureValidToken", () => {
    beforeEach(() => {
        vi.resetModules();
        vi.resetAllMocks();
    });

    afterEach(() => {
        vi.clearAllTimers();
        vi.useRealTimers();
        vi.restoreAllMocks();
    });

    it("shares one refresh request across twenty concurrent callers without timers", async () => {
        vi.useFakeTimers();
        const timeoutSpy = vi.spyOn(globalThis, "setTimeout");
        const { ensureValidToken, fetchToken } = await loadTokenManager();
        let resolveFetch!: (value: TokenResponse) => void;
        const fetchPromise = new Promise<TokenResponse>(resolve => {
            resolveFetch = resolve;
        });
        fetchToken.mockReturnValue(fetchPromise);

        const calls = Array.from({ length: 20 }, () => ensureValidToken(true));
        await Promise.resolve();
        expect(fetchToken).toHaveBeenCalledTimes(1);
        expect(timeoutSpy).not.toHaveBeenCalled();

        const value = token("a");
        resolveFetch(response(value));
        await expect(Promise.all(calls)).resolves.toEqual(Array(20).fill(value));
    });

    it("clears a rejected refresh so an immediate retry succeeds without timers", async () => {
        vi.useFakeTimers();
        const timeoutSpy = vi.spyOn(globalThis, "setTimeout");
        const { ensureValidToken, fetchToken } = await loadTokenManager();
        const failure = new Error("temporary token failure");
        fetchToken.mockRejectedValueOnce(failure);

        await expect(ensureValidToken(true)).rejects.toBe(failure);
        expect(fetchToken).toHaveBeenCalledTimes(1);
        expect(timeoutSpy).not.toHaveBeenCalled();

        const retryValue = token("b");
        fetchToken.mockResolvedValueOnce(response(retryValue));
        const retry = ensureValidToken(true);
        await Promise.resolve();
        expect(timeoutSpy).not.toHaveBeenCalled();
        await expect(retry).resolves.toBe(retryValue);
        expect(fetchToken).toHaveBeenCalledTimes(2);
    });

    it("uses cached tokens unless forceRefresh requests one new fetch", async () => {
        vi.useFakeTimers();
        const timeoutSpy = vi.spyOn(globalThis, "setTimeout");
        const { ensureValidToken, fetchToken } = await loadTokenManager();
        const firstValue = token("c");
        fetchToken.mockResolvedValueOnce(response(firstValue));

        await expect(ensureValidToken()).resolves.toBe(firstValue);
        await expect(ensureValidToken()).resolves.toBe(firstValue);
        expect(fetchToken).toHaveBeenCalledTimes(1);

        const refreshedValue = token("d");
        fetchToken.mockResolvedValueOnce(response(refreshedValue));
        await expect(ensureValidToken(true)).resolves.toBe(refreshedValue);
        await expect(ensureValidToken()).resolves.toBe(refreshedValue);
        expect(fetchToken).toHaveBeenCalledTimes(2);
        expect(timeoutSpy).not.toHaveBeenCalled();
    });
});
