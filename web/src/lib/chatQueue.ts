import type { ChatLine, TaskView } from '../api';

const terminal = new Set(['completed', 'failed', 'cancelled', 'budget_exhausted']);

/** Select the active turn by arrival, not by whichever task was just updated. */
export function chatQueue(tasks: TaskView[], lines: ChatLine[]) {
  const roots = tasks.filter(task => !task.parent_id).sort((a, b) => {
    const time = Date.parse(a.created_at) - Date.parse(b.created_at);
    const nanos = (stamp: string) => Number((stamp.match(/\.(\d+)/)?.[1] || '').padEnd(9, '0').slice(0, 9));
    return time || nanos(a.created_at) - nanos(b.created_at) || a.id.localeCompare(b.id);
  });
  const active = roots.filter(task => !terminal.has(task.status));
  const current = active[0];
  const currentTasks = current ? [current, ...tasks.filter(task => task.parent_id === current.id)] : [];
  const visible = new Set(lines.map(line => line.id));
  const missing = (task: TaskView) => Boolean(task.user_message_id && !visible.has(task.user_message_id));
  return {
    current,
    currentTasks,
    currentMessage: current && missing(current) ? current : undefined,
    waiting: active.slice(1).filter(missing),
    unsentErrors: roots.filter(task => (task.status === 'failed' || task.status === 'budget_exhausted') && missing(task)),
    tasksByMessage: new Map(roots.flatMap(task => task.user_message_id ? [[task.user_message_id, task] as const] : [])),
    latest: roots.at(-1),
  };
}
