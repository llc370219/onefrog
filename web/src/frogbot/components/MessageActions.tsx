import { Pencil, RefreshCw, SmilePlus } from 'lucide-react';
import { useState } from 'react';
import { api, type Msg } from '../api';
import { localTime } from '../lib/format';
import { CopyButton } from './Markdown';

export const EMOJIS = ['👍', '❤️', '😄', '🎉', '🤔', '👀'];

export function Reactions({ m }: { m: Msg }) {
  if (!m.reactions?.length) return null;
  return (
    <div className={`mt-1 flex gap-1 ${m.role === 'user' ? 'justify-end' : ''}`}>
      {m.reactions.map((r) => (
        <button key={r.by} onClick={() => r.by === 'user' && api.react(m.id, r.emoji).catch(() => {})} disabled={r.by !== 'user'}
          title={r.by === 'user' ? '你的回应（点一下取消）' : '回应'} aria-label={`${r.by === 'user' ? '你' : '助理'}回应了 ${r.emoji}`}
          className={`flex items-center gap-1 rounded-full border px-1.5 py-px text-[13px] leading-5 ${r.by === 'user' ? 'border-accent/40 bg-accent-soft' : 'border-line bg-s1'}`}>
          {r.emoji}{r.by === 'bot' && <img src="/frog-bot.png" alt="" className="size-3.5 rounded-full" />}
        </button>
      ))}
    </div>
  );
}

// 每条消息下面的小工具：时间、复制、表情回应、重新生成（最后一条回复）、编辑（最后一条用户消息）
export default function MessageActions({ m, canRegenerate, canEdit, onError }: { m: Msg; canRegenerate?: boolean; canEdit?: boolean; onError: (e: string) => void }) {
  const [picker, setPicker] = useState(false);
  const mine = m.reactions?.find((r) => r.by === 'user')?.emoji;
  return (
    <div className={`msg-actions relative flex items-center gap-0.5 text-fg4 ${m.role === 'user' ? 'justify-end' : ''}`}>
      <span className="px-1 text-[11.5px] tabular-nums" title={new Date(m.created_at).toLocaleString()}>{localTime(m.created_at)}</span>
      {m.text && <CopyButton text={m.text} label="复制这条消息" />}
      {m.role === 'assistant' && (
        <button onClick={() => setPicker(!picker)} aria-label="添加表情回应" aria-expanded={picker} title="回应" className="icon-btn"><SmilePlus size={14} /></button>
      )}
      {canRegenerate && <button onClick={() => api.regenerate().catch((e) => onError((e as Error).message))} aria-label="重新生成这条回复" title="重新生成" className="icon-btn"><RefreshCw size={14} /></button>}
      {canEdit && <button onClick={() => window.dispatchEvent(new Event('frogbot:edit-last'))} aria-label="编辑这条消息" title="编辑并重新发送（↑）" className="icon-btn"><Pencil size={14} /></button>}
      {picker && (
        <div className="rise absolute bottom-8 left-0 z-10 flex gap-0.5 rounded-full border border-line bg-s1 p-1 shadow-[0_8px_24px_-10px_var(--shadow)]" role="menu">
          {EMOJIS.map((e) => (
            <button key={e} role="menuitem" onClick={() => { setPicker(false); api.react(m.id, e).catch(() => {}); }} aria-label={`回应 ${e}`}
              className={`grid size-8 place-items-center rounded-full text-[17px] transition hover:scale-110 hover:bg-s2 ${mine === e ? 'bg-accent-soft' : ''}`}>{e}</button>
          ))}
        </div>
      )}
    </div>
  );
}
