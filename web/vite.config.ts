import { fileURLToPath, URL } from "node:url";
import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";
import tailwindcss from "@tailwindcss/vite";

// The controller serves the built dashboard from internal/web/dist (embedded in the binary).
// In development, Vite proxies the API to a locally running controller.
const controller = process.env.SYNCLOUD_CONTROLLER ?? "http://127.0.0.1:7070";

export default defineConfig({
  plugins: [react(), tailwindcss()],
  resolve: {
    alias: { "@": fileURLToPath(new URL("./src", import.meta.url)) },
  },
  server: {
    port: 5173,
    proxy: {
      // changeOrigin stays false so the controller's same-origin checks see the dashboard's own host.
      "/api": { target: controller, ws: true },
    },
  },
  build: {
    outDir: "../internal/web/dist",
    emptyOutDir: true,
    chunkSizeWarningLimit: 1200,
  },
});
