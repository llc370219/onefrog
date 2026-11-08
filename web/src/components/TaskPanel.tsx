import { ChevronRight, LoaderCircle, ShieldAlert } from 'lucide-react';
import { taskIsActive, type BotRow, type SettingsState, type TaskApprovalMeta, type TaskView } from '../api';
import { Button } from './ui';
import Markdown from './Markdown';
import TaskLiveCard from './TaskLiveCard';

/** Provider/model display name for a bot, from its effective route. Empty when unknown. */
export function modelLabelOf(bot: BotRow | undefined, settings: SettingsState | null | undefined): string {
  if (!bot) return '';
  const route = bot.effective_route?.provider_id && bot.effective_route?.model ? bot.effective_route
    : bot.model_route?.provider_id && bot.model_route?.model ? bot.model_route
    : undefined;
  if (!route?.model) return '';
  const provider = settings?.providers.find(p => p.id === route.provider_id);
  return provider ? `${provider.name} · ${route.model}` : route.model;
}

const approvalLabel: Record<string, string> = { pending: '待确认', allowed: '已批准', denied: '已拒绝' };

/** One durable task: real goal, plan, progress, next step and the controls that act on it. */
export default function TaskPanel({
  tasks,
  bots,
  parentOf,
  busy,
  settings,
  onAction,
  onGuide,
  onApproval,
  onDismiss,
}: {
  tasks: TaskView[];
  bots: BotRow[];
  /** Parent goal, when the task was delegated by another task. */
  parentOf?: (task: TaskView) => TaskView | undefined;
  busy?: string | string[];
  /** Model/provider settings, for resolving the card's model label. Optional. */
  settings?: SettingsState | null;
  onAction: (id: string, action: 'pause' | 'resume' | 'cancel') => void;
  onGuide: (id: string, content: string) => void;
  onApproval: (taskID: string, approvalID: string, allow: boolean) => void;
  onDismiss?: (id: string) => void;
}) {
  if (!tasks.length) return null;
  return (
    <div className="task-panel-stack" aria-label="当前任务">
      {tasks.map(task => <TaskCard key={task.id} task={task} parent={parentOf?.(task)} bots={bots} busy={busy} settings={settings} onAction={onAction} onGuide={onGuide} onApproval={onApproval} onDismiss={onDismiss}/>)}
    </div>
  );
}
function TaskCard({ task, parent, bots, busy, settings, onAction, onGuide, onApproval, onDismiss }: {
  task: TaskView;
  parent?: TaskView;
  bots: BotRow[];
  busy?: string | string[];
  settings?: SettingsState | null;
  onAction: (id: string, action: 'pause' | 'resume' | 'cancel') => void;
  onGuide: (id: string, content: string) => void;
  onApproval: (taskID: string, approvalID: string, allow: boolean) => void;
  onDismiss?: (id: string) => void;
}) {
  const active = taskIsActive(task);
  const bot = bots.find(b => b.id === task.bot_id);
  const parentBot = parent ? bots.find(b => b.id === parent.bot_id) : undefined;
  const pendingApprovals = (task.approvals || []).filter(a => a.status === 'pending');
  const decidedApprovals = (task.approvals || []).filter(a => a.status !== 'pending');
  const working = task.status === 'running' || task.status === 'queued';

  // The live card IS the card: goal headline, status/activity row, real
  // activities, pause/stop and the expandable details. Bot, parent relation
  // and history live in the expanded section so the compact view stays tight.
  return (
    <TaskLiveCard
      task={task}
      modelLabel={modelLabelOf(bot, settings)}
      busy={busy}
      onAction={onAction}
      onGuide={onGuide}
      onApproval={onApproval}
    >
      <div className="task-card-meta task-live-context">
        {bot ? <span className="task-bot">{parent ? '子任务 · ' : ''}执行 Bot：{bot.name || task.bot_id}</span> : <span className="task-bot">{parent ? '子任务 · ' : ''}执行 Bot：{task.bot_id}</span>}
        {parent ? <span title={parent.goal}>来自：{parentBot?.name || parent.bot_id} · {parent.goal}</span> : null}
        {task.session_id && task.session_id !== parent?.session_id && parent ? <span>在其他对话中执行</span> : null}
        {task.deadline ? <span>截止 {new Date(task.deadline).toLocaleString('zh-CN')}</span> : null}
      </div>
      {!!task.plan?.length && (
        <details className="task-detail">
          <summary>执行计划<ChevronRight size={12}/>{task.plan.length} 步</summary>
          <ol className="task-list">{task.plan.map((step, i) => <li key={i}>{step}</li>)}</ol>
        </details>
      )}
      {!!task.progress?.length && (
        <details className="task-detail" open={working}>
          <summary>已完成<ChevronRight size={12}/>{task.progress.length} 条</summary>
          <ul className="task-list progress">{[...task.progress].slice(-8).reverse().map((item, i) => <li key={i}>{item}</li>)}</ul>
        </details>
      )}
      {task.pending?.kind === 'job' && <p className="task-note"><LoaderCircle size={12} className="spin"/>正在等待外部任务，完成后会自动继续。</p>}
      {task.pending?.kind === 'user' && <p className="task-note">任务已停下，等待你确认或补充信息。</p>}
      {/* Buttons for pending approvals live directly on the compact card; the
          expanded section only adds the full context (reasons, history). */}
      {pendingApprovals.map(meta => <TaskApproval key={meta.id} meta={meta}/>)}
      {!active && task.result && (
        <div className="task-result"><div className="assistant-text markdown"><Markdown text={task.result}/></div></div>
      )}
      {decidedApprovals.length > 0 && (
        <details className="task-detail">
          <summary>审批记录<ChevronRight size={12}/>{decidedApprovals.length} 条</summary>
          <ul className="task-list">
            {decidedApprovals.map(meta => (
              <li key={meta.id}><span className={`task-approval-state ${meta.status}`}>{approvalLabel[meta.status] || meta.status}</span>{meta.command ? <code className="mono">{meta.command}</code> : null}{meta.note ? <small>{meta.note}</small> : null}</li>
            ))}
          </ul>
        </details>
      )}
      {!active && onDismiss && (
        <div className="task-actions">
          <Button className="small" variant="ghost" onClick={() => onDismiss(task.id)}>收起这张任务卡</Button>
        </div>
      )}
    </TaskLiveCard>
  );
}

function TaskApproval({ meta }: {
  meta: TaskApprovalMeta;
}) {
  // The actionable approve/deny buttons render on the compact card
  // (TaskLiveCard); this expanded block carries the safe command context.
  return (
    <div className="task-approval" role="region" aria-label="任务审批">
      <div className="task-approval-head"><ShieldAlert size={14}/><strong>执行前需要你确认</strong></div>
      {meta.command && <code className="mono">{meta.command}</code>}
      {!!meta.reasons?.length && <ul>{meta.reasons.map((reason, i) => <li key={i}>{reason}</li>)}</ul>}
    </div>
  );
}
