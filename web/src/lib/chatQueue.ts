import type { ChatLine, Choice, TaskView } from '../api';

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

/** A composer answer is sent only for the exact unanswered card. */
export function pendingChatCard(current: TaskView | undefined, lines: ChatLine[]): ChatLine | undefined {
  const id = current?.pending?.card_message_id;
  if (current?.status !== 'waiting_user' || !id) return;
  return lines.find(line => line.id === id && line.who === 'bot' &&
    ((line.choice && !line.choice.answer) || (line.secret && !line.secret.provided_at)));
}

export function composerChoiceAnswer(choice: Choice, input: string) {
  const text = input.trim();
  if (!text) return;
  const index = /^[1-6]$/.test(text) ? Number(text) - 1 :
    /^[A-F]$/i.test(text) ? text.toUpperCase().charCodeAt(0) - 65 : -1;
  const options = choice.options || [];
  const labeled = options.filter(option => option.label === text);
  const option = options[index] || (labeled.length === 1 ? labeled[0] : undefined);
  if (option) return { values: [option.value], custom: '' };
  if (choice.allow_custom && Array.from(text).length <= 500) return { values: [], custom: text };
}
