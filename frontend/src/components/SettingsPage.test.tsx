// @vitest-environment jsdom
import { act } from "react";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { mount, change, button, deferred } from "../test/render";
import { TooltipProvider } from "./ui/tooltip";

vi.mock("../../wailsjs/go/main/App", () => ({ GetDefaults: vi.fn(), LoadSettings: vi.fn(), SaveSettings: vi.fn(), SelectFolder: vi.fn() }));
vi.mock("@/lib/toast-with-sound", () => ({ toastWithSound: { success: vi.fn(), error: vi.fn() } }));
let view: Awaited<ReturnType<typeof mount>>;
beforeEach(() => {
    vi.resetModules(); vi.resetAllMocks(); localStorage.clear();
    vi.stubGlobal("IS_REACT_ACT_ENVIRONMENT", true);
    window.matchMedia = vi.fn().mockReturnValue({ matches: false, addEventListener: vi.fn(), removeEventListener: vi.fn() });
});
afterEach(async () => { await view?.unmount(); vi.unstubAllGlobals(); });

async function setup(path = "C:/Saved") {
    const api = await import("../../wailsjs/go/main/App");
    const settings = await import("@/lib/settings");
    const { toastWithSound: toast } = await import("@/lib/toast-with-sound");
    await settings.saveSettings({ ...settings.DEFAULT_SETTINGS, downloadPath: path });
    const { SettingsPage } = await import("./SettingsPage");
    const dirty = vi.fn();
    view = await mount(<TooltipProvider><SettingsPage onUnsavedChangesChange={dirty}/></TooltipProvider>);
    return { api, settings, toast, dirty };
}

it("retains editable unsaved settings after a failed save and allows a successful retry", async () => {
    const { api, settings, toast, dirty } = await setup();
    await act(async () => change(view.container.querySelector<HTMLInputElement>("#download-path")!, "C:/Edited"));
    vi.mocked(api.SaveSettings).mockRejectedValueOnce(new Error("disk full"));
    await act(async () => button(view.container, "Save Changes").click());
    expect(view.container.querySelector<HTMLInputElement>("#download-path")!.value).toBe("C:/Edited");
    expect(settings.getSettings().downloadPath).toBe("C:/Saved");
    expect(dirty).toHaveBeenLastCalledWith(true);
    expect(toast.success).not.toHaveBeenCalled();
    expect(toast.error).toHaveBeenCalledWith(expect.stringContaining("disk full"));
    await act(async () => button(view.container, "Save Changes").click());
    expect(settings.getSettings().downloadPath).toBe("C:/Edited");
    expect(dirty).toHaveBeenLastCalledWith(false);
    expect(toast.success).toHaveBeenCalledTimes(1);
});

it("retains the current settings and reset confirmation after a failed reset", async () => {
    const { api, settings, toast } = await setup();
    vi.mocked(api.GetDefaults).mockResolvedValue({ downloadPath: "C:/Default" });
    vi.mocked(api.SaveSettings).mockRejectedValueOnce(new Error("read only"));
    await act(async () => button(view.container, "Reset to Default").click());
    await act(async () => button(document.body, "Reset").click());
    expect(settings.getSettings().downloadPath).toBe("C:/Saved");
    expect(view.container.querySelector<HTMLInputElement>("#download-path")!.value).toBe("C:/Saved");
    expect(document.body.querySelector('[role="dialog"]')).not.toBeNull();
    expect(toast.success).not.toHaveBeenCalled();
    expect(toast.error).toHaveBeenCalledWith(expect.stringContaining("read only"));
    await act(async () => button(document.body, "Reset").click());
    expect(settings.getSettings().downloadPath).toBe("C:/Default");
    expect(document.body.querySelector('[role="dialog"]')).toBeNull();
});

it("keeps edits made during a pending save marked as unsaved", async () => {
    const { api, settings, dirty } = await setup();
    const saving = deferred<void>();
    vi.mocked(api.SaveSettings).mockReturnValueOnce(saving.promise);
    await act(async () => change(view.container.querySelector<HTMLInputElement>("#download-path")!, "C:/First"));
    await act(async () => button(view.container, "Save Changes").click());
    await act(async () => change(view.container.querySelector<HTMLInputElement>("#download-path")!, "C:/Later"));
    await act(async () => saving.resolve());
    expect(settings.getSettings().downloadPath).toBe("C:/First");
    expect(view.container.querySelector<HTMLInputElement>("#download-path")!.value).toBe("C:/Later");
    expect(dirty).toHaveBeenLastCalledWith(true);
});

it("reports failure to save a derived default path without marking it saved", async () => {
    const api = await import("../../wailsjs/go/main/App");
    vi.mocked(api.GetDefaults).mockResolvedValue({ downloadPath: "C:/Default" });
    const settings = await import("@/lib/settings");
    await settings.saveSettings(settings.DEFAULT_SETTINGS);
    vi.mocked(api.SaveSettings).mockRejectedValueOnce(new Error("default write failed"));
    const { SettingsPage } = await import("./SettingsPage");
    const { toastWithSound: toast } = await import("@/lib/toast-with-sound");
    view = await mount(<TooltipProvider><SettingsPage/></TooltipProvider>);
    expect(settings.getSettings().downloadPath).toBe("");
    expect(toast.error).toHaveBeenCalledWith(expect.stringContaining("default write failed"));
});

it("prevents user save or reset from racing the initial default save", async () => {
    const api = await import("../../wailsjs/go/main/App");
    vi.mocked(api.GetDefaults).mockResolvedValue({ downloadPath: "C:/Default" });
    const settings = await import("@/lib/settings");
    await settings.saveSettings(settings.DEFAULT_SETTINGS);
    const saving = deferred<void>();
    vi.mocked(api.SaveSettings).mockReturnValueOnce(saving.promise);
    const { SettingsPage } = await import("./SettingsPage");
    view = await mount(<TooltipProvider><SettingsPage/></TooltipProvider>);
    expect(button(view.container, "Save Changes").disabled).toBe(true);
    expect(button(view.container, "Reset to Default").disabled).toBe(true);
    await act(async () => saving.resolve());
    expect(button(view.container, "Save Changes").disabled).toBe(false);
});
