import type { ReactNode } from 'react';
import { taskIsActive, type ChatStep, type Choice, type Host, type SecretRequest, type TaskView, type TurnItem } from '../api';
import { replyFailure, replyWaitingStatus } from './TaskReplyControls';
import TurnStream from './TurnStream';

export type PanelJob = {
  pending: boolean;
  prompt: string;
  text: string;
  error: string;
  activeStatus?: string;
  steps: ChatStep[];
  timeline?: TurnItem[];
  sessionId: string;
  botId: string;
  messageId?: string;
  choice?: Choice;
  secret?: SecretRequest;
  serverId?: string;
  /** Durable task the dashboard is following; the runtime owns the work. */
  taskID?: string;
};

/** Results from server-card actions only; the dashboard has no chat launcher. */
export default function PanelTaskResult({ hosts, sending, job, task, onAnswer, onProvide, children }: {
  hosts: Host[];
  sending: boolean;
  job: PanelJob | null;
  task?: TaskView;
  onAnswer: (values: string[], custom: string) => void;
  onProvide: (value: string) => void;
  children?: ReactNode;
}) {
  if (!job) return null;
  const pending = task ? taskIsActive(task) : job.pending;
  const error = job.error || replyFailure(task);
  const text = task?.status === 'completed' ? task.result || job.text : job.text;
  if (!(pending || error || text || job.timeline?.length || job.choice || job.secret)) return null;
  return <section className="panel-operator" aria-label="任务执行结果" aria-live="polite">
    {children}
    {error && <p className="operator-error">{error}</p>}
    {(pending || text || job.timeline?.length || job.choice || job.secret) ? <TurnStream
      timeline={job.timeline || []}
      text={text}
      pending={pending}
      activeStatus={job.activeStatus}
      waitingStatus={task ? replyWaitingStatus([task]) : undefined}
      hosts={hosts}
      choice={job.choice}
      secret={job.secret}
      disabled={sending || (pending && task?.status !== 'waiting_user')}
      animateFinal={!pending}
      showCompletion={!error}
      onAnswerChoice={onAnswer}
      onProvideSecret={onProvide}
    /> : null}
  </section>;
}
