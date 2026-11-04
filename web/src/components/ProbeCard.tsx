import { useEffect, useRef } from 'react';
import { ArrowDown, ArrowUp, Bot, Boxes, ChevronRight, Radio, ShieldAlert, Wallet } from 'lucide-react';
import type { AssetRecord, Host, PingMetric, ProbeNode, ServiceView } from '../api';
import { money } from '../pages/Assets';
import { cssVar, useTheme } from '../theme';

export function bytes(value?:number,rate=false){if(value===undefined)return '—';let n=value;const units=['B','KiB','MiB','GiB','TiB','PiB'];let i=0;while(n>=1024&&i<units.length-1){n/=1024;i++;}return `${n.toFixed(i===0?0:n>=100?0:1)} ${units[i]}${rate?'/s':''}`;}
export function uptime(seconds?:number){if(seconds===undefined)return '—';const d=Math.floor(seconds/86400),h=Math.floor(seconds%86400/3600),m=Math.floor(seconds%3600/60);return d?`${d} 天 ${h} 小时`:h?`${h} 小时 ${m} 分钟`:`${m} 分钟`;}
export function probeStatus(node?:ProbeNode){return node?.status||'pending';}
export function Sparkline({values,label}: {values:number[];label:string}){
 const ref=useRef<HTMLCanvasElement>(null);
 const {theme}=useTheme();
 useEffect(()=>{const canvas=ref.current;if(!canvas)return;const ctx=canvas.getContext('2d');if(!ctx)return;const width=canvas.clientWidth,height=canvas.clientHeight,scale=window.devicePixelRatio||1;canvas.width=width*scale;canvas.height=height*scale;ctx.scale(scale,scale);ctx.clearRect(0,0,width,height);ctx.strokeStyle=cssVar('--chart-grid','#8882');ctx.lineWidth=1;for(let i=1;i<3;i++){ctx.beginPath();ctx.moveTo(0,height*i/3);ctx.lineTo(width,height*i/3);ctx.stroke();}if(values.length<2)return;const max=Math.max(10,...values)*1.15;ctx.beginPath();values.forEach((v,i)=>{const x=width*i/(values.length-1),y=height-4-v/max*(height-8);if(i===0)ctx.moveTo(x,y);else ctx.lineTo(x,y);});ctx.strokeStyle=cssVar('--chart-line','#a5b2ac');ctx.lineWidth=1.5;ctx.stroke();},[values,theme]);
 return <canvas ref={ref} className="probe-sparkline" role="img" aria-label={label}/>;
}
const cycleUnit: Record<string, string> = { monthly: '月', quarterly: '季', yearly: '年', once: '一次性' };
const pct = (used?: number, total?: number) => (used !== undefined && total ? used / total * 100 : undefined);
/** Short uptime for the card header: "12 天", "5 小时", "8 分钟". */
function shortUptime(seconds?: number) { if (seconds === undefined) return ''; const d = Math.floor(seconds / 86400), h = Math.floor(seconds / 3600), m = Math.floor(seconds / 60); return d ? `${d} 天` : h ? `${h} 小时` : `${m} 分钟`; }
function level(v?: number, warn = 70, danger = 90) { return v === undefined ? '' : v >= danger ? 'danger' : v >= warn ? 'warn' : ''; }

function Meter({ label, value, text, title }: { label: string; value?: number; text?: string; title?: string }) {
  const v = value === undefined ? undefined : Math.max(0, Math.min(100, value));
  return <div className={`pc-meter ${level(v, 75, 90)}`} title={title}><span>{label}<strong>{text ?? (v === undefined ? '—' : `${v.toFixed(v >= 10 ? 0 : 1)}%`)}</strong></span><i><b style={{ width: `${v ?? 0}%` }}/></i></div>;
}
/** Compact size for the spec line: "8G", "512M", "1.5T". */
function size(n?: number) { if (!n) return ''; const g = n / 1024 ** 3; return g >= 1024 ? `${+(g / 1024).toFixed(1)}T` : g >= 1 ? `${+g.toFixed(g >= 10 ? 0 : 1)}G` : `${Math.round(n / 1024 ** 2)}M`; }

function Line({ p }: { p: PingMetric }) {
  const lat = p.latency_ms, loss = p.loss_percent;
  const tone = lat === null || loss === 100 ? 'danger' : (loss ?? 0) >= 10 || lat >= 200 ? 'danger' : (loss ?? 0) > 0 || lat >= 100 ? 'warn' : 'ok';
  return <span className={`pc-isp ${tone}`} title={`${p.target} · ${p.samples} 次采样`}><em>{p.name}</em><strong>{lat === null ? '超时' : `${Math.round(lat)}ms`}</strong><small>{loss === null ? '—' : `${loss}%`}</small></span>;
}

/** Running services from the 服务管理 snapshot: containers first, then systemd units. */
function serviceNames(view?: ServiceView) {
  if (!view || view.status === 'pending') return null;
  const items = view.items.filter(i => i.kind !== 'ports' && /^(running|active|up)/i.test(i.state || 'running'));
  return items.sort((a, b) => (a.kind === b.kind ? 0 : a.kind === 'docker' ? -1 : 1)).map(i => i.name.replace(/\.service$/, ''));
}

export default function ProbeCard({ highlight, host, node, asset, services, onDetails, onConnect, onChat, onTest, onAsset, compact = false }: { highlight?: boolean; host: Host; node?: ProbeNode; asset?: AssetRecord; services?: ServiceView; onDetails: () => void; onConnect?: () => void; onChat?: () => void; onTest?: () => void; onAsset?: () => void; compact?: boolean }) {
  const s = node?.latest, status = probeStatus(node), live = status === 'online';
  const label = live ? '在线' : status === 'offline' ? '离线' : node?.enrolled ? '等待上报' : '未接探针';
  const isp = (s?.pings || []).filter(p => /^(电信|联通|移动)$/.test(p.name));
  const tr = node?.traffic;
  const names = serviceNames(services);
  const renew = asset ? (asset.derived_status === 'expired' ? '已到期' : asset.expires_at ? `剩 ${asset.days_left} 天` : '') : '';
  const price = asset && asset.amount_cents ? `${money(asset.amount_cents, asset.currency || 'CNY')}${asset.cycle && asset.cycle !== 'once' ? '/' + (cycleUnit[asset.cycle] || asset.cycle) : ''}` : '';
  const assetTone = asset?.derived_status === 'expired' ? 'danger' : asset?.derived_status === 'expiring' ? 'warn' : '';
  const spec = s ? [s.os.replace(/ GNU\/Linux| LTS| \(.*\)/g, ''), s.arch, `${s.cpu_cores}C ${size(s.memory_total)}`, size(s.disk_total)].filter(Boolean).join(' · ') : (host.role || host.host);
  const load = s?.load[0];
  return <article className={`probe-card pc ${highlight ? 'newly-added' : ''} ${live ? 'live' : ''} ${status} ${compact ? 'probe-compact' : ''}`}>
    <header className="pc-head">
      <span className={`pc-dot ${status}`} aria-hidden="true"/>
      <div className="pc-title"><button onClick={onDetails}>{host.name}</button><small title={s?.cpu_model}>{spec}</small></div>
      <span className={`pc-status ${status}`}>{label}{live && s ? ` · ${shortUptime(s.uptime_seconds)}` : ''}</span>
    </header>
    {host.status === 'host_key_mismatch' && <p className="probe-stale ssh-mismatch"><ShieldAlert size={12}/><span>SSH 主机指纹与已保存的不一致，已停止连接。{onTest && <button className="text-btn" onClick={onTest}>核对指纹</button>}</span></p>}
    {s ? <>
      <div className="pc-meters">
        <Meter label="CPU" value={s.cpu_percent} title={s.cpu_model}/>
        <Meter label="内存" value={pct(s.memory_used, s.memory_total)} title={`${bytes(s.memory_used)} / ${bytes(s.memory_total)}`}/>
        <Meter label="磁盘" value={pct(s.disk_used, s.disk_total)} title={`${bytes(s.disk_used)} / ${bytes(s.disk_total)}`}/>
        <Meter label="负载" value={load === undefined ? undefined : load / s.cpu_cores * 100} text={load === undefined ? '—' : load.toFixed(2)} title={`1 / 5 / 15 分钟平均负载：${s.load.map(v => v.toFixed(2)).join(' / ') || '—'}（${s.cpu_cores} 核）`}/>
      </div>
      <div className="pc-net">
        <span className="pc-rate" title="实时速率"><ArrowDown size={12}/>{bytes(s.rx_rate, true)}<ArrowUp size={12}/>{bytes(s.tx_rate, true)}</span>
        {tr ? <span className="pc-traffic" title={`本月（${tr.period}）自 ${new Date(tr.since).toLocaleDateString('zh-CN')} 起统计；单向计费看入站或出站，双向计费看合计`}>本月 入 {bytes(tr.rx)} · 出 {bytes(tr.tx)} · 双向 {bytes(tr.rx + tr.tx)}</span> : <span className="pc-traffic muted">本月流量统计中</span>}
      </div>
      {isp.length ? <div className="pc-isps" aria-label="三网延迟与丢包"><span className="pc-province" title="三网延迟测试的省份"><Radio size={12}/>{node?.province?.name || '三网'}</span>{isp.map((p, i) => <Line key={i} p={p}/>)}</div> : <div className="pc-isps none"><Radio size={12}/>未设置三网测速{onConnect && <button className="text-btn" onClick={onConnect}>选择省份</button>}</div>}
      {status === 'offline' && <p className="probe-stale">以上是最后一次上报的数据，已停止刷新。</p>}
    </> : <div className="probe-empty-body"><strong>{node?.enrolled ? '探针已接入，等待第一次上报' : '还没有监控数据'}</strong><p>{node?.enrolled ? '通常一分钟内出现。如果一直没有，请检查节点能否访问面板地址。' : '接入只读探针后显示 CPU、内存、磁盘、负载、实时速率、流量和三网延迟。'}</p></div>}
    <footer className="pc-foot">
      {asset && (renew || price) ? <button type="button" className={`pc-chip ${assetTone}`} onClick={onAsset} disabled={!onAsset}><Wallet size={12}/>{[renew, price].filter(Boolean).join(' · ')}</button> : onAsset ? <button type="button" className="pc-chip muted" onClick={onAsset}><Wallet size={12}/>登记资费</button> : null}
      <span className="pc-services" title={names?.join('、')}><Boxes size={12}/><span>{names === null ? '服务未检测' : names.length ? names.join(' · ') : '无运行服务'}</span>{!!names && names.length > 2 && <small>{names.length}</small>}</span>
      <span className="pc-actions">{onChat && <button className="pc-icon" onClick={onChat} title="让 Bot 处理这台服务器" aria-label="让 Bot 处理这台服务器"><Bot size={14}/></button>}{!node?.enrolled && onConnect ? <button className="text-btn strong" onClick={onConnect}>接入探针</button> : <button className="text-btn" onClick={onDetails}>详情<ChevronRight size={13}/></button>}</span>
    </footer>
  </article>;
}
