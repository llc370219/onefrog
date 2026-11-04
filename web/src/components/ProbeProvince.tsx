import { useEffect, useState } from 'react';
import { api, type Host, type Province } from '../api';
import { Button, Field, Modal, Notice } from './ui';

export default function ProbeProvince({host,province:current,onClose,onApplied,onManual}:{host:Host;province?:Province;onClose:()=>void;onApplied:()=>void;onManual:()=>void}) {
 const [province,setProvince]=useState(current?.code||'');
 const [provinces,setProvinces]=useState<Province[]>([]);
 const [loading,setLoading]=useState(true),[busy,setBusy]=useState(false),[error,setError]=useState(''),[message,setMessage]=useState('');
 async function load(){setLoading(true);setError('');try{const d=await api.enrollmentDefaults();if(!d.provinces?.length)throw new Error('暂无可用省份，请重试');setProvinces(d.provinces);}catch(e){setError(e instanceof Error?e.message:'省份加载失败');}finally{setLoading(false);}}
 useEffect(()=>{void load();},[]);
 async function apply(){if(!province||busy)return;setBusy(true);setError('');setMessage('');try{const result=await api.probeProvince(host.id,province==='none'?'':province);setMessage(result.message);onApplied();}catch(e){setError(e instanceof Error?e.message:'测速配置应用失败');}finally{setBusy(false);}}
 return <Modal title={`三网测速 · ${host.name}`} note="选择省份后保存，面板会通过 SSH 更新节点探针配置并重启采集服务。" onClose={onClose} onSubmit={message?onClose:apply} confirm={message?'完成':busy?'正在应用':'保存并应用'} cancel="关闭" busy={busy} submitDisabled={!message&&(loading||!province)} error={error}>
  <Field label="三网延迟测哪个省份" hint="检测所选省份电信、联通、移动的延迟与丢包。保存后通常在一分钟内显示新结果。">
   <select value={province} disabled={loading||busy} onChange={e=>{setProvince(e.target.value);setMessage('');}}>
    <option value="" disabled>{loading?'正在加载省份…':'请选择省份'}</option><option value="none">不测三网</option>
    {provinces.map(p=><option key={p.code} value={p.code}>{p.name}</option>)}
   </select>
  </Field>
  {!loading&&!provinces.length&&<Button onClick={load}>重新加载省份</Button>}
  {busy&&<p role="status">正在连接节点、更新测速配置并确认服务状态…</p>}
  {message&&<Notice>{message}</Notice>}
  <Button onClick={onManual} disabled={busy}>手动接入 / 重置探针凭据</Button>
 </Modal>;
}
