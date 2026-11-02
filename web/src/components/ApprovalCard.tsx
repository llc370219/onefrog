import { ChevronRight, ShieldAlert } from 'lucide-react';
import type { ApprovalCardData, ApprovalDecision, ApprovalPrompt } from '../api';
import { Button } from './ui';

const riskLabels: Record<string, string> = { low: '低风险', medium: '中风险', high: '高风险' };

export function approvalRuleKey(serverID?: string, command?: string) {
  const srv = (serverID || 'panel').trim();
  const cmd = (command || '').trim();
  const firstToken = cmd.split(/\s+/)[0] || cmd;
  return srv + '::' + firstToken + '::' + cmd.slice(0, 48);
}

export default function ApprovalCard({
  pending,
  card,
  disabled,
  onDecide,
}: {
  pending?: ApprovalPrompt;
  card?: ApprovalCardData;
  disabled?: boolean;
  onDecide?: (decision: ApprovalDecision) => void;
}) {
  const isPending = Boolean(pending) && !card;
  const status = card?.status || 'pending';
  const command = card?.command || pending?.command || '';
  const serverName = card?.server_name || pending?.server_name || card?.server_id || pending?.server_id || '面板';
  const botName = card?.bot_name || pending?.bot_name || pending?.bot || 'AI 助理';
  const risk = card?.risk || pending?.risk || 'medium';
  const reasons = card?.reasons?.length ? card.reasons : pending?.reasons?.length ? pending.reasons : pending?.warning ? [pending.warning] : [];
  const modelReason = card?.model_reason || pending?.model_reason || '';
  const untrusted = card?.untrusted || pending?.untrusted || '';
  const note = card?.note || '';

  const title = isPending
    ? '需要你的批准'
    : status === 'approved' || status === 'always'
    ? '已批准执行'
    : status === 'timeout'
    ? '审批已超时'
    : '已拒绝执行';

  const pillClass = isPending
    ? 'pending ' + risk
    : status === 'approved' || status === 'always'
    ? 'ok'
    : 'danger';

  const pillText = isPending
    ? (riskLabels[risk] || '待确认')
    : status === 'always'
    ? '本对话始终放行'
    : status === 'approved'
    ? '已批准'
    : status === 'timeout'
    ? '已超时'
    : '已拒绝';

  return (
    <div className={`grok-card approval-inline-card ${isPending ? 'is-pending' : 'is-resolved'}`} role="region" aria-label={title}>
      <div className="grok-card-head">
        <strong>{title}</strong>
        <span className={`grok-pill ${pillClass}`}><i/>{pillText}</span>
      </div>
      <div className="grok-card-cmd mono">
        {serverName && serverName !== '面板' ? <span className="grok-cmd-host">{serverName}</span> : null}
        <span>{command}</span>
      </div>
      <details className="grok-card-details">
        <summary>查看完整请求 <ChevronRight size={13}/></summary>
        <div className="grok-details-body">
          <p><strong>发起 Bot：</strong>{botName} · <strong>目标：</strong>{serverName} · <strong>风险：</strong>{riskLabels[risk] || risk}</p>
          {modelReason && <p><strong>操作目的：</strong>{modelReason}</p>}
          {reasons.length > 0 && (
            <ul>
              {reasons.map((r, i) => <li key={i}>{r}</li>)}
            </ul>
          )}
          {untrusted && (
            <p className="grok-untrusted"><ShieldAlert size={14}/>该操作涉及外部返回的数据内容，请确认确实是你本人要求执行。</p>
          )}
        </div>
      </details>
      {isPending && onDecide && (
        <div className="grok-card-actions">
          <Button variant="primary" disabled={disabled} onClick={() => onDecide({ allow: true, always: false })}>批准执行</Button>
          {!untrusted && (
            <Button disabled={disabled} onClick={() => onDecide({ allow: true, always: true })}>本对话始终放行</Button>
          )}
          <Button variant="ghost" className="danger-btn" disabled={disabled} onClick={() => onDecide({ allow: false })}>拒绝</Button>
        </div>
      )}
      {!isPending && note && <p className="grok-card-foot">{note}</p>}
    </div>
  );
}

