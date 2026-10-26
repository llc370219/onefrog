import { useCallback, useEffect, useRef, useState } from 'react';
import { ArrowRight, Blocks, Bot, Check, ChevronRight, History, KeyRound, LayoutDashboard, LoaderCircle, LockKeyhole, LogOut, Menu, MessageSquare, RefreshCw, Server, Settings, Terminal, X } from 'lucide-react';
import { api, type BotRow, type ChatLine, type ChatStep, type Hist, type Host, type Mod, type SecretRow, type SettingsState, type ServerTestResult, type ModelRoute, type Profile, type ChatSession, type Attachment, type UpdateStatus } from './api';
import { Button, Field, IconButton, Modal, Notice } from './components/ui';
import { RoutePicker } from "./components/ModelPicker";
import SettingsPanel from "./pages/Settings";
import AccountDialog, { Avatar } from './components/AccountDialog';
import SetupCredentials from './components/SetupCredentials';
import ApprovalChoice from './components/ApprovalChoice';
import { UpdateBanner } from './components/UpdateInfo';
import SecondaryNav from './components/SecondaryNav';
import Dashboard from './pages/Dashboard';
import Chat from './pages/Chat';
import Bots from './pages/Bots';
import AddServer from './components/AddServer';
import { ApprovalDialog, HostKeyDialog } from './components/SafetyDialogs';
import type { ChatControlState } from './components/ChatControls';
import Servers from "./pages/Servers";
import { HistoryPage, Modules, Secrets } from './pages/Resources';
import Assets from './pages/Assets';

type View = 'panel' | 'chat' | 'servers' | 'agents' | 'secrets' | 'modules' | 'history';
type Dialog = 'account' | 'settings' | 'modules' | 'server' | 'secret' | 'agent' | 'rollback' | null;
const navigation = [{id:'panel',label:'面板',icon:LayoutDashboard}, {id:'chat',label:'AI 助理',icon:MessageSquare}, {id:'modules',label:'模块',icon:Blocks}, {id:'servers',label:'服务器',icon:Server}, {id:'agents',label:'Bot',icon:Bot}, {id:'secrets',label:'密钥保险箱',icon:KeyRound}, {id:'history',label:'变更记录',icon:History}] as const;
const emptyForm = {name:'',host:'',role:'',value:'',port:'22',username:'root',auth_type:'password',description:'',system_prompt:''};
const message = (err: unknown) => err instanceof Error ? err.message : '操作失败，请重试';

export default function App() {
  const [authed,setAuthed] = useState<boolean | null>(null);
  const [user,setUser] = useState('admin'); const [password,setPassword] = useState('');
  const [profile,setProfile]=useState<Profile>({id:'',username:'admin',display_name:'',avatar:''});
  const [sessions,setSessions]=useState<ChatSession[]>([]);const [activeBot,setActiveBot]=useState('');const [activeModule,setActiveModule]=useState('');const [secondaryOpen,setSecondaryOpen]=useState(true);const chatLoad=useRef(0);
  const [view,setView] = useState<View>('panel');
  const [hosts,setHosts] = useState<Host[]>([]); const [mods,setMods] = useState<Mod[]>([]); const [history,setHistory] = useState<Hist[]>([]); const [secrets,setSecrets] = useState<SecretRow[]>([]); const [bots,setBots] = useState<BotRow[]>([]);
  const [selected,setSelected] = useState('');const [addedServer,setAddedServer]=useState('');
 const [chatConfig,setChatConfig]=useState<SettingsState|null>(null);const [controls,setControls]=useState<ChatControlState>({model:{provider_id:'',model:''},excluded:[],approval:'default'});const [attachments,setAttachments]=useState<Attachment[]>([]);const [uploading,setUploading]=useState(false);const uploadRef=useRef(false); const [dialog,setDialog] = useState<Dialog>(null);
  const [form,setForm] = useState(emptyForm);
  const [botModels,setBotModels] = useState<SettingsState|null>(null);
  const [botRoute,setBotRoute] = useState<ModelRoute>({provider_id:'',model:''});
  const [botModelsLoading,setBotModelsLoading] = useState(false);
  const [formError,setFormError] = useState(''); const [pageError,setPageError] = useState(''); const [notice,setNotice] = useState('');
  const [loading,setLoading] = useState(false); const [busy,setBusy] = useState(false);
  const [switching,setSwitching] = useState(''); const [testing,setTesting] = useState(''); const [rollback,setRollback] = useState('');
  const [draft,setDraft] = useState(''); const [lines,setLines] = useState<ChatLine[]>([]); const [session,setSession] = useState(''); const [sending,setSending] = useState(false);
  const sendingRef = useRef(false); const [mobileNav,setMobileNav] = useState(false);
  const [compact,setCompact] = useState(() => window.matchMedia('(max-width:760px)').matches); const [updated,setUpdated] = useState('');

  const load = useCallback(async () => {
    setLoading(true);
    try {
      const [h,m,r,s,b,c,settings] = await Promise.all([api.servers(),api.modules(),api.history(),api.secrets(),api.bots(),api.sessions(),api.settings()]);setChatConfig(settings);
      setHosts(h);setMods(m);setHistory(r);setSecrets(s);setBots(b);setSessions(c);setActiveBot(cur=>b.some(x=>x.id===cur)?cur:b.find(x=>x.enabled)?.id||'');
      setSelected(cur => cur === '' || h.some(item => item.id === cur) ? cur : '');
      setUpdated(new Date().toLocaleTimeString('zh-CN',{hour:'2-digit',minute:'2-digit'})); setPageError('');
    } catch(err) {setPageError(message(err));} finally {setLoading(false);}
  },[]);
  useEffect(() => {api.me().then(res => {setUser(res.username);setProfile(res); setAuthed(true); if(!res.must_change_credentials) return load();}).catch(() => setAuthed(false));},[load]);
  const [updateStatus,setUpdateStatus]=useState<UpdateStatus|null>(null);
  useEffect(()=>{if(!authed||profile.must_change_credentials)return;let active=true;const get=()=>api.updateStatus().then(s=>{if(active)setUpdateStatus(s);}).catch(()=>{});get();const timer=window.setInterval(get,60*60*1000);return()=>{active=false;window.clearInterval(timer);};},[authed,profile.must_change_credentials]);
  useEffect(() => {if(!notice) return; const timer = window.setTimeout(() => setNotice(''),4500); return () => clearTimeout(timer);},[notice]);
  useEffect(() => {const media = window.matchMedia('(max-width:760px)'); const changed = () => {setCompact(media.matches); if(!media.matches) setMobileNav(false);}; media.addEventListener('change',changed); return () => media.removeEventListener('change',changed);},[]);
  useEffect(() => {function key(e: KeyboardEvent) {if(e.key === 'Escape') setMobileNav(false);} document.addEventListener('keydown',key); return () => document.removeEventListener('keydown',key);},[]);
  useEffect(() => {
    if(dialog!=='agent') return;
    let active=true;setBotModels(null);setBotModelsLoading(true);setBotRoute({provider_id:'',model:''});
    api.settings().then(s=>{if(active)setBotModels(s);}).catch(e=>{if(active)setFormError(message(e));}).finally(()=>{if(active)setBotModelsLoading(false);});
    return()=>{active=false;};
  },[dialog]);
  function go(next: View) {setView(next);setSecondaryOpen(true);if(next!=='chat'&&next!=='modules')setMobileNav(false);setPageError('');}
  function open(next: Dialog) {setMobileNav(false);setForm(emptyForm);setFormError('');setDialog(next);}
  function close() {if(busy) return; setDialog(null);setForm(emptyForm);setFormError('');}
  function openSettings() {open('settings');}
  async function toggle(id: string) {
    if(switching) return;
    const item = mods.find(m => m.id === id); if(!item) return;
    setSwitching(id);
    try {await api.enableModule(id,!item.enabled); await load();} catch(err) {setPageError(message(err));} finally {setSwitching('');}
  }
  const [hostKey, setHostKey] = useState<{ id: string; result: ServerTestResult } | null>(null);
  async function testServer(id: string) {
    if(testing) return; setTesting(id);
    try {const res = await api.testServer(id); await load(); if(res.ok) setNotice('SSH 连接成功'); else if(res.status==='host_key_unknown'||res.status==='host_key_mismatch') setHostKey({id,result:res}); else setPageError('连接失败：'+res.detail);} catch(err) {setPageError(message(err));} finally {setTesting('');}
  }
  async function submit() {
    if(busy) return; setFormError('');setBusy(true);
    try {
      if(dialog === 'server') {const port = Number(form.port); if(!Number.isInteger(port) || port < 1 || port > 65535) throw new Error('端口必须是 1–65535 的整数'); await api.createServer({name:form.name.trim(),host:form.host.trim(),role:form.role.trim(),port,username:form.username.trim(),auth_type:form.auth_type,secret:form.value});}
      if(dialog === 'secret') await api.createSecret({name:form.name.trim(),value:form.value,description:form.description.trim()});
      if(dialog === 'agent') {if(!botModels||botModelsLoading) throw new Error('模型配置尚未加载，请关闭后重试');if(botRoute.provider_id&&!botRoute.model)throw new Error('请选择模型');await api.createBot({name:form.name.trim(),description:form.description.trim(),system_prompt:form.system_prompt.trim(),model_route:botRoute});}
      setDialog(null);setForm(emptyForm);setNotice('已保存');await load();
    } catch(err) {setFormError(message(err));} finally {setBusy(false);}
  }
  function openChat(text?: string) {go('chat');setMobileNav(false);if(text) setDraft(text);}
  function newChat(botID=bots.find(b=>b.id===activeBot&&b.enabled)?.id||bots.find(b=>b.enabled)?.id||''){if(sendingRef.current||uploadRef.current)return;chatLoad.current++;setActiveBot(botID);setSession('');setLines([]);setDraft('');setSelected('');setAttachments([]);setControls({model:{provider_id:'',model:''},excluded:[],approval:'default'});setMobileNav(false);}
  async function restoreChat(item:ChatSession){if(sendingRef.current||uploadRef.current)return;const request=++chatLoad.current;setLoading(true);try{const rows=await api.messages(item.id);if(request!==chatLoad.current)return;setActiveBot(item.bot_id);setSelected('');setSession(item.id);setLines(rows.filter(r=>r.role==='user'||r.role==='assistant').map(r=>({who:r.role==='user'?'user':'bot',text:r.content,attachments:r.attachments})));setDraft('');setAttachments([]);setControls({model:item.options?.model_route||{provider_id:'',model:''},excluded:(item.options?.excluded_bot_ids||[]).filter(id=>bots.some(b=>b.id===id)),approval:item.options?.approval_mode==='rules'?'off':item.options?.approval_mode||'default',review:item.options?.approval_route});setMobileNav(false);}catch(e){setPageError(message(e));}finally{if(request===chatLoad.current)setLoading(false);}}
  function chooseBot(id:string){if(sendingRef.current||uploadRef.current)return;chatLoad.current++;setActiveBot(id);setSession('');setLines([]);setControls(cur=>({...cur,model:{provider_id:'',model:''},excluded:cur.excluded.filter(x=>x!==id)}));}
  async function uploadFiles(files:File[]){if(!files.length||uploadRef.current||sendingRef.current)return;if(files.some(f=>f.size>5*1024*1024)){setPageError('每个文件不能超过 5 MB');return;}if(files.length+attachments.length>4){setPageError('每轮最多上传 4 个附件');return;}if(files.reduce((n,f)=>n+f.size,0)+attachments.reduce((n,f)=>n+f.size,0)>12*1024*1024){setPageError('附件合计不能超过 12 MB');return;}uploadRef.current=true;setUploading(true);setPageError('');try{for(const file of files){const ref=await api.upload(file);setAttachments(cur=>[...cur,ref]);}}catch(e){setPageError(message(e));}finally{uploadRef.current=false;setUploading(false);}}
  async function send() {
    if(sendingRef.current || uploadRef.current || loading || (!draft.trim()&&!attachments.length)) return;chatLoad.current++;
    if(!bots.some(b=>b.id===activeBot&&b.enabled)){setPageError('请先选取一个已启用的主 Bot');return;}
    sendingRef.current = true;setSending(true);
    const text = draft.trim();const sentAttachments=[...attachments];setDraft('');setAttachments([]);
    setLines(cur => [...cur,{who:'user',text,attachments:sentAttachments},{who:'bot',text:'',pending:true,steps:[]}]);
    const steps: ChatStep[] = [];
    const paint = (reply: string,pending: boolean) => setLines(cur => [...cur.slice(0,-1),{who:'bot',text:reply,pending,steps:[...steps]}]);
    try {const reply = await api.chatLive({content:text,server_id:selected,session_id:session,bot_id:activeBot,model_route:controls.model,excluded_bot_ids:controls.excluded,approval_mode:controls.approval,approval_route:controls.approval==='model'?controls.review:undefined,attachment_ids:sentAttachments.map(f=>f.id)},step => {steps.push(step);paint('',true);});setSession(reply.session_id);paint(reply.text,false);await load();}
    catch(err) {paint(message(err),false);setAttachments(sentAttachments);} finally {sendingRef.current=false;setSending(false);api.sessions().then(setSessions).catch(()=>{});}
  }
  async function login() {
    setBusy(true);setFormError('');
    try {const res = await api.login(user,password);setUser(res.username);setProfile(res);setAuthed(true);if(res.must_change_credentials)return;setPassword('');await load();} catch(err) {setFormError(message(err));} finally {setBusy(false);}
  }
  async function logout() {try {await api.logout();chatLoad.current++;setAuthed(false);setLines([]);setSession('');setPassword('');setNotice('');setDialog(null);} catch(err) {setPageError(message(err));}}
  if(authed === null) return <div className="boot-screen"><LoaderCircle className="spin" size={23}/><span>连接工作台…</span></div>;
  if(!authed) return <div className="login-screen"><div className="login-brand"><span className="brand-icon"><img src="/assets/onefrog-logo.png" alt=""/></span><strong>OneFrog<span>OPERATIONS CONSOLE</span></strong></div><form className="login-card" onSubmit={e => {e.preventDefault();login();}}><span className="eyebrow">WELCOME BACK</span><h1>进入你的运维工作台</h1><p>服务器、Bot 和密钥，一个地方管理。</p><Field label="账号"><input value={user} autoComplete="username" onChange={e => setUser(e.target.value)} required placeholder="管理员账号"/></Field><Field label="密码"><input type="password" value={password} autoComplete="current-password" onChange={e => setPassword(e.target.value)} required placeholder="输入登录密码"/></Field>{formError && <Notice error>{formError}</Notice>}<Button type="submit" variant="primary" busy={busy}>进入工作台<ArrowRight size={16}/></Button><small><LockKeyhole size={12}/>自托管 · 你的基础设施，由你掌控</small></form><footer>ONEFROG / OPERATIONS, SIMPLIFIED.</footer></div>;
  if(profile.must_change_credentials) return <SetupCredentials profile={profile} initialPassword={password} onDone={async next => {setProfile(next);setUser(next.username);setPassword(''); if(!next.must_change_credentials) await load();}} onLogout={logout}/>;
  if(profile.approval_choice_required) return <ApprovalChoice onDone={async mode => {setProfile({...profile,approval_choice_required:false});setChatConfig(c=>c?{...c,approval:{...c.approval,mode}}:c);await load();}} onLogout={logout}/>;

  const currentLabel = navigation.find(n => n.id === view)?.label;
  return <div className="app-shell">
    {mobileNav && <div className="nav-backdrop" onClick={() => setMobileNav(false)}/>}
    <div className={`navigation-shell ${mobileNav?'open':''}`} inert={Boolean(dialog)||(compact&&!mobileNav)}><aside className="sidebar"><a className="brand" href="#panel" onClick={e=>{e.preventDefault();go('panel');}}><span className="brand-icon"><img src="/assets/onefrog-logo.png" alt=""/></span><strong>OneFrog<span>YOUR CONTROL SPACE</span></strong></a><nav aria-label="主导航">{navigation.map((n,i)=><button key={n.id} title={n.label} className={`nav-item ${view===n.id?'active':''} ${i===2||i===3?'nav-separated':''}`} onClick={()=>go(n.id)} aria-current={view===n.id?'page':undefined}><n.icon size={18} strokeWidth={1.6}/><span>{n.label}</span>{(n.id==='chat'||n.id==='modules')&&<ChevronRight size={13} className="nav-chevron"/>}</button>)}</nav><div className="sidebar-bottom"><button className="nav-item" onClick={openSettings} title="设置"><Settings size={18} strokeWidth={1.6}/><span>设置</span></button><div className="account"><button className="account-edit" title="个人账号" onClick={()=>open('account')}><Avatar profile={profile}/><span><strong>{profile.display_name||profile.username}</strong><small>个人账号</small></span></button><IconButton label="退出登录" onClick={logout} disabled={sending}><LogOut size={16}/></IconButton></div></div></aside>
    {(view==='chat'||view==='modules')&&secondaryOpen&&<SecondaryNav key={view} view={view} bots={bots} sessions={sessions} mods={mods} activeBot={activeBot} session={session} module={activeModule} disabled={sending||loading||uploading} onBot={newChat} onSession={restoreChat} onNew={()=>newChat()} onAddBot={()=>open('agent')} onModule={id=>{setActiveModule(id);setMobileNav(false);}} onClose={()=>setSecondaryOpen(false)}/>}</div>
    <main className="app-main" inert={Boolean(dialog) || mobileNav}><UpdateBanner status={updateStatus}/><div className="topbar"><IconButton label="展开导航" className="mobile-menu" onClick={() => setMobileNav(true)}><Menu size={20}/></IconButton><span className="breadcrumb">OneFrog<ChevronRight size={13}/><strong>{currentLabel}</strong></span><div className="topbar-right"><span className="sync-time">{updated ? `更新于 ${updated}` : '尚未同步'}</span><IconButton label="刷新数据" onClick={load} disabled={loading}><RefreshCw size={15} className={loading ? 'spin' : ''}/></IconButton><span className="topbar-divider"/><span className="connection"><span className={`live-dot ${pageError ? 'warn' : ''}`}/>{pageError ? '同步异常' : 'API 已连接'}</span></div></div>
    {pageError && <div className="global-error"><Notice error>{pageError}</Notice><IconButton label="关闭错误提示" onClick={() => setPageError('')}><X size={15}/></IconButton></div>}
    <div className={`view-container ${view === 'chat' ? 'chat-view' : ''}`}>
    {view === 'panel' && <Dashboard hosts={hosts} mods={mods} history={history} bots={bots} selected={selected} onSelect={setSelected} onChat={openChat} onModules={() => open('modules')} onSettings={openSettings} onServers={() => go('servers')} onAssets={() => {setActiveModule('assets'); go('modules');}}/>}
    {view === 'chat' && <Chat onAddServer={()=>open('server')} controls={controls} onControls={setControls} settings={chatConfig} onBot={chooseBot} attachments={attachments} onFiles={uploadFiles} onRemoveFile={id=>setAttachments(cur=>cur.filter(f=>f.id!==id))} uploading={uploading} activeBot={bots.find(b=>b.id===activeBot)} hosts={hosts} bots={bots} selected={selected} onSelect={setSelected} draft={draft} onDraft={setDraft} lines={lines} onSend={send} sending={sending||loading} onNew={()=>newChat()} onNotice={setNotice}/>}
    {view === 'servers' && <Servers addedServer={addedServer} hosts={hosts} selected={selected} onSelect={setSelected} onAdd={() => open('server')} onTest={testServer} testing={testing} onChat={() => openChat()}/>}
    {view === 'agents' && <Bots bots={bots} onAdd={()=>open('agent')} onChanged={load} onChat={id=>{newChat(id);openChat();}}/>}
    {view === 'secrets' && <Secrets secrets={secrets} onAdd={() => open('secret')}/>}
    {view === 'modules' && activeModule === 'assets' && <Assets hosts={hosts} onAsk={text => {const bot = bots.find(b => (b.id === 'module-builder' || b.name === '模块构建') && b.enabled); if (bot) newChat(bot.id); go('chat'); setDraft(text);}}/>}
    {view === 'modules' && activeModule !== 'assets' && <Modules mods={activeModule?mods.filter(m=>m.id===activeModule):mods} onToggle={toggle} pending={switching} onOpen={id => setActiveModule(id)}/>}
    {view === 'history' && <HistoryPage history={history} onUndo={id => {setRollback(id);open('rollback');}}/>}
    </div></main>
    {notice && <div className="toast" role="status"><Check size={16}/>{notice}<IconButton label="关闭通知" onClick={() => setNotice('')}><X size={14}/></IconButton></div>}
    {hostKey && <HostKeyDialog serverID={hostKey.id} result={hostKey.result} onClose={() => {setHostKey(null); load();}} onDone={msg => {setHostKey(null); setNotice(msg); load();}}/>}
    <ApprovalDialog/>
    {dialog === 'account' && <AccountDialog profile={profile} onClose={close} onSaved={p=>{setProfile(p);setUser(p.username);setNotice('账号资料已保存');setDialog(null);}}/>}
    {dialog === 'settings' && <SettingsPanel bots={bots} onUpdateStatus={setUpdateStatus} onClose={close} onSaved={() => {setNotice('设置已保存，下一轮任务生效');load();}}/>}
    {dialog === 'modules' && <Modal title="定制面板模块" note="选择你想持续关注的内容，开关即时保存。" onClose={close} confirm="完成"><div className="module-picker">{mods.map(m => <div key={m.id}><span className="host-icon"><Blocks size={16}/></span><div><strong>{m.name}</strong><small>{m.desc}</small></div><button className={`switch ${m.enabled ? 'on' : ''}`} role="switch" aria-checked={m.enabled} aria-label={`显示${m.name}`} disabled={!!switching} onClick={() => toggle(m.id)}><i/></button></div>)}</div></Modal>}
    {dialog === 'server' && <AddServer onClose={close} onChanged={load} onDone={id=>{setSelected(id);setAddedServer(id);setNotice('服务器接入状态已更新');load();}}/>}
    {dialog === 'secret' && <Modal title="添加密钥" note="在这里录入敏感内容，Bot 只能按名称引用。" onClose={close} onSubmit={submit} busy={busy} error={formError}><Field label="密钥名称" hint="命令引用格式：{{secret:名称}}"><input required value={form.name} placeholder="例如 production-db-password" onChange={e => setForm({...form,name:e.target.value})}/></Field><Field label="密钥内容"><textarea required className="secret-textarea" autoComplete="off" value={form.value} placeholder="输入密码、API Key 或私钥" rows={4} onChange={e => setForm({...form,value:e.target.value})}/></Field><Field label="用途说明"><input value={form.description} placeholder="例如：生产数据库连接凭据" onChange={e => setForm({...form,description:e.target.value})}/></Field><div className="inline-help"><LockKeyhole size={15}/>关闭窗口会清空未保存的敏感内容。</div></Modal>}
    {dialog === 'agent' && <Modal title="创建 Bot" note="描述分工，让主控知道何时把任务交给它。" onClose={close} onSubmit={submit} busy={busy} submitDisabled={botModelsLoading||!botModels} error={formError} confirm="创建 Bot"><Field label="Bot 名称"><input required placeholder="例如：数据库专家" value={form.name} onChange={e => setForm({...form,name:e.target.value})}/></Field><Field label="职责描述"><input required placeholder="例如：分析数据库性能与慢查询" value={form.description} onChange={e => setForm({...form,description:e.target.value})}/></Field>{botModelsLoading?<div className="loading-inline"><LoaderCircle size={16} className="spin"/>加载可选模型…</div>:botModels&&<><RoutePicker label="Bot 模型" note="跟随协作默认模型，或为此 Bot 单独选择渠道和模型；创建后可在设置中修改。" providers={botModels.providers} value={botRoute} onChange={setBotRoute} inherit/>{!botModels.providers.some(p=>p.enabled)&&<p className="model-hidden-note">尚无可用渠道，可先跟随默认配置，稍后在设置中配置。</p>}</>}<Field label="工作指令" hint="可选，用于约束这个 Bot 的工作方式。"><textarea placeholder="例如：先收集证据，只执行只读查询。" rows={4} value={form.system_prompt} onChange={e => setForm({...form,system_prompt:e.target.value})}/></Field></Modal>}
    {dialog === 'rollback' && <Modal title="撤回详情" note="当前版本仅支持记录撤回标记，暂不恢复服务器上的文件或服务。" onClose={close} confirm="知道了"><div className="rollback-detail"><Terminal size={18}/><code>{history.find(h => h.id === rollback)?.action}</code></div><p className="muted">实际恢复需要有效快照与恢复执行器。这里暂不提供恢复操作，避免将记录更新误认为服务器已恢复。</p></Modal>}
  </div>;
}
