/// <reference types="vite/client" />
import { describe, expect, it } from "vitest";

const sources = import.meta.glob<string>(["../**/*.{ts,tsx,css}", "!./source-encoding.test.ts"], {
    query: "?raw",
    import: "default",
    eager: true,
});

// Production break caught: a save in the wrong code page turned "•" into
// "вЂў" in the download queue. UTF-8 punctuation read as cp1251 starts with "вЂ".
describe("frontend sources", () => {
    it("contain no UTF-8 text decoded as cp1251", () => {
        expect(Object.keys(sources).length).toBeGreaterThan(20);
        const damaged = Object.entries(sources)
            .filter(([, text]) => text.includes("вЂ"))
            .map(([file]) => file);
        expect(damaged).toEqual([]);
    });
});
