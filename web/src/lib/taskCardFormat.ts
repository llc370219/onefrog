import type { TaskActivity, TaskView } from '../api';

/**
 * Pure formatting for the compact live task card. Every value shown comes from
 * real TaskView fields; nothing is synthesised from prose, step counts or
 * unknown model progress. An older backend that omits the additive fields
 * degrades to the "waiting for tool activity" fallback instead of fabricating.
 */

/** Latest N activities shown in the collapsed card body. */
export const ACTIVITY_SHOWN = 5;

/** Ordered tool icon glyph keys resolved to lucide icons by the component. */
export type ActivityGlyph =
  | 'terminal'    // command / script execution
  | 'file'        // read or write a file
  | 'search'      // inspect, query, look up
  | 'server'      // server-scoped operation
  | 'network'     // remote call / web fetch
  | 'brain'       // thinking / planning step, no side effect
  | 'user'        // asked the operator or a bot
  | 'tool';       // generic tool call

/** Map a safe activity `name` to a glyph. Unknown names stay generic. */
export function activityGlyph(name: string): ActivityGlyph {
  const n = name.toLowerCase();
  if (n.includes('ask') || n.includes('question') || n.includes('choice')) return 'user';
  if (n.includes('delegate') || n.includes('agent')) return 'user';
  if (n.includes('plan') || n.includes('think') || n.includes('checkpoint')) return 'brain';
  if (n.includes('workspace') || n.includes('browse') || n.includes('fetch') || n.includes('http')) return 'network';
  if (n.includes('server') || n.includes('ssh') || n.includes('host')) return 'server';
  if (n.includes('run') || n.includes('exec') || n.includes('command') || n.includes('script')) return 'terminal';
  if (n.includes('read') || n.includes('write') || n.includes('edit') || n.includes('file')) return 'file';
  if (n.includes('search') || n.includes('query') || n.includes('list') || n.includes('inspect') || n.includes('memory')) return 'search';
  return 'tool';
}

/** Safe label for one activity; falls back to the tool name, never raw args. */
export function activityLabel(a: Pick<TaskActivity, 'label' | 'name'>): string {
  return a.label?.trim() || a.name?.trim() || '工具活动';
}

/** Actual duration of one activity, from the server's own numbers. */
export function activityDurationText(a: Pick<TaskActivity, 'status' | 'duration_ms' | 'started_at' | 'finished_at'>): string {
  if (a.status === 'running') return '进行中';
  if (a.status === 'interrupted') return '结果未知';
  let ms = a.duration_ms;
  if (typeof ms !== 'number' || !Number.isFinite(ms) || ms < 0) {
    if (!a.started_at || !a.finished_at) return '';
    ms = Date.parse(a.finished_at) - Date.parse(a.started_at);
    if (!Number.isFinite(ms) || ms < 0) return '';
  }
  return formatDuration(ms);
}

/** Compact duration, Chinese units. Negative/invalid never renders as a value. */
export function formatDuration(ms: number): string {
  if (!Number.isFinite(ms) || ms < 0) return '';
  if (ms < 1000) return '<1 秒';
  const total = Math.floor(ms / 1000);
  const h = Math.floor(total / 3600);
  const m = Math.floor((total % 3600) / 60);
  const s = total % 60;
  if (h) return `${h} 小时 ${m} 分`;
  if (m) return s ? `${m} 分 ${s} 秒` : `${m} 分`;
  return `${s} 秒`;
}

/** Elapsed wall-clock since `from`, frozen when `frozenAt` is set. */
export function formatElapsed(from: string | undefined, nowMs: number, frozenAt?: string): string {
  if (!from) return '';
  const start = Date.parse(from);
  if (!Number.isFinite(start)) return '';
  const end = frozenAt ? Date.parse(frozenAt) : nowMs;
  if (!Number.isFinite(end)) return '';
  return formatDuration(Math.max(0, end - start));
}

/**
 * Timestamp the elapsed clock freezes at. The confirmed contract has no
 * paused_at, so a paused task freezes at `updated_at` (the write that recorded
 * the pause); terminal tasks freeze at `finished_at` or, when the backend did
 * not send one, at `updated_at` — never at the viewer's wall clock, which would
 * keep the "elapsed" growing after refresh/remount. Waiting states keep
 * counting honestly: the task is parked, the clock is still its real age.
 */
export function elapsedAnchor(t: Pick<TaskView, 'status' | 'updated_at' | 'finished_at'>): string | undefined {
  if (t.status === 'paused') return t.updated_at;
  if (isTerminal(t.status)) return t.finished_at || t.updated_at;
  return undefined;
}

export function isTerminal(status: string): boolean {
  return status === 'completed' || status === 'failed' || status === 'cancelled' || status === 'budget_exhausted';
}

/** Waiting states still count time but never animate a determinate bar. */
export function isWaiting(status: string): boolean {
  return status === 'waiting_user' || status === 'waiting_approval' || status === 'waiting_job';
}

/** Clock running? Paused and terminal freeze; everything else ticks. */
export function elapsedRunning(status: string): boolean {
  return !isTerminal(status) && status !== 'paused';
}

/** One numbered plan stage: 1-based index, its text, and whether it is inside
 *  the completed prefix reported by the backend. */
export type PlanStage = { index: number; text: string; done: boolean };

/**
 * Numbered plan stages 1..N from the real `plan` string[], with the backend's
 * `plan_completed` prefix count. Completion is never invented: an undefined or
 * invalid count means 0 done; a count beyond the plan (e.g. after a replan
 * shrank it) clamps to N. Step budgets, time and activity counts are never a
 * progress source.
 */
export function planStages(plan: string[] | undefined, completed: number | undefined): PlanStage[] {
  if (!plan?.length) return [];
  const n = typeof completed === 'number' && Number.isFinite(completed)
    ? Math.max(0, Math.min(plan.length, Math.floor(completed)))
    : 0;
  return plan.map((text, i) => ({ index: i + 1, text, done: i < n }));
}

/**
 * Compact label above the segment strip: 第 X 步 / 共 N 步. The current step
 * is the completed prefix + 1 while anything remains (clamped to N); once the
 * prefix covers the whole plan it is the Nth. Derived only from the real
 * completed count — never from time, calls or budget.
 */
export function planProgressLabel(stages: PlanStage[]): string {
  const total = stages.length;
  if (!total) return '';
  const completed = stages.filter(s => s.done).length;
  const current = completed >= total ? total : Math.min(completed + 1, total);
  return `第 ${current} 步 / 共 ${total} 步`;
}

/** Latest activities, oldest first as stored; the card shows the last 5, newest on top. */
export function recentActivities(activities: TaskActivity[] | undefined, count = ACTIVITY_SHOWN): TaskActivity[] {
  if (!activities?.length) return [];
  return activities.slice(-count).reverse();
}

/**
 * The card's status headline. A running task with a live tool activity shows
 * that activity's safe label. Without one, the runtime's `next` note is
 * planning prose, not evidence of tool execution: it is prefixed 下一步： so it
 * can never read as the current action, and with no note at all the card says
 * 思考与规划中. Non-running states go through statusLine.
 */
export function currentActivityText(t: Pick<TaskView, 'status' | 'next'>, activities?: TaskActivity[]): string {
  if (t.status === 'running') {
    const running = [...(activities ?? [])].reverse().find(a => a.status === 'running');
    const label = running ? activityLabel(running) : '';
    if (label) return label;
    if (t.next) return `下一步：${t.next}`;
    return '思考与规划中';
  }
  return statusLine(t);
}

/**
 * How to render one activity row given the PARENT task status. A paused or
 * waiting parent cannot be executing its last reported running activity, so
 * the row must not animate a spinner or claim 进行中. The persisted backend
 * activity is untouched — this is render-time truth only.
 */
export type ActivityRender =
  | { kind: 'active' }                                   // parent running: row may spin
  | { kind: 'stale'; label: string };                    // parent not running: static, honest label

export function activityRender(parentStatus: string): ActivityRender {
  if (parentStatus === 'running') return { kind: 'active' };
  if (parentStatus === 'paused') return { kind: 'stale', label: '已暂停' };
  if (parentStatus === 'waiting_user') return { kind: 'stale', label: '等待你回答' };
  if (parentStatus === 'waiting_approval') return { kind: 'stale', label: '等待审批' };
  if (parentStatus === 'waiting_job') return { kind: 'stale', label: '等待外部任务' };
  if (isTerminal(parentStatus)) return { kind: 'stale', label: '结果待确认' };
  return { kind: 'stale', label: '结果待确认' };
}

/** Effective per-row time text for a rendered activity. */
export function activityRowTime(a: TaskActivity, render: ActivityRender): string {
  if (render.kind === 'stale' && a.status === 'running') return render.label;
  return activityDurationText(a);
}

/**
 * Every mutating control of ONE task shares the same busy key prefix, so a
 * running pause also disables stop, guide and approvals of that task — while
 * other tasks stay interactive.
 */
export function taskBusyId(busy: string | undefined, taskID: string): string | undefined {
  return busy && busy.startsWith(taskID + ':') ? taskID : undefined;
}

/** Status line under the goal; also the headline for non-running states. */
export function statusLine(t: Pick<TaskView, 'status' | 'next'>): string {
  switch (t.status) {
    case 'queued': return '排队中，等待运行时调度';
    case 'running': return '正在执行';
    case 'waiting_user': return '等待你回答卡片后继续';
    case 'waiting_approval': return '等待你批准一项操作';
    case 'waiting_job': return '等待外部任务完成';
    case 'paused': return '已暂停，时间停在暂停那一刻';
    case 'completed': return '已完成';
    case 'failed': return '失败';
    case 'cancelled': return '已取消';
    case 'budget_exhausted': return '步数预算用尽，任务没有完成';
    default: return t.status;
  }
}
