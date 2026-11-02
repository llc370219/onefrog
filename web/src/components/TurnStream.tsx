import { Check, CircleCheck, Copy, GitBranch, LoaderCircle, Terminal, XCircle } from 'lucide-react';
import type { ApprovalDecision, Choice, Host, SecretRequest, TurnItem } from '../api';
import ApprovalCard from './ApprovalCard';
import ChoiceCard from './ChoiceCard';
import Markdown from './Markdown';
import SecretCard from './SecretCard';
import { IconButton } from './ui';

/** Splits conversational markdown into sequential bubbles on blank lines outside code fences. */
export function splitChatBubbles(raw: string): string[] {
  const trimmed = (raw || '').trim();
  if (!trimmed) return [];
  const lines = trimmed.split('\n');
  const blocks: string[] = [];
  let buf: string[] = [];
  let inFence = false;
  for (const line of lines) {
    if (line.trimStart().startsWith('```')) {
      inFence = !inFence;
      buf.push(line);
      continue;
    }
    if (!inFence && line.trim() === '') {
      if (buf.length > 0) {
        blocks.push(buf.join('\n').trim());
        buf = [];
      }
      continue;
    }
    buf.push(line);
  }
  if (buf.length > 0) {
    blocks.push(buf.join('\n').trim());
  }
  return blocks.filter(Boolean);
}

export default function TurnStream({
  timeline = [],
  text = '',
  pending,
  activeStatus,
  hosts = [],
  choice,
  secret,
  stale,
  disabled,
  animateFinal,
  onDecideApproval,
  onAnswerChoice,
  onProvideSecret,
  onCopy,
}: {
  timeline?: TurnItem[];
  text?: string;
  pending?: boolean;
  activeStatus?: string;
  hosts?: Host[];
  choice?: Choice;
  secret?: SecretRequest;
  stale?: boolean;
  disabled?: boolean;
  animateFinal?: boolean;
  onDecideApproval?: (id: string, dec: ApprovalDecision) => void;
  onAnswerChoice?: (values: string[], custom: string) => void;
  onProvideSecret?: (value: string) => void;
  onCopy?: (text: string) => void;
}) {
  const waitingApproval = timeline.some(item => item.kind === 'approval_pending');
  const lastSay = [...timeline].reverse().find(item => item.kind === 'say')?.text?.trim() || '';
  const finalBubbles = splitChatBubbles(text).filter((b, idx) => !(idx === 0 && lastSay && b === lastSay));

  const waitingChoice = Boolean(choice && !choice.answer && !stale);
  const waitingSecret = Boolean(secret && !secret.provided_at && !stale);
  const fullCopyText = [
    ...timeline.filter(t => t.kind === 'say' && t.text).map(t => t.text!.trim()),
    ...finalBubbles,
  ].join('\n\n');

  return (
    <div className="turn-stream">
      {timeline.map((item, idx) => {
        if (item.kind === 'say' && item.text) {
          return (
            <div className="bot-bubble" key={'say-' + idx}>
              <div className="assistant-text markdown"><Markdown text={item.text}/></div>
            </div>
          );
        }
        if (item.kind === 'approval_pending' && item.pending_req) {
          const req = item.pending_req;
          return (
            <ApprovalCard
              key={'app-pend-' + req.id}
              pending={req}
              onDecide={dec => onDecideApproval?.(req.id, dec)}
            />
          );
        }
        if (item.kind === 'approval' && item.approval) {
          return <ApprovalCard key={'app-' + item.approval.id + '-' + idx} card={item.approval}/>;
        }
        if (item.kind === 'delegate') {
          return (
            <div className="inline-step delegate" key={'del-' + idx}>
              <GitBranch size={13}/>
              <span><strong>{item.bot || '主控'} → {item.target || '协作 Bot'}</strong> · {item.task}</span>
            </div>
          );
        }
        if (item.kind === 'step') {
          const srvName = hosts.find(h => h.id === item.server_id)?.name || item.server_id;
          const label = item.command || item.name || '执行操作';
          const ok = item.ok !== false;
          if (!item.output) {
            return (
              <div className={`inline-step ${ok ? 'ok' : 'fail'}`} key={'st-' + idx}>
                {ok ? <Check size={13}/> : <XCircle size={13}/>}
                <span>{srvName ? `[${srvName}] ` : ''}{label}</span>
              </div>
            );
          }
          return (
            <details className={`inline-step-details ${ok ? 'ok' : 'fail'}`} key={'st-' + idx}>
              <summary>
                {ok ? <Check size={13}/> : <XCircle size={13}/>}
                <span className="step-summary-label">{srvName ? `[${srvName}] ` : ''}{label}</span>
                <small>查看结果</small>
              </summary>
              <pre className="mono">{item.output}</pre>
            </details>
          );
        }
        return null;
      })}

      {finalBubbles.map((bubble, idx) => (
        <div
          className={`bot-bubble${animateFinal ? ' staggered' : ''}`}
          style={animateFinal && idx > 0 ? { animationDelay: `${Math.min(idx * 90, 360)}ms` } : undefined}
          key={'fin-' + idx}
        >
          <div className="assistant-text markdown"><Markdown text={bubble}/></div>
        </div>
      ))}

      {choice && onAnswerChoice && (
        <ChoiceCard choice={choice} disabled={disabled} stale={stale} onAnswer={onAnswerChoice}/>
      )}
      {secret && onProvideSecret && (
        <SecretCard request={secret} disabled={disabled} stale={stale} onProvide={onProvideSecret}/>
      )}

      {pending ? (
        <div className={`bot-working-bar${waitingApproval ? ' waiting' : ''}`} role="status" aria-live="polite">
          {waitingApproval ? (
            <>
              <span className="waiting-dot"/>
              <span>等待你在上方审批卡片确认是否执行…</span>
            </>
          ) : (
            <>
              <LoaderCircle size={14} className="spin"/>
              <span className="working-shimmer">{activeStatus || '正在思考与处理…'}</span>
              <span className="working-dots" aria-hidden="true"><i/><i/><i/></span>
            </>
          )}
        </div>
      ) : (
        <div className="bot-done-bar">
          {onCopy && fullCopyText && (
            <IconButton label="复制回复" onClick={() => onCopy(fullCopyText)}><Copy size={13}/></IconButton>
          )}
          <span className={`done-badge${waitingChoice || waitingSecret ? ' waiting' : ''}`}>
            {waitingSecret ? (
              <><span className="waiting-dot"/>等待你在卡片中输入加密内容</>
            ) : waitingChoice ? (
              <><span className="waiting-dot"/>等待你在卡片中选择以继续</>
            ) : (
              <><CircleCheck size={13}/><Terminal size={11} style={{ display: 'none' }}/>已完成</>
            )}
          </span>
        </div>
      )}
    </div>
  );
}

