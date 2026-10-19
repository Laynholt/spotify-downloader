import { act, type ReactNode } from "react";
import { createRoot } from "react-dom/client";

export async function mount(node: ReactNode) {
    const container = document.createElement("div");
    document.body.append(container);
    const root = createRoot(container);
    await act(async () => root.render(node));
    return {
        container,
        render: async (next: ReactNode) => { await act(async () => root.render(next)); },
        unmount: async () => { await act(async () => root.unmount()); container.remove(); },
    };
}

export function change(input: HTMLInputElement, value: string) {
    Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")!.set!.call(input, value);
    input.dispatchEvent(new Event("input", { bubbles: true }));
}

export function button(container: ParentNode, label: string) {
    const match = [...container.querySelectorAll("button")].find((item) => item.textContent?.trim() === label);
    if (!match) throw new Error(`Button missing: ${label}`);
    return match;
}

export function deferred<T>() {
    let resolve!: (value: T) => void;
    let reject!: (error: Error) => void;
    const promise = new Promise<T>((success, failure) => { resolve = success; reject = failure; });
    return { promise, resolve, reject };
}
