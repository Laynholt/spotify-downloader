import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

vi.mock("../../wailsjs/go/main/App", () => ({
    FetchSessionTokenWithParams: vi.fn(),
}));

vi.mock("./settings", () => ({
    MIN_TOKEN_TIMEOUT: 120,
    getSettings: () => ({ tokenRetry: 0, tokenTimeout: 120 }),
    isLikelySessionToken: (token: string | null | undefined) => Boolean(token && token.startsWith("eyJ") && token.length > 20),
}));

import { FetchSessionTokenWithParams } from "../../wailsjs/go/main/App";
import { ensureValidToken } from "./token-manager";

describe("ensureValidToken", () => {
    const fetchToken = vi.mocked(FetchSessionTokenWithParams);

    beforeEach(() => {
        fetchToken.mockReset();
    });

    afterEach(() => {
        vi.clearAllTimers();
        vi.useRealTimers();
        vi.restoreAllMocks();
    });

    it("shares one refresh request across twenty concurrent callers", async () => {
        vi.useFakeTimers();
        const timeoutSpy = vi.spyOn(globalThis, "setTimeout");
        let resolveFetch!: (response: { token: string; expires_at: number }) => void;
        const fetchPromise = new Promise<{ token: string; expires_at: number }>(resolve => {
            resolveFetch = resolve;
        });
        fetchToken.mockReturnValue(fetchPromise);

        const calls = Array.from({ length: 20 }, () => ensureValidToken(true));
        await Promise.resolve();
        expect(fetchToken).toHaveBeenCalledTimes(1);
        expect(timeoutSpy).not.toHaveBeenCalled();

        const token = "eyJ" + "a".repeat(40);
        resolveFetch({ token, expires_at: Math.floor(Date.now() / 1000) + 3600 });
        await expect(Promise.all(calls)).resolves.toEqual(Array(20).fill(token));
    });
});
