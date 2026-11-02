import { useCallback, useEffect, useRef, useState } from 'react';
import { Activity, ArrowRight, Box, Clock3, RefreshCw, Search, Server } from 'lucide-react';
import { api, type Host, type ServiceItem, type ServiceView } from '../api';
import { Button, Empty, Notice, PageHeading } from '../components/ui';

const sourceNames: Record<string, string> = { systemd: '系统服务', docker: 'Docker 容器', ports: '监听端口' };
const stateNames: Record<string, string> = { active: '运行中', running: '运行中', listening: '监听中', inactive: '未运行', failed: '失败', exited: '已退出', dead: '未运行', paused: '已暂停', restarting: '重启中', created: '已创建', activating: '启动中', deactivating: '停止中', removing: '移除中' };
const running = (i: ServiceItem) => ['active', 'running', 'listening'].includes(i.state);
const issue = (i: ServiceItem) => ['failed', 'restarting'].includes(i.state) || /\(unhealthy\)/.test(i.detail);
const when = (time?: string) => time ? new Date(time).toLocaleString() : '尚未检测';
const viewLabel = (v?: ServiceView) => !v || v.status === 'pending' ? '尚未检测' : v.status === 'failed' ? '检测失败' : v.status === 'partial' ? '部分来源可用' : '检测完成';
const pendingView = (id: string): ServiceView => ({ server_id: id, status: 'pending', sources: {}, items: [] });

export function ServiceGlance({ hosts, onOpen }: { hosts: Host[]; onOpen: (id?: string) => void }) {
  const [nodes, setNodes] = useState<ServiceView[]>([]);
  const [error, setError] = useState('');
  useEffect(() => {
    let active = true;
    const load = async () => { try { const r = await api.services(); if (active) { setNodes(r.nodes); setError(''); } } catch { if (active) setError('服务检测快照暂时不可用'); } };
    void load(); const timer = window.setInterval(() => { if (!document.hidden) void load(); }, 15000);
    return () => { active = false; clearInterval(timer); };
  }, []);
  return <div className="service-glance">{error && <Notice error>{error}</Notice>}{hosts.map(h => {
    const v = nodes.find(n => n.server_id === h.id);
    return <button className="service-host-link" key={h.id} onClick={() => onOpen(h.id)}><Server size={17}/><span><strong>{h.name}</strong><small>{v?.collected_at ? `${v.items.filter(i => i.kind !== 'ports' && running(i)).length} 个服务 / 容器运行中 · ${viewLabel(v)}` : viewLabel(v)}</small></span><ArrowRight size={14}/></button>;
  })}{!hosts.length && <Empty title="接入服务器后查看运行服务"/>}<p className="card-foot">基于 SSH 只读检测快照 · 点击服务器检测或查看详情</p></div>;
}

export default function Services({ hosts, focusServer = '', onServer, onAsk }: { hosts: Host[]; focusServer?: string; onServer: (id: string) => void; onAsk: (id: string, text: string) => void }) {
  const [nodes, setNodes] = useState<ServiceView[]>([]);
  const [server, setServer] = useState(focusServer);
  const [kind, setKind] = useState('all');
  const [state, setState] = useState('running');
  const [query, setQuery] = useState('');
  const [busy, setBusy] = useState<string[]>([]);
  const [error, setError] = useState('');
  const scanning = useRef(new Set<string>());
  const mounted = useRef(true);
  useEffect(() => { setServer(focusServer); }, [focusServer]);
  const load = useCallback(async () => {
    try { const r = await api.services(); if (mounted.current) { setNodes(r.nodes); setError(''); } }
    catch (e) { if (mounted.current) setError(e instanceof Error ? e.message : '读取服务列表失败'); }
  }, []);
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
        return [...old.filter(n => n.server_id !== id), { ...prev, status: 'failed', error: e instanceof Error ? e.message : '检测失败' }];
      });
    } finally { scanning.current.delete(id); if (mounted.current) setBusy([...scanning.current]); }
  }
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
  return <div className="page services-page"><PageHeading eyebrow="OFFICIAL MODULE / SERVICES" title="服务管理" note="按服务器查看真实的系统服务、Docker 容器和监听端口。" actions={<Button variant="primary" busy={busy.length > 0} disabled={!visibleHosts.length} onClick={() => void scanAll()}><RefreshCw size={14}/>{server ? '检测此服务器' : '检测全部服务器'}</Button>}/>
    <div className="probe-summary"><div><Server size={16}/><span>服务器<strong>{visibleHosts.length}</strong></span></div><div><Activity size={16}/><span>运行服务 / 容器（快照）<strong>{hasData ? all.filter(i => i.kind !== 'ports' && running(i)).length : '—'}</strong></span></div><div><Box size={16}/><span>Docker 容器<strong>{hasData ? all.filter(i => i.kind === 'docker').length : '—'}</strong></span></div><div><Activity size={16}/><span>监听端口条目<strong>{hasData ? all.filter(i => i.kind === 'ports').length : '—'}</strong></span></div></div>
    <div className="list-toolbar services-toolbar"><label>服务器<select aria-label="筛选服务器" value={server} onChange={e => setServer(e.target.value)}><option value="">全部服务器</option>{hosts.map(h => <option value={h.id} key={h.id}>{h.name}</option>)}</select></label><label>类型<select aria-label="筛选服务类型" value={kind} onChange={e => setKind(e.target.value)}><option value="all">全部类型</option>{Object.entries(sourceNames).map(([id, name]) => <option key={id} value={id}>{name}</option>)}</select></label><label>状态<select aria-label="筛选运行状态" value={state} onChange={e => setState(e.target.value)}><option value="running">运行中 / 监听中</option><option value="all">全部状态</option><option value="issues">异常状态</option></select></label><label className="search"><Search size={14}/><input aria-label="搜索服务" placeholder="搜索服务、容器、镜像、端口…" value={query} onChange={e => setQuery(e.target.value)}/></label></div>
    <p className="service-readonly"><Clock3 size={13}/>已检测 {snapshots.length} / {visibleHosts.length} 台。显示上次检测快照，点击检测获取最新状态。采集不读取环境变量、启动参数、日志或配置文件。</p>
    {error && <Notice error>{error}</Notice>}
    <div className="services-hosts">{visibleHosts.map(h => {
      const v = nodes.find(n => n.server_id === h.id) || pendingView(h.id);
      const rows = v.items.filter(i => (kind === 'all' || i.kind === kind) && (state === 'all' || (state === 'issues' ? issue(i) : running(i))) && `${i.name} ${i.image || ''} ${i.ports || ''} ${i.detail}`.toLowerCase().includes(query.toLowerCase()));
      const stale = !!v.collected_at && Date.now() - Date.parse(v.collected_at) > 120000;
      return <section className="card service-node" key={h.id}><header className="card-heading"><div><h2><Server size={16}/><button className="text-btn" onClick={() => onServer(h.id)}>{h.name}</button><span className={`tag ${v.status === 'failed' ? 'danger' : ''}`}>{busy.includes(h.id) ? '正在检测…' : viewLabel(v)}</span>{stale && <span className="tag warn">快照较早</span>}</h2><small className="muted">{h.host} · {when(v.collected_at)}</small></div><div className="service-node-actions"><Button busy={busy.includes(h.id)} onClick={() => void scan(h.id)}><RefreshCw size={13}/>检测</Button><Button onClick={() => onAsk(h.id, `检查服务器 ${h.name}（id=${h.id}）上运行的服务、容器和监听端口。先调用 inspect_services 实时检测并说明异常，修改前走审批。`)}>让 AI 检查</Button></div></header>
        {v.error && <Notice error>{v.error}{v.collected_at ? '。下方保留上次成功检测的数据。' : ''}</Notice>}
        {!!v.collected_at && <div className="service-source-status">{Object.entries(v.sources).map(([id, s]) => <span key={id} className={s.status === 'ok' ? 'muted' : 'warn'}>{sourceNames[id]}：{s.status === 'ok' ? '已检测' : s.detail || '不可用'}{s.truncated ? '（仅显示前 500 项）' : ''}</span>)}{v.sources.ports?.status === 'ok' && <span className="muted">非 root 用户可能看不到其他进程的名称</span>}</div>}
        {rows.length ? <div className="table-scroll"><table className="data-table"><thead><tr><th>类型</th><th>服务 / 容器</th><th>状态</th><th>镜像 / 详情</th><th>监听 / 映射端口</th><th/></tr></thead><tbody>{rows.map((i, index) => <tr key={`${i.kind}-${i.name}-${i.ports}-${index}`}><td>{sourceNames[i.kind]}</td><td><strong className="mono">{i.name}</strong></td><td><span className={`status ${issue(i) ? 'danger' : running(i) ? 'ok' : 'muted'}`}><i/>{stateNames[i.state] || i.state}</span></td><td className="service-details">{i.image && <strong className="mono">{i.image}</strong>}<small>{i.detail}</small></td><td className="mono service-ports">{i.ports || '—'}</td><td><button className="text-btn" onClick={() => onAsk(h.id, `检查服务器 ${h.name}（id=${h.id}）的${sourceNames[i.kind]} ${JSON.stringify(i.name)}。先只读确认状态和原因，修改前走审批。`)}>AI 检查</button></td></tr>)}</tbody></table></div> : <Empty title={busy.includes(h.id) ? '正在读取服务器状态' : !v.collected_at ? (v.status === 'failed' ? '暂未获取服务数据' : '点击检测，查看这台服务器运行的服务') : '当前筛选下没有匹配项'} body={!v.collected_at ? '使用已保存的 SSH 凭据，只读采集，通常需要几秒钟。' : '可切换全部状态或检查采集来源是否可用。'}/>}
      </section>;
    })}</div>{!hosts.length && <Empty title="还没有服务器" body="先接入服务器，再查看其运行服务。"/>}
  </div>;
}
