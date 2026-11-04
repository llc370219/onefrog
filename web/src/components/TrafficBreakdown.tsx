import { ArrowDown, ArrowUp, Database } from 'lucide-react';
import { trafficParts } from '../lib/subscriptions';
import { bytes } from '../lib/format';

export default function TrafficBreakdown({out,incoming}:{out?:number;incoming?:number}){
 const known=out!==undefined&&incoming!==undefined;
 const p=trafficParts(out||0,incoming||0);
 return <div className="pc-quota traffic-breakdown" aria-label="本月出入流量">
  <div className="traffic-totals"><span><ArrowUp size={12}/>Out<strong>{known?bytes(out):'—'}</strong></span><span><ArrowDown size={12}/>In<strong>{known?bytes(incoming):'—'}</strong></span><span><Database size={12}/>Total<strong>{known?bytes(p.total):'—'}</strong></span></div>
  <div className="traffic-split" role="img" aria-label={known?`Out ${bytes(out)}，In ${bytes(incoming)}，合计 ${bytes(p.total)}`:'等待本月流量上报'}><i className="out" style={{width:p.outPercent+'%'}}/><i className="in" style={{width:p.inPercent+'%'}}/></div>
 </div>;
}
