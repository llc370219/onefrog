import { useEffect, useState } from 'react';
import {
  Brain, ChevronRight, ChevronUp, CircleCheck, CircleAlert, CircleDashed,
  FileCode2, Globe, Pause, Play, Search, Server, Square,
  Terminal, User, Wrench, LoaderCircle,
} from 'lucide-react';
import { taskIsActive, type TaskActivity, type TaskView } from '../api';
import { Button } from './ui';
import {
  activityGlyph, activityLabel, elapsedAnchor,
  activityRender, activityRowTime, currentActivityText, elapsedRunning,
  formatElapsed, planProgressLabel, planStages, recentActivities, taskBusyId,
  type ActivityGlyph,
} from '../lib/taskCardFormat';

const glyphs: Record<ActivityGlyph, typeof Wrench> = {
  terminal: Terminal, file: FileCode2, search: Search, server: Server,
  network: Globe, brain: Brain, user: User, tool: Wrench,
};

/** Small status icon left of the activity line. */
function StatusIcon({ status }: { status: string }) {
  if (status === 'running' || status === 'queued') return <LoaderCircle size={14} className="spin"/>;
  if (status === 'completed') return <CircleCheck size={14}/>;
  if (status === 'failed') return <CircleAlert size={14}/>;
  if (status === 'paused') return <Pause size={14}/>;
  if (status === 'cancelled' || status === 'budget_exhausted') return <Square size={12}/>;
  return <CircleDashed size={14}/>;
}

/**
 * Segmented plan progress, matching the reference: one thin horizontal row of
 * N equal-width segments (completed prefix blue, pending dim). The single
 * 第 X 步 / 共 N 步 label is rendered by the caller at the right of the
 * model/workspace row directly above the strip — this component draws only
 * segments. No digits, chips or step titles on the strip; titles live in
 * segment tooltips and the expanded details. Fully static, no animation.
 */
function PlanStrip({ stages, missing }: { stages: ReturnType<typeof planStages>; missing: boolean }) {
  if (!stages.length) {
    // An active task without a plan says so honestly; finished tasks stay quiet.
    return missing ? <p className="task-live-plan-empty">尚未生成计划</p> : null;
  }
  const completed = stages.filter(s => s.done).length;
  return (
    <div
      className="task-live-plan"
      role="group"
      aria-label={`计划进度：${planProgressLabel(stages)}，已完成 ${completed} 步`}
    >
      <div className="task-live-plan-strip">
        {stages.map(s => (
          <i
            key={s.index}
            role="img"
            className={s.done ? 'done' : ''}
            aria-label={`第 ${s.index} 步${s.done ? '，已完成' : '，未完成'}：${s.text}`}
            title={`第 ${s.index} 步${s.done ? '（已完成）' : ''}：${s.text}`}
          />
        ))}
      </div>
    </div>
  );
}

/** One recent tool activity row: glyph, safe label, actual duration. */
function ActivityRow({ a, parentStatus }: { a: TaskActivity; parentStatus: string }) {
  const Icon = glyphs[activityGlyph(a.name)];
  const running = a.status === 'running';
  const interrupted = a.status === 'interrupted';
  const render = activityRender(parentStatus);
  // The parent's status, not the row's, decides whether a spinner may run.
  const spinning = running && render.kind === 'active';
  const time = activityRowTime(a, render);
  return (
    <li className={`task-activity ${a.status}`}>
      <span className="task-activity-icon" aria-hidden="true">{spinning ? <LoaderCircle size={12} className="spin"/> : interrupted ? <CircleAlert size={12}/> : <Icon size={12}/>}</span>
      <span className="task-activity-label" title={activityLabel(a)}>{activityLabel(a)}</span>
      <span className="task-activity-time" title={time}>{time}</span>
    </li>
  );
}

/**
 * Compact live status card for one durable task. Matches the reference: tight
 * bordered panel, status icon + current activity + elapsed on the first row,
 * provider/model + plan stage on the second, latest activities, and a footer
 * with pause/stop and the expandable details. Everything renders from real
 * TaskView fields; the additive live fields are optional and the card falls
 * back cleanly on an older backend.
 */
export default function TaskLiveCard({
  task,
  modelLabel,
  busy,
  onAction,
  onGuide,
  onApproval,
  onCollapse,
  children,
}: {
  task: TaskView;
  /** Resolved provider/model display name, when the caller knows it. */
  modelLabel?: string;
  busy?: string | string[];
  onAction: (id: string, action: 'pause' | 'resume' | 'cancel') => void;
  onGuide?: (id: string, content: string) => void;
  onApproval?: (taskID: string, approvalID: string, allow: boolean) => void;
  onCollapse?: () => void;
  /** Expanded body (plan, progress, approvals, result) from the host panel. */
  children?: React.ReactNode;
}) {
  const [open, setOpen] = useState(false);
  const [guide, setGuide] = useState('');
  const [now, setNow] = useState(() => Date.now());

  // A 1s clock only while the task can still progress; paused/terminal cards
  // render once and never leave a timer behind. `now` also never feeds SSR or
  // a stale render — the initial state is a real timestamp, not 0.
  const ticking = elapsedRunning(task.status);
  useEffect(() => {
    if (!ticking) return;
    const handle = window.setInterval(() => setNow(Date.now()), 1000);
    return () => window.clearInterval(handle);
  }, [ticking]);

  const anchor = elapsedAnchor(task);
  const elapsed = formatElapsed(task.started_at, now, anchor);
  const paused = task.status === 'paused';
  const active = taskIsActive(task);
  const activities = recentActivities(task.activities);
  const stages = planStages(task.plan, task.plan_completed);
  const model = task.model_label || modelLabel || '';
  const workspace = task.workspace_label || '';
  const pendingApprovals = (task.approvals || []).filter(a => a.status === 'pending');
  const headline = currentActivityText(task, task.activities);
  // One task busy at a time: any in-flight action on this task disables all of
  // its mutating controls; other tasks are unaffected.
  const taskBusy = taskBusyId(busy, task.id);
  const goal = task.goal?.trim() || '未命名任务';

  return (
    <section className={`task-live ${paused ? 'paused' : ''}`} aria-label={`任务：${goal}`}>
      <div className="task-live-head"><div className="task-live-goal" title={goal}>{goal}</div>
        {onCollapse && <button type="button" className="text-btn task-live-collapse" aria-label={'收起任务进度：' + goal} onClick={onCollapse}><ChevronUp size={14}/></button>}
      </div>
      <div className="task-live-row main">
        <StatusIcon status={task.status}/>
        <span className="task-live-activity" title={headline}>{headline}</span>
        {elapsed && <span className="task-live-elapsed" title="已运行时间">{elapsed}</span>}
      </div>
      <div className="task-live-row sub">
        <span className="task-live-model mono" title={model || undefined}>{model || '模型未知'}</span>
        {workspace && <span className="task-live-workspace" title={workspace}>{workspace}</span>}
        <span className="task-live-flex"/>
        {stages.length > 0 && <span className="task-live-plan-label">{planProgressLabel(stages)}</span>}
      </div>
      <PlanStrip stages={stages} missing={active && !task.plan?.length}/>
      {activities.length ? (
        <ul className="task-live-activities">
          {activities.map(a => <ActivityRow key={a.id} a={a} parentStatus={task.status}/>)}
        </ul>
      ) : active ? (
        <p className="task-live-empty" role="status">等待工具活动…</p>
      ) : null}
      {task.last_error && <p className="task-live-error"><CircleAlert size={13}/>{task.last_error}</p>}
      {pendingApprovals.length > 0 && onApproval && (
        <div className="task-live-approval-note" role="status">
          任务在等你批准一项操作。
          {pendingApprovals.map(meta => (
            <div className="task-live-approval-inline" key={meta.id}>
              {meta.command && <code className="mono">{meta.command}</code>}
              {typeof meta.note === 'string' && meta.note.trim() && <p className="task-live-approval-reason">{meta.note}</p>}
              {!!meta.reasons?.length && <ul className="task-live-approval-reasons">{meta.reasons.map((r, i) => <li key={i}>{r}</li>)}</ul>}
              <div className="task-approval-actions">
                <Button className="small" variant="primary" disabled={!!taskBusy} onClick={() => onApproval(task.id, meta.id, true)}>批准执行</Button>
                <Button className="small" variant="ghost" disabled={!!taskBusy} onClick={() => onApproval(task.id, meta.id, false)}>拒绝</Button>
              </div>
            </div>
          ))}
        </div>
      )}
      <div className="task-live-footer">
        {paused
          ? <Button className="small" variant="primary" disabled={!!taskBusy} onClick={() => onAction(task.id, 'resume')}><Play size={13}/>继续</Button>
          : active
          ? <Button className="small" disabled={!!taskBusy} onClick={() => onAction(task.id, 'pause')}><Pause size={13}/>暂停</Button>
          : null}
        {active && <Button className="small" variant="ghost" disabled={!!taskBusy} onClick={() => onAction(task.id, 'cancel')}><Square size={12}/>停止</Button>}
        <button
          type="button"
          className="text-btn task-live-expand"
          aria-expanded={open}
          onClick={() => setOpen(cur => !cur)}
        >
          查看任务<ChevronRight size={13} className={open ? 'open' : ''}/>
        </button>
      </div>
      {open && (
        <div className="task-live-details">
          {children}
          {active && onGuide && (
            <form className="task-guide" onSubmit={e => { e.preventDefault(); const text = guide.trim(); if (!text) return; onGuide(task.id, text); setGuide(''); }}>
              <input value={guide} maxLength={2000} aria-label="补充引导" placeholder="给任务补充引导，例如：只做只读检查，不要重启服务" onChange={e => setGuide(e.target.value)}/>
              <Button type="submit" className="small" disabled={!guide.trim() || !!taskBusy}><Play size={12}/>提交</Button>
            </form>
          )}
        </div>
      )}
    </section>
  );
}
