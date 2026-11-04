export function money(cents=0,currency='CNY'){
 const negative=cents<0,abs=Math.abs(Math.trunc(cents));
 const unit=({CNY:'¥',USD:'$',EUR:'€',GBP:'£',CAD:'CA$',AUD:'A$',SGD:'S$',HKD:'HK$',JPY:'JP¥',USDT:'USDT '} as Record<string,string>)[currency]||currency+' ';
 return (negative?'-':'')+unit+Math.floor(abs/100).toLocaleString('zh-CN')+'.'+String(abs%100).padStart(2,'0');
}
export function bytes(value?:number,rate=false){if(value===undefined)return '—';let n=value;const units=['B','KiB','MiB','GiB','TiB','PiB'];let i=0;while(n>=1024&&i<units.length-1){n/=1024;i++;}return `${n.toFixed(i===0?0:n>=100?0:1)} ${units[i]}${rate?'/s':''}`;}
