import { useState } from 'react';
import { Blocks, Code2, Search, Plus, ArrowLeft, ChevronRight } from 'lucide-react';
import type { Mod } from '../api';
import { IconButton } from './ui';

/** Module directory beside the 模块 view. */
export default function SecondaryNav({ mods, module, onModule, onClose }: { mods: Mod[]; module: string; onModule: (id: string) => void; onClose: () => void }) {
  const [query, setQuery] = useState('');
  const q = query.trim().toLowerCase();
  const filteredMods = mods.filter(m => (m.name + ' ' + m.desc).toLowerCase().includes(q));
  const builtinMods = filteredMods.filter(m => m.type !== 'custom');
  const customMods = filteredMods.filter(m => m.type === 'custom');

  return (
    <aside className="secondary-nav" aria-label="模块二级目录">
      <header><div><h2>模块</h2></div><IconButton label="收起二级目录" onClick={onClose}><ArrowLeft size={16}/></IconButton></header>
      <label className="directory-search"><Search size={15}/><input aria-label="搜索模块" placeholder="搜索模块" value={query} onChange={e => setQuery(e.target.value)}/></label>
      <div className="directory-scroll">
        <button className={`directory-all ${!module ? 'selected' : ''}`} onClick={() => onModule('')}><Blocks size={16}/><span>全部模块</span><small>{mods.length}</small></button>
        <div className="directory-section-title"><span>内置模块</span><small>{builtinMods.length}</small></div>
        {builtinMods.map(m => <button key={m.id} className={`directory-module ${module === m.id ? 'selected' : ''}`} onClick={() => onModule(m.id)}>{m.id === 'develop' ? <Code2 size={17}/> : <Blocks size={17}/>}<span><strong>{m.name}</strong><small><i className={m.enabled ? 'enabled' : ''}/>{m.enabled ? '已启用' : '未启用'}</small></span><ChevronRight size={13}/></button>)}
        <div className="directory-section-title conversation-section"><span>已创建的自定义模块</span><IconButton label="新建自定义模块" onClick={() => onModule('develop')}><Plus size={14}/></IconButton></div>
        {customMods.map(m => <button key={m.id} className={`directory-module ${module === m.id ? 'selected' : ''}`} onClick={() => onModule(m.id)}><Code2 size={17}/><span><strong>{m.name}</strong><small><i className={m.enabled ? 'enabled' : ''}/>{m.enabled ? '面板显示' : '仅模块页'}</small></span><ChevronRight size={13}/></button>)}
        {!customMods.length && <p className="directory-empty">{query ? '没有匹配的自定义模块' : '点上方 + 或进入「自定义模块」创建新模块。'}</p>}
      </div>
      <footer>点开模块进入对应面板，或在自定义模块中创建新功能。</footer>
    </aside>
  );
}
