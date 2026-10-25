import { useEffect, useMemo, useState } from 'react';
import { Bot, Pencil, Plus, Search, Trash2, Wallet } from 'lucide-react';
import { api, type AssetInput, type AssetRecord, type AssetSummary, type Host } from '../api';
import { Button, Empty, Field, Modal, Notice, PageHeading } from '../components/ui';

const cycles: Record<string, string> = { monthly: '月付', quarterly: '季付', yearly: '年付', once: '一次性' };
const currencies = ['CNY', 'USD', 'EUR', 'HKD', 'JPY', 'USDT'];
const empty = { name: '', server_id: '', provider: '', region: '', plan: '', cycle: 'monthly', currency: 'CNY', amount: '', paid_at: '', starts_at: '', expires_at: '', auto_renew: false, status: 'active', ip: '', specs: '', order_no: '', account: '', invoice_url: '', contact: '', tags: '', notes: '' };
type Draft = typeof empty;
const message = (err: unknown) => err instanceof Error ? err.message : '操作失败，请重试';

export function money(cents = 0, currency = 'CNY') {
  const neg = cents < 0;
  const abs = Math.abs(Math.trunc(cents));
  const unit = ({ CNY: '¥', USD: '$', EUR: '€', HKD: 'HK$', JPY: '¥', USDT: 'USDT ' } as Record<string, string>)[currency] || currency + ' ';
  return (neg ? '-' : '') + unit + Math.floor(abs / 100).toLocaleString('zh-CN') + '.' + String(abs % 100).padStart(2, '0');
}
function moneyMap(values: Record<string, number> = {}) {
  const rows = Object.entries(values).filter(([, n]) => n);
  return rows.length ? rows.map(([cur, n]) => money(n, cur)).join(' / ') : '—';
}
function toCents(value: string) {
  const text = value.trim();
  if (!text) return 0;
  if (!/^\d+(\.\d{1,2})?$/.test(text)) throw new Error('金额最多两位小数');
  const [major, minor = ''] = text.split('.');
  return Number(major) * 100 + Number((minor + '00').slice(0, 2));
}
function fromCents(cents = 0) {
  return Math.trunc(Math.abs(cents) / 100) + '.' + String(Math.abs(cents) % 100).padStart(2, '0');
}
function tone(status: string) {
  return status === 'expired' ? 'danger' : status === 'expiring' ? 'warn' : status === 'cancelled' ? 'muted' : 'ok';
}
function statusLabel(status: string) {
  return ({ active: '在用', expiring: '即将到期', expired: '已到期', cancelled: '已停用' } as Record<string, string>)[status] || '在用';
}
function remain(row: AssetRecord) {
  const today = new Date().toISOString().slice(0, 10);
  if (row.derived_status === 'cancelled') return '已停用，剩余价值按 0 计';
  if (!row.expires_at) return '未设置到期日';
  if (row.derived_status === 'expired') return '已到期';
  if (row.starts_at && row.starts_at > today) return '未开始 · ' + row.days_left + ' 天';
  return '剩余 ' + row.days_left + ' 天';
}

export function AssetGlance() {
  const [summary, setSummary] = useState<AssetSummary | null>(null);
  const [error, setError] = useState('');
  useEffect(() => { api.assets().then(v => setSummary(v.summary)).catch(() => setError('资产数据暂时不可用')); }, []);
  if (error) return <p className="card-foot danger">{error}</p>;
  if (!summary) return <p className="card-foot">正在读取资产…</p>;
  return <div className="asset-glance"><div><span>在册</span><strong>{summary.count}</strong></div><div><span>即将到期</span><strong className={summary.expiring ? 'warn' : ''}>{summary.expiring}</strong></div><div><span>折合月费</span><strong>{moneyMap(summary.monthly_cents)}</strong></div><div><span>剩余价值</span><strong>{moneyMap(summary.residual_cents)}</strong></div><p>{summary.next_name ? summary.next_name + ' 将在 ' + summary.next_expiry + ' 到期' : '还没有即将到期的订阅'}</p></div>;
}

export default function Assets({ hosts, onAsk }: { hosts: Host[]; onAsk: (text: string) => void }) {
  const [rows, setRows] = useState<AssetRecord[]>([]);
  const [summary, setSummary] = useState<AssetSummary | null>(null);
  const [query, setQuery] = useState('');
  const [filter, setFilter] = useState('all');
  const [error, setError] = useState('');
  const [draft, setDraft] = useState<Draft | null>(null);
  const [editID, setEditID] = useState('');
  const [busy, setBusy] = useState(false);
  const [formError, setFormError] = useState('');
  const [confirmID, setConfirmID] = useState('');
  async function load() {
    try { const data = await api.assets(); setRows(data.records); setSummary(data.summary); setError(''); }
    catch (err) { setError(message(err)); }
  }
  useEffect(() => { load(); }, []);
  const visible = useMemo(() => rows.filter(row => {
    const text = [row.name, row.provider, row.region, row.plan, row.ip, row.account, row.specs, ...(row.tags || [])].join(' ').toLowerCase();
    const matched = text.includes(query.trim().toLowerCase());
    if (!matched) return false;
    if (filter === 'expiring') return row.derived_status === 'expiring';
    if (filter === 'expired') return row.derived_status === 'expired';
    if (filter === 'cancelled') return row.derived_status === 'cancelled';
    return true;
  }), [rows, query, filter]);
  function edit(row?: AssetRecord) {
    setConfirmID(''); setFormError(''); setEditID(row?.id || '');
    setDraft(row ? { ...empty, ...row, amount: fromCents(row.amount_cents), tags: (row.tags || []).join('，'), auto_renew: !!row.auto_renew, status: row.status || 'active' } : { ...empty });
  }
  async function save() {
    if (!draft || busy) return;
    setBusy(true); setFormError('');
    try {
      const body: AssetInput = { ...draft, amount_cents: toCents(draft.amount), tags: draft.tags.split(/[,，]/).map(v => v.trim()).filter(Boolean) };
      await api.saveAsset(body, editID);
      setDraft(null); await load();
    } catch (err) { setFormError(message(err)); }
    finally { setBusy(false); }
  }
  async function remove(id: string) {
    setBusy(true); setError('');
    try { await api.deleteAsset(id); setConfirmID(''); await load(); }
    catch (err) { setError(message(err)); }
    finally { setBusy(false); }
  }
  const metrics = [
    ['在册资产', String(summary?.count || 0), '包含停用记录'],
    ['即将到期', String(summary?.expiring || 0), '14 天内到期'],
    ['折合月费', moneyMap(summary?.monthly_cents), '年付按 12 期，季付按 3 期'],
    ['剩余价值', moneyMap(summary?.residual_cents), '按剩余天数折算未消耗金额'],
  ];
  return <div className="page assets-page"><PageHeading eyebrow="MODULE / ASSETS" title="资产与订阅" note="记录服务器资费、提供商和到期时间，并按剩余天数计算剩余价值。" actions={<><Button onClick={() => onAsk('请列出资产与订阅里即将到期的服务器，并说明剩余价值和是否自动续费。')}><Bot size={15}/>交给模块构建</Button><Button variant="primary" onClick={() => edit()}><Plus size={15}/>登记资产</Button></>}/>
    {error && <Notice error>{error}</Notice>}
    <div className="metrics asset-metrics">{metrics.map(([label, value, note]) => <section className="metric" key={label}><div className="metric-top">{label}<Wallet size={15}/></div><div className="metric-value asset-metric-value">{value}</div><p>{note}</p></section>)}</div>
    <div className="list-toolbar"><div className="tabs" role="group" aria-label="资产筛选">{[['all','全部'],['expiring','即将到期'],['expired','已到期'],['cancelled','已停用']].map(([id,label]) => <button key={id} className={filter===id?'active':''} aria-pressed={filter===id} onClick={() => setFilter(id)}>{label}</button>)}</div><label className="search"><Search size={15}/><input aria-label="搜索资产" placeholder="搜索名称、提供商、IP…" value={query} onChange={e => setQuery(e.target.value)}/></label></div>
    {!visible.length ? <Empty title={rows.length ? '没有匹配的资产' : '还没有资产记录'} body="登记一台服务器的套餐、资费和到期日，剩余价值会自动计算。" action={!rows.length ? <Button onClick={() => edit()}>登记第一台</Button> : undefined}/> : <div className="asset-board">{visible.map(row => <article className="card asset-card" key={row.id}><header><span className="catalog-icon"><Wallet size={18}/></span><div><strong>{row.name}</strong><small>{row.provider || '未填提供商'}{row.region ? ' · ' + row.region : ''}</small></div><span className={'status ' + tone(row.derived_status)}><i/>{statusLabel(row.derived_status)}</span></header><dl><div><dt>资费</dt><dd>{money(row.amount_cents, row.currency)}<small>{(row.cycle && cycles[row.cycle]) || row.cycle || '—'}{row.auto_renew ? ' · 自动续费' : ''}</small></dd></div><div><dt>时间</dt><dd>{remain(row)}<small>{row.starts_at || '—'} → {row.expires_at || '—'}</small></dd></div><div><dt>剩余价值</dt><dd>{money(row.residual_cents, row.currency)}<small>{row.days_total ? '日均 ' + money(row.daily_cents, row.currency) : '缺少完整周期'}</small></dd></div><div><dt>服务器</dt><dd>{hosts.find(h => h.id === row.server_id)?.name || '未关联'}<small>{row.ip || row.plan || '—'}</small></dd></div></dl>{row.days_total > 0 && <div className={'meter ' + (row.derived_status === 'expiring' ? 'warn' : '')}><i style={{ width: Math.min(100, Math.round(row.elapsed_ratio * 100)) + '%' }}/></div>}<p>{[row.plan, row.specs, row.order_no && '订单 ' + row.order_no, row.account && '账号 ' + row.account, ...(row.tags || [])].filter(Boolean).join(' · ') || '可补充套餐、配置、订单号和账号备注'}</p>{row.notes && <p className="asset-note">{row.notes}</p>}<footer><Button onClick={() => edit(row)}><Pencil size={13}/>编辑</Button>{confirmID === row.id ? <Button onClick={() => remove(row.id)} disabled={busy}>确认删除</Button> : <Button variant="ghost" onClick={() => setConfirmID(row.id)}><Trash2 size={13}/>删除</Button>}</footer></article>)}</div>}
    <p className="page-footnote">剩余价值 = 本期金额 × 剩余天数 ÷ 周期总天数，首尾两天都计入。已到期和已停用不计入剩余价值。折合月费不包含一次性费用。账号栏只保存备注，不保存密码。</p>
    {draft && <Modal wide title={editID ? '编辑资产' : '登记资产'} note="金额按填写的货币保存。到期后剩余价值自动归零。" onClose={() => !busy && setDraft(null)} onSubmit={save} busy={busy} error={formError} confirm={editID ? '保存修改' : '登记'}><div className="form-grid asset-form"><Field label="名称"><input required value={draft.name} placeholder="例如 洛杉矶-01" onChange={e => setDraft({ ...draft, name: e.target.value })}/></Field><Field label="关联服务器"><select value={draft.server_id} onChange={e => setDraft({ ...draft, server_id: e.target.value })}><option value="">不关联</option>{hosts.map(h => <option key={h.id} value={h.id}>{h.name}</option>)}</select></Field><Field label="提供商"><input value={draft.provider} placeholder="搬瓦工、腾讯云、Hetzner…" onChange={e => setDraft({ ...draft, provider: e.target.value })}/></Field><Field label="地区"><input value={draft.region} placeholder="例如 洛杉矶 CN2" onChange={e => setDraft({ ...draft, region: e.target.value })}/></Field><Field label="套餐"><input value={draft.plan} placeholder="例如 CN2 GIA 2G" onChange={e => setDraft({ ...draft, plan: e.target.value })}/></Field><Field label="配置"><input value={draft.specs} placeholder="2C / 2G / 40G / 1T 流量" onChange={e => setDraft({ ...draft, specs: e.target.value })}/></Field><Field label="计费周期"><select value={draft.cycle} onChange={e => setDraft({ ...draft, cycle: e.target.value })}>{Object.entries(cycles).map(([id, label]) => <option key={id} value={id}>{label}</option>)}</select></Field><Field label="币种"><select value={draft.currency} onChange={e => setDraft({ ...draft, currency: e.target.value })}>{currencies.map(id => <option key={id}>{id}</option>)}</select></Field><Field label="本期金额" hint="按元填写，最多两位小数"><input inputMode="decimal" value={draft.amount} placeholder="36.50" onChange={e => setDraft({ ...draft, amount: e.target.value })}/></Field><Field label="IP 或主机名"><input value={draft.ip} placeholder="203.0.113.10" onChange={e => setDraft({ ...draft, ip: e.target.value })}/></Field><Field label="付款日期"><input type="date" value={draft.paid_at} onChange={e => setDraft({ ...draft, paid_at: e.target.value })}/></Field><Field label="开始日期"><input type="date" value={draft.starts_at} onChange={e => setDraft({ ...draft, starts_at: e.target.value })}/></Field><Field label="到期日期"><input type="date" value={draft.expires_at} onChange={e => setDraft({ ...draft, expires_at: e.target.value })}/></Field><Field label="订单号"><input value={draft.order_no} onChange={e => setDraft({ ...draft, order_no: e.target.value })}/></Field><Field label="提供商账号" hint="只写账号备注，不要写密码"><input value={draft.account} onChange={e => setDraft({ ...draft, account: e.target.value })}/></Field><Field label="联系方式"><input value={draft.contact} onChange={e => setDraft({ ...draft, contact: e.target.value })}/></Field></div><Field label="账单链接"><input value={draft.invoice_url} placeholder="https://" onChange={e => setDraft({ ...draft, invoice_url: e.target.value })}/></Field><Field label="标签" hint="用逗号分隔，例如 生产，CN2"><input value={draft.tags} onChange={e => setDraft({ ...draft, tags: e.target.value })}/></Field><Field label="备注"><textarea rows={3} value={draft.notes} onChange={e => setDraft({ ...draft, notes: e.target.value })}/></Field><div className="asset-toggles"><label><span>自动续费</span><button type="button" className={'switch ' + (draft.auto_renew ? 'on' : '')} role="switch" aria-checked={draft.auto_renew} onClick={() => setDraft({ ...draft, auto_renew: !draft.auto_renew })}><i/></button></label><label><span>停用这条记录</span><button type="button" className={'switch ' + (draft.status === 'cancelled' ? 'on' : '')} role="switch" aria-checked={draft.status === 'cancelled'} onClick={() => setDraft({ ...draft, status: draft.status === 'cancelled' ? 'active' : 'cancelled' })}><i/></button></label></div></Modal>}
  </div>;
}
