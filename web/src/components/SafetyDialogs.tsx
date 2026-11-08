import { useState } from 'react';
import { ShieldAlert, ShieldCheck } from 'lucide-react';
import { api, type ServerTestResult } from '../api';
import { Modal } from './ui';

const errText = (e: unknown) => (e instanceof Error ? e.message : '操作失败');
export const hostKeyCommand = 'for f in /etc/ssh/ssh_host_*_key.pub; do ssh-keygen -lf "$f"; done';
const keyTypes: Record<string, string> = { 'ssh-ed25519': 'ED25519', 'ecdsa-sha2-nistp256': 'ECDSA', 'ecdsa-sha2-nistp384': 'ECDSA', 'ecdsa-sha2-nistp521': 'ECDSA', 'ssh-rsa': 'RSA' };
/** Splits a host key pin "ssh-ed25519 SHA256:…" into a display type and the hash. Pins saved before v0.1.2 have no type. */
export function splitPin(pin = '') { const i = pin.lastIndexOf(' '); const raw = i >= 0 ? pin.slice(0, i) : ''; return { type: raw ? keyTypes[raw] || raw.toUpperCase() : '', sum: i >= 0 ? pin.slice(i + 1) : pin }; }
export function Fingerprint({ pin }: { pin?: string }) { const { type, sum } = splitPin(pin); return <span className="fingerprint">{type ? <span className="fingerprint-type">{type}</span> : <span className="fingerprint-type muted">类型未记录</span>}<code>{sum}</code></span>; }
export function VerifyHint({ pin }: { pin?: string }) { const { type } = splitPin(pin); return <p className="model-hidden-note">请通过可信渠道（如云厂商控制台）登录服务器，运行 <code>{hostKeyCommand}</code>，{type ? <>找到末尾标着 <strong>({type})</strong> 的那一行，</> : '在输出中找到'}核对 SHA256 值是否一致。</p>; }

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
  if (mismatch) return <Modal title="SSH 主机指纹不一致" note="已停止连接，已保存的指纹没有被修改。" onClose={onClose} onSubmit={reset} busy={busy} error={error} submitDisabled={!resetOK} confirm="重置已保存的指纹"><div className="oauth-risk"><ShieldAlert size={21}/><div>服务器出示的主机密钥与之前确认的不同。可能是服务器重装了系统，也可能是中间人攻击。</div></div><p>已保存：<Fingerprint pin={state.stored_fingerprint}/></p><p>当前出示：<Fingerprint pin={state.fingerprint}/></p><p className="model-hidden-note">重装系统、或部分服务商重启 / 重建实例后会重新生成主机密钥。</p><VerifyHint pin={state.fingerprint}/><label className="oauth-risk-accept"><input type="checkbox" checked={resetOK} onChange={e => setResetOK(e.target.checked)} />我确认服务器确实更换了主机密钥，重置后需要重新核对</label></Modal>;
  return <Modal title="确认 SSH 主机指纹" note="首次连接：核对一致后才会发送登录凭据并保存指纹。" onClose={onClose} onSubmit={confirm} busy={busy} error={error} confirm="指纹一致，信任并连接"><div className="oauth-risk"><ShieldCheck size={21}/><div>服务器出示的 SSH 主机指纹：<p><Fingerprint pin={state.fingerprint}/></p></div></div><VerifyHint pin={state.fingerprint}/><p className="model-hidden-note">不一致请勿继续。</p></Modal>;
}
