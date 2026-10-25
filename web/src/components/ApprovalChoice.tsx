import { useState } from 'react';
import { ArrowRight, LockKeyhole, ShieldCheck, TriangleAlert } from 'lucide-react';
import { api, type ApprovalMode } from '../api';
import { primaryModes, smartLimits } from '../approvalModes';
import { Button, Notice } from './ui';

/** First-login step: the operator actively chooses whether AI commands need confirmation. Nothing is pre-selected. */
export default function ApprovalChoice({ onDone, onLogout }: { onDone: (mode: ApprovalMode) => void; onLogout: () => void }) {
  const [mode, setMode] = useState<ApprovalMode>('');
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState('');
  async function save() {
    if (!mode) { setError('请选择一种方式'); return; }
    setBusy(true); setError('');
    try { await api.setApprovalMode(mode); onDone(mode); }
    catch (err) { setError(err instanceof Error ? err.message : '保存失败'); }
    finally { setBusy(false); }
  }
  return <div className="login-screen"><div className="login-brand"><span className="brand-icon"><img src="/assets/onefrog-logo.jpg" alt=""/></span><strong>OneFrog<span>FIRST SIGN IN</span></strong></div>
    <form className="login-card setup-card approval-choice" onSubmit={e => { e.preventDefault(); save(); }}>
      <span className="eyebrow">COMMAND APPROVAL</span>
      <h1>AI 执行命令前要不要问你？</h1>
      <p>AI 会通过 SSH 在你的服务器上执行命令。请选择一种方式，之后可以随时在“设置 → 命令审批”里修改。</p>
      <div className="approval-modes" role="radiogroup" aria-label="命令审批方式">
        {primaryModes.map(m => <label key={m.id} className={mode === m.id ? 'selected' : ''}>
          <input type="radio" name="approval-choice" value={m.id} checked={mode === m.id} onChange={() => setMode(m.id)} />
          <span><strong>{m.recommended ? <ShieldCheck size={14}/> : <TriangleAlert size={14}/>}{m.name}{m.recommended && <span className="tag">推荐</span>}</strong><small>{m.desc}</small>
            {m.risks && <ul className="approval-risks">{m.risks.map(r => <li key={r}>{r}</li>)}</ul>}</span>
        </label>)}
      </div>
      <p className="model-hidden-note">{smartLimits}</p>
      {error && <Notice error>{error}</Notice>}
      <Button type="submit" variant="primary" busy={busy} disabled={!mode}>确认并进入<ArrowRight size={16}/></Button>
      <button type="button" className="text-btn setup-logout" onClick={onLogout} disabled={busy}><LockKeyhole size={12}/>退出，稍后再选</button>
    </form></div>;
}
