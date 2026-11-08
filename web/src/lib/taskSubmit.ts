import type { ChatRequest, TaskView } from '../api';

/** Stable id for one submission attempt, so the runtime can deduplicate a retried POST. */
export function clientRequestID(): string {
  return 'req-' + Date.now().toString(36) + '-' + Math.random().toString(36).slice(2, 10);
}

/** Render key of a task list: a poll that changes nothing must not touch state. */
export function taskKeyOf(items: TaskView[]): string {
  return JSON.stringify(items);
}

/** Human readable one-liner for a task; it never invents progress the runtime did not report. */
export function taskStatusText(t: Pick<TaskView, 'status' | 'next'>): string {
  switch (t.status) {
    case 'queued': return '任务已排队，等待执行…';
    case 'running': return t.next ? '执行中：' + t.next : '执行中…';
    case 'waiting_user': return '等待你回答问题卡片…';
    case 'waiting_approval': return '等待你批准一项操作…';
    case 'waiting_job': return '等待外部任务完成…';
    case 'paused': return '任务已暂停';
    case 'completed': return '任务已完成（由运行时确认）';
    case 'failed': return '任务失败';
    case 'cancelled': return '任务已取消';
    case 'budget_exhausted': return '步数预算用尽，任务没有完成';
    default: return t.status;
  }
}

/**
 * True when the failure says nothing about whether the server accepted the
 * request: no HTTP status (connection dropped, response lost) or a 5xx.
 * A 4xx is a decision (validation, conflict, auth) and must not be replayed.
 */
export function retryableSubmitError(err: unknown): boolean {
  const status = (err as { status?: unknown } | null)?.status;
  return typeof status !== 'number' || status >= 500;
}

export type TaskSubmit =
  | { ok: true; task: TaskView; attempts: number }
  /** `uncertain`: the runtime may still have accepted the submission. */
  | { ok: false; error: unknown; uncertain: boolean; attempts: number };

/**
 * Submits a durable task, at most twice in total. When the first attempt fails
 * in a way that says nothing about acceptance (connection lost, 5xx) the
 * identical body is resent with the same `client_request_id`; the server
 * resolves a repeated id to the task it already accepted, so the work never
 * runs twice and an unrelated task that merely shares the goal is never
 * adopted. A 4xx is a decision and is never replayed.
 *
 * Callers must use this as the ONLY submission call: doing their own
 * `api.createTask` first and then calling this would triple the attempts.
 */
export async function submitTask(
  body: ChatRequest,
  create: (body: ChatRequest) => Promise<TaskView>,
): Promise<TaskSubmit> {
  try {
    return { ok: true, task: await create(body), attempts: 1 };
  } catch (err) {
    if (!retryableSubmitError(err)) return { ok: false, error: err, uncertain: false, attempts: 1 };
  }
  try {
    // Same object, same client_request_id: the runtime deduplicates this.
    return { ok: true, task: await create(body), attempts: 2 };
  } catch (err) {
    return { ok: false, error: err, uncertain: retryableSubmitError(err), attempts: 2 };
  }
}

/**
 * A lifecycle flag for async work started by a component. `enter()` on setup,
 * `exit()` on cleanup: re-entering after a cleanup (React StrictMode remounts,
 * route switches) must make the flag true again, so a late response is only
 * dropped while the component is really gone.
 */
export function lifecycleFlag() {
  let current = false;
  return {
    get current() { return current; },
    enter() { current = true; },
    exit() { current = false; },
  };
}

/** Timer injection so the poll loop can be tested without a browser. */
export type PollTimers = {
  setInterval?: (fn: () => void, ms: number) => number;
  clearInterval?: (id: number) => void;
};

export type PollHandle = { isCurrent: () => boolean; stop: () => void };

/**
 * Poll loop behind `useTaskPoll`. Ticks never overlap, and `isCurrent()` turns
 * false as soon as the loop stops (unmount, session switch, dep change), so a
 * tick that resolves later can check it after every await and drop its result.
 */
export function startTaskPoll(
  intervalMs: number,
  tick: (isCurrent: () => boolean) => Promise<void>,
  timers: PollTimers = {},
): PollHandle {
  const schedule = timers.setInterval ?? ((fn: () => void, ms: number) => window.setInterval(fn, ms));
  const cancel = timers.clearInterval ?? ((id: number) => window.clearInterval(id));
  let active = true;
  let inFlight = false;
  const isCurrent = () => active;
  const run = async () => {
    if (inFlight || !active) return;
    inFlight = true;
    try {
      await tick(isCurrent);
    } catch {
      /* the next tick retries */
    } finally {
      inFlight = false;
    }
  };
  void run();
  const timer = schedule(run, intervalMs);
  return {
    isCurrent,
    stop() {
      if (!active) return;
      active = false;
      cancel(timer);
    },
  };
}
