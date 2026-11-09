import { AlertTriangle, ArrowUpToLine, Check, ChevronDown, CircleSlash, FilePen, FileText, FolderOpen, Hourglass, Loader2, Pause, Play, RotateCcw, Search, ShieldAlert, Square, SquareTerminal, Wrench, X } from 'lucide-react';
import { useState, type ReactNode } from 'react';
import { api, type Step, type Task, type TaskAction } from '../api';
import { ago, describe, dur, elapsedOf, nowDoing, useNow } from '../lib/format';
import Artifact from './Artifact';
import { inline } from './Markdown';

const icons: Record<string, typeof Wrench> = { read_file: FileText, write_file: FilePen, edit_file: FilePen, list_dir: FolderOpen, grep: Search, shell: SquareTerminal };
export const QUIET_MS = 15_000; // 这么久没有新动作，就明确告诉用户"还在工作"

export function act(id: string, a: TaskAction) { api.task(id, a).catch((e) => alert((e as Error).message)); }

export function TaskButtons({ t, pos, small }: { t: Task; pos?: number; small?: boolean }) {
  const B = ({ a, label, icon, danger }: { a: TaskAction; label: string; icon: ReactNode; danger?: boolean }) => (
    <button onClick={(e) => { e.stopPropagation(); act(t.id, a); }} aria-label={label} title={label}
      className={`flex items-center gap-1 rounded-full border border-line2 ${small ? 'size-7 justify-center' : 'h-7 px-2.5'} text-[12px] text-fg2 transition hover:bg-s2 ${danger ? 'hover:border-danger/50 hover:text-danger' : 'hover:text-fg'}`}>
      {icon}{!small && <span>{label}</span>}
    </button>
  );
  const stop = <B a="cancel" label="停止" danger icon={<Square size={11} fill="currentColor" />} />;
  switch (t.status) {
    case 'running': return <>{!t.pausing && <B a="pause" label="暂停" icon={<Pause size={12} />} />}{stop}</>;
    case 'paused': return <><B a="resume" label={t.stop_reason === 'held' ? '开始' : '继续'} icon={<Play size={12} />} />{stop}</>;
    case 'queued': return <>{(pos ?? 0) > 1 && <B a="top" label="下一个做" icon={<ArrowUpToLine size={12} />} />}<B a="pause" label="挂起" icon={<Pause size={12} />} />{stop}</>;
    case 'failed': case 'cancelled': return <B a="retry" label="重试" icon={<RotateCcw size={12} />} />;
    default: return null;
  }
}

export function statusLine(t: Task, now: number, pos?: number): { icon: ReactNode; tone: string; text: string } {
  const el = dur(elapsedOf(t, now));
  switch (t.status) {
    case 'completed': return { icon: <Check size={15} />, tone: 'text-ok', text: `已完成 · 用时 ${el} · ${t.steps.length} 步${t.files.length ? ` · 改动 ${t.files.length} 个文件` : ''}` };
    case 'cancelled': return { icon: <CircleSlash size={15} />, tone: 'text-fg3', text: (t.stop_reason === 'stall' ? '长时间没有进展，已自动停止' : '已停止') + ` · ${t.steps.length} 步` };
    case 'failed': return { icon: <X size={15} />, tone: 'text-danger', text: `失败 · ${t.steps.length} 步 · ${el}` };
    case 'paused': {
      const why = t.stop_reason === 'held' ? '待办 · 还没开始，等你说开始' : t.stop_reason === 'restart' ? '服务重启后暂停 · 进度已保存' : t.stop_reason === 'chat' ? '先暂停，等聊完再继续 · 进度已保存' : '已暂停 · 进度已保存';
      return { icon: <Pause size={14} />, tone: 'text-warn', text: why + (t.steps.length ? ` · 已做 ${t.steps.length} 步` : '') };
    }
    case 'queued': return { icon: <Hourglass size={14} />, tone: 'text-fg3', text: pos ? `排队中 · 第 ${pos} 位，前面的做完就自动开始` : '马上开始…' };
    default: return { icon: <Loader2 size={15} className="animate-spin" />, tone: 'text-accent', text: `${nowDoing(t)} · 已做 ${t.steps.length} 步 · ${el}` };
  }
}

export default function WorkingCard({ task: t, pos, stallMs }: { task: Task; pos?: number; stallMs: number }) {
  const running = t.status === 'running';
  const live = running || t.status === 'queued';
  const [userOpen, setUserOpen] = useState<boolean | null>(null);
  const open = userOpen ?? running;
  const now = useNow(live);
  const sm = statusLine(t, now, pos);
  const quiet = running && t.awaiting === 0 ? now - t.lastEvent : 0;
  const result = (t.summary || t.error || t.output || '').trim();

  return (
    <div id={`task-${t.id}`} className={`rise overflow-hidden rounded-2xl border bg-s1 shadow-[0_1px_2px_var(--shadow)] ${t.awaiting ? 'border-warn/40' : 'border-line'}`}>
      <div className="flex items-center gap-3 px-4 py-3">
        <button onClick={() => setUserOpen(!open)} aria-expanded={open} aria-label={`${open ? '收起' : '展开'}任务「${t.title}」`} className="flex min-w-0 flex-1 items-center gap-3 text-left">
          <span className={`grid size-7 shrink-0 place-items-center rounded-full ${running ? 'bg-accent-soft' : t.status === 'paused' ? 'bg-warn-soft' : 'bg-s2'} ${sm.tone}`}>{sm.icon}</span>
          <div className="min-w-0 flex-1">
            <div className="flex items-baseline gap-2">
              <span className="truncate text-[14px] font-medium">{t.title}</span>
              {t.awaiting > 0 && <span className="flex shrink-0 items-center gap-1 rounded-full bg-warn-soft px-2 py-px text-[11px] font-medium text-warn"><ShieldAlert size={11} />等你确认</span>}
            </div>
            <div className={`truncate text-xs ${running ? 'text-fg2' : 'text-fg3'}`} aria-live="polite">{sm.text}</div>
          </div>
          <ChevronDown size={16} className={`shrink-0 text-fg4 transition-transform ${open ? 'rotate-180' : ''}`} />
        </button>
        <div className="flex shrink-0 items-center gap-1.5"><TaskButtons t={t} pos={pos} small /></div>
      </div>
      {running && <div className="progress-line" />}

      {running && quiet >= QUIET_MS && (
        quiet >= stallMs
          ? <div className="flex flex-wrap items-center gap-2 border-t border-line bg-warn-soft px-4 py-2.5 text-[12.5px] text-warn">
              <AlertTriangle size={14} className="shrink-0" />
              <span className="min-w-0 flex-1">已经 {ago(quiet).replace('前', '')}没有新进展了。可能在等一个很慢的命令，也可能卡住了。</span>
              <button onClick={() => act(t.id, 'cancel')} className="rounded-full border border-warn/40 px-2.5 py-0.5 hover:bg-s1">停止</button>
              <button onClick={async () => { await api.task(t.id, 'cancel').catch(() => {}); setTimeout(() => act(t.id, 'retry'), 400); }} className="rounded-full border border-warn/40 px-2.5 py-0.5 hover:bg-s1">停止并重试</button>
            </div>
          : <div className="flex items-center gap-2 border-t border-line px-4 py-2 text-[12.5px] text-fg3">
              <span className="typing scale-75"><i /><i /><i /></span>仍在工作…（上次动作 {ago(quiet)}，进程正常{now - t.lastBeat < 40_000 ? '' : '，等待响应'}）
            </div>
      )}

      {open && (
        <div className="border-t border-line px-4 py-3">
          {t.input && t.input !== t.title && <p className="mb-2.5 line-clamp-2 text-[12.5px] text-fg4">要求：{t.input}</p>}
          {t.steps.length === 0 && !t.text && <div className="flex items-center gap-2 text-[13px] text-fg3"><Hourglass size={14} />{t.status === 'queued' ? '等前面的任务做完…' : t.status === 'paused' ? '还没开始。' : '正在看要做什么…'}</div>}
          <ol className="space-y-2" aria-label="步骤">
            {t.steps.map((s, i) => <StepRow key={s.id + i} step={s} now={now} />)}
          </ol>
          {running && t.thinking && !t.text && <p className="mt-2.5 line-clamp-2 text-[12.5px] italic leading-relaxed text-fg4"><span className="shimmer-text not-italic">思考中</span> · {t.thinking.slice(-200)}</p>}
          {running && t.text && <p className="caret mt-2.5 whitespace-pre-wrap text-[13px] leading-relaxed text-fg2">{t.text.slice(-400)}</p>}
          {!live && t.error && <p className="mt-2.5 rounded-lg bg-danger-soft px-3 py-2 text-[13px] text-danger">{t.error}</p>}
          {!live && !t.error && result && t.status === 'completed' && <p className="mt-2.5 line-clamp-3 text-[13px] leading-relaxed text-fg3">{result.replace(/[*`#]/g, '')}</p>}
        </div>
      )}
      {t.files.length > 0 && !running && (
        <div className="flex flex-wrap gap-2 border-t border-line px-4 py-3">
          {t.files.map((f) => <Artifact key={f} path={f} compact={t.files.length > 2} />)}
        </div>
      )}
    </div>
  );
}

function StepRow({ step, now }: { step: Step; now: number }) {
  const [open, setOpen] = useState(false);
  const Icon = icons[step.tool] || Wrench;
  const d = describe(step.tool, step.args);
  const tone = step.denied ? 'text-danger' : step.error ? 'text-warn' : step.done ? 'text-fg4' : 'text-accent';
  return (
    <li className="text-[13px]">
      {step.thought && <p className="md mb-1 !text-[13px] !leading-relaxed text-fg2">{inline(step.thought)}</p>}
      <button onClick={() => step.output && setOpen(!open)} aria-expanded={open} className={`flex w-full items-center gap-2 text-left ${step.output ? '' : 'cursor-default'}`}>
        <Icon size={14} className={`shrink-0 ${tone}`} />
        <span className={`shrink-0 ${step.done ? 'text-fg3' : 'font-medium text-fg'}`}>{d.verb}</span>
        <code className="min-w-0 flex-1 truncate font-mono text-[12px] text-fg2">{d.target}</code>
        {!step.done ? <span className="flex shrink-0 items-center gap-1 text-[11px] text-accent">{now - step.at > 3000 && `${Math.round((now - step.at) / 1000)}s`}<Loader2 size={13} className="animate-spin" /></span>
          : step.denied ? <span className="shrink-0 text-[11px] text-danger">未批准</span>
          : step.error ? <span className="shrink-0 text-[11px] text-warn">出错</span>
          : <Check size={13} className="shrink-0 text-ok" />}
        {step.output && <ChevronDown size={13} className={`shrink-0 text-fg4 transition-transform ${open ? 'rotate-180' : ''}`} />}
      </button>
      {open && step.output && <pre className="mt-1.5 max-h-56 overflow-auto rounded-lg border border-line bg-code px-3 py-2 font-mono text-[12px] leading-relaxed whitespace-pre-wrap text-fg2">{step.output}</pre>}
    </li>
  );
}
