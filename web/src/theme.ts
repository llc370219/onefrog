import { useSyncExternalStore } from 'react';

/** What the user picked. "system" follows the OS light/dark setting. */
export type ThemePref = 'system' | 'light' | 'dark';
export type Theme = 'light' | 'dark';

// Keep in sync with public/assets/theme-init.js, which applies the choice before first paint.
const KEY = 'onefrog-theme';
const media = typeof window !== 'undefined' && window.matchMedia ? window.matchMedia('(prefers-color-scheme: dark)') : null;
const listeners = new Set<() => void>();

function readPref(): ThemePref {
  try {
    const v = localStorage.getItem(KEY);
    if (v === 'light' || v === 'dark') return v;
  } catch { /* storage blocked: fall back to the system setting */ }
  return 'system';
}

let pref: ThemePref = readPref();

function resolve(p: ThemePref): Theme {
  if (p !== 'system') return p;
  return media?.matches ? 'dark' : 'light';
}

let snapshot = { pref, theme: resolve(pref) };

function apply() {
  const root = document.documentElement;
  if (pref === 'system') delete root.dataset.theme;
  else root.dataset.theme = pref;
  snapshot = { pref, theme: resolve(pref) };
  const bg = getComputedStyle(root).getPropertyValue('--canvas').trim();
  document.querySelectorAll('meta[name="theme-color"]').forEach((m) => { if (bg) m.setAttribute('content', bg); });
  listeners.forEach((l) => l());
}

media?.addEventListener('change', () => { if (pref === 'system') apply(); });
// Sync data-theme and the browser chrome colour with the saved choice on load.
if (typeof document !== 'undefined') apply();

export function setThemePref(next: ThemePref) {
  pref = next;
  try {
    if (next === 'system') localStorage.removeItem(KEY);
    else localStorage.setItem(KEY, next);
  } catch { /* the choice still applies for this page view */ }
  apply();
}

function subscribe(l: () => void) {
  listeners.add(l);
  return () => { listeners.delete(l); };
}

/** Current preference and the theme actually shown. Re-renders when either changes. */
export function useTheme() {
  return useSyncExternalStore(subscribe, () => snapshot, () => snapshot);
}

/** Reads a CSS custom property from the active theme (for canvas drawing). */
export function cssVar(name: string, fallback: string) {
  if (typeof document === 'undefined') return fallback;
  return getComputedStyle(document.documentElement).getPropertyValue(name).trim() || fallback;
}
