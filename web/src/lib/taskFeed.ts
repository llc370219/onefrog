import { useEffect, useRef } from 'react';
import { api, taskIsActive, type ChatRequest, type TaskView } from '../api';
import { startTaskPoll, submitTask, type TaskSubmit } from './taskSubmit';

export { clientRequestID, lifecycleFlag, retryableSubmitError, startTaskPoll, taskKeyOf, taskStatusText } from './taskSubmit';

/**
 * The one submission path for every view: it owns the request id, so a lost
 * response is retried at most once with the same id, and a rejected (4xx)
 * request is never replayed. Views must not call `api.createTask` themselves.
 */
export function createTask(body: ChatRequest): Promise<TaskSubmit> {
  return submitTask(body, b => api.createTask(b));
}

/** True while a task can still run or is parked on an operator answer. */
export function anyTaskActive(items: TaskView[]): boolean {
  return items.some(taskIsActive);
}

/**
 * Polls one task feed while mounted. Ticks never overlap, and `isCurrent()`
 * turns false on unmount / dep change so a tick that is already awaiting can
 * drop its stale result instead of writing it into the new view.
 */
export function useTaskPoll(
  enabled: boolean,
  intervalMs: number,
  tick: (isCurrent: () => boolean) => Promise<void>,
  deps: unknown[],
) {
  const tickRef = useRef(tick);
  tickRef.current = tick;
  useEffect(() => {
    if (!enabled) return;
    const handle = startTaskPoll(intervalMs, current => tickRef.current(current));
    return handle.stop;
    // deps are supplied by the caller; tickRef always reads the latest closure.
  }, [enabled, intervalMs, ...deps]);
}
