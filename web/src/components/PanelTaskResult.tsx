import type { ApprovalDecision, ChatStep, Choice, Host, SecretRequest, TurnItem } from '../api';
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
};

/** Results from server-card actions only; the dashboard has no chat launcher. */
export default function PanelTaskResult({ hosts, sending, job, onAnswer, onProvide, onDecideApproval }: {
  hosts: Host[];
  sending: boolean;
  job: PanelJob | null;
  onAnswer: (values: string[], custom: string) => void;
  onProvide: (value: string) => void;
  onDecideApproval: (id: string, dec: ApprovalDecision) => void;
}) {
  if (!job || !(job.pending || job.error || job.text || job.timeline?.length || job.choice || job.secret)) return null;
  return <section className="panel-operator" aria-label="任务执行结果" aria-live="polite">
    {job.error ? <p className="operator-error">{job.error}</p> : <TurnStream
      timeline={job.timeline || []}
      text={job.text}
      pending={job.pending}
      activeStatus={job.activeStatus}
      hosts={hosts}
      choice={job.choice}
      secret={job.secret}
      disabled={sending || job.pending}
      animateFinal={!job.pending}
      onDecideApproval={onDecideApproval}
      onAnswerChoice={onAnswer}
      onProvideSecret={onProvide}
    />}
  </section>;
}
