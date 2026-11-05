import { useState } from 'react';
import { Blocks, Code2, Search, Users, Plus, ArrowLeft, ChevronRight, Inbox, Trash2 } from 'lucide-react';
import type { BotRow, ChatMode, ChatSession, Mod } from '../api';
import { IconButton } from './ui';
import { BotAvatar } from './BotAvatar';
import { groupMembers, groupSessions } from '../lib/chatGroups';

function when(value: string) {
  const d = new Date(value);
  return d.toDateString() === new Date().toDateString()
    ? d.toLocaleTimeString('zh-CN', { hour: '2-digit', minute: '2-digit' })
    : d.toLocaleDateString('zh-CN', { month: 'numeric', day: 'numeric' });
}

export function AvatarStack({ bots, members, max = 4 }: { bots: BotRow[]; members: BotRow[]; max?: number }) {
  const shown = members.slice(0, max);
  return (
    <span className="avatar-stack" title={members.map(b => b.name).join('、')}>
      {shown.map(b => <BotAvatar key={b.id} bots={bots} id={b.id} className="stack-avatar"/>)}
      {members.length > max && <span className="stack-more">+{members.length - max}</span>}
    </span>
  );
}

export default function SecondaryNav({ view, bots, sessions, mods, activeBot, session, module, disabled, mode, onBot, onSession, onNew, onAddBot, onDelete, onModule, onClose }: { view: 'chat' | 'modules'; bots: BotRow[]; sessions: ChatSession[]; mods: Mod[]; activeBot: string; session: string; module: string; disabled: boolean; mode: ChatMode; onBot: (id: string) => void; onSession: (s: ChatSession) => void; onNew: () => void; onAddBot: () => void; onDelete: (s: ChatSession) => void; onModule: (id: string) => void; onClose: () => void }) {
  const [query, setQuery] = useState('');
  const q = query.trim().toLowerCase();
  const hit = (s: ChatSession) => (s.title + ' ' + s.preview).toLowerCase().includes(q);
  const { groups, byBot } = groupSessions(sessions);
  const filteredBots = bots.filter(b => (b.name + ' ' + b.description).toLowerCase().includes(q) || (q && (byBot[b.id] || []).some(hit)));
  const filteredGroups = groups.filter(hit);
  const filteredMods = mods.filter(m => (m.name + ' ' + m.desc).toLowerCase().includes(q));
  const builtinMods = filteredMods.filter(m => m.type !== 'custom');
  const customMods = filteredMods.filter(m => m.type === 'custom');

  const chat = (
    <>
      <div className="directory-section-title">
        <span>Bot 单聊</span>
        <IconButton label="创建 Bot" onClick={onAddBot} disabled={disabled}><Plus size={14}/></IconButton>
      </div>
      {filteredBots.map(b => {
        const topics = byBot[b.id] || [];
        const recent = topics[0];
        const active = mode === 'direct' && activeBot === b.id;
        const shownTopics = q ? topics.filter(hit) : topics;
        return (
          <div className={`directory-bot-group ${active ? 'active' : ''}`} key={b.id}>
            <button className={`directory-bot ${active ? 'selected' : ''}`} title={`和 ${b.name} 单聊，历史会保留`} aria-current={active ? 'true' : undefined} onClick={() => onBot(b.id)} disabled={disabled || !b.enabled}>
              <BotAvatar bots={bots} id={b.id} className="bot-symbol"/>
              <span>
                <span className="directory-row-title"><strong>{b.name}</strong>{recent && <time>{when(recent.updated_at)}</time>}</span>
                <small>{!b.enabled ? '已停用' : recent?.preview || b.description || '开始单聊'}</small>
              </span>
            </button>
            {(active || q) && shownTopics.length > 0 && (
              <div className="directory-topics" aria-label={`${b.name} 的单聊话题`}>
                {shownTopics.map(s => (
                  <div className={`directory-topic ${session === s.id ? 'selected' : ''}`} key={s.id}>
                    <button className="conversation-open" onClick={() => onSession(s)} disabled={disabled}>
                      <strong>{s.id.startsWith('inbox-') && <span className="inbox-tag"><Inbox size={11}/>收件箱</span>}{s.title}</strong>
                      <time>{when(s.updated_at)}</time>
                    </button>
                    <IconButton label={`删除话题 ${s.title}`} disabled={disabled} onClick={() => onDelete(s)}><Trash2 size={12}/></IconButton>
                  </div>
                ))}
              </div>
            )}
          </div>
        );
      })}
      {!filteredBots.length && <p className="directory-empty">{query ? '没有匹配的 Bot' : '还没有 Bot'}</p>}
      <div className="directory-section-title conversation-section"><span>群聊</span><small>{filteredGroups.length}</small></div>
      {filteredGroups.map(s => {
        const members = groupMembers(bots, s.bot_id, s.options?.excluded_bot_ids);
        return (
          <div className={`directory-conversation ${session === s.id ? 'selected' : ''}`} key={s.id}>
            <button className="conversation-open" onClick={() => onSession(s)} disabled={disabled}>
              <strong>{s.title}</strong>
              <span><AvatarStack bots={bots} members={members}/><small>{members.length} 个 Bot</small><time>{when(s.updated_at)}</time></span>
            </button>
            <IconButton label={`删除群聊 ${s.title}`} disabled={disabled} onClick={() => onDelete(s)}><Trash2 size={13}/></IconButton>
          </div>
        );
      })}
      {!filteredGroups.length && <p className="directory-empty">{query ? '没有匹配的群聊' : '点上方「新建群聊」拉几个 Bot 一起协作。'}</p>}
    </>
  );

  const modules = (
    <>
      <button className={`directory-all ${!module ? 'selected' : ''}`} onClick={() => onModule('')}><Blocks size={16}/><span>全部模块</span><small>{mods.length}</small></button>
      <div className="directory-section-title"><span>内置模块</span><small>{builtinMods.length}</small></div>
      {builtinMods.map(m => <button key={m.id} className={`directory-module ${module === m.id ? 'selected' : ''}`} onClick={() => onModule(m.id)}>{m.id === 'develop' ? <Code2 size={17}/> : <Blocks size={17}/>}<span><strong>{m.name}</strong><small><i className={m.enabled ? 'enabled' : ''}/>{m.enabled ? '已启用' : '未启用'}</small></span><ChevronRight size={13}/></button>)}
      <div className="directory-section-title conversation-section"><span>已创建的自定义模块</span><IconButton label="新建自定义模块" onClick={() => onModule('develop')} disabled={disabled}><Plus size={14}/></IconButton></div>
      {customMods.map(m => <button key={m.id} className={`directory-module ${module === m.id ? 'selected' : ''}`} onClick={() => onModule(m.id)}><Code2 size={17}/><span><strong>{m.name}</strong><small><i className={m.enabled ? 'enabled' : ''}/>{m.enabled ? '面板显示' : '仅模块页'}</small></span><ChevronRight size={13}/></button>)}
      {!customMods.length && <p className="directory-empty">{query ? '没有匹配的自定义模块' : '点上方 + 或进入「自定义模块」创建新模块。'}</p>}
    </>
  );

  return (
    <aside className="secondary-nav" aria-label={view === 'chat' ? 'AI 助理二级目录' : '模块二级目录'}>
      <header><div><h2>{view === 'chat' ? 'AI 助理' : '模块'}</h2></div><IconButton label="收起二级目录" onClick={onClose}><ArrowLeft size={16}/></IconButton></header>
      {view === 'chat' && (
        <button className={`new-conversation ${mode === 'team' && !session ? 'selected' : ''}`} onClick={onNew} disabled={disabled} title="选几个 Bot 建一个群聊">
          <Users size={16}/><span>新建群聊<small>选几个 Bot 一起协作</small></span><Plus size={14}/>
        </button>
      )}
      <label className="directory-search"><Search size={15}/><input aria-label={view === 'chat' ? '搜索 Bot 和群聊' : '搜索模块'} placeholder={view === 'chat' ? '搜索 Bot 和群聊' : '搜索模块'} value={query} onChange={e => setQuery(e.target.value)}/></label>
      <div className="directory-scroll">{view === 'chat' ? chat : modules}</div>
      <footer>{view === 'chat' ? '点 Bot 进入单聊，历史会保留；新建群聊可拉多个 Bot 协作。' : '点开模块进入对应面板，或在自定义模块中创建新功能。'}</footer>
    </aside>
  );
}
