import * as React from "react";
import { useHref, useNavigate } from "react-router-dom";
import { Toaster } from "react-hot-toast";
import { I18nProvider } from "@react-aria/i18n";

import { HeroUIProvider } from "@/shadcn-bridge/heroui/system";
import { ThemeProvider } from "@/components/theme-provider";

export interface ProvidersProps {
  children: React.ReactNode;
}

// Shared toast surface for every toast type: the per-type style in
// react-hot-toast replaces rather than merges, so each type spreads this.
// Matte glass (2026-11-04): --toaster-bg is the surface-tier glass
// gradient (stops 0.85 -> 0.75 -> 0.70) and --toaster-color the
// near-black foreground, so toasts follow the site-wide matte-glass
// recipe like every floating surface, with the surface-tier blur.
const toastSurfaceStyle = {
  background: "var(--toaster-bg)",
  color: "var(--toaster-color)",
  border: "1px solid var(--toaster-border)",
  borderRadius: 12,
  boxShadow: "0 6px 20px rgb(24 24 27 / 0.08)",
  backdropFilter: "blur(18px) saturate(150%)",
  WebkitBackdropFilter: "blur(18px) saturate(150%)",
} as const;

export function Provider({ children }: ProvidersProps) {
  const navigate = useNavigate();

  return (
    <I18nProvider locale="zh-CN">
      <HeroUIProvider navigate={navigate} useHref={useHref}>
        <ThemeProvider>
          {children}
          <Toaster
            position="top-center"
            toastOptions={{
              duration: 2000,
              style: { ...toastSurfaceStyle },
              success: {
                duration: 2000,
                style: { ...toastSurfaceStyle },
              },
              error: {
                // P2-3：错误常含长 URL/堆栈，2 秒看不全且不折行会被截断；
                // 延长到 8 秒并允许长串断行、限制最大宽度。
                duration: 8000,
                style: {
                  ...toastSurfaceStyle,
                  maxWidth: "90vw",
                  wordBreak: "break-all",
                },
              },
            }}
          />
        </ThemeProvider>
      </HeroUIProvider>
    </I18nProvider>
  );
}
