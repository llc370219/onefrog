import { useEffect, useState } from 'react';
import { Code2, FileCode2, GitBranch, HardDrive, RefreshCw, Send } from 'lucide-react';
import { api, type ChatSession, type Host, type WorkspaceCapabilities, type WorkspaceResult, type WorkspaceTarget } from '../api';
import { Button, Empty, Field, Notice, PageHeading } from '../components/ui';

const errorText = (err: unknown) => err instanceof Error ? err.message : '工作区操作失败';
const equal = (a?: WorkspaceTarget, b?: WorkspaceTarget) => a?.server_id === b?.server_id && a?.root === b?.root;

export default function Workspace({ hosts, target, onTarget, sessions, activeTask, onReturnToTask, onNewTask, onResume, sending, onAddServer }: {
  hosts: Host[];
  target?: WorkspaceTarget;
  onTarget: (target: WorkspaceTarget) => void;
  sessions: ChatSession[];
  activeTask?: WorkspaceTarget;
  onReturnToTask: () => void;
  onNewTask: (target: WorkspaceTarget, task: string) => void;
  onResume: (session: ChatSession) => void;
  sending: boolean;
  onAddServer: () => void;
}) {
  const [server, setServer] = useState(target?.server_id || '');
  const [root, setRoot] = useState(target?.root || '');
  const [info, setInfo] = useState<WorkspaceResult>();
  const [status, setStatus] = useState('');
  const [diff, setDiff] = useState<WorkspaceResult>();
  const [query, setQuery] = useState('');
  const [subdir, setSubdir] = useState('');
  const [path, setPath] = useState('');
  const [file, setFile] = useState<{path:string; revision:string; original:string}>();
  const [content, setContent] = useState('');
  const [task, setTask] = useState('');
  const [tab, setTab] = useState<'file'|'diff'>('file');
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState('');
  const [notice, setNotice] = useState('');
  const dirty = !!file && (!file.revision || content !== file.original);
  const opened = info && target ? target : undefined;
  const related = sessions.filter(s => s.options?.workspace && equal(s.options.workspace, target));

  // Server-provided local workspace capability. An older backend 404s and the
  // resolved value is disabled, so the SSH flow stays exactly as before.
  const [caps, setCaps] = useState<WorkspaceCapabilities | null>(null);
  useEffect(() => {
    let live = true;
    api.workspaceCapabilities().then(c => { if (live) setCaps(c); }).catch(() => { if (live) setCaps(null); });
    return () => { live = false; };
  }, []);
  const localEnabled = Boolean(caps?.local.enabled && caps.local.roots.length);

  function allowDiscard() { return !dirty || window.confirm('放弃此文件还没保存的修改？'); }

  async function repo(t: WorkspaceTarget, prefix = '') {
    // 列表必须成功；status 在非 Git 目录可能失败，单独兜底，不能挡住打开。
    const files = await api.workspace(t, {action:'list', path:prefix});
    let text = '';
    try { text = (await api.workspace(t, {action:'status'})).text || ''; } catch { text = ''; }
    setInfo(files);
    setStatus(text);
  }

  async function open() {
    if (busy || !allowDiscard()) return;
    // kind: 'local' rides along only when an operator-allowlisted root is used;
    // the default stays an SSH target, backward compatible with the old backend.
    const isLocal = server === 'local';
    const next: WorkspaceTarget = {
      server_id: isLocal ? 'local' : server,
      root: root.trim(),
      ...(isLocal ? {kind: 'local' as const} : {}),
    };
    setBusy(true); setError(''); setNotice('');
    try {
      await repo(next);
      onTarget(next); setFile(undefined); setPath(''); setQuery(''); setSubdir(''); setDiff(undefined); setTab('file');
    } catch (err) { setError(errorText(err)); }
    finally { setBusy(false); }
  }

  useEffect(() => {
    if (!target) return;
    let live = true;
    setBusy(true);
    Promise.all([api.workspace(target, {action:'list'}), api.workspace(target, {action:'status'})])
      .then(([rows,state]) => { if(live) { setInfo(rows); setStatus(state.text || ''); } })
      .catch(err => { if(live) setError(errorText(err)); })
      .finally(() => { if(live) setBusy(false); });
    return () => { live = false; };
    // Reopen the previous checkout once when navigating back from the task.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  useEffect(() => {
    if (!dirty) return;
    function warn(event: BeforeUnloadEvent) { event.preventDefault(); }
    window.addEventListener('beforeunload', warn);
    return () => window.removeEventListener('beforeunload', warn);
  }, [dirty]);

  async function read(next: string) {
    if (!opened || !next.trim() || busy || !allowDiscard()) return;
    setBusy(true); setError(''); setNotice('');
    try {
      const p = next.trim();
      const result = await api.workspace(opened, {action:'read', path:p});
      if (!result.revision) throw new Error('宿主机没有返回文件版本');
      setFile({path:p, revision:result.revision, original:result.text || ''});
      setContent(result.text || ''); setPath(p); setTab('file');
    } catch (err) { setError(errorText(err)); }
    finally { setBusy(false); }
  }

  function create() {
    if (!opened || !path.trim() || sending || !allowDiscard()) return;
    setFile({path:path.trim(), revision:'', original:''}); setContent(''); setTab('file'); setError(''); setNotice('');
  }

  async function save() {
    if (!opened || !file || !dirty || busy || sending) return;
    setBusy(true); setError(''); setNotice('');
    try {
      const result = await api.workspace(opened, {action:'write', path:file.path, content, revision:file.revision});
      if (!result.revision) throw new Error('宿主机没有返回保存结果，请重新读取');
      setFile({...file, original:content, revision:result.revision});
      setNotice('已保存 '+file.path);
      setDiff(undefined);
      await repo(opened, subdir.trim());
    } catch (err) { setError(errorText(err)); }
    finally { setBusy(false); }
  }

  async function refresh() {
    if (!opened || busy) return;
    setBusy(true); setError('');
    try { await repo(opened, subdir.trim()); setDiff(undefined); setTab('file'); }
    catch (err) { setError(errorText(err)); }
    finally { setBusy(false); }
  }

  async function showDiff() {
    if (!opened || busy) return;
    setBusy(true); setError('');
    try { setDiff(await api.workspace(opened, {action:'diff'})); setTab('diff'); }
    catch (err) { setError(errorText(err)); }
    finally { setBusy(false); }
  }

  const visible = (info?.files || []).filter(p => p.toLowerCase().includes(query.toLowerCase()));
  return <section className="page workspace-page">
    <PageHeading title="代码工作区" note="打开服务器仓库或本机允许的目录，查看改动，或把开发任务交给助手。" actions={<Button onClick={onAddServer}>添加服务器</Button>}/>
    {error && <Notice error>{error}</Notice>}
    {notice && <Notice>{notice}</Notice>}
    <form className="workspace-connect" onSubmit={e=>{e.preventDefault();void open();}}>
      <Field label="位置">{localEnabled ? <select value={server} onChange={e=>{
        const next = e.target.value;
        setServer(next);
        // Picking the server host offers its first allowlisted root; the
        // operator can still switch between allowed roots below.
        if (next === 'local') setRoot(caps?.local.roots[0] || '');
      }} required disabled={busy}>
        <option value="">选择位置</option>
        <option value="local">{caps?.local.execution_environment === 'container' ? '本机（OneFrog 容器内）' : '本机（服务器所在主机）'}</option>
        {hosts.map(h=><option key={h.id} value={h.id} disabled={h.username==='root'}>{h.name}{h.username==='root'?'（需要非 root 账号）':''}</option>)}
      </select> : <select value={server} onChange={e=>setServer(e.target.value)} required disabled={busy}>
        <option value="">选择已确认指纹的服务器</option>
        {hosts.map(h=><option key={h.id} value={h.id} disabled={h.username==='root'}>{h.name}{h.username==='root'?'（需要非 root 账号）':''}</option>)}
      </select>}</Field>
      {server === 'local' && localEnabled ? <Field label="本机允许的目录" hint={
        `仅限管理员在本机允许的目录${caps?.local.command_execution ? '；本机工作区允许执行命令' : '；本机工作区不执行命令'}。${caps?.local.execution_environment === 'container' ? '本机指容器环境，不是宿主机。' : '命令以服务进程账号权限直接执行，不是沙箱隔离；'}高危操作仍需审批。`
      }><select value={root} onChange={e=>setRoot(e.target.value)} required disabled={busy}>
        <option value="">选择允许的目录</option>
        {caps?.local.roots.map(r=><option key={r} value={r}>{r}</option>)}
      </select></Field> : <Field label="Git 仓库根目录"><input value={root} onChange={e=>setRoot(e.target.value)} placeholder="/home/onefrog-ops/src/onefrog" required maxLength={1024} disabled={busy} spellCheck={false} autoCapitalize="off"/></Field>}
      <Button type="submit" busy={busy && !opened} disabled={busy || !server || !root.trim()}><Code2 size={15}/>打开仓库</Button>
    </form>
    {!opened ? <Empty title={busy ? '正在连接目录' : '还没有打开目录'} body={localEnabled ? '选择本机允许的目录或已接入的服务器仓库。本机目录不要求是 Git 仓库；服务器仓库需要 Git 和 Python 3.9 或更新版本。' : '在宿主机的源码目录运行 pwd -P 获取路径。SSH 账号需要能访问该目录，宿主机需有 Git 和 Python 3.9 或更新版本。'}/> : <>
      <div className="workspace-location"><GitBranch size={15}/><code title={opened.root}>{opened.kind === 'local' ? <><HardDrive size={13}/> 本机 · </> : <>{hosts.find(h=>h.id===opened.server_id)?.name || opened.server_id} · </>}{opened.root}</code><Button onClick={refresh} disabled={busy}><RefreshCw size={14}/>刷新仓库</Button></div>
      <div className="workspace-grid">
        <aside className="workspace-files" aria-label="仓库文件">
          <Field label="筛选当前列表"><input type="search" value={query} onChange={e=>setQuery(e.target.value)} placeholder="文件名或目录"/></Field>
          {(info?.truncated || subdir !== "") && <form className="workspace-subdir" onSubmit={e=>{e.preventDefault();void refresh();}}><Field label="按子目录加载"><input value={subdir} onChange={e=>setSubdir(e.target.value)} placeholder="例如 internal/bot"/></Field><Button type="submit" disabled={busy}>加载</Button></form>}
          <div className="workspace-file-list">{visible.map(p=><button type="button" key={p} title={p} disabled={busy} aria-current={p===file?.path?'true':undefined} onClick={()=>void read(p)}><FileCode2 size={14}/><span>{p}</span></button>)}
          {!visible.length && <p className="muted">没有匹配的文件。</p>}</div>
          <pre className="workspace-status" aria-label="Git 状态">{status || '当前目录没有可用的 Git 状态（可能不是 Git 仓库），仍可浏览和编辑文件。'}</pre>
        </aside>
        <div className="workspace-editor">
          <div className="workspace-tabs"><Button onClick={()=>setTab('file')} aria-pressed={tab==='file'}>编辑器</Button><Button onClick={()=>void showDiff()} aria-pressed={tab==='diff'} disabled={busy}>Git 差异</Button></div>
          {tab==='file' ? <>
            <form className="workspace-path" onSubmit={e=>{e.preventDefault();void read(path);}}>
              <Field label="文件相对路径"><input value={path} onChange={e=>setPath(e.target.value)} placeholder="例如 README.md" maxLength={1024} spellCheck={false} autoCapitalize="off"/></Field>
              <Button type="submit" disabled={busy||!path.trim()}>打开</Button>
              <Button onClick={create} disabled={busy||sending||!path.trim()}>新建</Button>
            </form>
            {file ? <>
              <div className="workspace-file-head"><code title={file.path}>{file.path}{dirty?' · 未保存':''}</code><Button variant="primary" onClick={()=>void save()} disabled={busy||sending||!dirty}>保存文件</Button></div>
              <textarea className="workspace-source mono" aria-label="源码编辑器" value={content} readOnly={busy||sending} spellCheck={false} autoCapitalize="off" onChange={e=>setContent(e.target.value)}/>
            </> : <Empty title="选择一个文件" body="支持 256 KiB 以内的 UTF-8 文本。"/>}
          </> : <><pre className="workspace-diff" aria-label="Git 差异">{diff?.text || '没有已跟踪文件的差异。新文件见左侧 Git 状态。'}</pre>{diff?.truncated && <p className="muted">差异较长，已截断。</p>}</>}
        </div>
      </div>
      <section className="workspace-task">
        <h2>交给开发助手</h2>
        <p className="muted">助手可以读代码、改文件、跑测试。每项写入和命令都按你的审批设置独立审查，有风险的操作会先暂停等你确认。</p>
        <form onSubmit={e=>{e.preventDefault();if(!dirty&&!busy&&!sending&&task.trim())onNewTask(opened,task.trim());}}>
          <Field label="开发任务"><textarea rows={3} value={task} onChange={e=>setTask(e.target.value)} placeholder="例如：修复这个接口的空结果处理，跑相关测试并汇报差异。"/></Field>
          <Button type="submit" variant="primary" disabled={dirty||sending||busy||!task.trim()}><Send size={14}/>去任务对话</Button>
        </form>
        {dirty && <p className="muted">请先保存文件，再交给助手。</p>}
        {activeTask && equal(activeTask, opened) && <Button onClick={onReturnToTask} disabled={dirty}>返回当前任务</Button>}
        {related.length>0 && <div className="workspace-previous" aria-label="该仓库的代码任务">{related.slice(0,8).map(s=><Button key={s.id} disabled={dirty||sending||busy} onClick={()=>onResume(s)}>{s.title} · 继续</Button>)}</div>}
      </section>
    </>}
  </section>;
}
