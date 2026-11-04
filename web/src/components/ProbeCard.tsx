import { useEffect, useRef, useState } from 'react';
import { createPortal } from 'react-dom';
import { ArrowDown, ArrowUp, ChartColumn, Bot, Boxes, ChevronRight, Cpu, Gauge, Database, HardDrive, MemoryStick, Radio, Server, ShieldAlert, Wallet } from 'lucide-react';
import { api, type AssetInput, type AssetRecord, type Host, type PingMetric, type ProbeNode, type ServiceView } from '../api';
import { Field, Modal } from './ui';
import { money } from '../pages/Assets';
import { cssVar, useTheme } from '../theme';
import TrafficBreakdown from './TrafficBreakdown';
import { bytes } from '../lib/format';
export { bytes } from '../lib/format';

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
  return <div className="pc-isp" title={last ? `${last.target} · 每轮 ${last.samples} 次连接尝试${last.measured_at?' · 检测于 '+new Date(last.measured_at).toLocaleTimeString():''}${last.next_check_at?' · 下次检测不早于 '+new Date(last.next_check_at).toLocaleTimeString():''} · 连接失败率不等于网络丢包率` : undefined}>
    <div><span>{name}</span><strong className={latTone(lat)}>{lat === undefined ? '—' : lat === null ? (last?.samples?'未连通':'未测得') : Math.round(lat)}{typeof lat === 'number' && <small>ms</small>}</strong></div>
    <History values={history} tone={p => p ? latTone(p.latency_ms) : ''}/>
    <div><span>连接失败率</span><strong className={lossTone(loss)}>{loss === undefined || loss === null ? '—' : +loss.toFixed(1)}{typeof loss === 'number' && <small>%</small>}</strong></div>
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


/** Quick edit of the plan's monthly traffic allowance, saved on the server's asset record. */
function QuotaDialog({ asset, onClose, onSaved }: { asset: AssetRecord; onClose: () => void; onSaved?: (row: AssetRecord) => void }) {
  const [gb, setGb] = useState(asset.traffic_gb ? String(asset.traffic_gb) : '');
  const [mode, setMode] = useState(asset.traffic_mode || 'both');
  const [busy, setBusy] = useState(false), [error, setError] = useState('');
  async function save() {
    const value = gb.trim() ? Number(gb) : 0;
    if (!Number.isFinite(value) || value < 0) { setError('额度请填数字（GB），不限量留空'); return; }
    setBusy(true); setError('');
    try {
      const { id, tags, created_at: _c, updated_at: _u, days_total: _t, days_left: _l, elapsed_ratio: _e, residual_cents: _r, daily_cents: _d, derived_status: _s, ...rest } = asset;
      const body: AssetInput = { ...rest, tags, traffic_gb: value, traffic_mode: mode };
      const row = await api.saveAsset(body, id) as AssetRecord;
      onSaved?.(row); onClose();
    } catch (e) { setError(e instanceof Error ? e.message : String(e)); } finally { setBusy(false); }
  }
  return <Modal title={`月流量额度 · ${asset.name}`} note="按服务商的计费方式统计本月用量，卡片上显示已用和剩余。" onClose={() => !busy && onClose()} onSubmit={save} busy={busy} error={error}>
    <div className="quota-form">
      <Field label="每月额度（GB）" hint="不限量留空。"><input autoFocus inputMode="decimal" value={gb} placeholder="例如 1024" onChange={e => setGb(e.target.value)}/></Field>
      <Field label="计费方式"><select value={mode} onChange={e => setMode(e.target.value)}><option value="both">双向（入 + 出）</option><option value="out">单向（只算出站）</option><option value="in">单向（只算入站）</option><option value="max">单向（取入、出较大者）</option></select></Field>
    </div>
  </Modal>;
}

export default function ProbeCard({ highlight, host, node, asset, services, onDetails, onConnect, onChat, onTest, onAsset, onAssetSaved, compact = false }: { onAssetSaved?: (row: AssetRecord) => void; highlight?: boolean; host: Host; node?: ProbeNode; asset?: AssetRecord; services?: ServiceView; onDetails: () => void; onConnect?: () => void; onChat?: () => void; onTest?: () => void; onAsset?: () => void; compact?: boolean }) {
  const s = node?.latest, status = probeStatus(node), live = status === 'online';
  const label = live ? '在线' : status === 'offline' ? '离线' : node?.enrolled ? '等待上报' : '未接探针';
  const tr = node?.traffic;
  const names = serviceNames(services);
  const [quotaOpen, setQuotaOpen] = useState(false);
  const editQuota = asset ? () => setQuotaOpen(true) : onAsset;
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
  const matchesProvince = (target:string) => !node?.province || target.startsWith(node.province.code+'-');
  const carriers = ['电信', '联通', '移动'].filter(n => s?.pings.some(p => p.name === n&&matchesProvince(p.target)));
  const ispHistory = (n: string) => { const unique=new Map<string,PingMetric>();for(const h of history){const p=h.pings.find(p=>p.name===n&&matchesProvince(p.target));if(p)unique.set(p.measured_at||h.received_at,p);}const rows=[...unique.values()].slice(-HISTORY);return [...Array<undefined>(HISTORY-rows.length).fill(undefined),...rows]; };
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
      {quota ? <div className={`pc-quota ${level(usedPct, 80, 95)}`}>
        <span><Database size={13}/>流量{editQuota ? <button type="button" className="text-btn muted" onClick={editQuota} title="修改月流量额度">{modeLabel[mode]}</button> : <small> {modeLabel[mode]}</small>}<strong>{used === undefined ? '—' : bytes(used)}<small> / {bytes(quota)}</small></strong></span>
        <Blocks value={usedPct} tone={level(usedPct, 80, 95) || 'quota'}/>
      </div> : <TrafficBreakdown out={tr?.tx} incoming={tr?.rx} action={editQuota && <button type="button" className="text-btn" onClick={editQuota} title={asset ? '设置月流量额度' : '先登记资产，再设置月流量额度'}>设置额度</button>}/>}
      {carriers.length ? <div className="pc-isps" aria-label="三网 TCP 延迟与连接失败率">
        <div className="pc-isps-head"><Radio size={12}/>三网延迟<small>{node?.province?.name || ''}</small>{onConnect&&<button type="button" className="text-btn" onClick={onConnect}>更换省份</button>}</div>
        {carriers.map(n => <IspRow key={n} name={n} history={ispHistory(n)}/>)}
      </div> : <div className="pc-isps none"><Radio size={12}/>{node?.province?`${node.province.name} · 等待测速上报`:'未设置三网测速'}{onConnect && <button type="button" className="text-btn" onClick={onConnect}>{node?.province?'更换省份':'选择省份'}</button>}</div>}
      {status === 'offline' && <p className="probe-stale">以上是最后一次上报的数据，已停止刷新。</p>}
    </> : <div className="probe-empty-body"><strong>{node?.enrolled ? '探针已接入，等待第一次上报' : '还没有监控数据'}</strong><p>{node?.enrolled ? '通常一分钟内出现。如果一直没有，请检查节点能否访问面板地址。' : '接入只读探针后显示 CPU、内存、磁盘、负载、实时速率、流量和三网延迟。'}</p></div>}
    <footer className="pc-foot">
      {asset && (renew || price) ? <button type="button" className={`pc-chip ${assetTone}`} onClick={onAsset} disabled={!onAsset}><Wallet size={12}/>{[renew, price].filter(Boolean).join(' · ')}</button> : onAsset ? <button type="button" className="pc-chip muted" onClick={onAsset}><Wallet size={12}/>登记资费</button> : null}
      <span className="pc-services" title={names?.join('、')}><Boxes size={12}/><span>{names === null ? '服务未检测' : names.length ? names.join(' · ') : '无运行服务'}</span>{!!names && names.length > 2 && <small>{names.length}</small>}</span>
      <span className="pc-actions">{onChat && <button className="pc-icon" onClick={onChat} title="让 Bot 处理这台服务器" aria-label="让 Bot 处理这台服务器"><Bot size={14}/></button>}{!node?.enrolled && onConnect ? <button className="text-btn strong" onClick={onConnect}>接入探针</button> : <button className="text-btn" onClick={onDetails}>详情<ChevronRight size={13}/></button>}</span>
    </footer>
    {quotaOpen && asset && createPortal(<QuotaDialog asset={asset} onClose={() => setQuotaOpen(false)} onSaved={onAssetSaved}/>, document.body)}
  </article>;
}
