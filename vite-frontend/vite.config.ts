import path from "path";

import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";
import { VitePWA } from "vite-plugin-pwa";

export default defineConfig({
  plugins: [
    react(),
    VitePWA({
      // No service worker for the online demo.
      disable: process.env.VITE_DEMO === "true",
      // autoUpdate: new versions must take over on their own. With
      // "prompt" the new SW waits for a toast click that users can
      // dismiss or never see, and the waiting SW keeps serving the
      // precached old UI — refreshes then show the old panel forever.
      registerType: "autoUpdate",
      injectRegister: "auto",
      includeAssets: ["favicon.ico", "apple-touch-icon.png"],
      manifest: {
        name: "FLMX",
        short_name: "FLMX",
        description: "FLMX forwarding management panel",
        theme_color: "#2563eb",
        background_color: "#f6f7fb",
        display: "standalone",
        start_url: "/",
        scope: "/",
        icons: [
          {
            src: "pwa-192x192.png",
            sizes: "192x192",
            type: "image/png",
          },
          {
            src: "pwa-512x512.png",
            sizes: "512x512",
            type: "image/png",
          },
          {
            src: "pwa-maskable-512x512.png",
            sizes: "512x512",
            type: "image/png",
            purpose: "maskable",
          },
        ],
      },
      workbox: {
        // No navigateFallback (vite-plugin-pwa defaults it to
        // "index.html", so it must be cleared explicitly — merely
        // omitting it leaves the default in place): navigations must
        // go to the network, not to the precache. A precached
        // index.html is what made a plain refresh show the old panel
        // after an upgrade: the SW served the old precache (the
        // precache route matches "/" via directoryIndex, and the
        // NavigationRoute served it for every navigation), and only a
        // hard refresh — which bypasses the SW — fetched the new
        // build. With no precached shell, index.html is always
        // fetched fresh (nginx: no-store) and a plain refresh shows
        // the latest build directly. Hashed assets under /assets/
        // stay precached + immutable, which is safe because a new
        // index.html references new hashes. Offline navigation loses
        // its precached fallback; the panel needs the live API
        // anyway, so that costs nothing.
        // (index.html is excluded from the precache below; leaving it
        // in would let the precache route serve it for "/".)
        navigateFallback: undefined,
        globIgnores: ["**/index.html"],
        cleanupOutdatedCaches: true,
        // Belt and braces for autoUpdate: activate immediately and
        // take control of open tabs instead of waiting for all tabs
        // to close (vite-plugin-pwa also sets these for autoUpdate).
        skipWaiting: true,
        clientsClaim: true,
        maximumFileSizeToCacheInBytes: 5 * 1024 * 1024,
      },
    }),
  ],
  // The online demo is served from the docs site (VITE_BASE=/flmx-max/demo/).
  base: process.env.VITE_BASE || "/",
  resolve: {
    alias: {
      "@": path.resolve(__dirname, "./src"),
    },
  },
  server: {
    port: 3000,
    host: "0.0.0.0",
  },
  build: {
    outDir: "dist",
    sourcemap: false,
  },
});
