import { Play, Square } from 'lucide-react';
import { taskIsActive, type BotRow, type TaskView } from '../api';
import { taskBusyId } from '../lib/taskCardFormat';
import ApprovalCard from './ApprovalCard';

/** Parked tasks need a reply or a decision, so they don't animate as working. */
export function replyWaitingStatus(tasks: TaskView[]): string | undefined {
  const active = tasks.filter(taskIsActive);
  if (active.some(t => t.status === 'waiting_approval' || t.approvals?.some(a => a.status === 'pending'))) return '等待你批准一项操作';
  const waitingUser = active.find(t => t.status === 'waiting_user');
  if (waitingUser) return waitingUser.pending?.card_message_id ? '等待你的回复' : '本轮未提供可回答的问题';
  if (active.length && active.every(t => t.status === 'paused')) return '已暂停';
}

export function replyFailure(task?: TaskView): string {
  switch (task?.status) {
    case 'failed': return task.last_error || '回复失败，请重试。';
    case 'budget_exhausted': return task.last_error || '回复未完成，已达到执行上限。';
    case 'cancelled': return '回复已停止。';
    default: return '';
  }
}

/** Actions for durable replies without a task progress card. */
export default function TaskReplyControls({ tasks, bots, busy, onAction, onApproval }: {
  tasks: TaskView[];
  bots: BotRow[];
  busy?: string | string[];
  onAction: (id: string, action: 'resume' | 'cancel') => void;
  onApproval: (taskID: string, approvalID: string, allow: boolean) => void;
}) {
  const active = tasks.filter(taskIsActive);
  const roots = active.filter(t => !t.parent_id);
  return <>
    {active.flatMap(task => (task.approvals || []).filter(a => a.status === 'pending').map(a =>
      <ApprovalCard key={task.id + ':' + a.id} disabled={!!taskBusyId(busy, task.id)}
        pending={{ id: a.id, bot: bots.find(b => b.id === task.bot_id)?.name || task.bot_id,
          command: a.command || '', reasons: a.reasons, model_reason: a.note,
          server_id: '', server_name: '', timeout_seconds: 0, single_use: true }}
        onDecide={decision => onApproval(task.id, a.id, decision.allow)}/>
    ))}
    {roots.length > 0 && <div className="reply-actions">
      {roots.map(task => {
        const locked = !!taskBusyId(busy, task.id);
        return <div className="task-actions" key={task.id}>
          {task.status === 'paused' && <button type="button" className="text-btn" disabled={locked} onClick={() => onAction(task.id, 'resume')}><Play size={13}/>继续回复</button>}
          <button type="button" className="text-btn" disabled={locked} onClick={() => onAction(task.id, 'cancel')}><Square size={13}/>停止回复</button>
        </div>;
      })}
    </div>}
  </>;
}
