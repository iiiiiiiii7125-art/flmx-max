import ReactDOM from "react-dom/client";
import { BrowserRouter, HashRouter } from "react-router-dom";
import { registerSW } from "virtual:pwa-register";

import App from "./App.tsx";
import { IS_DEMO, installDemo } from "./demo";
import { DemoBanner } from "./demo/banner";
import { Provider } from "./provider.tsx";
import "@/styles/globals.css";

// A page chunk from the previous build is gone after an upgrade: reload
// to pick up the new build instead of showing a blank page. At most once
// a minute, so a chunk that is really missing doesn't loop.
window.addEventListener("vite:preloadError", (event) => {
  const key = "flmx_chunk_reload";

  try {
    const last = Number(sessionStorage.getItem(key) || 0);

    if (Date.now() - last < 60000) return;
    sessionStorage.setItem(key, String(Date.now()));
  } catch {
    return;
  }
  event.preventDefault();
  window.location.reload();
});

if (IS_DEMO) installDemo();

// registerType is "autoUpdate" (see vite.config.ts): a new service
// worker activates immediately and the page reloads into the new
// build on its own. The previous "prompt" flow waited on a toast the
// user could dismiss ("稍后") or never see; while waiting, the old SW
// kept serving the precached old UI and refreshes changed nothing.
//
// The remaining staleness is the SW update check itself: browsers
// throttle it to at most once every 24h, so after an upgrade the old
// SW can keep serving the precached old UI and ordinary refreshes
// change nothing (only a hard refresh, which bypasses the SW, shows
// the new build). Ask for an update check explicitly on load and
// whenever the tab becomes visible again — registration.update()
// bypasses the 24h throttle, so the new SW installs promptly and the
// built-in "activated" listener reloads the page into the new build.
// (No interval polling: once the new SW is in, navigations are
// network-first — see vite.config.ts — so a plain refresh is fresh by
// construction and nothing needs to poll.)
registerSW({
  immediate: true,
  onRegisteredSW(_swUrl, registration) {
    if (!registration) return;
    const checkForUpdate = () => {
      if (document.visibilityState !== "visible") return;
      registration.update().catch(() => {});
    };
    checkForUpdate();
    document.addEventListener("visibilitychange", () => {
      if (!document.hidden) checkForUpdate();
    });
    window.addEventListener("focus", checkForUpdate);
  },
});

// The demo is static files on GitHub Pages: hash routes need no server.
const Router = IS_DEMO ? HashRouter : BrowserRouter;

ReactDOM.createRoot(document.getElementById("root")!).render(
  <Router>
    <Provider>
      <App />
      {IS_DEMO && <DemoBanner />}
    </Provider>
  </Router>,
);
