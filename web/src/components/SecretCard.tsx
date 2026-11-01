import { FormEvent, useState } from 'react';
import { Check, ShieldCheck } from 'lucide-react';
import type { SecretRequest } from '../api';
import { Button } from './ui';

/** Acknowledgement shown as the user's message. The typed value is never included. */
export function secretAckText(name: string) {
  return `已把密钥「${name}」存进保险箱。模型看不到明文。`;
}

/** Password card for request_secret. The field is cleared as soon as it is submitted. */
export default function SecretCard({ request, disabled, stale, onProvide }: { request: SecretRequest; disabled?: boolean; stale?: boolean; onProvide: (value: string) => void }) {
  const [value, setValue] = useState('');
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
  return <form className={'secret-card' + (provided || skipped ? ' saved' : '')} onSubmit={submit} aria-label={request.label}>
    <div className="secret-title"><ShieldCheck size={16} /><div><strong>{request.label}</strong>{request.name && <code>{'{{secret:' + request.name + '}}'}</code>}</div></div>
    {request.description && <p>{request.description}</p>}
    {provided ? <p className="secret-done"><Check size={13} />已安全保存，模型看不到明文。</p>
      : skipped ? <p className="secret-done">已跳过，对话已继续。可以在密钥保险箱里保存。</p>
      : <>
        <textarea className="secret-textarea" rows={3} autoComplete="off" spellCheck={false} aria-label={request.label} placeholder="输入后只加密保存，不会出现在对话里" value={value} disabled={locked} onChange={e => setValue(e.target.value)} />
        <div className="secret-actions"><Button type="submit" variant="primary" disabled={locked || !value}>安全保存</Button></div>
        <p className="secret-note"><ShieldCheck size={12} />加密保存，模型看不到明文。</p>
      </>}
  </form>;
}
