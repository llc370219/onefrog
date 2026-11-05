import { useEffect, useState, useSyncExternalStore } from 'react';
import { api, type AssetSummary } from '../api';
import { displayCurrencies, monthlyTotal, type ExchangeRates } from '../lib/subscriptions';
import { money } from '../lib/format';

const key='onefrog-subscription-currency';
let fallback='USD';
const listeners=new Set<()=>void>();
function selected(){try{const value=localStorage.getItem(key)||fallback;return displayCurrencies.includes(value)?value:'USD';}catch{return fallback;}}
function subscribe(fn:()=>void){listeners.add(fn);window.addEventListener('storage',fn);return()=>{listeners.delete(fn);window.removeEventListener('storage',fn);};}

export default function SubscriptionSummary({summary,compact=false}:{summary:AssetSummary|null;compact?:boolean}){
 const currency=useSyncExternalStore(subscribe,selected,()=> 'USD');
 const [rates,setRates]=useState<ExchangeRates|null>(null),[rateError,setRateError]=useState(false),[loading,setLoading]=useState(false);
 async function load(){setLoading(true);setRateError(false);try{setRates(await api.assetRates());}catch{setRateError(true);}finally{setLoading(false);}}
 useEffect(()=>{void load();},[]);
 function change(value:string){fallback=value;try{localStorage.setItem(key,value);}catch{}listeners.forEach(fn=>fn());}
 const total=monthlyTotal(summary?.monthly_cents,currency,rates);
 const missing=Object.entries(total.missing);
 const amount=missing.length?(loading?'读取汇率…':total.count?'部分合计 '+money(total.cents,currency):'暂无法折算'):(total.converted?'≈ ':'')+money(total.cents,currency);
 const items=[
  {label:'订阅数',value:String(summary?.subscriptions||0),note:'当前订阅，包含服务器与其他服务'},
  {label:'7 天内到期',value:String(summary?.expiring||0),note:'从今天起未来 7 天',tone:summary?.expiring?'warn':''},
  {label:'折合月费',value:amount,note:'月付 + 季付 ÷ 3 + 年付 ÷ 12'},
  {label:'历史订阅',value:String(summary?.historical||0),note:'已停用或已到期且不再续费'},
 ];
 return <div className={compact?'asset-glance':'metrics asset-metrics'}>{items.map((item,i)=><section className={compact?'subscription-stat':'metric'} key={item.label}>
  <div className={compact?'subscription-stat-label':'metric-top'}><span>{item.label}</span>{i===2&&<select className="currency-select" aria-label="月费显示币种" value={currency} onChange={e=>change(e.target.value)}>{displayCurrencies.map(cur=><option key={cur}>{cur}</option>)}</select>}</div>
  <strong className={(compact?'':'metric-value asset-metric-value ')+(item.tone||'')}>{item.value}</strong>
  {!compact&&<p>{item.note}</p>}
  {i===2&&<div className="exchange-note">{rates&&total.converted&&<span>{rates.source} · {rates.date}{rates.stale?' · 缓存汇率':''}</span>}{missing.length>0&&<><span>未折算：{missing.map(([cur,value])=>money(value,cur)).join(' / ')}</span>{rateError&&<button type="button" className="text-btn" onClick={load} disabled={loading}>重试汇率</button>}</>}</div>}
 </section>)}{compact&&<p>{summary?.next_name?`${summary.next_name} 将在 ${summary.next_expiry} 到期`:'暂无待提醒的订阅'}</p>}</div>;
}
