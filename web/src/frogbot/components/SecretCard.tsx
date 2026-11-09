import { Eye, EyeOff, KeyRound, Lock, ShieldCheck, X } from 'lucide-react';
import { useState } from 'react';
import { useActiveBot, type SecretReq } from '../api';

// SecretCard：请用户保存一个密钥。值只通过 POST /api/secrets 发给后端加密保存，
// 不会进入聊天记录、事件流或模型上下文；卡片里也不会再显示它。
export default function SecretCard({ req }: { req: SecretReq }) {
  const { api } = useActiveBot();
  const [value, setValue] = useState('');
  const [show, setShow] = useState(false);
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState('');

  if (req.status === 'saved') return (
    <div className="rise flex items-center gap-2 rounded-xl border border-line bg-s1/60 px-3.5 py-2 text-[13px]">
      <ShieldCheck size={15} className="shrink-0 text-ok" />
      <span className="text-ok">已安全保存</span>
      <code className="font-mono text-[12.5px] text-fg">{req.name}</code>
      <span className="ml-auto flex items-center gap-1 text-[11.5px] text-fg4"><Lock size={11} />值已加密，不会显示</span>
    </div>
  );
  if (req.status === 'cancelled') return (
    <div className="rise flex items-center gap-2 rounded-xl border border-line bg-s1/60 px-3.5 py-2 text-[13px] text-fg3">
      <X size={15} className="shrink-0" />已取消，未保存 <code className="font-mono text-[12.5px]">{req.name}</code>
    </div>
  );

  const save = async () => {
    if (!value) return;
    setBusy(true); setErr('');
    try { await api.saveSecret(req.id, value); setValue(''); }
    catch (e) { setErr((e as Error).message); }
    finally { setBusy(false); }
  };
  const cancel = async () => {
    setBusy(true); setErr('');
    try { await api.cancelSecret(req.id); setValue(''); } catch (e) { setErr((e as Error).message); } finally { setBusy(false); }
  };

  return (
    <div className="rise overflow-hidden rounded-2xl border border-accent/30 bg-s1 shadow-[0_1px_2px_var(--shadow)]">
      <div className="flex items-start gap-3 px-4 pt-3.5">
        <span className="grid size-8 shrink-0 place-items-center rounded-full bg-accent-soft text-accent"><KeyRound size={16} /></span>
        <div className="min-w-0 flex-1">
          <div className="flex flex-wrap items-baseline gap-x-2">
            <span className="text-[14.5px] font-medium">{req.exists ? '更新密钥' : '保存密钥'}</span>
            <code className="rounded-md bg-s2 px-1.5 py-px font-mono text-[12.5px] text-fg">{req.name}</code>
          </div>
          {req.description && <p className="mt-0.5 text-[13px] leading-relaxed text-fg3">{req.description}</p>}
        </div>
      </div>
      <form className="px-4 pb-4 pt-3" onSubmit={(e) => { e.preventDefault(); save(); }}>
        <div className="flex items-center gap-2 rounded-xl border border-line2 bg-composer px-3 focus-within:border-accent">
          <input type={show ? 'text' : 'password'} value={value} onChange={(e) => setValue(e.target.value)} disabled={busy}
            autoComplete="new-password" spellCheck={false} autoCapitalize="off" autoCorrect="off" name={`secret-${req.id}`}
            placeholder="粘贴密钥…" aria-label={`${req.name} 的值`}
            className="h-10 min-w-0 flex-1 bg-transparent font-mono text-[13.5px] text-fg outline-none placeholder:font-sans placeholder:text-fg4" />
          <button type="button" onClick={() => setShow(!show)} title={show ? '隐藏' : '显示'} aria-label={show ? '隐藏' : '显示'}
            className="grid size-7 shrink-0 place-items-center rounded-full text-fg4 hover:bg-s2 hover:text-fg">{show ? <EyeOff size={15} /> : <Eye size={15} />}</button>
        </div>
        <p className="mt-2 flex items-start gap-1.5 text-[11.5px] leading-relaxed text-fg4">
          <Lock size={11} className="mt-[3px] shrink-0" />直接加密保存到 OneFrog 密钥保险箱，不会进入聊天记录，助理也看不到；使用时只按名字引用，每次都会先请你确认。
        </p>
        {err && <p className="mt-2 text-xs text-danger">{err}</p>}
        <div className="mt-3 flex flex-wrap gap-2">
          <button type="submit" disabled={busy || !value}
            className="inline-flex items-center gap-1.5 rounded-full bg-accent px-4 py-1.5 text-[13px] font-medium text-on-accent transition hover:bg-accent-hover disabled:opacity-50">
            <ShieldCheck size={14} />保存
          </button>
          <button type="button" onClick={cancel} disabled={busy}
            className="rounded-full border border-line2 px-4 py-1.5 text-[13px] text-fg2 transition hover:border-danger hover:text-danger">取消</button>
        </div>
      </form>
    </div>
  );
}
