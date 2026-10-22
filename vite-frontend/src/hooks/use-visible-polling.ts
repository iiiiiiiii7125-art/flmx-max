import { useEffect, useRef } from "react";

/**
 * Pull cached results while visible; resume immediately after returning
 * online. Probe results change every few minutes (or after a reconnect), so
 * 15 seconds keeps them current without loading the panel.
 *
 * 移植自 kure29/flyx 3.3.33（DB-3）：节点/入站/中转三页共用——页面
 * 不可见或离线时不轮询，回前台/恢复联网立即补一次，防重入。
 */
export function useVisiblePolling(
  refresh: () => Promise<void>,
  interval = 15_000,
) {
  const refreshRef = useRef(refresh);

  useEffect(() => {
    refreshRef.current = refresh;
  }, [refresh]);

  useEffect(() => {
    let stopped = false;
    let running = false;
    let timer: ReturnType<typeof setTimeout>;
    const visible = () =>
      document.visibilityState !== "hidden" && navigator.onLine !== false;
    const schedule = () => {
      clearTimeout(timer);
      if (!stopped && visible()) timer = setTimeout(tick, interval);
    };
    const tick = async () => {
      if (stopped || running || !visible()) return;
      running = true;
      try {
        await refreshRef.current();
      } finally {
        running = false;
        schedule();
      }
    };
    const resume = () => {
      clearTimeout(timer);
      if (visible()) void tick();
    };

    schedule();
    document.addEventListener("visibilitychange", resume);
    window.addEventListener("focus", resume);
    window.addEventListener("online", resume);

    return () => {
      stopped = true;
      clearTimeout(timer);
      document.removeEventListener("visibilitychange", resume);
      window.removeEventListener("focus", resume);
      window.removeEventListener("online", resume);
    };
  }, [interval]);
}
