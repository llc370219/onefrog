import { CalendarClock, Pause, Play, Trash2, Zap } from 'lucide-react';
import { useEffect, useState, type ReactNode } from 'react';
import { api, type Schedule } from '../api';

// 定时计划托盘：只显示“未来会跑”的计划，不动画成“正在工作”。
export default function ScheduleDock({ schedules, onChange, onError }: {
  schedules: Schedule[];
  onChange: (list: Schedule[]) => void;
  onError: (e: string) => void;
}) {
  const [open, setOpen] = useState(false);
  const enabled = schedules.filter((s) => s.enabled);
  const paused = schedules.filter((s) => !s.enabled);
  if (schedules.length === 0) return null;

  const act = async (id: string, action: 'pause' | 'resume' | 'run' | 'delete') => {
    try {
      if (action === 'delete' && !confirm('删除这个定时计划？')) return;
      await api.schedule(id, action);
      onChange(await api.schedules());
    } catch (e) {
      onError((e as Error).message);
    }
  };

  const row = (s: Schedule) => (
    <li key={s.id} className="flex items-center gap-2.5 rounded-xl px-3 py-2 hover:bg-s2/70">
      <CalendarClock size={14} className={`shrink-0 ${s.enabled ? 'text-accent' : 'text-fg4'}`} />
      <div className="min-w-0 flex-1">
        <div className="truncate text-[13.5px] font-medium text-fg">{s.title}</div>
        <div className="truncate text-[12px] text-fg3">
          {s.enabled
            ? (s.next_run_local ? `下次 ${s.next_run_local}` : '没有下次触发')
            : '已暂停'}
          {s.kind ? ` · ${kindCN(s)}` : ''}
        </div>
      </div>
      <div className="flex shrink-0 items-center gap-1">
        {s.enabled
          ? <IconBtn label="暂停" onClick={() => act(s.id, 'pause')}><Pause size={13} /></IconBtn>
          : <IconBtn label="恢复" onClick={() => act(s.id, 'resume')}><Play size={13} /></IconBtn>}
        <IconBtn label="立刻执行一次" onClick={() => act(s.id, 'run')}><Zap size={13} /></IconBtn>
        <IconBtn label="删除" onClick={() => act(s.id, 'delete')}><Trash2 size={13} /></IconBtn>
      </div>
    </li>
  );

  return (
    <div className="mx-auto w-full max-w-3xl px-3 sm:px-4">
      {open && (
        <div className="rise mb-2 max-h-[40vh] overflow-y-auto rounded-2xl border border-line bg-s1 p-1.5 shadow-[0_12px_40px_-18px_var(--shadow)]" role="region" aria-label="定时计划">
          {enabled.length > 0 && (
            <section>
              <h3 className="px-3 pb-1 pt-2.5 text-[11px] font-medium uppercase tracking-wide text-fg4">启用中</h3>
              <ul>{enabled.map(row)}</ul>
            </section>
          )}
          {paused.length > 0 && (
            <section>
              <h3 className="px-3 pb-1 pt-2.5 text-[11px] font-medium uppercase tracking-wide text-fg4">已暂停</h3>
              <ul>{paused.map(row)}</ul>
            </section>
          )}
        </div>
      )}
      <button onClick={() => setOpen(!open)} aria-expanded={open} aria-label="定时计划"
        className="mb-2 flex w-full items-center gap-2.5 rounded-full border border-line bg-s1 px-3.5 py-1.5 text-left text-[12.5px] text-fg2 shadow-[0_2px_10px_-6px_var(--shadow)] transition hover:bg-s2">
        <CalendarClock size={14} className="shrink-0 text-accent" />
        <span className="min-w-0 flex-1 truncate">
          {enabled.length
            ? `${enabled.length} 个定时计划 · 最近 ${nearest(enabled)}`
            : `${paused.length} 个定时计划已暂停`}
        </span>
        <span className="text-fg4">{open ? '收起' : '展开'}</span>
      </button>
    </div>
  );
}

function IconBtn({ label, onClick, children }: { label: string; onClick: () => void; children: ReactNode }) {
  return (
    <button type="button" title={label} aria-label={label} onClick={onClick}
      className="rounded-lg p-1.5 text-fg3 hover:bg-s2 hover:text-fg">{children}</button>
  );
}

function kindCN(s: Schedule) {
  if (s.kind === 'once') return `单次 · ${s.timezone || 'Asia/Shanghai'}`;
  if (s.kind === 'daily') return `每天 ${pad(s.hour)}:${pad(s.minute)} · ${s.timezone || 'Asia/Shanghai'}`;
  if (s.kind === 'weekly') return `每周 · ${pad(s.hour)}:${pad(s.minute)} · ${s.timezone || 'Asia/Shanghai'}`;
  return s.kind;
}
function pad(n?: number) { return String(n ?? 0).padStart(2, '0'); }
function nearest(list: Schedule[]) {
  const withNext = list.filter((s) => s.next_run_local);
  if (!withNext.length) return '无下次';
  withNext.sort((a, b) => (a.next_run || '').localeCompare(b.next_run || ''));
  return withNext[0].next_run_local!;
}

// 首次挂载时拉一次列表（SSE 事件会增量更新）
export function useSchedules(ready: boolean) {
  const [list, setList] = useState<Schedule[]>([]);
  useEffect(() => {
    if (!ready) return;
    api.schedules().then(setList).catch(() => {});
  }, [ready]);
  return [list, setList] as const;
}
