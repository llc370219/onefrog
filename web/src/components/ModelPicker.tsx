import type { ModelRoute, ProviderSettings } from '../api';

export const visibleModels = (provider?: ProviderSettings) => provider?.models.filter(m => !(provider.hidden_models || []).includes(m)) || [];

export function ModelSelect({provider,value,onChange,label,disabled}: {provider?:ProviderSettings;value:string;onChange:(s:string)=>void;label:string;disabled?:boolean}) {
  const models=visibleModels(provider);
  const unavailable=!!value&&!models.includes(value);
  return <select aria-label={label} value={value} disabled={disabled || !provider} onChange={e=>onChange(e.target.value)}>
    <option value="">{!provider?'先选择渠道':models.length?'选择模型':'暂无可选模型，请拉取并开启显示'}</option>
    {unavailable&&<option value={value} disabled>{value}（已隐藏或不在目录，当前配置保留）</option>}
    {models.map(m=><option key={m} value={m}>{m}</option>)}
  </select>;
}

export function RoutePicker({label,note,value,onChange,providers,inherit}: {label:string;note:string;value:ModelRoute;onChange:(r:ModelRoute)=>void;providers:ProviderSettings[];inherit?:boolean}) {
  const provider=providers.find(p=>p.id===value.provider_id);
  return <section className="route-row"><div><h3>{label}</h3><p>{note}</p></div><div className="route-inputs">
    <select aria-label={`${label}渠道`} value={value.provider_id} onChange={e=>{const p=providers.find(p=>p.id===e.target.value);onChange({provider_id:e.target.value,model:visibleModels(p)[0]||''});}}>
      <option value="">{inherit?'跟随默认模型':'选择渠道'}</option>
      {provider&&!provider.enabled&&<option value={provider.id} disabled>{provider.name}（已停用）</option>}
      {providers.filter(p=>p.enabled).map(p=><option key={p.id} value={p.id}>{p.name}</option>)}
    </select>
    <ModelSelect label={`${label}的模型`} provider={provider} value={value.model} onChange={model=>onChange({...value,model})}/>
  </div>{provider&&!!value.model&&(provider.hidden_models||[]).includes(value.model)&&<p className="model-hidden-note">此模型已隐藏，现有绑定仍会使用；重新选取时仅显示已开启的模型。</p>}</section>;
}
