import { FormEvent, useState } from 'react';
import { CheckCircle2, Eye, EyeOff, KeyRound, LockKeyhole } from 'lucide-react';
import type { SecretRequest } from '../api';
import { Button } from './ui';

/** Acknowledgement stored on the backend; hidden in the visual stream since the card itself shows Saved. */
export function secretAckText(name: string) {
  return `已把密钥「${name}」存进保险箱。模型看不到明文。`;
}

/** Grok-style encrypted input card. Cleared immediately on submit; collapses to a clean Saved card. */
export default function SecretCard({ request, disabled, stale, onProvide }: { request: SecretRequest; disabled?: boolean; stale?: boolean; onProvide: (value: string) => void }) {
  const [value, setValue] = useState('');
  const [reveal, setReveal] = useState(false);
  const [multiline, setMultiline] = useState(false);
  const provided = !!request.provided_at;
  const skipped = !provided && !!stale;
  const locked = provided || !!disabled || skipped;

  function submit(event: FormEvent) {
    event.preventDefault();
    const next = value;
    if (!next || locked) return;
    setValue('');
    onProvide(next);
  }

  if (provided || skipped) {
    return (
      <div className="grok-card secret-inline-card is-saved" aria-label={request.label}>
        <div className="grok-saved-row">
          <div>
            <strong>{request.label}</strong>
            <p>{request.description || (provided ? `已加密存入密钥保险箱（{{secret:${request.name}}}）` : '已跳过，可在密钥保险箱中添加')}</p>
          </div>
          <span className={`grok-pill ${provided ? 'saved' : 'muted'}`}>
            {provided ? <><CheckCircle2 size={15}/>已保存</> : '已跳过'}
          </span>
        </div>
      </div>
    );
  }

  return (
    <form className="grok-card secret-inline-card" onSubmit={submit} aria-label={request.label}>
      <div className="grok-card-head">
        <div>
          <strong>{request.label}</strong>
          {request.description && <p className="grok-card-sub">{request.description}</p>}
        </div>
        {request.name && <code className="grok-code-pill">{'{{secret:' + request.name + '}}'}</code>}
      </div>
      {multiline ? (
        <textarea
          className="secret-textarea mono"
          rows={4}
          autoComplete="off"
          spellCheck={false}
          aria-label={request.label}
          placeholder="粘贴多行私钥或凭据内容，仅加密写入保险箱…"
          value={value}
          disabled={locked}
          onChange={e => setValue(e.target.value)}
        />
      ) : (
        <div className="secret-input-row">
          <input
            type={reveal ? 'text' : 'password'}
            autoComplete="off"
            spellCheck={false}
            aria-label={request.label}
            placeholder="输入密码或密钥，不会进入对话记录…"
            value={value}
            disabled={locked}
            onChange={e => {
              if (e.target.value.includes('\n')) setMultiline(true);
              setValue(e.target.value);
            }}
          />
          <button type="button" className="icon-btn" aria-label={reveal ? '隐藏内容' : '显示内容'} onClick={() => setReveal(v => !v)}>
            {reveal ? <EyeOff size={15}/> : <Eye size={15}/>}
          </button>
          <Button type="submit" variant="primary" disabled={locked || !value}>加密保存</Button>
        </div>
      )}
      <div className="secret-bottom-bar">
        <span className="secret-note"><LockKeyhole size={12}/>AES-256-GCM 加密写入保险箱，模型无法读取明文</span>
        <div className="secret-bottom-actions">
          <button type="button" className="text-btn" onClick={() => setMultiline(m => !m)}>
            <KeyRound size={12}/>{multiline ? '切回单行密码' : '粘贴多行私钥'}
          </button>
          {multiline && <Button type="submit" variant="primary" disabled={locked || !value}>加密保存</Button>}
        </div>
      </div>
    </form>
  );
}
