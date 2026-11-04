import { trafficParts } from '../lib/subscriptions';
import { bytes } from '../lib/format';

export default function TrafficBreakdown({out,incoming}:{out?:number;incoming?:number}){
 const known=out!==undefined&&incoming!==undefined;
 const p=trafficParts(out||0,incoming||0);
 const outBlocks=known&&p.total>0?Math.max(out!>0?1:0,Math.min(incoming!>0?19:20,Math.round(p.outPercent/5))):0;
 return <div className="pc-quota traffic-breakdown" aria-label="本月出入流量">
  <div className="traffic-totals"><span>Out<strong>{known?bytes(out):'—'}</strong></span><span>In<strong>{known?bytes(incoming):'—'}</strong></span><span>Total<strong>{known?bytes(p.total):'—'}</strong></span></div>
  <span className="pc-blocks traffic-blocks" role="img" aria-label={known?`Out ${bytes(out)}，In ${bytes(incoming)}，合计 ${bytes(p.total)}；方块表示出入流量比例`:'等待本月流量上报'}>{Array.from({length:20},(_,i)=><i key={i} className={known&&p.total>0?(i<outBlocks?'out':'in'):''}/>)}</span>
 </div>;
}
