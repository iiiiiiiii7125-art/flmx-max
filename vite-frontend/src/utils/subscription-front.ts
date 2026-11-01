import { useEffect, useState } from "react";

import { getCachedConfig } from "@/config/site";

/** Config key of the subscription front base list (JSON string array). */
export const SUBSCRIPTION_FRONT_URLS_KEY = "subscription_front_urls";

/**
 * Parse the stored front base list. The backend normalizes on save, so
 * entries here are already canonical (scheme + host[:port]); parsing
 * stays defensive anyway — a malformed value yields no fronts rather
 * than broken links.
 */
export function parseSubscriptionFrontURLs(
  raw: string | null | undefined,
): string[] {
  if (!raw) return [];

  try {
    const list = JSON.parse(raw);

    if (!Array.isArray(list)) return [];

    return list
      .filter((v): v is string => typeof v === "string")
      .map((v) => v.trim())
      .filter((v) => v !== "");
  } catch {
    return [];
  }
}

/**
 * The configured subscription front bases for link assembly. Empty when
 * none are configured — every presentation surface then behaves exactly
 * as before (panel origin only).
 */
export function useSubscriptionFrontURLs(): string[] {
  const [fronts, setFronts] = useState<string[]>([]);

  useEffect(() => {
    let alive = true;

    getCachedConfig(SUBSCRIPTION_FRONT_URLS_KEY)
      .then((value) => {
        if (alive) setFronts(parseSubscriptionFrontURLs(value));
      })
      .catch(() => {});

    return () => {
      alive = false;
    };
  }, []);

  return fronts;
}
