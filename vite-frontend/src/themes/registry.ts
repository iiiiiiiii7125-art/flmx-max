/**
 * Theme Registry
 * ==============
 * Manages the set of installed themes and the currently active theme.
 * Handles CSS variable injection, `<style>` element management, and
 * lifecycle callbacks.
 *
 * This module is framework-agnostic (no React dependency).  The React
 * integration lives in `./context.tsx`.
 */

import type {
  ThemePackage,
  ThemeTokens,
  ComponentKey,
  LayoutKey,
  PageKey,
} from "./types";

// ─── internal state ──────────────────────────────────────────────────────────

const installed = new Map<string, ThemePackage>();
let activeId: string | null = null;
let injectedStyleEl: HTMLStyleElement | null = null;

const STORAGE_KEY = "flmx:active-theme";
const LEGACY_STORAGE_KEY = "flvx:active-theme";
const MODE_KEY = "flmx:theme"; // backwards-compat with old use-theme
const LEGACY_MODE_KEY = "flvx:theme";

// 改名迁移：新键无值时回退读旧键，并把值搬运到新键（一次性无感迁移）。
function readMigrated(key: string, legacyKey: string): string | null {
  try {
    const v = localStorage.getItem(key);

    if (v != null) return v;
    const legacy = localStorage.getItem(legacyKey);

    if (legacy != null) {
      localStorage.setItem(key, legacy);
      localStorage.removeItem(legacyKey);

      return legacy;
    }
  } catch {
    // storage unavailable (privacy mode etc.)
  }

  return null;
}

type ChangeListener = () => void;
const changeListeners = new Set<ChangeListener>();

function notify() {
  changeListeners.forEach((fn) => fn());
}

// ─── public API ──────────────────────────────────────────────────────────────

// ─── token vocabulary ────────────────────────────────────────────────────────

/**
 * The named token keys of the ThemeTokens contract (types.ts).  Only
 * an entry under one of these keys can ever take effect: a key
 * outside this set (a typo, a case variant — CSS custom properties
 * are case-sensitive — a bare name without the `--` prefix, a name
 * with a trailing space, a numeric key) names a property nothing in
 * the app consumes, so it must not count towards a usable light set.
 * The index-signature escape hatch in ThemeTokens lets authors stage
 * extra variables for their own `css`, but such entries alone do
 * not make a token set usable.  This list is pinned against types.ts
 * by check-theme-light-required.mjs — extend both together.
 */
export const THEME_TOKEN_KEYS: ReadonlySet<string> = new Set([
    "--background",
    "--foreground",
    "--border",
    "--input",
    "--ring",
    "--content1",
    "--divider",
    "--default-50",
    "--default-100",
    "--default-200",
    "--default-300",
    "--default-400",
    "--default-500",
    "--default-600",
    "--default-700",
    "--default-800",
    "--default-900",
    "--primary",
    "--primary-foreground",
    "--primary-50",
    "--primary-100",
    "--primary-200",
    "--primary-300",
    "--primary-400",
    "--primary-500",
    "--primary-600",
    "--primary-700",
    "--primary-800",
    "--primary-900",
    "--secondary",
    "--secondary-foreground",
    "--secondary-50",
    "--secondary-100",
    "--secondary-200",
    "--secondary-300",
    "--secondary-400",
    "--secondary-500",
    "--secondary-600",
    "--secondary-700",
    "--secondary-800",
    "--secondary-900",
    "--danger",
    "--danger-50",
    "--danger-100",
    "--danger-200",
    "--danger-300",
    "--danger-400",
    "--danger-500",
    "--danger-600",
    "--danger-700",
    "--danger-800",
    "--danger-900",
    "--success",
    "--success-50",
    "--success-100",
    "--success-200",
    "--success-300",
    "--success-400",
    "--success-500",
    "--success-600",
    "--success-700",
    "--success-800",
    "--success-900",
    "--warning",
    "--warning-50",
    "--warning-100",
    "--warning-200",
    "--warning-300",
    "--warning-400",
    "--warning-500",
    "--warning-600",
    "--warning-700",
    "--warning-800",
    "--warning-900",
    "--font-sans",
    "--font-mono",
    "--radius",
    "--radius-sm",
    "--radius-lg",
]);

/**
 * A token value is usable only when something remains after removing
 * whitespace and Unicode format characters (category Cf — zero-width
 * space, word joiner, soft hyphen, bidi controls, …).  A value made
 * solely of format characters survives `String.prototype.trim`
 * (they are not in its whitespace set) yet is not a valid token
 * value once substituted through var(), so it computes to nothing
 * at used-value time — the same "injects nothing" outcome as a
 * blank string.
 */
const isUsableTokenValue = (value: unknown): value is string =>
  typeof value === "string" && value.replace(/[\s\p{Cf}]/gu, "") !== "";

/**
 * `light` must be a key→value map: a plain object.  A string (whose
 * entries would enumerate single characters under numeric keys), a
 * boxed String object, an array, and every non-object form are
 * rejected as containers outright.
 */
const isTokenMap = (value: unknown): value is Record<string, unknown> =>
  typeof value === "object" &&
  value !== null &&
  !Array.isArray(value) &&
  !(value instanceof String);

/**
 * CSS-wide keywords (CSS Values and Units — the complete set,
 * matched case-insensitively as CSS keywords are).  A custom
 * property whose whole value is one of these computes to the
 * guaranteed-invalid value, so every var() consumer of the token
 * fails at computed-value time.  The value has real characters, so
 * the character layer above passes it, yet it injects nothing —
 * the same outcome as the blank / format-only forms.
 */
const CSS_WIDE_KEYWORDS: ReadonlySet<string> = new Set([
  "initial",
  "inherit",
  "unset",
  "revert",
  "revert-layer",
]);

/**
 * Decode CSS escapes in an identifier: a var() reference may spell
 * a property name with escapes (`--prim\ary`), while the token
 * map's keys are already literal — decode before comparing.
 */
const unescapeCssIdent = (raw: string): string =>
  raw.replace(
    /\\([0-9a-fA-F]{1,6})\s?|\\([\s\S])/g,
    (_m, hex: string | undefined, ch: string | undefined) =>
      hex !== undefined ? String.fromCodePoint(parseInt(hex, 16)) : (ch ?? ""),
  );

/**
 * Extract the property name of every var() reference in a token
 * value, including references nested inside fallbacks — the
 * dependency graph is formed before fallback substitution, so a
 * nested reference is still a dependency.  A `var(` not followed
 * by a custom property name is not a reference and is ignored.
 */
const extractVarReferenceNames = (value: string): string[] => {
  const names: string[] = [];
  const lower = value.toLowerCase();
  let i = 0;

  for (;;) {
    const start = lower.indexOf("var(", i);

    if (start === -1) break;
    let j = start + 4;

    while (j < value.length && /\s/.test(value[j])) j++;
    const nameMatch = /^--(?:\\[\s\S]|[^\s(),;])+/.exec(value.slice(j));

    if (nameMatch) names.push(unescapeCssIdent(nameMatch[0]));
    i = start + 4;
  }

  return names;
};

/**
 * The single availability judgement for a light token set, shared
 * by both consumers so the two can never drift apart (R9-12):
 * validateThemePackage counts the entries in this set (a set with
 * no available entry is no light set at all), and injectTokens
 * sets exactly the entries in this set (an unavailable entry is
 * skipped, so it can no longer overwrite the built-in :root
 * fallback with a value that computes to nothing).  An entry is
 * available when its key is a named ThemeTokens token and its
 * value can actually produce a token stream at computed-value
 * time (R9-28 / R9-29):
 *   - character layer (isUsableTokenValue): a string with real
 *     content after removing whitespace and format characters;
 *   - keyword layer: the whole value is not a CSS-wide keyword
 *     (guaranteed-invalid once substituted);
 *   - reference layer: the value does not sit on a var()
 *     dependency cycle within this set — a self-reference or a
 *     mutual reference chain computes every member of the cycle
 *     to the guaranteed-invalid value.
 * References that leave the set do not taint a value: they
 * resolve against the app defaults, and an entry this set skips
 * leaves its default in place, so merely depending on an
 * unavailable entry is survivable (a fallback, or the default
 * itself, still produces a value) — only a cycle, whose members
 * can resolve only through each other, cannot.
 */
const computeUsableTokenKeys = (
  light: Record<string, unknown>,
): Set<string> => {
  const keys = Object.keys(light);
  const inSet = new Set(keys);
  const contentOk = new Map<string, boolean>();
  const refsByKey = new Map<string, string[]>();

  for (const key of keys) {
    const value = light[key];
    let ok = isUsableTokenValue(value);

    if (ok) {
      const whole = (value as string)
        .replace(/[\s\p{Cf}]/gu, "")
        .toLowerCase();

      if (CSS_WIDE_KEYWORDS.has(whole)) ok = false;
    }
    contentOk.set(key, ok);
    refsByKey.set(
      key,
      typeof value === "string" ? extractVarReferenceNames(value) : [],
    );
  }

  // Dependency cycles within the set: a key is cyclic when it can
  // reach itself by following var() references to other keys of
  // the same set.
  const cyclic = new Set<string>();

  for (const key of keys) {
    const seen = new Set<string>();
    const stack = (refsByKey.get(key) ?? []).filter((name) =>
      inSet.has(name),
    );

    while (stack.length > 0) {
      const cur = stack.pop() as string;

      if (cur === key) {
        cyclic.add(key);
        break;
      }
      if (seen.has(cur)) continue;
      seen.add(cur);
      for (const name of refsByKey.get(cur) ?? []) {
        if (inSet.has(name)) stack.push(name);
      }
    }
  }

  const usable = new Set<string>();

  for (const key of keys) {
    if (
      THEME_TOKEN_KEYS.has(key) &&
      contentOk.get(key) === true &&
      !cyclic.has(key)
    ) {
      usable.add(key);
    }
  }

  return usable;
};

/**
 * Validate a theme package against the format contract.  Returns a
 * human-readable rejection reason, or null when the package is valid.
 *
 * LIGHT-ONLY contract: dark mode was removed app-wide, so a package
 * that provides `tokens` MUST provide a usable `light` token set.  A
 * dark-only package is rejected outright — there is no dark fallback
 * to read colours from, and silently injecting nothing while
 * pretending activation succeeded would hide the mistake.  "Usable"
 * is a conjunction of three axes, all judged against what injection
 * would actually put to work:
 *   - container: `light` itself is a plain key→value object (not an
 *     array, not a string or boxed String, not any non-object);
 *   - key: the entry's key is a named ThemeTokens token (see
 *     THEME_TOKEN_KEYS above) — out-of-vocabulary keys inject
 *     properties nothing consumes;
 *   - value: the value is a string with real content — non-blank
 *     after removing whitespace and Unicode format characters.
 * A light set with no entry passing all three axes counts as no
 * light set at all and is rejected the same way: that covers a
 * present-but-empty set (`light: {}`), all-`undefined` values
 * (injectTokens skips undefined values, so it would inject
 * nothing), empty/whitespace/format-only strings
 * (`setProperty(name, "")` removes the property, and a blank value
 * computes to nothing after var() substitution), runtime values
 * outside the type contract (null, numbers, booleans, objects),
 * sets whose keys are all outside the token vocabulary, and
 * non-map `light` values.  A light set that mixes unusable entries
 * with real values is a valid partial set — the unusable entries
 * are skipped and the rest fall back as usual.  A package without
 * `tokens` at all (component / layout / page overrides only) is
 * fine: the `tokens` field itself stays optional.
 */
export function validateThemePackage(pkg: ThemePackage): string | null {
  if (pkg.tokens != null) {
    const light: unknown = pkg.tokens.light;
    const usableCount = isTokenMap(light)
      ? computeUsableTokenKeys(light).size
      : 0;

    if (usableCount === 0) {
      return (
        `theme "${pkg.id}" provides tokens but no usable light token set ` +
        `(tokens.light must be a plain object defining at least one known ` +
        `token — a ThemeTokens key from types.ts — with a non-empty string ` +
        `value; missing, empty, all-undefined, blank- or format-only-valued, ` +
        `non-string-valued, unknown-key-only, and non-object light sets are ` +
        `not accepted)`
      );
    }
  }

  return null;
}

/**
 * Register a theme package.  Call this for every theme you want available.
 * Registering a theme with an existing id replaces the previous one.
 * Packages that fail validateThemePackage are rejected: they are not
 * registered (so they never appear in the picker and can never be
 * activated), the rejection is logged with the reason, and any
 * previously registered theme with the same id stays in place.
 */
export function registerTheme(pkg: ThemePackage): void {
  const problem = validateThemePackage(pkg);

  if (problem != null) {
    console.error(`[flmx-theme] registerTheme rejected: ${problem}`);

    return;
  }

  installed.set(pkg.id, pkg);
  notify();
}

/** Unregister a theme by id. */
export function unregisterTheme(id: string): void {
  if (activeId === id) deactivateTheme();
  installed.delete(id);
  notify();
}

/** Get all registered themes. */
export function getRegisteredThemes(): ThemePackage[] {
  return Array.from(installed.values());
}

/** Get a specific theme by id. */
export function getTheme(id: string): ThemePackage | undefined {
  return installed.get(id);
}

/** Get the id of the currently active theme (or null). */
export function getActiveThemeId(): string | null {
  return activeId;
}

/** Get the currently active ThemePackage (or null). */
export function getActiveTheme(): ThemePackage | null {
  return activeId ? (installed.get(activeId) ?? null) : null;
}

// ─── theme mode ──────────────────────────────────────────────────────────────

export type ThemeMode = "light" | "dark" | "system";

export function resolveSystemMode(): "light" | "dark" {
  // LIGHT-ONLY: dark mode was removed (2026-11-02, user directive — one
  // light mode for the whole app). The system preference is never
  // consulted; this stays exported only for API compatibility.
  return "light";
}

export function getSavedMode(): ThemeMode {
  if (typeof window === "undefined") return "system";
  const raw = readMigrated(MODE_KEY, LEGACY_MODE_KEY);

  if (raw === "light" || raw === "dark" || raw === "system") return raw;

  return "system";
}

export function saveMode(mode: ThemeMode): void {
  localStorage.setItem(MODE_KEY, mode);
  notify();
}

export function getEffectiveMode(): "light" | "dark" {
  // LIGHT-ONLY: pinned to "light" regardless of any stored preference
  // (a legacy "dark"/"system" value in localStorage must never render
  // dark again) and regardless of the OS setting.
  return "light";
}

// ─── activation ──────────────────────────────────────────────────────────────

/**
 * Activate a theme by id.  This:
 * 1. Calls `onDeactivate` on the previous theme.
 * 2. Injects CSS tokens onto `document.documentElement`.
 * 3. Injects the theme's `css` string into a `<style>` element.
 * 4. Calls `onActivate` on the new theme.
 * 5. Persists the choice to localStorage.
 */
export function activateTheme(id: string): void {
  const pkg = installed.get(id);

  if (!pkg) {
    return;
  }

  // Deactivate previous
  deactivateTheme();

  activeId = id;

  // Inject tokens (LIGHT-ONLY: always the light token set)
  const tokens = pkg.tokens?.light;

  if (tokens) injectTokens(tokens);

  // Inject custom CSS
  if (pkg.css) {
    injectedStyleEl = document.createElement("style");
    injectedStyleEl.setAttribute("data-flmx-theme", id);
    injectedStyleEl.textContent = pkg.css;
    document.head.appendChild(injectedStyleEl);
  }

  // LIGHT-ONLY: the app never carries the dark class
  const root = document.documentElement;

  root.classList.remove("dark");
  root.style.colorScheme = "light";

  // Lifecycle
  pkg.onActivate?.();

  // Choke point: a theme package's lifecycle hook must not be able to
  // re-enable dark (the bundled cyberpunk example once forced it).
  root.classList.remove("dark");
  root.style.colorScheme = "light";

  // Persist
  localStorage.setItem(STORAGE_KEY, id);

  notify();
}

/** Deactivate the current theme, reverting all overrides. */
export function deactivateTheme(): void {
  const prev = activeId ? installed.get(activeId) : null;

  prev?.onDeactivate?.();

  // Remove injected tokens
  clearInjectedTokens();

  // Remove injected style element
  if (injectedStyleEl) {
    injectedStyleEl.remove();
    injectedStyleEl = null;
  }

  activeId = null;
}

/** Re-apply the active theme (e.g. after mode changes). */
export function reapplyActiveTheme(): void {
  if (activeId) {
    const id = activeId;
    // quick re-inject without full lifecycle
    const pkg = installed.get(id);

    if (!pkg) return;

    clearInjectedTokens();
    const tokens = pkg.tokens?.light;

    if (tokens) injectTokens(tokens);

    const root = document.documentElement;

    root.classList.remove("dark");
    root.style.colorScheme = "light";
  } else {
    // No theme active — LIGHT-ONLY: ensure the dark class stays off
    const root = document.documentElement;

    root.classList.remove("dark");
    root.style.colorScheme = "light";
  }
  notify();
}

// ─── component resolution ────────────────────────────────────────────────────

/**
 * Resolve a component: returns the themed override if present, otherwise
 * returns the fallback (default implementation).
 */
export function resolveComponent<P = any>(
  key: ComponentKey,
  fallback: React.ComponentType<P>,
): React.ComponentType<P> {
  const pkg = activeId ? installed.get(activeId) : null;
  const override = pkg?.components?.[key];

  return (override as React.ComponentType<P>) ?? fallback;
}

/** Resolve a layout override. */
export function resolveLayout(
  key: LayoutKey,
  fallback: React.ComponentType<{ children: React.ReactNode }>,
): React.ComponentType<{ children: React.ReactNode }> {
  const pkg = activeId ? installed.get(activeId) : null;

  return pkg?.layouts?.[key] ?? fallback;
}

/** Resolve a page override. */
export function resolvePage<P = any>(
  key: PageKey,
  fallback: React.ComponentType<P>,
): React.ComponentType<P> {
  const pkg = activeId ? installed.get(activeId) : null;
  const override = pkg?.pages?.[key];

  return (override as React.ComponentType<P>) ?? fallback;
}

// ─── subscriber API (for React) ──────────────────────────────────────────────

export function subscribe(listener: ChangeListener): () => void {
  changeListeners.add(listener);

  return () => changeListeners.delete(listener);
}

// ─── initialisation ──────────────────────────────────────────────────────────

/**
 * Call once at app boot.  Restores the previously active theme from
 * localStorage (if the theme is registered).
 */
export function initThemeSystem(): void {
  const savedId = readMigrated(STORAGE_KEY, LEGACY_STORAGE_KEY);

  if (savedId && installed.has(savedId)) {
    activateTheme(savedId);
  } else {
    // Just apply mode
    reapplyActiveTheme();
  }
}

// ─── internal helpers ────────────────────────────────────────────────────────

const injectedVars: string[] = [];

function injectTokens(tokens: ThemeTokens): void {
  const root = document.documentElement;
  // Injection applies the same availability judgement as
  // validation (computeUsableTokenKeys): unavailable entries are
  // skipped, never set — setting one would overwrite the built-in
  // :root fallback with a value that computes to nothing.
  const usable = computeUsableTokenKeys(tokens as Record<string, unknown>);

  for (const [varName, value] of Object.entries(tokens)) {
    if (value !== undefined && usable.has(varName)) {
      root.style.setProperty(varName, value);
      injectedVars.push(varName);
    }
  }
}

function clearInjectedTokens(): void {
  const root = document.documentElement;

  for (const varName of injectedVars) {
    root.style.removeProperty(varName);
  }
  injectedVars.length = 0;
}
