import { useEffect, useState } from 'react';
import { ArrowUpCircle, Check, Copy, ExternalLink, LoaderCircle, RefreshCw, X } from 'lucide-react';
import { api, type UpdateStatus } from '../api';
import { Button, IconButton, Notice } from './ui';

const DISMISS_KEY = 'onefrog.update.dismissed';
const errorText = (e: unknown) => e instanceof Error ? e.message : '请求失败';

/** Commands that upgrade an install made with install.sh; data and settings are kept. */
export function upgradeCommands(repo: string, installDir = '', project = '', channel = 'stable') {
  const dir = installDir || '/opt/onefrog', name = project || 'onefrog';
  const beta = channel === 'beta';
  // Test installs use the installer from the dev branch and stay on the beta channel.
  const env = (dir === '/opt/onefrog' ? '' : ` OF_INSTALL_DIR=${dir}`) + (beta ? ' OF_CHANNEL=beta' : '');
  return [
    { label: '重新运行安装命令（推荐）', cmd: `curl -fsSL https://raw.githubusercontent.com/${repo}/${beta ? 'dev' : 'main'}/install.sh | sudo${env} bash` },
    { label: '或者只更新镜像', cmd: `cd ${dir} && sudo docker compose -p ${name} pull && sudo docker compose -p ${name} up -d` },
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
  return <div className="update-help">{upgradeCommands(status.repo, status.install_dir, status.project, status.channel).map(c => <CommandLine key={c.cmd} {...c}/>)}<p>在安装 OneFrog 的服务器上以 root 执行。{!status.install_dir && '如果安装时改过 OF_INSTALL_DIR，请换成你的安装目录。'}账号、设置、对话和数据卷都会保留；升级会重启容器，进行中的回复会中断，登录状态不受影响。</p></div>;
}

function interrupted(msg: string) {
  return msg === 'Failed to fetch' || msg === 'Load failed' || msg.includes('NetworkError') || msg.includes('服务暂时不可用');
}

/** Asks the host helper to pull the pinned image and recreate the container. */
function ApplyUpgrade({ startConfirm, onCancel }: { startConfirm?: boolean; onCancel?: () => void }) {
  const [phase, setPhase] = useState<'idle' | 'confirm' | 'done'>(startConfirm ? 'confirm' : 'idle');
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState('');
  const [message, setMessage] = useState('');
  async function apply() {
    setBusy(true); setError('');
    try {
      const res = await api.applyUpdate();
      setMessage(res.message);
      setPhase('done');
    } catch (e) {
      const msg = errorText(e);
      if (interrupted(msg)) {
        setMessage('连接已中断，升级可能已经开始。请稍等一两分钟后刷新。账号和数据会保留。');
        setPhase('done');
      } else setError(msg);
    } finally { setBusy(false); }
  }
  if (phase === 'done') return <Notice>{message}</Notice>;
  if (phase === 'confirm') return <div className="update-apply"><p>升级会拉取新镜像并重启面板，进行中的回复会中断。账号、设置和数据会保留，登录状态不受影响。</p>{error && <Notice error>{error}</Notice>}<div><Button onClick={() => onCancel ? onCancel() : setPhase('idle')} disabled={busy}>取消</Button><Button variant="primary" busy={busy} onClick={apply}>确认升级</Button></div></div>;
  return <Button variant="primary" onClick={() => setPhase('confirm')}><ArrowUpCircle size={14}/>立即升级</Button>;
}

/** Dismissible banner shown above every page when a newer release exists. */
export function UpdateBanner({ status }: { status: UpdateStatus | null }) {
  const blocked = !!(status?.helper_stale && status.latest?.includes('-beta.'));
  const [dismissed, setDismissed] = useState(() => { try { return localStorage.getItem(DISMISS_KEY) || ''; } catch { return ''; } });
  const [open, setOpen] = useState(false);
  if (!status?.available || !status.latest || dismissed === status.latest) return null;
  const latest = status.latest;
  function dismiss() { try { localStorage.setItem(DISMISS_KEY, latest); } catch { /* ignore */ } setDismissed(latest); }
  const oneClick = !!status.can_apply && !blocked;
  return <div className="update-banner" role="status"><div className="update-banner-row"><ArrowUpCircle size={17}/><span><strong>OneFrog {latest} 已发布</strong><small>当前 {status.current}</small></span>{status.url && <a href={status.url} target="_blank" rel="noreferrer noopener">更新说明<ExternalLink size={12}/></a>}{oneClick ? <Button variant="primary" onClick={() => setOpen(v => !v)}>{open ? '收起' : '立即升级'}</Button> : <button type="button" className="text-btn" onClick={() => setOpen(v => !v)}>{open ? '收起' : '如何升级'}</button>}<IconButton label="不再提示此版本" onClick={dismiss}><X size={15}/></IconButton></div>{open && (oneClick ? <ApplyUpgrade startConfirm onCancel={() => setOpen(false)}/> : <><Notice error>这台机器的升级程序还不会切换到测试版，点升级只会重新拉取正式版。</Notice><UpgradeHelp status={status}/></>)}</div>;
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
      <div><small>当前版本</small><strong>{status.current}{status.channel === 'beta' && <span className="tag channel-tag">测试版</span>}</strong></div>
      <div><small>{status.channel === 'beta' ? '最新版本（含测试版）' : '最新正式版'}</small><strong>{status.latest || '—'}</strong>{status.url && <a href={status.url} target="_blank" rel="noreferrer noopener">更新说明<ExternalLink size={12}/></a>}</div>
      <div><small>上次检查</small><strong>{checked}</strong></div>
    </div>
    {error && <Notice error>{error}</Notice>}
    {status.enabled && status.error && <Notice error>{status.error}</Notice>}
    {!status.available && status.enabled && status.latest && <Notice>已是最新版本。</Notice>}
    {status.available && status.helper_stale && status.latest?.includes('-beta.') && <Notice error>这台机器的升级程序还不会切换到测试版。请先在服务器上重新运行下面的安装命令，之后再在面板里升级。</Notice>}
    {status.available && status.can_apply && !(status.helper_stale && status.latest?.includes('-beta.')) && <Notice><strong>有新版本 {status.latest}。</strong>可以直接升级。</Notice>}
    {status.available && status.can_apply && !(status.helper_stale && status.latest?.includes('-beta.')) && <ApplyUpgrade/>}
    {status.available && status.can_apply && !(status.helper_stale && status.latest?.includes('-beta.')) && <details className="update-notes"><summary>仍使用命令升级</summary><UpgradeHelp status={status}/></details>}
    {status.available && status.helper_stale && status.latest?.includes('-beta.') && <UpgradeHelp status={status}/>}
    {status.available && !status.can_apply && <Notice><strong>有新版本 {status.latest}。</strong>这次安装还不能在面板里点升级，请先执行下面的命令。装上之后，以后就可以点按钮。</Notice>}
    {status.available && !status.can_apply && <UpgradeHelp status={status}/>}
    {status.available && status.notes && <details className="update-notes"><summary>本次更新内容</summary><pre>{status.notes}</pre></details>}
    <div className="settings-subheading"><h3>检查更新</h3><p>开启后，服务端在启动时和每 12 小时向 GitHub 查询一次 {status.repo}（只读，不发送你的数据，也不会自动安装）。{status.channel === 'beta' ? '当前是测试版，正式版和测试版都会提示。' : '当前是正式版，只提示正式发布，不会提示测试版。'}有新版本时面板顶部会提示。</p></div>
    <div className="channel-choice" role="group" aria-label="更新渠道"><span>更新渠道</span><div><button type="button" aria-pressed={status.channel !== 'beta'} disabled={busy || !status.enabled} onClick={() => run(() => api.setUpdateChannel('stable'))}>正式版</button><button type="button" aria-pressed={status.channel === 'beta'} disabled={busy || !status.enabled} onClick={() => run(() => api.setUpdateChannel('beta'))}>测试版</button></div></div>
    <p className="muted">选「测试版」后点「立即升级」。用旧安装脚本装的机器，第一次切换请重新运行安装命令，升级程序才会改镜像标签。</p>
    <div className="about-actions">
      <label className="enable-label"><span>自动检查更新</span><input type="checkbox" checked={status.disabled_by !== 'settings' && status.disabled_by !== 'env'} disabled={busy || status.disabled_by === 'env'} onChange={e => run(() => api.setUpdateCheck(e.target.checked))}/></label>
      <Button onClick={() => run(api.checkUpdate)} busy={busy} disabled={!status.enabled}><RefreshCw size={14}/>立即检查</Button>
    </div>
    {!status.enabled && status.disabled_by && <p className="muted">{disabledText[status.disabled_by]}</p>}
    <div className="settings-subheading"><h3>开源许可</h3><p>OneFrog 以 AGPL-3.0 发布，源码见 <a href={`https://github.com/${status.repo}`} target="_blank" rel="noreferrer noopener">github.com/{status.repo}</a>。</p></div>
  </div>;
}
