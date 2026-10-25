import { useEffect, useState } from 'react';
import { ShieldAlert, ShieldCheck, Terminal, TriangleAlert } from 'lucide-react';
import { api, setApprovalHandler, setApprovalTimeoutHandler, type ApprovalPrompt, type ServerTestResult } from '../api';
import { Button, Modal } from './ui';

const errText = (e: unknown) => (e instanceof Error ? e.message : '操作失败');
const verifyHint = '请通过可信渠道（如云厂商控制台）在服务器上运行 ssh-keygen -lf /etc/ssh/ssh_host_ed25519_key.pub 核对指纹。';

/** Confirms a first-seen SSH host fingerprint, or explains a mismatch and offers the explicit reset. */
export function HostKeyDialog({ serverID, result, onClose, onDone }: { serverID: string; result: ServerTestResult; onClose: () => void; onDone: (message: string) => void }) {
  const [busy, setBusy] = useState(false), [error, setError] = useState(''), [state, setState] = useState(result), [resetOK, setResetOK] = useState(false);
  const mismatch = state.status === 'host_key_mismatch';
  async function confirm() {
    if (!state.fingerprint) return;
    setBusy(true); setError('');
    try {
      const res = await api.testServer(serverID, state.fingerprint);
      if (res.ok) onDone('SSH 连接成功，已保存确认过的主机指纹');
      else if (res.status === 'host_key_unknown' || res.status === 'host_key_mismatch') setState(res);
      else setError('连接失败：' + res.detail);
    } catch (e) { setError(errText(e)); } finally { setBusy(false); }
  }
  async function reset() {
    setBusy(true); setError('');
    try { await api.resetHostKey(serverID); const res = await api.testServer(serverID); setState(res); setResetOK(false); } catch (e) { setError(errText(e)); } finally { setBusy(false); }
  }
  if (mismatch) return <Modal title="SSH 主机指纹不一致" note="已停止连接，已保存的指纹没有被修改。" onClose={onClose} onSubmit={reset} busy={busy} error={error} submitDisabled={!resetOK} confirm="重置已保存的指纹"><div className="oauth-risk"><ShieldAlert size={21}/><div>服务器出示的主机密钥与之前确认的不同。可能是服务器重装了系统，也可能是中间人攻击。</div></div><p>已保存：<code>{state.stored_fingerprint}</code></p><p>当前出示：<code>{state.fingerprint}</code></p><p className="model-hidden-note">{verifyHint}</p><label className="oauth-risk-accept"><input type="checkbox" checked={resetOK} onChange={e => setResetOK(e.target.checked)} />我确认服务器确实更换了主机密钥，重置后需要重新核对</label></Modal>;
  return <Modal title="确认 SSH 主机指纹" note="首次连接：核对一致后才会发送登录凭据并保存指纹。" onClose={onClose} onSubmit={confirm} busy={busy} error={error} confirm="指纹一致，信任并连接"><div className="oauth-risk"><ShieldCheck size={21}/><div>服务器出示的 SSH 主机指纹：<p><code>{state.fingerprint}</code></p></div></div><p className="model-hidden-note">{verifyHint}不一致请勿继续。</p></Modal>;
}

const riskText: Record<string, string> = { low: '低', medium: '中', high: '高' };

/** Shows a command that needs the operator's decision (smart or manual approval) with the reasons, and returns the decision. */
export function ApprovalDialog() {
  const [queue, setQueue] = useState<{ req: ApprovalPrompt; resolve: (allow: boolean) => void }[]>([]);
  useEffect(() => {
    setApprovalHandler(req => new Promise<boolean>(resolve => setQueue(q => [...q, { req, resolve }])));
    setApprovalTimeoutHandler(id => setQueue(q => { q.filter(x => x.req.id === id).forEach(x => x.resolve(false)); return q.filter(x => x.req.id !== id); }));
    return () => { setApprovalHandler(null); setApprovalTimeoutHandler(null); };
  }, []);
  const head = queue[0];
  if (!head) return null;
  const decide = (allow: boolean) => { head.resolve(allow); setQueue(q => q.slice(1)); };
  const { req } = head;
  const reasons = req.reasons?.length ? req.reasons : req.warning ? [req.warning] : [];
  const high = req.risk === 'high';
  return <Modal title={req.mode === 'manual' ? '批准执行这条命令？' : '这条命令需要你确认'} note={`${req.bot || 'Bot'} 想在服务器「${req.server_name || req.server_id}」上执行，未批准不会执行。`} onClose={() => decide(false)} onSubmit={() => decide(true)} confirm="批准执行">
    <div className="rollback-detail"><Terminal size={18} /><code>{req.command}</code></div>
    {req.model_reason && <p className="approval-purpose"><strong>AI 说明的目的：</strong>{req.model_reason}</p>}
    {req.risk && <p className={'approval-level ' + req.risk}>风险等级：{riskText[req.risk] || req.risk}</p>}
    {reasons.length > 0 && <div className="oauth-risk">{high ? <ShieldAlert size={21} /> : <TriangleAlert size={21} />}<div><strong>为什么需要确认</strong><ul className="approval-risks">{reasons.map(r => <li key={r}>{r}</li>)}</ul></div></div>}
    {req.untrusted && <p className="model-hidden-note">这条命令可能是 AI 从服务器输出或附件里读到的，不是你直接要求的。如果你没有让它这么做，请拒绝。</p>}
    <p className="model-hidden-note">请看清命令的影响范围和能否回滚再批准。{Math.round((req.timeout_seconds || 300) / 60)} 分钟内未处理将自动拒绝。</p>
    <Button variant="ghost" onClick={() => decide(false)}>拒绝</Button>
  </Modal>;
}
