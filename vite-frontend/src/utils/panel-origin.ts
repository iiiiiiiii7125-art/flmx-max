import axios from "axios";

/**
 * The panel origin the browser talks to (the axios baseURL resolved
 * against the page origin), so assembled links work behind a proxy or
 * a switched API base address. Single definition shared by every
 * subscription link surface: inbound/share.tsx, subscription.tsx,
 * my-subscription.tsx and subscription/front-picker.tsx.
 */
export function panelOrigin(): string {
  const base = axios.defaults.baseURL || "";

  try {
    return new URL(base, window.location.origin).origin;
  } catch {
    return window.location.origin;
  }
}
