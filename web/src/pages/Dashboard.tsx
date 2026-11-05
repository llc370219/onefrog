import { useEffect, useState } from "react";
import { Activity, ArrowRight, Bot, Box, CalendarClock, Code2, HardDrive, LayoutGrid, Server, ShieldCheck, SlidersHorizontal, TriangleAlert, Wallet } from 'lucide-react';
import { api, type ApprovalDecision, type ProbeNode, type Host, type Mod, type Hist, type BotRow, type AssetRecord, type ServiceView } from '../api';
import PanelTaskResult, { type PanelJob } from '../components/PanelTaskResult';
import { Button, Empty, PageHeading } from '../components/ui';
import ProbeCard from '../components/ProbeCard';
import ProbeSetup from '../components/ProbeSetup';
import CustomModuleFrame from '../components/CustomModuleFrame';
import { AssetGlance } from './Assets';
import { DevelopGlance } from './Develop';
import { ServiceGlance } from './Services';

const moduleIcons = { probe: Server, services: Activity, alerts: TriangleAlert, compare: HardDrive, assets: Wallet, develop: Code2 };
const order = ['probe', 'alerts', 'services', 'compare', 'assets', 'develop'];
const rank = (id: string) => { const idx = order.indexOf(id); return idx === -1 ? 99 : idx; };
export default function Dashboard({ hosts, mods, history, bots, job, sending, onSelect, onRun, onAnswer, onProvide, onDecideApproval, onModules, onSettings, onServers, onBots, onHistory, onAssets, onOpenModule }: { hosts: Host[]; mods: Mod[]; history: Hist[]; bots: BotRow[]; job: PanelJob | null; sending: boolean; onSelect: (id: string) => void; onRun: (text: string, serverId?: string) => void; onAnswer: (values: string[], custom: string) => void; onProvide: (value: string) => void; onDecideApproval: (id: string, dec: ApprovalDecision) => void; onModules: () => void; onSettings: () => void; onServers: () => void; onBots: () => void; onHistory: (filter?: string, query?: string) => void; onAssets: () => void; onOpenModule: (id: string, serverId?: string) => void }) {
  const [probeNodes,setProbeNodes]=useState<ProbeNode[]>([]);
  const [probeError,setProbeError]=useState('');
  const [setup,setSetup]=useState('');
  const setupHost=hosts.find(h=>h.id===setup);
  const [assets,setAssets]=useState<AssetRecord[]>([]);
  const [expiringSubscriptions,setExpiringSubscriptions]=useState<number|null|undefined>(undefined);
  const [services,setServices]=useState<ServiceView[]>([]);
  useEffect(()=>{let active=true;async function refresh(){const [res,ast,svc]=await Promise.all([api.monitor().catch(()=>null),api.assets().catch(()=>null),api.services().catch(()=>null)]);if(active){if(res){setProbeNodes(res.nodes);setProbeError('');}else setProbeError('探针数据暂时不可用');setAssets(ast?.records||[]);setExpiringSubscriptions(ast?.summary.expiring??null);setServices(svc?.nodes||[]);}}refresh();const timer=window.setInterval(()=>{if(!document.hidden)refresh();},15000);return()=>{active=false;clearInterval(timer);};},[]);
  hosts=hosts.map(h=>{const n=probeNodes.find(n=>n.server_id===h.id);const s=n?.latest;return {...h,status:n?.status==='pending'?'unknown':n?.status||'unknown',up:n?.status==='online',disk:s&&s.disk_total?s.disk_used/s.disk_total*100:0,load:s?.load.map(v=>v.toFixed(2)).join(' / ')||'—'};});
  const online = probeNodes.filter(n=>n.status==='online').length;
  const offline = hosts.filter(h => h.status === 'offline');
  const blocked = history.filter(h => h.blocked);
  const warning = hosts.filter(h => h.up && h.disk >= 70);
  const enabled = mods.filter(m => m.enabled && (m.id !== 'activity' || m.type === 'custom')).sort((a,b) => rank(a.id) - rank(b.id));
  const stats = [
    { label: '在线服务器', value: online.toString(), suffix: `/ ${hosts.length.toString()}`, icon: Server, note: `${probeNodes.filter(n=>n.status==='pending').length} 台等待探针上报`, tone: '', action: onServers },
    { label: '需要关注', value: (offline.length + warning.length).toString(), suffix: '', icon: TriangleAlert, note: `${offline.length} 台离线 · ${warning.length} 台磁盘偏高`, tone: offline.length + warning.length ? 'warn' : '', action: onServers },
    { label: '协作 Bot', value: bots.length.toString(), suffix: '', icon: Bot, note: bots.length ? bots.map(b => b.name).join(' / ') : '尚未创建 Bot', tone: '', action: onBots },
    { label: '即将到期', value: expiringSubscriptions==null?'—':String(expiringSubscriptions), suffix: '', icon: CalendarClock, note: expiringSubscriptions===undefined?'正在读取订阅…':expiringSubscriptions===null?'订阅数据暂时不可用':'7 天内到期的订阅', tone: expiringSubscriptions?'warn':'', action: onAssets },
  ];
  return <div className="page dashboard"><PageHeading eyebrow="WORKSPACE / OVERVIEW" title="面板" note="掌握服务器状态，让助理直接处理。" actions={<><Button onClick={onSettings}><SlidersHorizontal size={15}/>设置</Button><Button variant="primary" onClick={onModules}><LayoutGrid size={15}/>选取模块</Button></>}/>
    <div className="metrics">{stats.map(({label, value, suffix, icon: Icon, note, tone, action}) => <button type="button" className="metric metric-btn" key={label} onClick={action}><div className="metric-top">{label}<Icon size={16} strokeWidth={1.5}/></div><div className={`metric-value ${tone}`}>{value}<span>{suffix}</span></div><p>{note}</p></button>)}</div>
    <PanelTaskResult hosts={hosts} sending={sending} job={job} onAnswer={onAnswer} onProvide={onProvide} onDecideApproval={onDecideApproval}/>
    <div className="section-label"><span><span className="live-dot"/>工作台模块 <small>{enabled.length} 个已启用</small></span><span className="muted">按需组合你的工作台</span></div>
    {!enabled.length ? <Empty title="定制你的第一个工作台" body="选择服务器、服务和告警模块，集中查看需要的信息。" action={<Button onClick={onModules}>选取模块</Button>}/> : <div className="dashboard-grid">{enabled.map(mod => {
      const Icon = mod.type === 'custom' ? Code2 : (moduleIcons[mod.id as keyof typeof moduleIcons] || Box);
      const cardClass = mod.type === 'custom' ? 'module-custom' : `module-${mod.id}`;
      return <section className={`card ${cardClass}`} key={mod.id}><header className="card-heading"><h2><Icon size={16}/>{mod.name}</h2>{mod.id === 'assets' ? <button className="text-btn" onClick={onAssets}>打开面板<ArrowRight size={13}/></button> : mod.id === 'develop' ? <button className="text-btn" onClick={() => onOpenModule('develop')}>进入自定义模块<ArrowRight size={13}/></button> : mod.type === 'custom' ? <button className="text-btn" onClick={() => onOpenModule(mod.id)}>打开模块<ArrowRight size={13}/></button> : mod.id === 'services' ? <button className="text-btn" onClick={() => onOpenModule('services')}>打开服务监控<ArrowRight size={13}/></button> : (mod.id === 'probe' || mod.id === 'services' || mod.id === 'compare') ? <button className="text-btn" onClick={onServers}>查看服务器<ArrowRight size={13}/></button> : <span className="tag">内置模块</span>}</header>
      {mod.id === 'probe' && <>{probeError&&<p className="card-foot danger">{probeError}</p>}{hosts.length?<div className="probe-card-grid dashboard-probes">{hosts.map(h=><ProbeCard key={h.id} onAssetSaved={row=>setAssets(cur=>[...cur.filter(r=>r.id!==row.id),row])} compact host={h} node={probeNodes.find(n=>n.server_id===h.id)} asset={assets.find(r=>r.server_id===h.id||(!!r.ip&&r.ip===h.host))} services={services.find(v=>v.server_id===h.id)} onAsset={onAssets} onDetails={()=>{onSelect(h.id);onServers();}} onConnect={()=>setSetup(h.id)} onChat={()=>{onSelect(h.id);onRun("检查服务器 "+h.name+"（id="+h.id+"）。先调用 panel test_server，再只读查看状态。不要删除，不要改配置。", h.id);}}/>)}</div>:<Empty title="还没有服务器" action={<Button onClick={onServers}>接入服务器</Button>}/>}</>}
      {mod.id === 'alerts' && <div className="alert-list">{offline.map(h => <div className="alert-item" key={h.id}><TriangleAlert size={16} className="danger"/><div><strong>{h.name} 已离线</strong><p>检查网络与 SSH 连接</p><button className="text-btn" onClick={() => {onSelect(h.id); onRun("检查 "+h.name+"（id="+h.id+"）为什么离线。调用 panel test_server，只说明原因，不要改配置。", h.id);}}>让助理检查<ArrowRight size={13}/></button></div></div>)}{warning.map(h => <div className="alert-item" key={h.id}><HardDrive size={16} className="warn"/><div><strong>{h.name} 磁盘使用 {h.disk}%</strong><p>建议检查占用，暂不执行清理</p><button className="text-btn" onClick={() => {onSelect(h.id); onRun("查看 "+h.name+"（id="+h.id+"）磁盘占用，只做检查，不要删除。", h.id);}}>让助理检查<ArrowRight size={13}/></button></div></div>)}{!offline.length && !warning.length && <Empty title="暂时没有主机告警" body="离线与磁盘偏高会显示在这里。"/>}{blocked.length > 0 && <button type="button" className="alert-foot text-btn" onClick={() => onHistory('blocked', '')}><ShieldCheck size={14}/>{blocked.length} 次高危操作已拦截 · 查看详情<ArrowRight size={12}/></button>}</div>}
      {mod.id === 'services' && <ServiceGlance hosts={hosts} onOpen={id => {onSelect(id || '');onOpenModule('services', id);}}/>}
      {mod.id === 'compare' && <div className="compare-list">{hosts.filter(h => h.up).map(h => <div key={h.id}><span className="mono">{h.name}</span><span className={`meter ${h.disk >= 70 ? 'warn' : ''}`}><i style={{width: `${h.disk}%`}}/></span><span className="mono muted">{h.disk}%</span></div>)}</div>}
      
      {mod.id === 'assets' && <AssetGlance/>}
      {mod.id === 'develop' && <DevelopGlance onOpenStudio={() => onOpenModule('develop')} onOpenModule={onOpenModule}/>}
      {mod.type === 'custom' && <CustomModuleFrame moduleId={mod.id} title={mod.name} compact/>}</section>;
    })}</div>}
    {setupHost&&<ProbeSetup key={setupHost.id} host={setupHost} enrolled={!!probeNodes.find(n=>n.server_id===setup)?.enrolled} province={probeNodes.find(n=>n.server_id===setup)?.province} pingIntervalSeconds={probeNodes.find(n=>n.server_id===setup)?.ping_interval_seconds} onClose={()=>setSetup('')} onIssued={()=>{api.monitor().then(r=>setProbeNodes(r.nodes)).catch(()=>setProbeError('探针数据暂时不可用'));}}/>}
    <footer className="workspace-foot"><span><ShieldCheck size={13}/>凭据加密存储 · 执行前审核</span><span>ONEFROG / OPERATIONS</span></footer>
  </div>;
}
