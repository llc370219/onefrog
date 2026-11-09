import { Bell, BellOff, Monitor, Moon, Sun } from 'lucide-react';
import { useState } from 'react';

type Pref = 'system' | 'light' | 'dark';
const KEY = 'frogbot-theme';

function applyTheme(p: Pref) {
  const root = document.documentElement;
  if (p === 'system') {
    delete root.dataset.theme;
    localStorage.removeItem(KEY);
  } else {
    root.dataset.theme = p;
    localStorage.setItem(KEY, p);
  }
}

export default function TopActions({
  notify,
  onNotify,
}: {
  notify: boolean;
  onNotify: () => void;
}) {
  const [pref, setPref] = useState<Pref>(() => (localStorage.getItem(KEY) as Pref) || 'system');
  const next: Record<Pref, Pref> = { system: 'light', light: 'dark', dark: 'system' };
  const Icon = pref === 'light' ? Sun : pref === 'dark' ? Moon : Monitor;

  return (
    <div className="fixed top-3 right-4 z-40 flex items-center gap-1.5 rounded-full border border-line bg-canvas/80 px-2 py-1 shadow-xs backdrop-blur-md">
      <button
        onClick={onNotify}
        className="grid size-8 place-items-center rounded-full text-fg3 transition hover:bg-s2 hover:text-fg"
        title={notify ? '通知已开启（点击关闭）' : '开启系统通知'}
        aria-label={notify ? '关闭系统通知' : '开启系统通知'}
        aria-pressed={notify}
      >
        {notify ? <Bell size={16} /> : <BellOff size={16} />}
      </button>
      <button
        onClick={() => {
          const p = next[pref];
          setPref(p);
          applyTheme(p);
        }}
        className="grid size-8 place-items-center rounded-full text-fg3 transition hover:bg-s2 hover:text-fg"
        title={{ system: '跟随系统', light: '浅色', dark: '深色' }[pref]}
        aria-label="切换黑白主题"
      >
        <Icon size={16} />
      </button>
    </div>
  );
}
