import { useCallback, useEffect, useRef, useState } from 'react';
import { Activity, Clock3, Eye, RefreshCw, Search, Server } from 'lucide-react';
import { api, type Host, type ServiceItem, type ServiceView, type ServiceWatch } from '../api';
import { Button, Empty, Notice, PageHeading } from '../components/ui';
import { counts, glanceItems, intervalLabel, intervals, isWatched, sameSelection, stateNames as watchStateNames, stateTone, targetOf, toggle, watchKey, type Selection, type Target } from '../lib/serviceWatch';

const sourceNames: Record<string, string> = { systemd: '系统服务', docker: 'Docker 容器', ports: '监听端口' };
const stateNames: Record<string, string> = { active: '运行中', running: '运行中', listening: '监听中', inactive: '未运行', failed: '失败', exited: '已退出', dead: '未运行', paused: '已暂停', restarting: '重启中', created: '已创建', activating: '启动中', deactivating: '停止中', removing: '移除中' };
const running = (i: ServiceItem) => ['active', 'running', 'listening'].includes(i.state);
const issue = (i: ServiceItem) => ['failed', 'restarting'].includes(i.state) || /\(unhealthy\)/.test(i.detail);
const when = (time?: string) => time ? new Date(time).toLocaleString() : '尚未检测';
const viewLabel = (v?: ServiceView) => !v || v.status === 'pending' ? '尚未检测' : v.status === 'failed' ? '检测失败' : v.status === 'partial' ? '部分来源可用' : '检测完成';
const pendingView = (id: string): ServiceView => ({ server_id: id, status: 'pending', sources: {}, items: [] });
const errText = (e: unknown, fallback: string) => e instanceof Error ? e.message : fallback;
const shortTime = (time?: string) => time ? new Date(time).toLocaleString([], { month: '2-digit', day: '2-digit', hour: '2-digit', minute: '2-digit', second: '2-digit', hour12: false }) : '—';
const maxPerServer = 100;
const kindNames: Record<string, string> = { systemd: '系统服务', docker: '容器', ports: '端口' };

export function ServiceGlance({ hosts, onOpen }: { hosts: Host[]; onOpen: (id?: string) => void }) {
  const [watch, setWatch] = useState<ServiceWatch | null>(null);
  const [error, setError] = useState('');
  useEffect(() => {
    let active = true;
    const load = async () => { try { const w = await api.serviceWatch(); if (!active) return; setWatch(w); setError(''); } catch { if (active) setError('服务监控暂时不可用'); } };
    void load(); const timer = window.setInterval(() => { if (!document.hidden) void load(); }, 15000);
    return () => { active = false; clearInterval(timer); };
  }, []);
  const rows = glanceItems(watch?.servers || {}, watch?.status || [], hosts.map(h => h.id));
  const summary = counts(rows);
  const hostName = (id: string) => hosts.find(h => h.id === id)?.name || '未知服务器';
  return <div className="service-glance">
    {error && <div className="service-glance-error"><Notice error>{error}</Notice></div>}
    {!watch && !error && <p className="card-foot">正在读取监控项…</p>}
    {watch && !rows.length && <Empty title="还没有监控项" body="这里只显示你勾选的服务、容器或端口，不会列出全部服务器。" action={<Button onClick={() => onOpen()}>去选择</Button>}/>}
    {!!rows.length && <div className="service-watch-glance">{rows.map(row => <button className="service-watch-item" key={row.server_id + watchKey(row)} onClick={() => onOpen(row.server_id)} aria-label={row.name + '，' + hostName(row.server_id) + '，' + (watchStateNames[row.state] || row.state)}>
      <span className={'status ' + stateTone(row.state)}><i/>{watchStateNames[row.state] || row.state}</span>
      <span className="service-watch-main"><strong title={row.name}>{row.name}</strong><small title={hostName(row.server_id)}>{hostName(row.server_id)} · {kindNames[row.kind] || row.kind}</small></span>
    </button>)}</div>}
    {!!rows.length && <footer className="service-glance-foot"><span>监控 {summary.total} 项{summary.bad ? ' · ' + summary.bad + ' 异常' : ''}</span><span>{watch ? '每 ' + intervalLabel(watch.interval_seconds) + '轮询' : ''}</span></footer>}
  </div>;
}

export default function Services({ hosts, focusServer = '', onServer, onAsk }: { hosts: Host[]; focusServer?: string; onServer: (id: string) => void; onAsk: (id: string, text: string) => void }) {
  const [nodes, setNodes] = useState<ServiceView[]>([]);
  const [server, setServer] = useState(focusServer);
  const [kind, setKind] = useState('all');
  const [state, setState] = useState('running');
  const [query, setQuery] = useState('');
  const [busy, setBusy] = useState<string[]>([]);
  const [error, setError] = useState('');
  const [watch, setWatch] = useState<ServiceWatch | null>(null);
  const [sel, setSel] = useState<Selection>({});
  const [saved, setSaved] = useState<Selection>({});
  const [every, setEvery] = useState(60);
  const [saving, setSaving] = useState(false);
  const [watchError, setWatchError] = useState('');
  const scanning = useRef(new Set<string>());
  const mounted = useRef(true);
  const dirtyRef = useRef(false);
  const watchSeq = useRef(0);
  const dirty = !!watch && (!sameSelection(sel, saved) || every !== watch.interval_seconds);
  dirtyRef.current = dirty;
  useEffect(() => { setServer(focusServer); }, [focusServer]);
  // reset=false keeps the user's unsaved ticks while background polls refresh status.
  const applyWatch = useCallback((w: ServiceWatch, reset: boolean) => {
    const servers = w.servers || {};
    setWatch({ ...w, servers, status: w.status || [] });
    setSaved(servers);
    if (reset) { setSel(servers); setEvery(w.interval_seconds); }
  }, []);
  const load = useCallback(async () => {
    const seq = watchSeq.current;
    const [s, w] = await Promise.allSettled([api.services(), api.serviceWatch()]);
    if (!mounted.current) return;
    if (s.status === 'fulfilled') { setNodes(s.value.nodes); setError(''); } else setError(errText(s.reason, '读取服务列表失败'));
    if (seq !== watchSeq.current) return; // a save happened meanwhile; its response is newer
    if (w.status === 'fulfilled') { applyWatch(w.value, !dirtyRef.current); if (!dirtyRef.current) setWatchError(''); }
    else setWatchError(errText(w.reason, '读取监控配置失败'));
  }, [applyWatch]);
  useEffect(() => {
    mounted.current = true; void load();
    const timer = window.setInterval(() => { if (!document.hidden && scanning.current.size === 0) void load(); }, 15000);
    return () => { mounted.current = false; clearInterval(timer); };
  }, [load]);
  async function scan(id: string) {
    if (scanning.current.has(id)) return;
    scanning.current.add(id); if (mounted.current) setBusy([...scanning.current]);
    try {
      const v = await api.scanServices(id);
      if (mounted.current) setNodes(old => [...old.filter(n => n.server_id !== id), v]);
    } catch (e) {
      if (mounted.current) setNodes(old => {
        const prev = old.find(n => n.server_id === id) || pendingView(id);
        return [...old.filter(n => n.server_id !== id), { ...prev, status: 'failed', error: errText(e, '检测失败') }];
      });
    } finally { scanning.current.delete(id); if (mounted.current) setBusy([...scanning.current]); }
  }
  function pick(id: string, t: Target) {
    setWatchError('');
    if (!isWatched(sel, id, t) && (sel[id] || []).length >= maxPerServer) { setWatchError(`每台服务器最多监控 ${maxPerServer} 项`); return; }
    setSel(old => toggle(old, id, t));
  }
  async function saveWatch() {
    watchSeq.current++; setSaving(true); setWatchError('');
    try {
      const w = await api.saveServiceWatch({ interval_seconds: every, servers: sel });
      if (!mounted.current) return;
      watchSeq.current++; applyWatch(w, true);
      // The first poll starts right after saving; pick up its result shortly.
      window.setTimeout(() => { if (mounted.current) void load(); }, 4000);
    } catch (e) { if (mounted.current) setWatchError(errText(e, '保存监控配置失败')); }
    finally { if (mounted.current) setSaving(false); }
  }
  function revert() { if (!watch) return; setSel(saved); setEvery(watch.interval_seconds); setWatchError(''); }
  const hostName = (id: string) => hosts.find(h => h.id === id)?.name || id;
  const visibleHosts = hosts.filter(h => !server || h.id === server);
  async function scanAll() {
    const queue = visibleHosts.map(h => h.id);
    await Promise.all(Array.from({ length: Math.min(3, queue.length) }, async () => {
      while (queue.length && mounted.current) { const id = queue.shift(); if (id) await scan(id); }
    }));
  }
  const snapshots = nodes.filter(n => (!server || n.server_id === server) && n.collected_at);
  const hasData = snapshots.length > 0;
  const all = snapshots.flatMap(n => n.items);
  const statusOf = (id: string, t: Target) => watch?.status.find(s => s.server_id === id && watchKey(s) === watchKey(t));
  const watchRows = Object.entries(sel).filter(([id]) => !server || id === server)
    .flatMap(([id, list]) => list.map(t => ({ id, t, status: statusOf(id, t), unsaved: !isWatched(saved, id, t) })));
  const watched = counts((watch?.status || []).filter(s => isWatched(saved, s.server_id, s)), server || undefined);
  const watchHint = !watch ? '正在读取监控配置…'
    : dirty ? '有未保存的改动，保存后按新配置轮询。'
    : watched.total ? `后台每 ${intervalLabel(watch.interval_seconds)}检测一次，状态变化写入历史记录。`
    : '在下方服务列表勾选「监控」，保存后由后台轮询。';
  const intervalOptions = intervals.includes(every) ? intervals : [...intervals, every].sort((a, b) => a - b);
  const stateOk = (id: string, i: ServiceItem) => state === 'all' || (state === 'issues' ? issue(i) : state === 'watched' ? isWatched(sel, id, targetOf(i)) : running(i));
  return <div className="page services-page"><PageHeading eyebrow="OFFICIAL MODULE / SERVICES" title="服务监控" note="勾选要监控的服务，后台按间隔轮询并记录状态变化。" actions={<Button variant="primary" busy={busy.length > 0} disabled={!visibleHosts.length} onClick={() => void scanAll()}><RefreshCw size={14}/>{server ? '检测此服务器' : '检测全部服务器'}</Button>}/>
    <div className="probe-summary"><div><Server size={16}/><span>服务器<strong>{visibleHosts.length}</strong></span></div><div><Activity size={16}/><span>运行服务 / 容器（快照）<strong>{hasData ? all.filter(i => i.kind !== 'ports' && running(i)).length : '—'}</strong></span></div><div><Activity size={16}/><span>监听端口条目<strong>{hasData ? all.filter(i => i.kind === 'ports').length : '—'}</strong></span></div><div><Eye size={16}/><span>监控项<strong>{watch ? watched.total : '—'}</strong></span></div></div>
    <section className="card service-watch"><header className="card-heading"><div><h2><Eye size={16}/>监控项<span className="tag">{watched.total} 项</span>{watched.bad > 0 && <span className="tag danger">{watched.bad} 异常</span>}{watched.unknown > 0 && <span className="tag warn">{watched.unknown} 无法判断</span>}</h2><small className="muted">{watchHint}</small></div>
      <div className="service-watch-actions"><label>轮询间隔<select aria-label="轮询间隔" value={every} disabled={!watch || saving} onChange={e => setEvery(Number(e.target.value))}>{intervalOptions.map(s => <option key={s} value={s}>每 {intervalLabel(s)}</option>)}</select></label>{dirty && <Button variant="ghost" disabled={saving} onClick={revert}>撤销</Button>}<Button variant="primary" busy={saving} disabled={!dirty} onClick={() => void saveWatch()}>保存监控</Button></div></header>
      {watchError && <Notice error>{watchError}</Notice>}
      {watchRows.length ? <div className="table-scroll"><table className="data-table service-watch-table"><thead><tr><th>服务器</th><th>类型</th><th>监控对象</th><th>状态</th><th>详情</th><th>最近检测</th><th>最近变化</th><th/></tr></thead><tbody>{watchRows.map(({ id, t, status, unsaved }) => {
        const st = status?.state || 'pending';
        return <tr key={id + ' ' + watchKey(t)}><td><button className="text-btn" onClick={() => setServer(id)}>{hostName(id)}</button></td><td>{sourceNames[t.kind]}</td><td><strong className="mono">{t.name}</strong></td><td>{unsaved ? <span className="tag">未保存</span> : <span className={`status ${stateTone(st)}`}><i/>{watchStateNames[st] || st}</span>}</td><td className="service-details"><small>{unsaved ? '保存后开始监控' : status?.detail || '—'}</small></td><td className="service-watch-time mono">{unsaved ? '—' : shortTime(status?.checked_at)}</td><td className="service-watch-time mono">{unsaved ? '—' : shortTime(status?.changed_at)}</td><td><button className="text-btn" disabled={saving} onClick={() => pick(id, t)}>取消监控</button></td></tr>;
      })}</tbody></table></div> : <Empty title={watch ? '还没有监控项' : '正在读取监控配置'} body="在下方服务列表的「监控」列勾选系统服务、容器或监听端口，保存后后台开始轮询。"/>}
    </section>
    <div className="list-toolbar services-toolbar"><label>服务器<select aria-label="筛选服务器" value={server} onChange={e => setServer(e.target.value)}><option value="">全部服务器</option>{hosts.map(h => <option value={h.id} key={h.id}>{h.name}</option>)}</select></label><label>类型<select aria-label="筛选服务类型" value={kind} onChange={e => setKind(e.target.value)}><option value="all">全部类型</option>{Object.entries(sourceNames).map(([id, name]) => <option key={id} value={id}>{name}</option>)}</select></label><label>状态<select aria-label="筛选运行状态" value={state} onChange={e => setState(e.target.value)}><option value="running">运行中 / 监听中</option><option value="all">全部状态</option><option value="issues">异常状态</option><option value="watched">已监控</option></select></label><label className="search"><Search size={14}/><input aria-label="搜索服务" placeholder="搜索服务、容器、镜像、端口…" value={query} onChange={e => setQuery(e.target.value)}/></label></div>
    <p className="service-readonly"><Clock3 size={13}/>已检测 {snapshots.length} / {visibleHosts.length} 台。列表显示上次检测快照，勾选「监控」后由后台轮询并记录状态变化。采集不读取环境变量、启动参数、日志或配置文件。</p>
    {error && <Notice error>{error}</Notice>}
    <div className="services-hosts">{visibleHosts.map(h => {
      const v = nodes.find(n => n.server_id === h.id) || pendingView(h.id);
      const rows = v.items.filter(i => (kind === 'all' || i.kind === kind) && stateOk(h.id, i) && `${i.name} ${i.image || ''} ${i.ports || ''} ${i.detail}`.toLowerCase().includes(query.toLowerCase()));
      const stale = !!v.collected_at && Date.now() - Date.parse(v.collected_at) > 120000;
      return <section className="card service-node" key={h.id}><header className="card-heading"><div><h2><Server size={16}/><button className="text-btn" onClick={() => onServer(h.id)}>{h.name}</button><span className={`tag ${v.status === 'failed' ? 'danger' : ''}`}>{busy.includes(h.id) ? '正在检测…' : viewLabel(v)}</span>{stale && <span className="tag warn">快照较早</span>}</h2><small className="muted">{h.host} · {when(v.collected_at)}</small></div><div className="service-node-actions"><Button busy={busy.includes(h.id)} onClick={() => void scan(h.id)}><RefreshCw size={13}/>检测</Button><Button onClick={() => onAsk(h.id, `检查服务器 ${h.name}（id=${h.id}）上运行的服务、容器和监听端口。先调用 inspect_services 实时检测并说明异常，修改前走审批。`)}>让 AI 检查</Button></div></header>
        {v.error && <Notice error>{v.error}{v.collected_at ? '。下方保留上次成功检测的数据。' : ''}</Notice>}
        {!!v.collected_at && <div className="service-source-status">{Object.entries(v.sources).map(([id, s]) => <span key={id} className={s.status === 'ok' ? 'muted' : 'warn'}>{sourceNames[id]}：{s.status === 'ok' ? '已检测' : s.detail || '不可用'}{s.truncated ? '（仅显示前 500 项）' : ''}</span>)}{v.sources.ports?.status === 'ok' && <span className="muted">非 root 用户可能看不到其他进程的名称</span>}</div>}
        {rows.length ? <div className="table-scroll"><table className="data-table"><thead><tr><th className="service-watch-check">监控</th><th>类型</th><th>服务 / 容器</th><th>状态</th><th>镜像 / 详情</th><th>监听 / 映射端口</th><th/></tr></thead><tbody>{rows.map((i, index) => {
          const t = targetOf(i);
          const on = isWatched(sel, h.id, t);
          return <tr className={on ? 'service-watched' : undefined} key={`${i.kind}-${i.name}-${i.ports}-${index}`}><td className="service-watch-check"><input type="checkbox" aria-label={`监控 ${i.name}`} title={!t ? '没有可监控的标识' : i.kind === 'ports' ? `按监听地址 ${t.name} 监控` : `监控 ${t.name}`} checked={on} disabled={!t || saving || !watch} onChange={() => t && pick(h.id, t)}/></td><td>{sourceNames[i.kind]}</td><td><strong className="mono">{i.name}</strong></td><td><span className={`status ${issue(i) ? 'danger' : running(i) ? 'ok' : 'muted'}`}><i/>{stateNames[i.state] || i.state}</span></td><td className="service-details">{i.image && <strong className="mono">{i.image}</strong>}<small>{i.detail}</small></td><td className="mono service-ports">{i.ports || '—'}</td><td><button className="text-btn" onClick={() => onAsk(h.id, `检查服务器 ${h.name}（id=${h.id}）的${sourceNames[i.kind]} ${JSON.stringify(i.name)}。先只读确认状态和原因，修改前走审批。`)}>AI 检查</button></td></tr>;
        })}</tbody></table></div> : <Empty title={busy.includes(h.id) ? '正在读取服务器状态' : !v.collected_at ? (v.status === 'failed' ? '暂未获取服务数据' : '点击检测，查看这台服务器运行的服务') : '当前筛选下没有匹配项'} body={!v.collected_at ? '使用已保存的 SSH 凭据，只读采集，通常需要几秒钟。' : '可切换全部状态或检查采集来源是否可用。'}/>}
      </section>;
    })}</div>{!hosts.length && <Empty title="还没有服务器" body="先接入服务器，再查看其运行服务。"/>}
  </div>;
}
