import { defineConfig } from "vite";

// The board is served under any prefix (for example /team-worker/ behind
// nginx), so every asset and API URL stays relative to the page.
export default defineConfig({
  base: "./",
  build: {
    outDir: "../dist",
    emptyOutDir: true,
    target: "es2022",
    // ghostty-web is one lazily loaded chunk with its WebAssembly inlined.
    chunkSizeWarningLimit: 700,
  },
  server: {
    // `npm run dev` proxies the API to a running `team-worker board serve`.
    proxy: {
      "/api": { target: "http://127.0.0.1:8080", ws: true },
    },
  },
});
