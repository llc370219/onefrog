import { useEffect, useId, useRef, type ReactNode, type ButtonHTMLAttributes } from 'react';
import { X, LoaderCircle, Inbox, Check, AlertCircle } from 'lucide-react';

export function Button({ children, variant = 'secondary', busy, className = '', ...props }: ButtonHTMLAttributes<HTMLButtonElement> & { variant?: 'primary' | 'secondary' | 'ghost'; busy?: boolean }) {
  return <button type="button" {...props} disabled={props.disabled || busy} className={`btn ${variant} ${className}`}>{children}{busy && <LoaderCircle size={15} className="spin" />}</button>;
}
export function IconButton({ label, children, ...props }: ButtonHTMLAttributes<HTMLButtonElement> & { label: string }) {
  return <button type="button" {...props} className={`icon-btn ${props.className || ''}`} title={label} aria-label={label}>{children}</button>;
}
export function Field({ label, hint, children }: { label: string; hint?: string; children: ReactNode }) {
  return <label className="field"><span>{label}</span>{children}{hint && <small>{hint}</small>}</label>;
}
export function Status({ status }: { status: string }) {
  const [label, tone] = ({ online: ['在线', 'ok'], offline: ['离线', 'danger'], host_key_mismatch: ['指纹不一致', 'danger'], server_deleted: ['已删除服务器', 'muted'], host_key_confirmed: ['已确认指纹', 'ok'], host_key_reset: ['已重置指纹', 'muted'], unknown: ['未检测', 'muted'], success: ['已完成', 'ok'], failed: ['执行失败', 'danger'], blocked: ['已拦截', 'warn'], approval_allowed: ['审批通过', 'ok'], approval_denied: ['审批拒绝', 'warn'], rolled_back: ['已记录撤回', 'muted'] } as Record<string, string[]>)[status] || ['未知', 'muted'];
  return <span className={`status ${tone}`}><i />{label}</span>;
}
export function Empty({ title, body, action }: { title: string; body?: string; action?: ReactNode }) {
  return <div className="empty"><Inbox size={26} strokeWidth={1.3}/><strong>{title}</strong>{body && <p>{body}</p>}{action}</div>;
}
export function Notice({ error, children }: { error?: boolean; children: ReactNode }) {
  return <div className={`notice ${error ? 'error' : ''}`} role={error ? 'alert' : 'status'}>{error ? <AlertCircle size={16}/> : <Check size={16}/>}<span>{children}</span></div>;
}
export function PageHeading({ eyebrow, title, note, actions }: { eyebrow?: string; title: string; note: string; actions?: ReactNode }) {
  return <header className="page-heading"><div>{eyebrow && <div className="eyebrow">{eyebrow}</div>}<h1>{title}</h1><p>{note}</p></div>{actions && <div className="heading-actions">{actions}</div>}</header>;
}
export function Modal({ title, note, children, onClose, onSubmit, busy, error, submitDisabled, confirm = '保存', wide }: { title: string; note: string; children?: ReactNode; onClose: () => void; onSubmit?: () => void; busy?: boolean; error?: string; submitDisabled?: boolean; confirm?: string; wide?: boolean }) {
  const ref = useRef<HTMLDivElement>(null);
  const titleID = useId();
  const close = useRef(onClose); close.current = onClose;
  const working = useRef(busy); working.current = busy;
  useEffect(() => {
    const previous = document.activeElement as HTMLElement | null;
    ref.current?.querySelector<HTMLElement>('input, select, textarea, button')?.focus();
    function key(event: KeyboardEvent) {
      if (event.key === 'Escape' && !working.current) close.current();
      if (event.key !== 'Tab') return;
      const nodes = ref.current?.querySelectorAll<HTMLElement>('button:not(:disabled), input:not(:disabled), select:not(:disabled), textarea:not(:disabled), [tabindex="0"]');
      if (!nodes?.length) return;
      const first = nodes[0], last = nodes[nodes.length - 1];
      if (event.shiftKey && document.activeElement === first) { event.preventDefault(); last.focus(); }
      if (!event.shiftKey && document.activeElement === last) { event.preventDefault(); first.focus(); }
    }
    document.addEventListener('keydown', key);
    return () => { document.removeEventListener('keydown', key); previous?.focus(); };
  }, []);
  return <div className="modal-backdrop" onClick={() => !busy && onClose()}><div ref={ref} className={'modal' + (wide ? ' wide' : '')} role="dialog" aria-modal="true" aria-labelledby={titleID} onClick={e => e.stopPropagation()}><form onSubmit={e => { e.preventDefault(); if (!busy) (onSubmit || onClose)(); }}><header><div><h2 id={titleID}>{title}</h2><p>{note}</p></div><IconButton label="关闭弹窗" onClick={onClose} disabled={busy}><X size={18}/></IconButton></header><div className="modal-body">{children}{error && <Notice error>{error}</Notice>}</div><footer><Button onClick={onClose} disabled={busy}>取消</Button><Button type="submit" variant="primary" busy={busy} disabled={submitDisabled}>{confirm}</Button></footer></form></div></div>;
}
