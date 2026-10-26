import { useEffect, useState } from 'react';
import { ArrowUpCircle, Check, Copy, ExternalLink, LoaderCircle, RefreshCw, X } from 'lucide-react';
import { api, type UpdateStatus } from '../api';
import { Button, IconButton, Notice } from './ui';

const DISMISS_KEY = 'onefrog.update.dismissed';
const errorText = (e: unknown) => e instanceof Error ? e.message : '请求失败';

/** Commands that upgrade an install made with install.sh; data and settings are kept. */
export function upgradeCommands(repo: string) {
  return [
    { label: '重新运行安装命令（推荐）', cmd: `curl -fsSL https://raw.githubusercontent.com/${repo}/main/install.sh | sudo bash` },
    { label: '或者只更新镜像', cmd: 'cd /opt/onefrog && sudo docker compose -p onefrog pull && sudo docker compose -p onefrog up -d' },
  ];
}

async function copyText(text: string) {
  try { await navigator.clipboard.writeText(text); return true; } catch { /* plain HTTP has no clipboard API */ }
  const area = document.createElement('textarea');
  area.value = text; area.setAttribute('readonly', ''); area.style.position = 'fixed'; area.style.opacity = '0';
  document.body.appendChild(area); area.select();
  let ok = false;
  try { ok = document.execCommand('copy'); } catch { ok = false; }
  area.remove();
  return ok;
}

function CommandLine({ label, cmd }: { label: string; cmd: string }) {
  const [state, setState] = useState<'' | 'ok' | 'fail'>('');
  return <div className="update-command"><small>{label}</small><div><code>{cmd}</code><IconButton label={state === 'ok' ? '已复制' : '复制命令'} onClick={async () => { setState(await copyText(cmd) ? 'ok' : 'fail'); window.setTimeout(() => setState(''), 1800); }}>{state === 'ok' ? <Check size={14}/> : <Copy size={14}/>}</IconButton></div>{state === 'fail' && <small className="warn">复制失败，请手动选择命令。</small>}</div>;
}

function UpgradeHelp({ status }: { status: UpdateStatus }) {
  return <div className="update-help">{upgradeCommands(status.repo).map(c => <CommandLine key={c.cmd} {...c}/>)}<p>在安装 OneFrog 的服务器上以 root 执行。账号、设置和数据卷都会保留；升级会重启容器，进行中的对话会中断。默认数据库为内存模式时，对话记录在重启后不保留。</p></div>;
}

/** Dismissible banner shown above every page when a newer release exists. */
export function UpdateBanner({ status }: { status: UpdateStatus | null }) {
  const [dismissed, setDismissed] = useState(() => { try { return localStorage.getItem(DISMISS_KEY) || ''; } catch { return ''; } });
  const [open, setOpen] = useState(false);
  if (!status?.available || !status.latest || dismissed === status.latest) return null;
  const latest = status.latest;
  function dismiss() { try { localStorage.setItem(DISMISS_KEY, latest); } catch { /* ignore */ } setDismissed(latest); }
  return <div className="update-banner" role="status"><div className="update-banner-row"><ArrowUpCircle size={17}/><span><strong>OneFrog {latest} 已发布</strong><small>当前 {status.current}</small></span>{status.url && <a href={status.url} target="_blank" rel="noreferrer noopener">更新说明<ExternalLink size={12}/></a>}<button type="button" className="text-btn" onClick={() => setOpen(v => !v)}>{open ? '收起' : '如何升级'}</button><IconButton label="不再提示此版本" onClick={dismiss}><X size={15}/></IconButton></div>{open && <UpgradeHelp status={status}/>}</div>;
}

const disabledText: Record<string, string> = {
  env: '已通过环境变量 OF_UPDATE_CHECK=0 关闭，面板不会访问 GitHub。',
  settings: '已在这里关闭，面板不会访问 GitHub。',
  dev: '这是未标记版本的开发构建，不检查更新。',
  repo: 'OF_UPDATE_REPO 格式无效（应为 owner/name），已停用检查。',
};

/** Settings → 关于与更新. */
export function AboutSection({ onStatus }: { onStatus?: (s: UpdateStatus) => void }) {
  const [status, setStatus] = useState<UpdateStatus | null>(null);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState('');
  useEffect(() => { let active = true; api.updateStatus().then(s => { if (active) setStatus(s); }).catch(e => active && setError(errorText(e))); return () => { active = false; }; }, []);
  function apply(s: UpdateStatus) { setStatus(s); onStatus?.(s); }
  async function run(fn: () => Promise<UpdateStatus>) { setBusy(true); setError(''); try { apply(await fn()); } catch (e) { setError(errorText(e)); } finally { setBusy(false); } }
  if (!status) return error ? <Notice error>{error}</Notice> : <div className="loading-inline"><LoaderCircle size={18} className="spin"/>读取版本信息…</div>;
  const checked = status.checked_at ? new Date(status.checked_at).toLocaleString() : '尚未检查';
  return <div className="about-section">
    <div className="about-grid">
      <div><small>当前版本</small><strong>{status.current}</strong></div>
      <div><small>最新正式版</small><strong>{status.latest || '—'}</strong>{status.url && <a href={status.url} target="_blank" rel="noreferrer noopener">更新说明<ExternalLink size={12}/></a>}</div>
      <div><small>上次检查</small><strong>{checked}</strong></div>
    </div>
    {error && <Notice error>{error}</Notice>}
    {status.enabled && status.error && <Notice error>{status.error}</Notice>}
    {status.available ? <Notice><strong>有新版本 {status.latest}。</strong>按下面的命令升级：</Notice> : status.enabled && status.latest && <Notice>已是最新版本。</Notice>}
    {status.available && <UpgradeHelp status={status}/>}
    {status.available && status.notes && <details className="update-notes"><summary>本次更新内容</summary><pre>{status.notes}</pre></details>}
    <div className="settings-subheading"><h3>检查更新</h3><p>开启后，服务端在启动时和每 12 小时向 GitHub 查询一次 {status.repo} 的最新正式版本（只读，不发送你的数据，也不会自动安装）。有新版本时面板顶部会提示。</p></div>
    <div className="about-actions">
      <label className="enable-label"><span>自动检查更新</span><input type="checkbox" checked={status.disabled_by !== 'settings' && status.disabled_by !== 'env'} disabled={busy || status.disabled_by === 'env'} onChange={e => run(() => api.setUpdateCheck(e.target.checked))}/></label>
      <Button onClick={() => run(api.checkUpdate)} busy={busy} disabled={!status.enabled}><RefreshCw size={14}/>立即检查</Button>
    </div>
    {!status.enabled && status.disabled_by && <p className="muted">{disabledText[status.disabled_by]}</p>}
    <div className="settings-subheading"><h3>开源许可</h3><p>OneFrog 以 AGPL-3.0 发布，源码见 <a href={`https://github.com/${status.repo}`} target="_blank" rel="noreferrer noopener">github.com/{status.repo}</a>。</p></div>
  </div>;
}
