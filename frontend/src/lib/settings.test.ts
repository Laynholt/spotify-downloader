import { beforeEach, afterEach, expect, it, vi } from "vitest";

vi.mock("../../wailsjs/go/main/App", () => ({
    GetDefaults: vi.fn(), LoadSettings: vi.fn(), SaveSettings: vi.fn(),
}));

beforeEach(() => {
    vi.resetModules();
    vi.resetAllMocks();
    const values = new Map<string, string>();
    vi.stubGlobal("localStorage", {
        getItem: (key: string) => values.get(key) ?? null,
        setItem: (key: string, value: string) => values.set(key, value),
    });
    vi.stubGlobal("window", { navigator: { platform: "Win32" }, dispatchEvent: vi.fn() });
    vi.stubGlobal("CustomEvent", class { constructor(public type: string, public options: unknown) {} });
});
afterEach(() => vi.unstubAllGlobals());

it("keeps saved settings and notifications unchanged until the backend confirms the write", async () => {
    const api = await import("../../wailsjs/go/main/App");
    const settings = await import("./settings");
    const original = { ...settings.DEFAULT_SETTINGS, downloadPath: "C:/Music" };
    await settings.saveSettings(original);
    const stored = localStorage.getItem("spotidownloader-settings");
    const events = vi.mocked(window.dispatchEvent).mock.calls.length;
    let reject!: (error: Error) => void;
    vi.mocked(api.SaveSettings).mockReturnValue(new Promise((_resolve, fail) => { reject = fail; }));
    const failure = new Error("disk full");
    const saving = settings.saveSettings({ ...original, downloadPath: "C:/New" });
    expect(settings.getSettings().downloadPath).toBe("C:/Music");
    expect(localStorage.getItem("spotidownloader-settings")).toBe(stored);
    reject(failure);
    await expect(saving).rejects.toBe(failure);
    expect(settings.getSettings().downloadPath).toBe("C:/Music");
    expect(localStorage.getItem("spotidownloader-settings")).toBe(stored);
    expect(window.dispatchEvent).toHaveBeenCalledTimes(events);
});

it("commits confirmed settings and strips session credentials", async () => {
    const settings = await import("./settings");
    await settings.saveSettings({ ...settings.DEFAULT_SETTINGS, downloadPath: "C:/Confirmed", sessionToken: "secret" });
    expect(settings.getSettings().downloadPath).toBe("C:/Confirmed");
    expect(JSON.parse(localStorage.getItem("spotidownloader-settings")!)).toMatchObject({ downloadPath: "C:/Confirmed" });
    expect(localStorage.getItem("spotidownloader-settings")).not.toContain("secret");
    expect(window.dispatchEvent).toHaveBeenCalledTimes(1);
});

it("keeps the last saved settings when resetting defaults fails", async () => {
    const api = await import("../../wailsjs/go/main/App");
    const settings = await import("./settings");
    await settings.saveSettings({ ...settings.DEFAULT_SETTINGS, downloadPath: "C:/Custom", theme: "blue" });
    vi.mocked(api.GetDefaults).mockResolvedValue({ downloadPath: "C:/Default" });
    vi.mocked(api.SaveSettings).mockRejectedValue(new Error("permission denied"));
    await expect(settings.resetToDefaultSettings()).rejects.toThrow("permission denied");
    expect(settings.getSettings()).toMatchObject({ downloadPath: "C:/Custom", theme: "blue" });
});

it("never overwrites an unreadable config file with local or default settings", async () => {
    const api = await import("../../wailsjs/go/main/App");
    const settings = await import("./settings");
    const failure = new Error("invalid character '}' in config.json");
    vi.mocked(api.LoadSettings).mockRejectedValue(failure);
    const loaded = await settings.loadSettings();
    expect(loaded).toEqual(settings.DEFAULT_SETTINGS);
    expect(api.SaveSettings).not.toHaveBeenCalled();
    expect(settings.getSettingsLoadError()).toBe(failure);

    vi.mocked(api.LoadSettings).mockResolvedValue({ downloadPath: "C:/Fixed" });
    expect((await settings.loadSettings()).downloadPath).toBe("C:/Fixed");
    expect(settings.getSettingsLoadError()).toBeNull();
});

it("migrates local settings only when no config file exists", async () => {
    const api = await import("../../wailsjs/go/main/App");
    const settings = await import("./settings");
    vi.mocked(api.LoadSettings).mockResolvedValue(null as unknown as Record<string, unknown>);
    await settings.loadSettings();
    expect(api.SaveSettings).toHaveBeenCalledTimes(1);
});

it("derives the default path without mutating or persisting the saved settings", async () => {
    const api = await import("../../wailsjs/go/main/App");
    const settings = await import("./settings");
    await settings.saveSettings(settings.DEFAULT_SETTINGS);
    vi.mocked(api.GetDefaults).mockResolvedValue({ downloadPath: "C:/Default" });
    vi.mocked(api.SaveSettings).mockClear();
    expect((await settings.getSettingsWithDefaults()).downloadPath).toBe("C:/Default");
    expect(settings.getSettings().downloadPath).toBe("");
    expect(api.SaveSettings).not.toHaveBeenCalled();
});
