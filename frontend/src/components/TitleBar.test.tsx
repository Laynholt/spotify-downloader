// @vitest-environment jsdom
import { act } from "react";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { mount } from "../test/render";

vi.mock("../../wailsjs/go/main/App", () => ({ GetDefaults: vi.fn(), LoadSettings: vi.fn(), SaveSettings: vi.fn() }));
vi.mock("../../wailsjs/runtime/runtime", () => ({ WindowMinimise: vi.fn(), WindowToggleMaximise: vi.fn(), Quit: vi.fn() }));
vi.mock("@/lib/toast-with-sound", () => ({ toastWithSound: { error: vi.fn() } }));
let view: Awaited<ReturnType<typeof mount>>;
beforeEach(() => { vi.resetModules(); vi.resetAllMocks(); localStorage.clear(); vi.stubGlobal("IS_REACT_ACT_ENVIRONMENT", true); });
afterEach(async () => { await view?.unmount(); vi.unstubAllGlobals(); });

it("keeps the previous token timeout selected after a failed menu save", async () => {
    const api = await import("../../wailsjs/go/main/App");
    const settings = await import("@/lib/settings");
    await settings.saveSettings({ ...settings.DEFAULT_SETTINGS, downloadPath: "C:/Music" });
    vi.mocked(api.SaveSettings).mockRejectedValueOnce(new Error("menu write failed"));
    const { TitleBar } = await import("./TitleBar");
    const { toastWithSound: toast } = await import("@/lib/toast-with-sound");
    view = await mount(<TitleBar/>);
    const trigger = view.container.querySelector<HTMLElement>('[role="menuitem"]')!;
    await act(async () => trigger.dispatchEvent(new KeyboardEvent("keydown", { key: "ArrowDown", bubbles: true })));
    const timeout = [...document.querySelectorAll<HTMLElement>('[role="menuitem"]')].find(item => item.textContent === "Timeout")!;
    await act(async () => timeout.dispatchEvent(new KeyboardEvent("keydown", { key: "ArrowRight", bubbles: true })));
    const option = [...document.querySelectorAll<HTMLElement>('[role="menuitemradio"]')].find(item => item.textContent === "180s")!;
    await act(async () => option.click());
    expect(settings.getSettings().tokenTimeout).toBe(120);
    expect(toast.error).toHaveBeenCalledWith(expect.stringContaining("menu write failed"));
    await act(async () => trigger.dispatchEvent(new KeyboardEvent("keydown", { key: "ArrowDown", bubbles: true })));
    const timeoutAgain = [...document.querySelectorAll<HTMLElement>('[role="menuitem"]')].find(item => item.textContent === "Timeout")!;
    await act(async () => timeoutAgain.dispatchEvent(new KeyboardEvent("keydown", { key: "ArrowRight", bubbles: true })));
    const selected = document.querySelector('[role="menuitemradio"][aria-checked="true"]');
    expect(selected?.textContent).toBe("120s");
});
