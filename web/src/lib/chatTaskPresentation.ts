import type { TaskView } from '../api';
// Explicit .ts specifier: the Node test runner strips types but does not add
// extensions, so this module must be importable as-is by tests/*.test.mjs.
import { formatDuration, isTerminal, statusLine } from './taskCardFormat.ts';

/**
 * Which durable task deserves the big live card in a chat transcript.
 *
 * Every chat turn is a durable task, but most turns are a single short model
 * reply. The large card is reserved for work that is actually long running or
 * that needs the operator (approval, ask_user, external job, pause, delegation,
 * an explicit multi-step plan). Everything else collapses to a one-line status
 * row, so a normal exchange reads like a conversation instead of a dashboard.
 *
 * All decisions are pure and driven by the server's own timestamps: `now` only
 * advances a live task that is still running, terminal/paused tasks freeze at
 * their real `finished_at`/`updated_at`, and nothing is stored in the browser.
 */

/** A task must have really run this long before it earns the big card. */
export const LONG_TASK_THRESHOLD_MS = 15_000;

export type ChatTaskMode = 'card' | 'compact';
export type CompactTone = 'running' | 'ok' | 'warn' | 'danger' | 'muted';
export type CompactLine = { text: string; tone: CompactTone };
export type ChatTaskEntry = { task: TaskView; mode: ChatTaskMode; compact: CompactLine };

/** Real start of the work: the runtime's start, else the creation time. */
function startedAt(task: Pick<TaskView, 'started_at' | 'created_at'>): string | undefined {
  return task.started_at || task.created_at || undefined;
}

/**
 * Elapsed time from the server's own numbers. Running tasks advance with
 * `nowMs`; paused tasks stop at the write that recorded the pause; terminal
 * tasks stop at `finished_at` (falling back to `updated_at`) so a browser that
 * stays open can never turn a finished short task into a "long" one.
 */
export function taskElapsedMs(
  task: Pick<TaskView, 'status' | 'created_at' | 'updated_at' | 'started_at' | 'finished_at'>,
  nowMs: number,
): number | undefined {
  const start = Date.parse(startedAt(task) || '');
  if (!Number.isFinite(start)) return undefined;
  let end: number;
  if (isTerminal(task.status)) {
    end = Date.parse(task.finished_at || '');
    if (!Number.isFinite(end)) end = Date.parse(task.updated_at || '');
  } else if (task.status === 'paused') {
    end = Date.parse(task.updated_at || '');
  } else {
    end = Number.isFinite(nowMs) ? nowMs : start;
  }
  if (!Number.isFinite(end)) return undefined;
  return Math.max(0, end - start);
}

/** True once the task has really been running for the long-task threshold. */
export function isLongTask(
  task: Pick<TaskView, 'status' | 'created_at' | 'updated_at' | 'started_at' | 'finished_at'>,
  nowMs: number,
): boolean {
  const ms = taskElapsedMs(task, nowMs);
  return ms !== undefined && ms >= LONG_TASK_THRESHOLD_MS;
}

/** The task is parked on, or holding, something only the operator can resolve. */
export function taskNeedsOperator(task: Pick<TaskView, 'status' | 'approvals'>): boolean {
  if (task.status === 'waiting_user' || task.status === 'waiting_approval') return true;
  if (task.status === 'waiting_job' || task.status === 'paused') return true;
  return (task.approvals || []).some(a => a.status === 'pending');
}

/** An explicit plan with two or more real steps means multi-step work. */
export function isMultiStepPlan(task: Pick<TaskView, 'plan'>): boolean {
  return (task.plan || []).filter(step => typeof step === 'string' && step.trim()).length >= 2;
}

/** A delegated child task carries its own approvals and progress: always a card. */
export function isDelegatedTask(task: Pick<TaskView, 'parent_id'>): boolean {
  return Boolean(task.parent_id);
}

/** Does this task still need a live clock? Only while it can cross the threshold. */
export function chatTaskClockActive(task: Pick<TaskView, 'status'>): boolean {
  return task.status === 'running' || task.status === 'queued';
}

/**
 * The presentation decision for one task. Ordered so that anything needing the
 * operator or explicitly multi-step wins over the duration rule; the duration
 * rule then promotes genuinely long work; everything else stays one line.
 */
export function chatTaskMode(task: TaskView, nowMs: number): ChatTaskMode {
  if (isMultiStepPlan(task) || isDelegatedTask(task)) return 'card';
  if (taskNeedsOperator(task)) return 'card';
  if (isLongTask(task, nowMs)) return 'card';
  return 'compact';
}

/** Collapse a runtime error into a short, readable single line. */
export function errorSummary(value: string | undefined, max = 180): string {
  const text = (value || '').replace(/\s+/g, ' ').trim();
  if (!text) return '';
  return text.length > max ? text.slice(0, max - 1).trimEnd() + '…' : text;
}

/**
 * The single line shown instead of the big card. Wording is honest: a failed
 * short task keeps its real error (or says none was returned) instead of
 * vanishing silently, and a completed one still points at the transcript.
 */
export function chatTaskCompact(task: TaskView, nowMs: number): CompactLine {
  const ms = taskElapsedMs(task, nowMs);
  const duration = ms !== undefined ? formatDuration(ms) : '';
  switch (task.status) {
    case 'completed':
      return { text: duration ? `已完成 · 用时 ${duration}` : '已完成', tone: 'ok' };
    case 'failed': {
      const detail = errorSummary(task.last_error);
      return { text: detail ? `任务失败：${detail}` : '任务失败：没有返回错误详情，可查看对话记录', tone: 'danger' };
    }
    case 'cancelled':
      return { text: '已取消', tone: 'muted' };
    case 'budget_exhausted':
      return { text: '步数预算用尽，任务没有完成', tone: 'warn' };
    case 'paused':
      return { text: statusLine(task), tone: 'warn' };
    case 'waiting_user':
    case 'waiting_approval':
    case 'waiting_job':
      return { text: statusLine(task), tone: 'warn' };
    case 'queued':
      return { text: '已提交，等待运行时调度…', tone: 'running' };
    case 'running':
      return { text: '正在处理…', tone: 'running' };
    default:
      return { text: statusLine(task), tone: 'muted' };
  }
}

/** Decide every task of one chat in order; the caller renders card/compact. */
export function presentChatTasks(tasks: TaskView[], nowMs: number): ChatTaskEntry[] {
  return tasks.map(task => ({ task, mode: chatTaskMode(task, nowMs), compact: chatTaskCompact(task, nowMs) }));
}

/** Tasks that still get a rendered card, in their original order. */
export function cardTasks(entries: ChatTaskEntry[]): TaskView[] {
  // The final assistant message belongs in the transcript, not below a process card.
  return entries.filter(entry => entry.mode === 'card' && entry.task.status !== 'completed').map(entry => entry.task);
}

/** Tasks collapsed to a one-line status row, in their original order. */
export function compactTasks(entries: ChatTaskEntry[]): ChatTaskEntry[] {
  return entries.filter(entry => entry.mode === 'compact' && entry.task.status !== 'completed');
}
