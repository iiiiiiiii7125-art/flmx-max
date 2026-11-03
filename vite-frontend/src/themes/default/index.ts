/**
 * Default Theme
 * =============
 * This is the built-in "stock" FLMX theme.  It doesn't override any
 * components — it only declares the CSS tokens that match the colours
 * already defined in `globals.css`.  This serves as the **reference
 * implementation** that theme authors can copy and modify.
 *
 * When this theme is active, the frontend looks identical to an
 * unmodified FLMX install.
 */

import type { ThemePackage } from "../types";

const defaultTheme: ThemePackage = {
  id: "default",
  name: "默认主题",
  author: "FLMX Team",
  version: "1.0.0",
  description: "FLMX 内置默认主题，与全站色板一致",

  tokens: {
    light: {
      "--background": "#fafafa",
      "--foreground": "#18181b",
      "--border": "#e4e4e7",
      "--input": "#d4d4d8",
      "--ring": "#a5b4fc",
      "--content1": "#ffffff",
      "--divider": "#e4e4e7",

      "--default-50": "#fafafa",
      "--default-100": "#f4f4f5",
      "--default-200": "#e4e4e7",
      "--default-300": "#d4d4d8",
      "--default-400": "#a1a1aa",
      "--default-500": "#71717a",
      "--default-600": "#52525b",
      "--default-700": "#3f3f46",
      "--default-800": "#27272a",
      "--default-900": "#18181b",

      "--primary": "#4f46e5",
      "--primary-foreground": "#ffffff",
      "--primary-50": "#eef2ff",
      "--primary-100": "#e0e7ff",
      "--primary-200": "#c7d2fe",
      "--primary-300": "#a5b4fc",
      "--primary-400": "#818cf8",
      "--primary-500": "#6366f1",
      "--primary-600": "#4f46e5",
      "--primary-700": "#4338ca",
      "--primary-800": "#3730a3",
      "--primary-900": "#312e81",

      "--secondary": "#7c3aed",
      "--secondary-foreground": "#ffffff",
      "--secondary-50": "#f5f3ff",
      "--secondary-100": "#ede9fe",
      "--secondary-200": "#ddd6fe",
      "--secondary-300": "#c4b5fd",
      "--secondary-400": "#a78bfa",
      "--secondary-500": "#8b5cf6",
      "--secondary-600": "#7c3aed",
      "--secondary-700": "#6d28d9",
      "--secondary-800": "#5b21b6",
      "--secondary-900": "#4c1d95",

      "--danger": "#dc2626",
      "--danger-50": "#fef2f2",
      "--danger-100": "#fee2e2",
      "--danger-200": "#fecaca",
      "--danger-300": "#fca5a5",
      "--danger-400": "#f87171",
      "--danger-500": "#ef4444",
      "--danger-600": "#dc2626",
      "--danger-700": "#b91c1c",
      "--danger-800": "#991b1b",
      "--danger-900": "#7f1d1d",

      "--success": "#16a34a",
      "--success-50": "#f0fdf4",
      "--success-100": "#dcfce7",
      "--success-200": "#bbf7d0",
      "--success-300": "#86efac",
      "--success-400": "#4ade80",
      "--success-500": "#22c55e",
      "--success-600": "#16a34a",
      "--success-700": "#15803d",
      "--success-800": "#166534",
      "--success-900": "#14532d",

      "--warning": "#d97706",
      "--warning-50": "#fffbeb",
      "--warning-100": "#fef3c7",
      "--warning-200": "#fde68a",
      "--warning-300": "#fcd34d",
      "--warning-400": "#fbbf24",
      "--warning-500": "#f59e0b",
      "--warning-600": "#d97706",
      "--warning-700": "#b45309",
      "--warning-800": "#92400e",
      "--warning-900": "#78350f",
    },
  },

  // No component/layout/page overrides — uses all defaults.
};

export default defaultTheme;
