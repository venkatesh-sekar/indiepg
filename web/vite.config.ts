import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";
import { fileURLToPath, URL } from "node:url";

// The Go binary embeds this output via //go:embed all:dist in
// internal/server/web/embed.go, so we build straight into that package's dist/.
export default defineConfig({
  plugins: [react()],
  resolve: {
    alias: {
      "@": fileURLToPath(new URL("./src", import.meta.url)),
    },
  },
  build: {
    outDir: "../internal/server/web/dist",
    emptyOutDir: true,
    sourcemap: false,
    chunkSizeWarningLimit: 900,
  },
  server: {
    port: 5173,
    proxy: {
      // During `vite dev` proxy the API to a locally running pgpanel binary.
      "/api": {
        target: "http://127.0.0.1:8443",
        changeOrigin: true,
        secure: false,
      },
    },
  },
});
