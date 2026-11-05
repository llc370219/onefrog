import { Database } from 'lucide-react';
import { trafficParts } from '../lib/subscriptions';
import { bytes } from '../lib/format';

export default function TrafficBreakdown({out,incoming,onSetQuota}:{out?:number;incoming?:number;onSetQuota?:()=>void}){
 const known=out!==undefined&&incoming!==undefined;
 const p=trafficParts(out||0,incoming||0);
 const outBlocks=known&&p.total>0?Math.max(out!>0?1:0,Math.min(incoming!>0?19:20,Math.round(p.outPercent/5))):0;
 return <div className="pc-quota traffic-breakdown" aria-label="本月出入流量">
  <span className="traffic-heading">
   <span className="traffic-label"><Database size={13}/>流量{onSetQuota&&<button type="button" className="text-btn" onClick={onSetQuota}>设置流量</button>}</span>
   <span className="traffic-totals"><span><small>Out</small><strong>{known?bytes(out):'—'}</strong></span><small aria-hidden="true">/</small><span><small>In</small><strong>{known?bytes(incoming):'—'}</strong></span><small aria-hidden="true">/</small><span><small>Total</small><strong>{known?bytes(p.total):'—'}</strong></span></span>
  </span>
  <span className="pc-blocks traffic-blocks" role="img" aria-label={known?`Out ${bytes(out)}，In ${bytes(incoming)}，合计 ${bytes(p.total)}；方块表示出入流量比例`:'等待本月流量上报'}>{Array.from({length:20},(_,i)=><i key={i} className={known&&p.total>0?(i<outBlocks?'out':'in'):''}/>)}</span>
 </div>;
}
