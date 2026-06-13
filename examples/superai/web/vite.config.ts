import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";

// Build to dist/, which the Go server embeds. Proxy /api to the running Go
// backend during `npm run dev`.
export default defineConfig({
  plugins: [react()],
  build: { outDir: "dist", emptyOutDir: true },
  server: {
    proxy: {
      "/api": { target: "http://127.0.0.1:43517", changeOrigin: true },
    },
  },
});
