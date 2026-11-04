import { useEffect, useRef } from 'react';
import { ArrowDown, ArrowUp, ChartColumn, Bot, Boxes, ChevronRight, Cpu, Gauge, Database, HardDrive, MemoryStick, Radio, Server, ShieldAlert, Wallet } from 'lucide-react';
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
/** Short uptime: "12 天", "5 小时", "8 分钟". */
function shortUptime(seconds?: number) { if (seconds === undefined) return ''; const d = Math.floor(seconds / 86400), h = Math.floor(seconds / 3600), m = Math.floor(seconds / 60); return d ? `${d} 天` : h ? `${h} 小时` : `${m} 分钟`; }
/** Compact size for the spec line: "8G", "512M", "1.5T". */
function size(n?: number) { if (!n) return ''; const g = n / 1024 ** 3; return g >= 1024 ? `${+(g / 1024).toFixed(1)}T` : g >= 1 ? `${+g.toFixed(g >= 10 ? 0 : 1)}G` : `${Math.round(n / 1024 ** 2)}M`; }

// Where a server is, guessed from the asset's region, the server name or its role.
const places: [RegExp, string, string][] = [
  [/日本|东京|大阪|japan|tokyo|osaka|^(tyo|nrt|hnd|kix|osa)\b/i, 'JP', '日本'],
  [/香港|hong ?kong|^hkg?\b/i, 'HK', '香港'],
  [/台湾|台北|taiwan|taipei|^tpe\b/i, 'TW', '台湾'],
  [/新加坡|singapore|^sin\b|^sg\b/i, 'SG', '新加坡'],
  [/韩国|首尔|korea|seoul|^(icn|sel)\b/i, 'KR', '韩国'],
  [/美国|洛杉矶|圣何塞|西雅图|纽约|芝加哥|达拉斯|弗里蒙特|硅谷|united states|usa|los angeles|san jose|seattle|new york|chicago|dallas|fremont|^(lax|sjc|sea|nyc|ewr|ord|dfw|iad|mia|phx|las)\b|^us\b/i, 'US', '美国'],
  [/德国|法兰克福|germany|frankfurt|^(fra|ber)\b/i, 'DE', '德国'],
  [/英国|伦敦|united kingdom|london|^(lon|lhr)\b|^uk\b/i, 'GB', '英国'],
  [/荷兰|阿姆斯特丹|netherlands|amsterdam|^ams\b/i, 'NL', '荷兰'],
  [/法国|巴黎|france|paris|^(par|cdg)\b/i, 'FR', '法国'],
  [/俄罗斯|莫斯科|russia|moscow|^(mow|svo)\b/i, 'RU', '俄罗斯'],
  [/加拿大|canada|toronto|^(yyz|yvr)\b/i, 'CA', '加拿大'],
  [/澳大利亚|悉尼|australia|sydney|^syd\b/i, 'AU', '澳大利亚'],
  [/中国|北京|上海|广州|深圳|杭州|成都|china|beijing|shanghai|^(pek|pvg|can|szx)\b/i, 'CN', '中国'],
];
export function serverPlace(...hints: (string | undefined)[]) {
  for (const hint of hints) {
    const text = (hint || '').trim();
    if (!text) continue;
    for (const [re, code, name] of places) if (re.test(text)) return { code, name, flag: String.fromCodePoint(...[...code].map(c => 0x1f1a5 + c.charCodeAt(0))) };
  }
  return undefined;
}

const SEGMENTS = 20, HISTORY = 18;
/** Segmented bar: `value` 0–100 lights up that share of the blocks. */
function Blocks({ value, tone }: { value?: number; tone: string }) {
  const lit = value === undefined ? 0 : Math.max(value > 0 ? 1 : 0, Math.round(Math.min(100, value) / 100 * SEGMENTS));
  return <span className={`pc-blocks ${tone}`} aria-hidden="true">{Array.from({ length: SEGMENTS }, (_, i) => <i key={i} className={i < lit ? 'on' : ''}/>)}</span>;
}
function level(v: number | undefined, warn: number, danger: number) { return v === undefined ? '' : v >= danger ? 'danger' : v >= warn ? 'warn' : ''; }

function Meter({ icon: Icon, label, value, text, title, tone }: { icon: typeof Cpu; label: string; value?: number; text?: string; title?: string; tone: string }) {
  const v = value === undefined ? undefined : Math.max(0, Math.min(100, value));
  const state = level(v, 75, 90);
  return <div className={`pc-meter ${state}`} title={title}>
    <span><Icon size={13}/>{label}<strong>{text ?? (v === undefined ? '—' : `${v.toFixed(v >= 10 ? 0 : 1)}%`)}</strong></span>
    <Blocks value={v} tone={state || tone}/>
  </div>;
}

/** Latency grade per sample: ok < 100ms ≤ caution < 200ms ≤ warn < 300ms ≤ danger (and timeouts). */
const latTone = (lat: number | null | undefined) => lat === undefined ? '' : lat === null ? 'danger' : lat < 100 ? 'ok' : lat < 200 ? 'caution' : lat < 300 ? 'warn' : 'danger';
const lossTone = (loss: number | null | undefined) => loss === undefined || loss === null ? '' : loss === 0 ? 'ok' : loss < 10 ? 'caution' : loss < 30 ? 'warn' : 'danger';
function History({ values, tone }: { values: (PingMetric | undefined)[]; tone: (p?: PingMetric) => string }) {
  return <span className="pc-history" aria-hidden="true">{values.map((p, i) => <i key={i} className={tone(p)}/>)}</span>;
}
function IspRow({ name, history }: { name: string; history: (PingMetric | undefined)[] }) {
  const last = history[history.length - 1];
  const lat = last?.latency_ms, loss = last?.loss_percent;
  return <div className="pc-isp" title={last ? `${last.target} · 每次 ${last.samples} 个探测` : undefined}>
    <div><span>{name}</span><strong className={latTone(lat)}>{lat === undefined ? '—' : lat === null ? '超时' : Math.round(lat)}{typeof lat === 'number' && <small>ms</small>}</strong></div>
    <History values={history} tone={p => p ? latTone(p.latency_ms) : ''}/>
    <div><span>丢包</span><strong className={lossTone(loss)}>{loss === undefined || loss === null ? '—' : +loss.toFixed(1)}{typeof loss === 'number' && <small>%</small>}</strong></div>
    <History values={history} tone={p => p ? lossTone(p.loss_percent) : ''}/>
  </div>;
}

/** Running services from the 服务管理 snapshot: containers first, then systemd units. */
function serviceNames(view?: ServiceView) {
  if (!view || view.status === 'pending') return null;
  const items = view.items.filter(i => i.kind !== 'ports' && /^(running|active|up)/i.test(i.state || 'running'));
  return items.sort((a, b) => (a.kind === b.kind ? 0 : a.kind === 'docker' ? -1 : 1)).map(i => i.name.replace(/\.service$/, ''));
}

const modeLabel: Record<string, string> = { both: '双向', out: '单向 · 出站', in: '单向 · 入站', max: '单向 · 取大' };
/** Traffic counted against the plan's allowance, per the provider's billing mode. */
function billed(mode: string, rx: number, tx: number) { return mode === 'out' ? tx : mode === 'in' ? rx : mode === 'max' ? Math.max(rx, tx) : rx + tx; }

export default function ProbeCard({ highlight, host, node, asset, services, onDetails, onConnect, onChat, onTest, onAsset, compact = false }: { highlight?: boolean; host: Host; node?: ProbeNode; asset?: AssetRecord; services?: ServiceView; onDetails: () => void; onConnect?: () => void; onChat?: () => void; onTest?: () => void; onAsset?: () => void; compact?: boolean }) {
  const s = node?.latest, status = probeStatus(node), live = status === 'online';
  const label = live ? '在线' : status === 'offline' ? '离线' : node?.enrolled ? '等待上报' : '未接探针';
  const tr = node?.traffic;
  const names = serviceNames(services);
  const place = serverPlace(asset?.region, host.name, host.role);
  const renew = asset ? (asset.derived_status === 'expired' ? '已到期' : asset.expires_at ? `剩 ${asset.days_left} 天` : '') : '';
  const price = asset && asset.amount_cents ? `${money(asset.amount_cents, asset.currency || 'CNY')}${asset.cycle && asset.cycle !== 'once' ? '/' + (cycleUnit[asset.cycle] || asset.cycle) : ''}` : '';
  const assetTone = asset?.derived_status === 'expired' ? 'danger' : asset?.derived_status === 'expiring' ? 'warn' : '';
  const spec = s ? [place?.name, `${s.cpu_cores} vCPU`, size(s.memory_total), size(s.disk_total), s.os.replace(/ GNU\/Linux| LTS| \(.*\)/g, '')].filter(Boolean).join(' · ') : [place?.name, host.role || host.host].filter(Boolean).join(' · ');
  const tag = asset?.plan || asset?.provider || (s ? s.arch : '');
  const load = s?.load[0];
  const mode = asset?.traffic_mode || 'both';
  const quota = asset?.traffic_gb ? asset.traffic_gb * 1024 ** 3 : 0;
  const used = tr ? billed(mode, tr.rx, tr.tx) : undefined;
  const usedPct = quota && used !== undefined ? used / quota * 100 : undefined;
  const history = node?.history || [];
  const carriers = ['电信', '联通', '移动'].filter(n => s?.pings.some(p => p.name === n));
  const ispHistory = (n: string) => { const rows = history.slice(-HISTORY).map(h => h.pings.find(p => p.name === n)); return [...Array<undefined>(HISTORY - rows.length).fill(undefined), ...rows]; };
  return <article className={`probe-card pc ${highlight ? 'newly-added' : ''} ${live ? 'live' : ''} ${status} ${compact ? 'probe-compact' : ''}`}>
    <header className="pc-head">
      <span className={`pc-flag ${place ? '' : 'none'}`} title={place?.name} aria-hidden="true">{place ? place.flag : <Server size={14}/>}</span>
      <button className="pc-name" onClick={onDetails}>{host.name}</button>
      <span className={`pc-status ${status}`}><i/>{label}{live && s ? ` · ${shortUptime(s.uptime_seconds)}` : ''}</span>
      <button className="pc-icon" onClick={onDetails} title="详情与历史" aria-label="详情与历史"><ChartColumn size={15}/></button>
    </header>
    <div className="pc-spec"><span title={s?.cpu_model}>{spec}</span>{tag && <em>{tag}</em>}</div>
    {host.status === 'host_key_mismatch' && <p className="probe-stale ssh-mismatch"><ShieldAlert size={12}/><span>SSH 主机指纹与已保存的不一致，已停止连接。{onTest && <button className="text-btn" onClick={onTest}>核对指纹</button>}</span></p>}
    {s ? <>
      <div className="pc-meters">
        <Meter icon={Cpu} tone="cpu" label="CPU" value={s.cpu_percent} title={s.cpu_model}/>
        <Meter icon={MemoryStick} tone="mem" label="内存" value={pct(s.memory_used, s.memory_total)} title={`${bytes(s.memory_used)} / ${bytes(s.memory_total)}`}/>
        <Meter icon={HardDrive} tone="disk" label="磁盘" value={pct(s.disk_used, s.disk_total)} title={`${bytes(s.disk_used)} / ${bytes(s.disk_total)}`}/>
        <Meter icon={Gauge} tone="load" label="负载" value={load === undefined ? undefined : load / s.cpu_cores * 100} text={load === undefined ? '—' : load.toFixed(2)} title={`1 / 5 / 15 分钟平均负载：${s.load.map(v => v.toFixed(2)).join(' / ') || '—'}（${s.cpu_cores} 核）`}/>
      </div>
      <div className="pc-net">
        <div title="实时速率"><span><ArrowUp size={13}/>上传<strong>{bytes(s.tx_rate, true)}</strong></span><span><ArrowDown size={13}/>下载<strong>{bytes(s.rx_rate, true)}</strong></span></div>
        <div title={tr ? `本月（${tr.period}）自 ${new Date(tr.since).toLocaleDateString('zh-CN')} 起统计` : '本月流量统计中'}><span><ArrowUp size={13}/>本月出<strong>{tr ? bytes(tr.tx) : '—'}</strong></span><span><ArrowDown size={13}/>本月入<strong>{tr ? bytes(tr.rx) : '—'}</strong></span></div>
      </div>
      <div className={`pc-quota ${level(usedPct, 80, 95)}`}>
        <span><Database size={13}/>流量 <small>{quota ? modeLabel[mode] : '双向合计'}</small><strong>{used === undefined ? '—' : bytes(used)}{quota ? <small> / {bytes(quota)}</small> : null}</strong></span>
        {quota ? <Blocks value={usedPct} tone={level(usedPct, 80, 95) || 'quota'}/> : onAsset ? <button type="button" className="text-btn" onClick={onAsset}>登记月流量额度，显示用量进度</button> : null}
      </div>
      {carriers.length ? <div className="pc-isps" aria-label="三网延迟与丢包">
        <div className="pc-isps-head"><Radio size={12}/>三网延迟<small>{node?.province?.name || ''}</small></div>
        {carriers.map(n => <IspRow key={n} name={n} history={ispHistory(n)}/>)}
      </div> : <div className="pc-isps none"><Radio size={12}/>未设置三网测速{onConnect && <button className="text-btn" onClick={onConnect}>选择省份</button>}</div>}
      {status === 'offline' && <p className="probe-stale">以上是最后一次上报的数据，已停止刷新。</p>}
    </> : <div className="probe-empty-body"><strong>{node?.enrolled ? '探针已接入，等待第一次上报' : '还没有监控数据'}</strong><p>{node?.enrolled ? '通常一分钟内出现。如果一直没有，请检查节点能否访问面板地址。' : '接入只读探针后显示 CPU、内存、磁盘、负载、实时速率、流量和三网延迟。'}</p></div>}
    <footer className="pc-foot">
      {asset && (renew || price) ? <button type="button" className={`pc-chip ${assetTone}`} onClick={onAsset} disabled={!onAsset}><Wallet size={12}/>{[renew, price].filter(Boolean).join(' · ')}</button> : onAsset ? <button type="button" className="pc-chip muted" onClick={onAsset}><Wallet size={12}/>登记资费</button> : null}
      <span className="pc-services" title={names?.join('、')}><Boxes size={12}/><span>{names === null ? '服务未检测' : names.length ? names.join(' · ') : '无运行服务'}</span>{!!names && names.length > 2 && <small>{names.length}</small>}</span>
      <span className="pc-actions">{onChat && <button className="pc-icon" onClick={onChat} title="让 Bot 处理这台服务器" aria-label="让 Bot 处理这台服务器"><Bot size={14}/></button>}{!node?.enrolled && onConnect ? <button className="text-btn strong" onClick={onConnect}>接入探针</button> : <button className="text-btn" onClick={onDetails}>详情<ChevronRight size={13}/></button>}</span>
    </footer>
  </article>;
}
