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
registerSW({ immediate: true });

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
