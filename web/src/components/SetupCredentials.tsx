import { useState } from 'react';
import { ArrowRight, LockKeyhole } from 'lucide-react';
import { api, type Profile } from '../api';
import { Button, Field, Notice } from './ui';

export default function SetupCredentials({ profile, initialPassword, onDone, onLogout }: { profile: Profile; initialPassword: string; onDone: (profile: Profile) => void; onLogout: () => void }) {
  const [username, setUsername] = useState('');
  const [current, setCurrent] = useState(initialPassword);
  const [password, setPassword] = useState('');
  const [confirm, setConfirm] = useState('');
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState('');
  async function save() {
    setError('');
    if (!/^[A-Za-z0-9_.-]{3,64}$/.test(username)) { setError('用户名需为 3–64 位字母、数字、下划线、点或短横线'); return; }
    if (username === profile.username) { setError('第一次登录必须更换用户名'); return; }
    if (password.length < 8 || password.length > 72) { setError('新密码需为 8–72 字节'); return; }
    if (password !== confirm) { setError('两次输入的新密码不一致'); return; }
    if (!current) { setError('请输入安装时给出的初始密码'); return; }
    if (password === current) { setError('新密码不能与当前密码相同'); return; }
    setBusy(true);
    try {
      const next = await api.profile({ username, display_name: profile.display_name || '', avatar: profile.avatar || '', current_password: current, new_password: password });
      onDone(next);
    } catch (err) { setError(err instanceof Error ? err.message : '保存失败'); }
    finally { setBusy(false); }
  }
  return <div className="login-screen"><div className="login-brand"><span className="brand-icon"><img src="/assets/onefrog-logo.png" alt=""/></span><strong>OneFrog<span>FIRST SIGN IN</span></strong></div><form className="login-card setup-card" onSubmit={e => { e.preventDefault(); save(); }}><span className="eyebrow">SECURE THIS INSTALL</span><h1>设置你的账号</h1><p>这是安装时生成的初始账号。进入面板前，必须换成你自己的用户名和密码。</p><Field label="新用户名" hint="不能继续使用初始用户名。"><input value={username} autoComplete="username" required placeholder="例如 onefrog" onChange={e => setUsername(e.target.value)} /></Field><Field label="初始密码"><input type="password" value={current} autoComplete="current-password" required onChange={e => setCurrent(e.target.value)} /></Field><Field label="新密码" hint="8–72 字节，不要与初始密码相同。"><input type="password" value={password} autoComplete="new-password" required onChange={e => setPassword(e.target.value)} /></Field><Field label="确认新密码"><input type="password" value={confirm} autoComplete="new-password" required onChange={e => setConfirm(e.target.value)} /></Field>{error && <Notice error>{error}</Notice>}<Button type="submit" variant="primary" busy={busy}>完成并进入<ArrowRight size={16}/></Button><button type="button" className="text-btn setup-logout" onClick={onLogout} disabled={busy}><LockKeyhole size={12}/>退出，稍后再设置</button></form></div>;
}

