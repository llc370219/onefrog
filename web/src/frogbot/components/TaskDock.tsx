import { ChevronUp, Loader2, Pause, ShieldAlert } from 'lucide-react';
import { useState } from 'react';
import { terminal, type Task } from '../api';
import { ago, clock, elapsedOf, nowDoing, useNow } from '../lib/format';
import { QUIET_MS, statusLine, TaskButtons } from './WorkingCard';

export function jumpTo(id: string) {
  const el = document.getElementById(`task-${id}`);
  if (!el) return;
  el.scrollIntoView({ behavior: 'smooth', block: 'center' });
  el.classList.add('flash');
  setTimeout(() => el.classList.remove('flash'), 1200);
}

// 输入框上方常驻的"工作条"：只要有任务在跑/排队/暂停，就一直显示，让人一眼知道我还在干活。
// 点开是任务托盘：运行中、等你确认、已暂停/待办、排队中、最近完成。
export default function TaskDock({ tasks, pos }: { tasks: Task[]; pos: Record<string, number> }) {
  const [open, setOpen] = useState(false);
  const running = tasks.filter((t) => t.status === 'running');
  const awaiting = running.filter((t) => t.awaiting > 0);
  const queued = tasks.filter((t) => t.status === 'queued').sort((a, b) => a.prio - b.prio);
  const paused = tasks.filter((t) => t.status === 'paused');
  const live = running.length + queued.length > 0;
  const now = useNow(live || open);
  const done = tasks.filter((t) => terminal(t.status) && t.ended && now - t.ended < 30 * 60_000).sort((a, b) => (b.ended || 0) - (a.ended || 0)).slice(0, 3);
  if (!live && !paused.length) return null;

  const lead = awaiting[0] || [...running].sort((a, b) => b.lastEvent - a.lastEvent)[0];
  const quiet = lead && !lead.awaiting ? now - lead.lastEvent : 0;
  const counts = [
    running.length > 1 && `${running.length} 个在跑`,
    queued.length > 0 && `${queued.length} 个排队`,
    paused.length > 0 && `${paused.length} 个暂停/待办`,
  ].filter(Boolean).join(' · ');

  const group = (label: string, list: Task[]) => list.length > 0 && (
    <section>
      <h3 className="px-3 pb-1 pt-2.5 text-[11px] font-medium uppercase tracking-wide text-fg4">{label}</h3>
      <ul>
        {list.map((t) => {
          const sm = statusLine(t, now, pos[t.id]);
          return (
            <li key={t.id} className="flex items-center gap-2.5 rounded-xl px-3 py-2 hover:bg-s2/70">
              <span className={`shrink-0 ${sm.tone}`}>{sm.icon}</span>
              <button onClick={() => { jumpTo(t.id); setOpen(false); }} className="min-w-0 flex-1 text-left" aria-label={`查看任务「${t.title}」`}>
                <div className="truncate text-[13.5px] font-medium text-fg">{t.title}</div>
                <div className="truncate text-[12px] text-fg3">{sm.text}</div>
              </button>
              <div className="flex shrink-0 items-center gap-1"><TaskButtons t={t} pos={pos[t.id]} small /></div>
            </li>
          );
        })}
      </ul>
    </section>
  );

  return (
    <div className="mx-auto w-full max-w-3xl px-3 sm:px-4">
      {open && (
        <div className="rise mb-2 max-h-[45vh] overflow-y-auto rounded-2xl border border-line bg-s1 p-1.5 shadow-[0_12px_40px_-18px_var(--shadow)]" role="region" aria-label="任务托盘">
          {group('等你确认', awaiting)}
          {group('运行中', running.filter((t) => !t.awaiting))}
          {group('排队中', queued)}
          {group('已暂停 / 待办', paused)}
          {group('最近完成', done)}
        </div>
      )}
      <button onClick={() => setOpen(!open)} aria-expanded={open} aria-label="任务托盘"
        className={`mb-2 flex w-full items-center gap-2.5 rounded-full border px-3.5 py-1.5 text-left text-[12.5px] shadow-[0_2px_10px_-6px_var(--shadow)] transition ${awaiting.length ? 'border-warn/40 bg-warn-soft text-warn' : 'border-line bg-s1 text-fg2 hover:bg-s2'}`}>
        {awaiting.length ? <ShieldAlert size={14} className="shrink-0" />
          : lead ? <span className="working-orb shrink-0" aria-hidden />
          : queued.length ? <Loader2 size={14} className="shrink-0 animate-spin text-fg3" />
          : <Pause size={13} className="shrink-0 text-warn" />}
        <span className="min-w-0 flex-1 truncate" aria-live="polite">
          {lead ? <>
            <span className="font-medium text-fg">{lead.title}</span>
            <span className="text-fg3"> · {quiet >= QUIET_MS ? `仍在工作…（上次动作 ${ago(quiet)}）` : nowDoing(lead)}</span>
          </> : queued.length ? <span>{queued.length} 个任务排队，马上开始</span>
            : <span>{paused.length} 个任务已暂停或待办，随时可以继续</span>}
        </span>
        {lead && <span className="shrink-0 font-mono text-[11.5px] tabular-nums text-fg3">已做 {lead.steps.length} 步 · {clock(elapsedOf(lead, now))}</span>}
        {lead && counts && <span className="hidden shrink-0 text-fg3 sm:inline">· {counts}</span>}
        <ChevronUp size={15} className={`shrink-0 text-fg4 transition-transform ${open ? '' : 'rotate-180'}`} />
      </button>
    </div>
  );
}
