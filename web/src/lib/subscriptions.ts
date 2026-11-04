import type { AssetSummary } from '../api';

export const displayCurrencies=['USD','CNY','EUR','GBP','CAD','HKD','JPY','AUD','SGD'];
export type ExchangeRates={base:string;rates:Record<string,number>;date:string;source:string;stale:boolean};

export function monthlyTotal(values:AssetSummary['monthly_cents']={},currency:string,rates?:ExchangeRates|null){
 let total=0,converted=false,count=0;const missing:Record<string,number>={};
 for(const [source,cents]of Object.entries(values)){
  if(!cents)continue;
  if(source===currency){total+=cents;count++;continue;}
  const from=rates?.rates[source],to=rates?.rates[currency];
  if(!from||!to){missing[source]=cents;continue;}
  total+=cents/from*to;converted=true;count++;
 }
 return {cents:Math.round(total),converted,missing,count};
}

export function trafficParts(out:number,incoming:number){
 const total=Math.max(0,out)+Math.max(0,incoming);
 return {total,outPercent:total?Math.max(0,out)/total*100:0,inPercent:total?Math.max(0,incoming)/total*100:0};
}
