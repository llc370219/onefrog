import { useEffect, useState } from "react";
import { Activity, ArrowRight, Bot, Box, HardDrive, LayoutGrid, Server, ShieldCheck, SlidersHorizontal, Terminal, TriangleAlert, Wallet } from 'lucide-react';
import { api, type ProbeNode, type Host, type Mod, type Hist, type BotRow } from '../api';
import { Button, Empty, PageHeading, Status } from '../components/ui';
import ProbeCard from '../components/ProbeCard';
import { AssetGlance } from './Assets';

const moduleIcons = { probe: Server, services: Activity, alerts: TriangleAlert, activity: Terminal, compare: HardDrive, assets: Wallet };
const order = ['probe', 'alerts', 'services', 'compare', 'activity', 'assets'];
export default function Dashboard({ hosts, mods, history, bots, onSelect, onChat, onModules, onSettings, onServers, onAssets }: { hosts: Host[]; mods: Mod[]; history: Hist[]; bots: BotRow[]; selected: string; onSelect: (id: string) => void; onChat: (text?: string) => void; onModules: () => void; onSettings: () => void; onServers: () => void; onAssets: () => void }) {
  const [probeNodes,setProbeNodes]=useState<ProbeNode[]>([]);
  const [probeError,setProbeError]=useState('');
  useEffect(()=>{let active=true;async function refresh(){try{const res=await api.monitor();if(active){setProbeNodes(res.nodes);setProbeError('');}}catch{if(active)setProbeError('探针数据暂时不可用');}}refresh();const timer=window.setInterval(()=>{if(!document.hidden)refresh();},15000);return()=>{active=false;clearInterval(timer);};},[]);
  hosts=hosts.map(h=>{const n=probeNodes.find(n=>n.server_id===h.id);const s=n?.latest;return {...h,status:n?.status==='pending'?'unknown':n?.status||'unknown',up:n?.status==='online',disk:s&&s.disk_total?s.disk_used/s.disk_total*100:0,load:s?.load.map(v=>v.toFixed(2)).join(' / ')||'—'};});
  const online = probeNodes.filter(n=>n.status==='online').length;
  const offline = hosts.filter(h => h.status === 'offline');
  const blocked = history.filter(h => h.blocked);
  const warning = hosts.filter(h => h.up && h.disk >= 70);
  const enabled = mods.filter(m => m.enabled).sort((a,b) => order.indexOf(a.id) - order.indexOf(b.id));
  const stats = [
    { label: '在线服务器', value: online.toString().padStart(2,'0'), suffix: `/ ${hosts.length.toString().padStart(2,'0')}`, icon: Server, note: `${probeNodes.filter(n=>n.status==='pending').length} 台等待探针上报`, tone: '' },
    { label: '需要关注', value: (offline.length + warning.length).toString().padStart(2,'0'), suffix: '', icon: TriangleAlert, note: `${offline.length} 台离线 · ${warning.length} 台磁盘偏高`, tone: offline.length + warning.length ? 'warn' : '' },
    { label: '协作 Bot', value: bots.length.toString().padStart(2,'0'), suffix: '', icon: Bot, note: bots.length ? bots.map(b => b.name).join(' / ') : '尚未创建 Bot', tone: '' },
    { label: '安全拦截', value: blocked.length.toString().padStart(2,'0'), suffix: '', icon: ShieldCheck, note: '当前操作记录中的高危拦截', tone: '' },
  ];
  return <div className="page dashboard"><PageHeading eyebrow="WORKSPACE / OVERVIEW" title="面板" note="掌握服务器状态，让 Bot 接手下一步。" actions={<><Button onClick={onSettings}><SlidersHorizontal size={15}/>设置</Button><Button variant="primary" onClick={onModules}><LayoutGrid size={15}/>选取模块</Button></>}/>
    <div className="metrics">{stats.map(({label, value, suffix, icon: Icon, note, tone}) => <section className="metric" key={label}><div className="metric-top">{label}<Icon size={16} strokeWidth={1.5}/></div><div className={`metric-value ${tone}`}>{value}<span>{suffix}</span></div><p>{note}</p></section>)}</div>
    <section className="agent-launch"><div className="launch-icon"><Bot size={23} strokeWidth={1.4}/></div><div><strong>一句话，开始处理。</strong><p>主控分派任务，执行过程和结果都在对话中。</p></div><Button onClick={() => onChat()} variant="ghost">打开AI 助理<ArrowRight size={16}/></Button></section>
    <div className="section-label"><span><span className="live-dot"/>工作台模块 <small>{enabled.length} 个已启用</small></span><span className="muted">按需组合你的工作台</span></div>
    {!enabled.length ? <Empty title="定制你的第一个工作台" body="选择服务器、服务和告警模块，集中查看需要的信息。" action={<Button onClick={onModules}>选取模块</Button>}/> : <div className="dashboard-grid">{enabled.map(mod => {
      const Icon = moduleIcons[mod.id as keyof typeof moduleIcons] || Box;
      return <section className={`card module-${mod.id}`} key={mod.id}><header className="card-heading"><h2><Icon size={16}/>{mod.name}</h2>{mod.id === 'assets' ? <button className="text-btn" onClick={onAssets}>打开面板<ArrowRight size={13}/></button> : mod.id === 'probe' ? <button className="text-btn" onClick={onServers}>全部服务器<ArrowRight size={13}/></button> : <span className="tag">{mod.id === 'activity' ? '操作记录' : '内置模块'}</span>}</header>
      {mod.id === 'probe' && <>{probeError&&<p className="card-foot danger">{probeError}</p>}{hosts.length?<div className="probe-card-grid dashboard-probes">{hosts.map(h=><ProbeCard key={h.id} compact host={h} node={probeNodes.find(n=>n.server_id===h.id)} onDetails={()=>{onSelect(h.id);onServers();}} onConnect={onServers} onChat={()=>{onSelect(h.id);onChat();}}/>)}</div>:<Empty title="还没有服务器" action={<Button onClick={onServers}>接入服务器</Button>}/>}</>}
      {mod.id === 'alerts' && <div className="alert-list">{offline.map(h => <div className="alert-item" key={h.id}><TriangleAlert size={16} className="danger"/><div><strong>{h.name} 已离线</strong><p>检查网络与 SSH 连接</p><button className="text-btn" onClick={() => {onSelect(h.id); onChat(`检查 ${h.name} 的连接状态，说明失败原因。`);}}>交给 Bot<ArrowRight size={13}/></button></div></div>)}{warning.map(h => <div className="alert-item" key={h.id}><HardDrive size={16} className="warn"/><div><strong>{h.name} 磁盘使用 {h.disk}%</strong><p>建议检查占用，暂不执行清理</p><button className="text-btn" onClick={() => {onSelect(h.id); onChat(`查看 ${h.name} 磁盘占用，只做检查。`);}}>检查磁盘<ArrowRight size={13}/></button></div></div>)}{!offline.length && !warning.length && <Empty title="暂时没有主机告警" body="离线与磁盘偏高会显示在这里。"/>}{blocked.length > 0 && <div className="alert-foot"><ShieldCheck size={14}/>{blocked.length} 次高危操作已拦截</div>}</div>}
      {mod.id === 'services' && <div className="service-grid">{hosts.filter(h => h.role).map(h => <div className="service-item" key={h.id}><div className="service-icon"><Box size={19} strokeWidth={1.4}/></div><div><strong className="mono">{h.role}</strong><small>{h.name}</small></div><Status status={h.status}/></div>)}<p className="card-foot">按主机状态展示，服务进程需通过 Bot 进一步检查。</p></div>}
      {mod.id === 'activity' && <>{history.length ? <div className="table-scroll"><table className="data-table"><thead><tr><th>时间</th><th>Bot / 服务器</th><th>执行内容</th><th>结果</th></tr></thead><tbody>{history.slice(0,5).map(h => <tr key={h.id}><td className="mono muted">{h.time}</td><td><strong>{h.bot}</strong><small>{h.server}</small></td><td className="command-cell mono" title={h.action}>{h.action}</td><td><Status status={h.status}/></td></tr>)}</tbody></table></div> : <Empty title="还没有操作记录"/>}</>}
      {mod.id === 'compare' && <div className="compare-list">{hosts.filter(h => h.up).map(h => <div key={h.id}><span className="mono">{h.name}</span><span className={`meter ${h.disk >= 70 ? 'warn' : ''}`}><i style={{width: `${h.disk}%`}}/></span><span className="mono muted">{h.disk}%</span></div>)}</div>}
      
      {mod.id === 'assets' && <AssetGlance/>}</section>;
    })}</div>}
    <footer className="workspace-foot"><span><ShieldCheck size={13}/>凭据加密存储 · 执行前审核</span><span>ONEFROG / OPERATIONS</span></footer>
  </div>;
}
