import { Monitor, Moon, Sun } from 'lucide-react';
import { setThemePref, useTheme, type ThemePref } from '../theme';

const ORDER: ThemePref[] = ['system', 'light', 'dark'];
const LABEL: Record<ThemePref, string> = { system: '跟随系统', light: '浅色', dark: '深色' };
const ICON = { system: Monitor, light: Sun, dark: Moon };

/** Icon button that cycles 跟随系统 → 浅色 → 深色. */
export function ThemeToggle({ className = '' }: { className?: string }) {
  const { pref } = useTheme();
  const next = ORDER[(ORDER.indexOf(pref) + 1) % ORDER.length];
  const Icon = ICON[pref];
  const label = `外观：${LABEL[pref]}（点击切换为${LABEL[next]}）`;
  return <button type="button" className={`icon-btn theme-toggle ${className}`} title={label} aria-label={label} onClick={() => setThemePref(next)}><Icon size={15}/></button>;
}

/** Three-way segmented picker for settings / account screens. */
export function ThemePicker() {
  const { pref } = useTheme();
  return <div className="theme-picker" role="radiogroup" aria-label="外观">
    {ORDER.map((p) => { const Icon = ICON[p]; return <button key={p} type="button" role="radio" aria-checked={pref === p} className={pref === p ? 'on' : ''} onClick={() => setThemePref(p)}><Icon size={14}/>{LABEL[p]}</button>; })}
  </div>;
}
