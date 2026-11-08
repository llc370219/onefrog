import { Check, CircleCheck, Copy, GitBranch, LoaderCircle, Send, Terminal, XCircle } from 'lucide-react';
import type { ApprovalDecision, BotRow, Choice, Host, SecretRequest, TurnItem } from '../api';
import { speakerOf } from '../lib/chatGroups';
import ApprovalCard from './ApprovalCard';
import { BotAvatar } from './BotAvatar';
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
  waitingStatus,
  hosts = [],
  choice,
  secret,
  stale,
  disabled,
  animateFinal,
  showCompletion = true,
  onDecideApproval,
  onAnswerChoice,
  onProvideSecret,
  onCopy,
  bots = [],
  group,
  lead,
}: {
  timeline?: TurnItem[];
  text?: string;
  pending?: boolean;
  activeStatus?: string;
  waitingStatus?: string;
  hosts?: Host[];
  choice?: Choice;
  secret?: SecretRequest;
  stale?: boolean;
  disabled?: boolean;
  animateFinal?: boolean;
  /** False while a durable task is still running: its later commentary must
   *  not claim 已完成, which only the runtime can confirm. */
  showCompletion?: boolean;
  onDecideApproval?: (id: string, dec: ApprovalDecision) => void;
  onAnswerChoice?: (values: string[], custom: string) => void;
  onProvideSecret?: (value: string) => void;
  onCopy?: (text: string) => void;
  bots?: BotRow[];
  group?: boolean;
  lead?: BotRow;
}) {
  const waitingApproval = timeline.some(item => item.kind === 'approval_pending');
  const isLead = (name?: string) => !name || name === lead?.name || name === lead?.id;
  const lastSay = [...timeline].reverse().find(item => item.kind === 'say' && isLead(item.bot))?.text?.trim() || '';
  const finalBubbles = splitChatBubbles(text).filter((b, idx) => !(idx === 0 && lastSay && b === lastSay));

  // In a group chat each reply carries the speaking bot's avatar and name, shown once per run of messages.
  let current = '';
  const speakerHead = (name: string | undefined, key: string) => {
    if (!group) return null;
    const bot = isLead(name) ? lead : speakerOf(bots, name);
    const who = bot?.id || name || lead?.id || '';
    if (who === current) return null;
    current = who;
    return (
      <div className="group-speaker" key={key}>
        <BotAvatar bots={bots} id={bot?.id || who} className="speaker-avatar"/>
        <strong>{bot?.name || name || lead?.name || '主控'}</strong>
      </div>
    );
  };

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
          const head = speakerHead(item.bot, 'who-' + idx);
          const bubble = (
            <div className="bot-bubble" key={'say-' + idx}>
              <div className="assistant-text markdown"><Markdown text={item.text}/></div>
            </div>
          );
          return head ? [head, bubble] : bubble;
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
        if (item.kind === 'message') {
          return (
            <div className="inline-step message" key={'msg-' + idx} title="异步消息，已放进对方的收件箱">
              <Send size={13}/>
              <span><strong>{item.bot || '主控'} → {item.target || 'Bot'} 收件箱</strong> · {item.task}</span>
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

      {finalBubbles.length > 0 && speakerHead(undefined, 'who-final')}
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
        <div className={`bot-working-bar${waitingApproval || waitingStatus ? ' waiting' : ''}`} role="status" aria-live="polite">
          {waitingApproval || waitingStatus ? (
            <>
              <span className="waiting-dot"/>
              <span>{waitingStatus || '等待你在上方审批卡片确认是否执行…'}</span>
            </>
          ) : (
            <>
              <LoaderCircle size={14} className="spin"/>
              <span className="working-shimmer">{activeStatus || '正在思考与处理…'}</span>
              <span className="working-dots" aria-hidden="true"><i/><i/><i/></span>
            </>
          )}
        </div>
      ) : (showCompletion || waitingChoice || waitingSecret || onCopy) && (
        <div className="bot-done-bar">
          {onCopy && fullCopyText && (
            <IconButton label="复制回复" onClick={() => onCopy(fullCopyText)}><Copy size={13}/></IconButton>
          )}
          {(showCompletion || waitingChoice || waitingSecret) && (
            <span className={`done-badge${waitingChoice || waitingSecret ? ' waiting' : ''}`}>
              {waitingSecret ? (
                <><span className="waiting-dot"/>等待你在卡片中输入加密内容</>
              ) : waitingChoice ? (
                <><span className="waiting-dot"/>等待你在卡片中选择以继续</>
              ) : showCompletion ? (
                <><CircleCheck size={13}/><Terminal size={11} style={{ display: 'none' }}/>已完成</>
              ) : null}
            </span>
          )}
        </div>
      )}
    </div>
  );
}

