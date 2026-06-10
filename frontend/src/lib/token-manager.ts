import { FetchSessionTokenWithParams } from "../../wailsjs/go/main/App";
import { getSettings, updateSettings, isTokenExpired, isLikelySessionToken, MIN_TOKEN_TIMEOUT } from "./settings";
import { logger } from "./logger";
let isFetchingToken = false;
let lastFetchTime = 0;
export class ChromeNotInstalledError extends Error {
    constructor(message: string) {
        super(message);
        this.name = "ChromeNotInstalledError";
    }
}
export async function ensureValidToken(forceRefresh: boolean = false): Promise<string> {
    const settings = getSettings();
    if (!forceRefresh && !isTokenExpired(settings)) {
        logger.debug("session token: using cached token");
        return settings.sessionToken;
    }
    if (isFetchingToken) {
        await new Promise(resolve => setTimeout(resolve, 1000));
        return ensureValidToken(forceRefresh);
    }
    const now = Date.now();
    if (now - lastFetchTime < 2000) {
        await new Promise(resolve => setTimeout(resolve, 2000 - (now - lastFetchTime)));
    }
    isFetchingToken = true;
    lastFetchTime = Date.now();
    try {
        const timeout = Math.max(settings.tokenTimeout || MIN_TOKEN_TIMEOUT, MIN_TOKEN_TIMEOUT);
        const retry = Math.max(settings.tokenRetry ?? 0, 0);
        logger.info(`session token: fetching with browser window (timeout ${timeout}s, retry ${retry})`);
        const response = await FetchSessionTokenWithParams(timeout, retry);
        if (!isLikelySessionToken(response.token)) {
            throw new Error("Session token fetch returned an empty or invalid token");
        }
        await updateSettings({
            sessionToken: response.token,
            sessionTokenExpiry: response.expires_at,
        });
        logger.success("session token: fetched successfully");
        return response.token;
    }
    catch (error) {
        const errorMessage = error instanceof Error ? error.message : String(error);
        if (errorMessage.includes("CHROME_NOT_INSTALLED")) {
            const message = errorMessage.replace("CHROME_NOT_INSTALLED: ", "");
            window.dispatchEvent(new CustomEvent("chromeNotInstalled", { detail: { message } }));
            throw new ChromeNotInstalledError(message);
        }
        throw error;
    }
    finally {
        isFetchingToken = false;
    }
}
