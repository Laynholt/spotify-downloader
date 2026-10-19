import path from "path";
import fs from "fs";
import tailwindcss from "@tailwindcss/vite";
import react from "@vitejs/plugin-react";
import { defineConfig } from "vite";
const wailsJsonPath = path.resolve(__dirname, "../wails.json");
const wailsJson = JSON.parse(fs.readFileSync(wailsJsonPath, "utf-8"));
const appVersion = wailsJson.info.productVersion;
export default defineConfig({
    plugins: [react(), tailwindcss()],
    resolve: {
        alias: {
            "@": path.resolve(__dirname, "./src"),
        },
    },
    define: {
        __APP_VERSION__: JSON.stringify(appVersion),
    },
    build: {
        rollupOptions: {
            output: {
                manualChunks(id) {
                    if (!id.includes("node_modules")) return;
                    if (/\/node_modules\/(react|react-dom|scheduler)\//.test(id)) return "react";
                    if (/\/node_modules\/(motion|motion-dom|motion-utils|framer-motion)\//.test(id)) return "motion";
                    if (id.includes("/@radix-ui/") || id.includes("/lucide-react/")) return "ui";
                },
            },
        },
    },
});
