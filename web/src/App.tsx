import { useCallback, useEffect, useRef, useState } from 'react';
import { ArrowRight, Blocks, Bot, Check, ChevronRight, Code2, History, KeyRound, LayoutDashboard, LoaderCircle, LockKeyhole, LogOut, Menu, MessageSquare, RefreshCw, Server, Settings, Terminal, X } from 'lucide-react';
import { ThemeToggle } from './components/ThemeToggle';
import { api, type ApprovalDecision, type BotRow, type ChatLine, type ChatMode, type ChatStep, type Hist, type Host, type LiveChatHandlers, type Mod, type SecretRow, type SettingsState, type ServerTestResult, type ModelRoute, type Profile, type ChatSession, type Attachment, type TurnItem, type UpdateStatus, type Choice, type WorkspaceTarget } from './api';
import { approvalRuleKey } from './components/ApprovalCard';
import { Button, Field, IconButton, Modal, Notice } from './components/ui';
import { RoutePicker } from "./components/ModelPicker";
import SettingsPanel from "./pages/Settings";
import BackupMigration, { migrationStatus, type MigrationStatus } from './components/BackupMigration';
import AccountDialog, { Avatar } from './components/AccountDialog';
import SetupCredentials from './components/SetupCredentials';
import ApprovalChoice from './components/ApprovalChoice';
import { UpdateBanner } from './components/UpdateInfo';
import SecondaryNav from './components/SecondaryNav';
import Dashboard from './pages/Dashboard';
import type { PanelJob } from './components/PanelTaskResult';
import Chat from './pages/Chat';
import Bots from './pages/Bots';
import AddServer from './components/AddServer';
import { ApprovalDialog, HostKeyDialog } from './components/SafetyDialogs';
import type { ChatControlState } from './components/ChatControls';
import Servers from "./pages/Servers";
import Services from "./pages/Services";
import { HistoryPage, Modules, Secrets } from './pages/Resources';
import Assets from './pages/Assets';
import Develop, { CustomModulePage } from './pages/Develop';
import { BotAvatar } from './components/BotAvatar';
import { latestDirect } from './lib/chatGroups';
import Automations from './pages/Automations';
import Workspace from './pages/Workspace';

type View = 'panel' | 'chat' | 'workspace' | 'servers' | 'agents' | 'secrets' | 'modules' | 'history';
type Dialog = 'account' | 'settings' | 'modules' | 'server' | 'secret' | 'rotate' | 'agent' | 'rollback' | 'group' | null;
const navigation = [{id:'panel',label:'面板',icon:LayoutDashboard}, {id:'chat',label:'AI 助理',icon:MessageSquare}, {id:'workspace',label:'代码工作区',icon:Code2}, {id:'modules',label:'模块',icon:Blocks}, {id:'servers',label:'服务器',icon:Server}, {id:'agents',label:'Bot',icon:Bot}, {id:'secrets',label:'密钥保险箱',icon:KeyRound}, {id:'history',label:'变更记录',icon:History}] as const;
const emptyForm = {name:'',host:'',role:'',value:'',port:'22',username:'root',auth_type:'password',description:'',system_prompt:'',secret_mode:'manual',gen_length:'32',gen_charset:'alnum'};
type SecretForm = typeof emptyForm;
const secretCharsets = [['alnum','大小写字母和数字'],['symbols','字母、数字和安全符号'],['hex','十六进制'],['base64url','Base64URL'],['digits','纯数字']] as const;

/** Lets the user type a secret or have the server generate one that nobody sees. */
function SecretSource({form,onForm,rotate}:{form:SecretForm;onForm:(f:SecretForm)=>void;rotate?:boolean}) {
  const random = form.secret_mode === 'random';
  return <><div className="tabs secret-mode" role="group" aria-label="密钥来源">{[['manual','手动输入'],['random','随机生成']].map(([id,label]) => <button type="button" key={id} className={form.secret_mode===id?'active':''} aria-pressed={form.secret_mode===id} onClick={() => onForm({...form,secret_mode:id,value:''})}>{label}</button>)}</div>
    {random ? <><div className="form-grid"><Field label="长度" hint="8–256 个字符"><input type="number" min={8} max={256} required value={form.gen_length} onChange={e => onForm({...form,gen_length:e.target.value})}/></Field><Field label="字符集"><select value={form.gen_charset} onChange={e => onForm({...form,gen_charset:e.target.value})}>{secretCharsets.map(([id,label]) => <option key={id} value={id}>{label}</option>)}</select></Field></div><p className="model-hidden-note">由服务端用安全随机数生成并直接加密保存，浏览器和 Bot 都看不到明文。</p></>
      : <Field label={rotate ? '新的密钥内容' : '密钥内容'}><textarea required className="secret-textarea" autoComplete="off" value={form.value} placeholder={rotate ? '输入新的密码、API Key 或私钥' : '输入密码、API Key 或私钥'} rows={4} onChange={e => onForm({...form,value:e.target.value})}/></Field>}</>;
}
const message = (err: unknown) => err instanceof Error ? err.message : '操作失败，请重试';

/** The chat opens on the lead bot (主控) by default, never on specialist module bots. */
const defaultBot=(list:BotRow[])=>(list.find(x=>x.id==='main'&&x.enabled)||list.find(x=>x.enabled&&x.id!=='module-builder'&&x.id!=='develop')||list.find(x=>x.enabled))?.id||'';
/** Inbox sessions belong to one bot, so they open as a direct chat. */
const sessionMode=(s:ChatSession):ChatMode=>s.options?.mode==='direct'||s.id.startsWith('inbox-')||s.id.startsWith('auto-')?'direct':'team';

type MessageRow = Awaited<ReturnType<typeof api.messages>>[number];
/** Turns stored messages into chat lines, hiding the automatic follow-ups sent after a choice or secret card. */
function chatLines(rows:MessageRow[]):ChatLine[]{
  const visible=rows.filter((r,idx)=>{if(r.role!=='user'&&r.role!=='assistant')return false;if(r.role==='user'&&idx>0){const prev=rows[idx-1];const autoChoice=Boolean(prev.role==='assistant'&&prev.choice?.answer&&r.content.startsWith('我的选择：'));const autoSecret=Boolean(prev.role==='assistant'&&prev.secret_request?.provided_at&&r.content.startsWith('已把密钥「')&&r.content.endsWith('模型看不到明文。'));if(autoChoice||autoSecret)return false;}return true;});
  // Inbox messages carry a "[来自 X（id=…）] " prefix for the model; the UI shows a label instead.
  return visible.map(r=>({who:r.role==='user'?'user':'bot',text:r.from_bot?r.content.replace(/^\[来自 [^\]\n]*\] /,''):r.content,attachments:r.attachments,id:r.id,fromBot:r.from_bot||undefined,choice:r.choice||undefined,secret:r.secret_request||undefined,timeline:r.timeline||undefined}));
}

export default function App() {
  const [authed,setAuthed] = useState<boolean | null>(null);
  const [user,setUser] = useState('admin'); const [password,setPassword] = useState('');
  const [profile,setProfile]=useState<Profile>({id:'',username:'admin',display_name:'',avatar:''});
  const [sessions,setSessions]=useState<ChatSession[]>([]);const [activeBot,setActiveBot]=useState('');const [chatMode,setChatMode]=useState<ChatMode>('team');const [activeModule,setActiveModule]=useState('');const [secondaryOpen,setSecondaryOpen]=useState(true);const chatLoad=useRef(0);
  const [view,setView] = useState<View>('panel');
  const [workspaceTarget,setWorkspaceTarget] = useState<WorkspaceTarget>();
  const [chatWorkspace,setChatWorkspace] = useState<WorkspaceTarget>();
  const [hosts,setHosts] = useState<Host[]>([]); const [mods,setMods] = useState<Mod[]>([]); const [history,setHistory] = useState<Hist[]>([]); const [secrets,setSecrets] = useState<SecretRow[]>([]); const [bots,setBots] = useState<BotRow[]>([]);
  const [selected,setSelected] = useState('');const [addedServer,setAddedServer]=useState('');
  const [serviceFocus,setServiceFocus] = useState('');
  const [assetFocusServer,setAssetFocusServer]=useState<Host|null>(null);
  const [historyFilter,setHistoryFilter]=useState({filter:'all',query:''});
 const [chatConfig,setChatConfig]=useState<SettingsState|null>(null);const [controls,setControls]=useState<ChatControlState>({model:{provider_id:'',model:''},excluded:[],approval:'default'});const [attachments,setAttachments]=useState<Attachment[]>([]);const [uploading,setUploading]=useState(false);const uploadRef=useRef(false); const [dialog,setDialog] = useState<Dialog>(null);
  const [form,setForm] = useState(emptyForm); const [rotateID,setRotateID] = useState('');
  const [botModels,setBotModels] = useState<SettingsState|null>(null);
  const [botRoute,setBotRoute] = useState<ModelRoute>({provider_id:'',model:''});
  const [botModelsLoading,setBotModelsLoading] = useState(false);
  const [formError,setFormError] = useState(''); const [pageError,setPageError] = useState(''); const [notice,setNotice] = useState('');
  const [loading,setLoading] = useState(false); const [busy,setBusy] = useState(false);
  const [migration,setMigration] = useState<MigrationStatus|null>(null);
  const [apiPrompt,setApiPrompt] = useState(false);
  const [switching,setSwitching] = useState(''); const [testing,setTesting] = useState(''); const [rollback,setRollback] = useState('');
  const [draft,setDraft] = useState(''); const [lines,setLines] = useState<ChatLine[]>([]); const [session,setSession] = useState(''); const [sending,setSending] = useState(false); const [groupPick,setGroupPick] = useState<string[]>([]); const [groupName,setGroupName] = useState(''); const [projectTitle,setProjectTitle] = useState('');
  const sendingRef = useRef(false); const [mobileNav,setMobileNav] = useState(false);
  const [panelJob, setPanelJob] = useState<PanelJob | null>(null);
  const panelSession = useRef('');
  const chatAllowRules = useRef<Set<string>>(new Set());
  const panelAllowRules = useRef<Set<string>>(new Set());
  const chatApprovalResolvers = useRef<Map<string, (dec: ApprovalDecision) => void>>(new Map());
  const panelApprovalResolvers = useRef<Map<string, (dec: ApprovalDecision) => void>>(new Map());
  const [compact,setCompact] = useState(() => window.matchMedia('(max-width:760px)').matches); const [updated,setUpdated] = useState('');

  const load = useCallback(async () => {
    setLoading(true);
    try {
      const migrationState=await migrationStatus();setMigration(migrationState);
      if(migrationState.paused)return;
      const [h,m,r,s,b,c,settings] = await Promise.all([api.servers(),api.modules(),api.history(),api.secrets(),api.bots(),api.sessions(),api.settings()]);setChatConfig(settings);
      if(!settings.providers.some(p=>p.enabled)&&localStorage.getItem('onefrog-api-prompt')!=='dismissed')setApiPrompt(true);
      setHosts(h);setMods(m);setHistory(r);setSecrets(s);setBots(b);setSessions(c);setActiveBot(cur=>b.some(x=>x.id===cur)?cur:defaultBot(b));
      setSelected(cur => cur === '' || h.some(item => item.id === cur) ? cur : '');
      setUpdated(new Date().toLocaleTimeString('zh-CN',{hour:'2-digit',minute:'2-digit'})); setPageError('');
    } catch(err) {setPageError(message(err));} finally {setLoading(false);}
  },[]);
  useEffect(() => {api.me().then(res => {setUser(res.username);setProfile(res); setAuthed(true); if(!res.must_change_credentials) return load();}).catch(() => setAuthed(false));},[load]);
  useEffect(()=>{if(!authed||profile.must_change_credentials)return;const timer=setInterval(()=>{migrationStatus().then(s=>setMigration(old=>{if(old?.paused&&!s.paused)window.setTimeout(()=>location.reload(),100);return s;})).catch(()=>{});},3000);return()=>clearInterval(timer);},[authed,profile.must_change_credentials]);
  const [updateStatus,setUpdateStatus]=useState<UpdateStatus|null>(null);
  useEffect(()=>{if(!authed||profile.must_change_credentials)return;let active=true;const get=()=>api.updateStatus().then(s=>{if(active)setUpdateStatus(s);}).catch(()=>{});get();const timer=window.setInterval(get,60*60*1000);return()=>{active=false;window.clearInterval(timer);};},[authed,profile.must_change_credentials]);
  useEffect(() => {if(!notice) return; const timer = window.setTimeout(() => setNotice(''),4500); return () => clearTimeout(timer);},[notice]);
  // Bots write into each other's inboxes in the background. While the chat view
  // is idle, refresh the list so new inbox chats appear, and reload an open
  // inbox chat when it changes.
  const inboxSeen=useRef('');
  useEffect(()=>{
    if(!authed||view!=='chat'||sending||loading||uploading)return;
    let active=true;
    const tick=async()=>{
      try{
        const list=await api.sessions();if(!active||sendingRef.current||uploadRef.current)return;setSessions(list);
        const cur=session.startsWith('inbox-')||session.startsWith('auto-')?list.find(s=>s.id===session):undefined;
        const mark=cur?cur.id+'|'+cur.updated_at:'';
        if(!cur||inboxSeen.current===mark)return;
        const first=!inboxSeen.current.startsWith(cur.id+'|');inboxSeen.current=mark;if(first)return;
        const request=chatLoad.current;const rows=await api.messages(cur.id);
        if(active&&request===chatLoad.current&&!sendingRef.current)setLines(chatLines(rows));
      }catch{/* the next tick retries */}
    };
    const timer=window.setInterval(tick,10000);
    return()=>{active=false;window.clearInterval(timer);};
  },[authed,view,session,sending,loading,uploading]);
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
      if(dialog === 'secret' || dialog === 'rotate') {
        if(form.secret_mode === 'random') {
          const length = Number(form.gen_length);
          if(!Number.isInteger(length) || length < 8 || length > 256) throw new Error('长度必须是 8–256 的整数');
          const spec = {length, charset: form.gen_charset, description: form.description.trim()};
          if(dialog === 'secret') await api.generateSecret({name:form.name.trim(), ...spec});
          else await api.regenerateSecret(rotateID, spec);
        } else {
          const value = form.value;
          setForm(cur => ({...cur, value: ''}));
          if(dialog === 'secret') await api.createSecret({name:form.name.trim(),value,description:form.description.trim()});
          else await api.rotateSecret(rotateID,{value,description:form.description.trim()});
        }
      }
      if(dialog === 'agent') {if(!botModels||botModelsLoading) throw new Error('模型配置尚未加载，请关闭后重试');if(botRoute.provider_id&&!botRoute.model)throw new Error('请选择模型');await api.createBot({name:form.name.trim(),description:form.description.trim(),system_prompt:form.system_prompt.trim(),model_route:botRoute});}
      setDialog(null);setForm(emptyForm);setNotice('已保存');await load();
    } catch(err) {setFormError(message(err));} finally {setBusy(false);}
  }
  function openHistory(filter='all', query='') { setHistoryFilter({filter,query}); go('history'); }
  function openServices(id='') {setServiceFocus(id);setActiveModule('services');go('modules');}
  function openAssetForServer(host: Host) { setAssetFocusServer(host); setActiveModule('assets'); go('modules'); }
  function openServerById(id: string) { setSelected(id); go('servers'); }
  function openServerByName(name: string) { const hit = hosts.find(h => h.name === name || h.id === name); if (hit) setSelected(hit.id); go('servers'); }
  function openBotByName(name: string) { const hit = bots.find(b => b.name === name || b.id === name); if (hit && hit.enabled) { openBot(hit.id); go('chat'); } else { go('agents'); } }
  function openChat(text?: string) {go('chat');setMobileNav(false);if(text) setDraft(text);}
  /** newChat(botID) opens a direct chat with that bot; newChat() opens a team chat led by the lead bot. */
  function newChat(botID?:string){if(sendingRef.current||uploadRef.current)return;chatLoad.current++;chatAllowRules.current.clear();setChatMode(botID?'direct':'team');setActiveBot(botID||defaultBot(bots));setSession('');setProjectTitle('');setChatWorkspace(undefined);setLines([]);setDraft('');setSelected('');setAttachments([]);setControls({model:{provider_id:'',model:''},excluded:[],approval:'default'});setMobileNav(false);}
  /** Clicking a bot reopens its latest one-to-one chat, so leaving and coming back keeps the history. */
  function openBot(id:string){const hit=latestDirect(sessions,id);if(hit)restoreChat(hit);else newChat(id);}
  /** A new group chat starts by asking which bots join; every enabled bot is ticked by default. */
  function openGroupPicker(){if(sendingRef.current||uploadRef.current)return;open('group');setGroupName('');setGroupPick(bots.filter(b=>b.enabled).map(b=>b.id));}
  function startGroup(){const lead=defaultBot(bots);const enabled=bots.filter(b=>b.enabled).map(b=>b.id);newChat();setProjectTitle(groupName.trim());setControls(c=>({...c,excluded:enabled.filter(id=>id!==lead&&!groupPick.includes(id))}));setDialog(null);if(view!=='chat')go('chat');}
  async function removeChat(item:ChatSession){if(sendingRef.current||uploadRef.current)return;if(!window.confirm('删除对话「'+item.title+'」？会删除消息和项目工作记忆，不能恢复。'))return;try{await api.deleteSession(item.id);setSessions(cur=>cur.filter(s=>s.id!==item.id));if(session===item.id){chatLoad.current++;chatAllowRules.current.clear();setSession('');setProjectTitle('');setChatWorkspace(undefined);setLines([]);setDraft('');setAttachments([]);setSelected('');}setNotice('已删除对话');}catch(err){setPageError(message(err));}}
  async function restoreChat(item:ChatSession){if(sendingRef.current||uploadRef.current)return;const request=++chatLoad.current;setLoading(true);try{const rows=await api.messages(item.id);if(request!==chatLoad.current)return;const mode=sessionMode(item);chatAllowRules.current.clear();setChatMode(mode);setActiveBot(item.bot_id);setChatWorkspace(item.options?.workspace);setSelected(item.options?.workspace?.server_id||'');setSession(item.id);setProjectTitle(mode==='team'?item.title:'');setLines(chatLines(rows));setDraft('');setAttachments([]);setControls({model:item.options?.model_route||{provider_id:'',model:''},excluded:mode==='direct'?[]:(item.options?.excluded_bot_ids||[]).filter(id=>bots.some(b=>b.id===id)),approval:item.options?.approval_mode==='rules'?'off':item.options?.approval_mode||'default',review:item.options?.approval_route});setMobileNav(false);}catch(e){setPageError(message(e));}finally{if(request===chatLoad.current)setLoading(false);}}
  /** Per-turn options shared by send and card answers; a direct chat never carries team scope. */
  const turnOptions=()=>({bot_id:activeBot,workspace:chatWorkspace,mode:chatMode,model_route:controls.model,excluded_bot_ids:chatMode==='direct'?[]:controls.excluded,approval_mode:controls.approval,approval_route:controls.approval==='model'?controls.review:undefined});
  function startCodeTask(target:WorkspaceTarget, task:string) {
    if(sendingRef.current||uploadRef.current)return;
    newChat();
    setChatWorkspace(target);
    setSelected(target.server_id);
    setProjectTitle('开发 · '+(target.root.split('/').pop()||target.root));
    setDraft(task);
    go('chat');
  }
  async function openCodeTask(item:ChatSession) {
    if(sendingRef.current||uploadRef.current)return;
    await restoreChat(item);
    go('chat');
  }
  function viewCode() {
    if(chatWorkspace)setWorkspaceTarget(chatWorkspace);
    go('workspace');
  }
  async function uploadFiles(files:File[]){if(!files.length||uploadRef.current||sendingRef.current)return;if(files.some(f=>f.size>5*1024*1024)){setPageError('每个文件不能超过 5 MB');return;}if(files.length+attachments.length>4){setPageError('每轮最多上传 4 个附件');return;}if(files.reduce((n,f)=>n+f.size,0)+attachments.reduce((n,f)=>n+f.size,0)>12*1024*1024){setPageError('附件合计不能超过 12 MB');return;}uploadRef.current=true;setUploading(true);setPageError('');try{for(const file of files){const ref=await api.upload(file);setAttachments(cur=>[...cur,ref]);}}catch(e){setPageError(message(e));}finally{uploadRef.current=false;setUploading(false);}}

  function makeStreamHandlers(
    allowSet: Set<string>,
    resolvers: Map<string, (dec: ApprovalDecision) => void>,
    stepsOut: ChatStep[],
    timelineOut: TurnItem[],
    syncUI: (activeStatus: string) => void
  ): LiveChatHandlers {
    return {
      onSay: (bot, content) => {
        if (content.trim()) {
          timelineOut.push({ kind: 'say', bot, text: content.trim() });
          syncUI('正在继续执行…');
        }
      },
      onApproval: req => {
        const key = approvalRuleKey(req.server_id, req.command);
        if (!req.untrusted && !req.single_use && allowSet.has(key)) {
          return Promise.resolve({ allow: true, always: true });
        }
        timelineOut.push({ kind: 'approval_pending', pending_req: req });
        syncUI('等待你在上方卡片确认…');
        return new Promise<ApprovalDecision>(resolve => {
          resolvers.set(req.id, dec => {
            if (dec.allow && dec.always && !req.untrusted && !req.single_use) {
              allowSet.add(key);
            }
            const idx = timelineOut.findIndex(x => x.kind === 'approval_pending' && x.pending_req?.id === req.id);
            if (idx >= 0) {
              timelineOut[idx] = {
                kind: 'approval',
                approval: {
                  id: req.id,
                  bot_name: req.bot_name || req.bot,
                  server_id: req.server_id,
                  server_name: req.server_name,
                  command: req.command,
                  mode: req.mode,
                  risk: req.risk,
                  reasons: req.reasons,
                  model_reason: req.model_reason,
                  untrusted: req.untrusted,
                  status: dec.allow ? (dec.always ? 'always' : 'approved') : 'denied',
                },
              };
            }
            syncUI(dec.allow ? '已批准，正在继续执行…' : '已拒绝，正在整理结果…');
            resolve(dec);
          });
        });
      },
      onApprovalResolved: card => {
        const idx = timelineOut.findIndex(
          x => (x.kind === 'approval' && x.approval?.id === card.id) || (x.kind === 'approval_pending' && x.pending_req?.id === card.id)
        );
        if (idx >= 0) timelineOut[idx] = { kind: 'approval', approval: card };
        else timelineOut.push({ kind: 'approval', approval: card });
        syncUI('正在继续处理…');
      },
      onApprovalTimeout: id => {
        const fn = resolvers.get(id);
        if (fn) { resolvers.delete(id); fn({ allow: false }); }
      },
      onStep: step => {
        stepsOut.push(step);
        if (step.ok && step.server && (step.command || '').includes('接入')) {
          setSelected(step.server);
          setAddedServer(step.server);
        }
        if (step.kind === 'delegate') {
          timelineOut.push({ kind: 'delegate', bot: step.bot, target: step.target, task: step.task });
          syncUI('已交由「' + (step.target || '协作 Bot') + '」处理…');
        } else if (step.kind === 'message') {
          timelineOut.push({ kind: 'message', bot: step.bot, target: step.target, task: step.task });
          syncUI('已给「' + (step.target || 'Bot') + '」发消息，正在继续…');
        } else {
          timelineOut.push({ kind: 'step', bot: step.bot, command: step.command, server_id: step.server, ok: step.ok, output: step.output });
          const shortCmd = (step.command || '操作').slice(0, 32);
          syncUI('已完成「' + shortCmd + '」，正在继续…');
        }
      },
    };
  }

  function decideChatApproval(id: string, dec: ApprovalDecision) {
    const fn = chatApprovalResolvers.current.get(id);
    if (!fn) return;
    chatApprovalResolvers.current.delete(id);
    fn(dec);
  }

  function decidePanelApproval(id: string, dec: ApprovalDecision) {
    const fn = panelApprovalResolvers.current.get(id);
    if (!fn) return;
    panelApprovalResolvers.current.delete(id);
    fn(dec);
  }

  async function send() {
    if(sendingRef.current || uploadRef.current || loading || (!draft.trim()&&!attachments.length)) return;chatLoad.current++;
    if(!bots.some(b=>b.id===activeBot&&b.enabled)){setPageError('请先选取一个已启用的 Bot');return;}
    sendingRef.current = true;setSending(true);
    const text = draft.trim();const sentAttachments=[...attachments];setDraft('');setAttachments([]);
    setLines(cur => [...cur,{who:'user',text,attachments:sentAttachments},{who:'bot',text:'',pending:true,activeStatus:'正在思考…',steps:[],timeline:[]}]);
    const steps: ChatStep[] = [];
    const timeline: TurnItem[] = [];
    const paint = (reply: string,pending: boolean,extra?:Partial<ChatLine>) => setLines(cur => [...cur.slice(0,-1),{who:'bot',text:reply,pending,steps:[...steps],timeline:[...timeline],...extra}]);
    try {
      const handlers = makeStreamHandlers(chatAllowRules.current, chatApprovalResolvers.current, steps, timeline, status => paint('', true, { activeStatus: status }));
      const reply = await api.chatLive({content:text,server_id:selected,session_id:session,project_title:chatMode==='team'&&!session?projectTitle:undefined,...turnOptions(),attachment_ids:sentAttachments.map(f=>f.id)}, handlers);
      setSession(reply.session_id);
      paint(reply.text,false,{id:reply.message_id,choice:reply.choice,secret:reply.secret_request,timeline:reply.timeline||[...timeline]});
      await load();
      if(steps.some(step => step.ok && (step.command||'').includes('接入'))) setNotice('服务器已接入，已出现在服务器页面');
    }
    catch(err) {paint(message(err),false);setAttachments(sentAttachments);} finally {sendingRef.current=false;setSending(false);api.sessions().then(setSessions).catch(()=>{});}
  }
  /** Answers an ask_user card once; the answer becomes the next user message and resumes the turn. */
  async function answerChoice(card: ChatLine, values: string[], custom: string) {
    if(sendingRef.current || uploadRef.current || loading || !card.id || !card.choice || card.choice.answer || !session) return;chatLoad.current++;
    sendingRef.current = true;setSending(true);
    const sid = session, cardID = card.id;
    const mark = (answer?: Choice['answer']) => setLines(cur => cur.map(l => l.id === cardID && l.choice ? {...l,choice:{...l.choice,answer}} : l));
    mark({values,custom:custom.trim()||undefined});
    setLines(cur => [...cur,{who:'bot',text:'',pending:true,activeStatus:'收到选择，正在继续…',steps:[],timeline:[]}]);
    const steps: ChatStep[] = [];
    const timeline: TurnItem[] = [];
    const paint = (reply: string,pending: boolean,extra?:Partial<ChatLine>) => setLines(cur => [...cur.slice(0,-1),{who:'bot',text:reply,pending,steps:[...steps],timeline:[...timeline],...extra}]);
    const resync = async () => {try {const rows = await api.messages(sid);const row = rows.find(r => r.id === cardID);mark(row?.choice?.answer);} catch {mark(undefined);}};
    try {
      const handlers = makeStreamHandlers(chatAllowRules.current, chatApprovalResolvers.current, steps, timeline, status => paint('', true, { activeStatus: status }));
      const reply = await api.chatLive({content:'',server_id:selected,session_id:sid,...turnOptions(),choice_answer:{message_id:cardID,values,custom:custom.trim()||undefined}}, handlers);
      if(reply.answered){setLines(cur => cur.slice(0,-1));await resync();return;}
      paint(reply.text,false,{id:reply.message_id,choice:reply.choice,secret:reply.secret_request,timeline:reply.timeline||[...timeline]});await load();}
    catch(err) {paint(message(err),false);await resync();} finally {sendingRef.current=false;setSending(false);api.sessions().then(setSessions).catch(()=>{});}
  }
  async function answerSecret(card: ChatLine, value: string) {
    if(sendingRef.current || uploadRef.current || loading || !card.id || !card.secret || card.secret.provided_at || !session) return;chatLoad.current++;
    sendingRef.current = true;setSending(true);
    const sid = session, cardID = card.id;
    const mark = (provided?: string) => setLines(cur => cur.map(l => l.id === cardID && l.secret ? {...l,secret:{...l.secret,provided_at:provided}} : l));
    mark(new Date().toISOString());
    setLines(cur => [...cur,{who:'bot',text:'',pending:true,activeStatus:'密钥已加密保存，正在继续…',steps:[],timeline:[]}]);
    const steps: ChatStep[] = [];
    const timeline: TurnItem[] = [];
    const paint = (reply: string,pending: boolean,extra?:Partial<ChatLine>) => setLines(cur => [...cur.slice(0,-1),{who:'bot',text:reply,pending,steps:[...steps],timeline:[...timeline],...extra}]);
    const resync = async () => {try {const rows = await api.messages(sid);const row = rows.find(r => r.id === cardID);mark(row?.secret_request?.provided_at);} catch {mark(undefined);}};
    try {
      const handlers = makeStreamHandlers(chatAllowRules.current, chatApprovalResolvers.current, steps, timeline, status => paint('', true, { activeStatus: status }));
      const reply = await api.chatLive({content:'',server_id:selected,session_id:sid,...turnOptions(),secret_answer:{message_id:cardID,value}}, handlers);
      if(reply.answered){setLines(cur => cur.slice(0,-1));await resync();return;}
      paint(reply.text,false,{id:reply.message_id,choice:reply.choice,secret:reply.secret_request,timeline:reply.timeline||[...timeline]});await load();}
    catch(err) {setLines(cur => cur.slice(0,-1));setPageError(message(err));await resync();} finally {sendingRef.current=false;setSending(false);api.sessions().then(setSessions).catch(()=>{});}
  }
  async function runPanel(prompt: string, serverId?: string) {
    if (sendingRef.current || uploadRef.current || loading) return;
    const botID = defaultBot(bots);
    if (!bots.some(b => b.id === botID && b.enabled)) { setPageError('请先启用主控 Bot'); return; }
    if (serverId) setSelected(serverId);
    sendingRef.current = true; setSending(true);
    const steps: ChatStep[] = [];
    const timeline: TurnItem[] = [];
    setPanelJob({ pending: true, prompt, text: '', error: '', activeStatus: '正在处理面板请求…', steps, timeline: [], sessionId: panelSession.current, botId: botID, serverId });
    const paint = (patch: Partial<PanelJob>) => setPanelJob(cur => cur ? { ...cur, ...patch, steps: [...steps], timeline: patch.timeline || [...timeline] } : cur);
    try {
      const handlers = makeStreamHandlers(panelAllowRules.current, panelApprovalResolvers.current, steps, timeline, status => paint({ pending: true, activeStatus: status }));
      const reply = await api.chatLive({ content: prompt, server_id: serverId, session_id: panelSession.current, mode: 'direct', bot_id: botID, approval_mode: 'default' }, handlers);
      panelSession.current = reply.session_id || panelSession.current;
      paint({ pending: false, text: reply.text, error: '', sessionId: panelSession.current, messageId: reply.message_id, choice: reply.choice, secret: reply.secret_request, timeline: reply.timeline || [...timeline] });
      await load();
    } catch (err) {
      paint({ pending: false, error: message(err) });
    } finally {
      sendingRef.current = false; setSending(false);
    }
  }
  async function answerPanel(values: string[], custom: string) {
    const job = panelJob;
    if (sendingRef.current || uploadRef.current || loading || !job?.messageId || !job.choice || job.choice.answer || !panelSession.current) return;
    sendingRef.current = true; setSending(true);
    const cardID = job.messageId;
    const steps: ChatStep[] = [];
    const timeline: TurnItem[] = [];
    setPanelJob({ ...job, pending: true, activeStatus: '收到选择，正在继续…', error: '', steps, timeline: [], choice: { ...job.choice, answer: { values, custom: custom.trim() || undefined } } });
    const paint = (patch: Partial<PanelJob>) => setPanelJob(cur => cur ? { ...cur, ...patch, steps: [...steps], timeline: patch.timeline || [...timeline] } : cur);
    try {
      const handlers = makeStreamHandlers(panelAllowRules.current, panelApprovalResolvers.current, steps, timeline, status => paint({ pending: true, activeStatus: status }));
      const reply = await api.chatLive({ content: '', server_id: job.serverId, session_id: panelSession.current, mode: 'direct', bot_id: job.botId || defaultBot(bots), approval_mode: 'default', choice_answer: { message_id: cardID, values, custom: custom.trim() || undefined } }, handlers);
      if (reply.answered) { paint({ pending: false, text: '这个问题已经回答过了' }); return; }
      panelSession.current = reply.session_id || panelSession.current;
      paint({ pending: false, text: reply.text, error: '', sessionId: panelSession.current, messageId: reply.message_id, choice: reply.choice, secret: reply.secret_request, timeline: reply.timeline || [...timeline] });
      await load();
    } catch (err) {
      paint({ pending: false, error: message(err), choice: job.choice, secret: job.secret });
    } finally {
      sendingRef.current = false; setSending(false);
    }
  }
  async function answerPanelSecret(value: string) {
    const job = panelJob;
    if (sendingRef.current || uploadRef.current || loading || !job?.messageId || !job.secret || job.secret.provided_at || !panelSession.current) return;
    sendingRef.current = true; setSending(true);
    const cardID = job.messageId;
    const original = job.secret;
    const steps: ChatStep[] = [];
    const timeline: TurnItem[] = [];
    setPanelJob({ ...job, pending: true, activeStatus: '密钥已保存，正在继续…', error: '', steps, timeline: [], secret: { ...original, provided_at: new Date().toISOString() } });
    const paint = (patch: Partial<PanelJob>) => setPanelJob(cur => cur ? { ...cur, ...patch, steps: [...steps], timeline: patch.timeline || [...timeline] } : cur);
    try {
      const handlers = makeStreamHandlers(panelAllowRules.current, panelApprovalResolvers.current, steps, timeline, status => paint({ pending: true, activeStatus: status }));
      const reply = await api.chatLive({ content: '', server_id: job.serverId, session_id: panelSession.current, mode: 'direct', bot_id: job.botId || defaultBot(bots), approval_mode: 'default', secret_answer: { message_id: cardID, value } }, handlers);
      if (reply.answered) { paint({ pending: false, text: '这项密钥已经保存', secret: { ...original, provided_at: original.provided_at || new Date().toISOString() } }); return; }
      panelSession.current = reply.session_id || panelSession.current;
      paint({ pending: false, text: reply.text, error: '', sessionId: panelSession.current, messageId: reply.message_id, choice: reply.choice, secret: reply.secret_request, timeline: reply.timeline || [...timeline] });
      await load();
    } catch (err) {
      paint({ pending: false, error: message(err), secret: original });
    } finally {
      sendingRef.current = false; setSending(false);
    }
  }
  function openRotate(row: SecretRow) {
    setMobileNav(false);
    setForm({...emptyForm, name: row.name, description: row.desc});
    setRotateID(row.id);
    setFormError('');
    setDialog('rotate');
  }
  async function removeSecret(id: string) {
    const row = secrets.find(item => item.id === id);
    if (!row) return;
    if (!window.confirm('删除密钥「' + row.name + '」？命令里的 {{secret:' + row.name + '}} 会失效。')) return;
    try { await api.deleteSecret(id); setNotice('已删除'); await load(); } catch (err) { setPageError(message(err)); }
  }
  async function login() {
    setBusy(true);setFormError('');
    try {const res = await api.login(user,password);setUser(res.username);setProfile(res);setAuthed(true);if(res.must_change_credentials)return;setPassword('');await load();} catch(err) {setFormError(message(err));} finally {setBusy(false);}
  }
  async function logout() {try {await api.logout();chatLoad.current++;chatAllowRules.current.clear();panelAllowRules.current.clear();setAuthed(false);setLines([]);setSession('');setWorkspaceTarget(undefined);setChatWorkspace(undefined);setProjectTitle('');setPassword('');setNotice('');setDialog(null);setPanelJob(null);panelSession.current='';} catch(err) {setPageError(message(err));}}

  if(authed === null) return <div className="boot-screen"><LoaderCircle className="spin" size={23}/><span>连接工作台…</span></div>;
  if(!authed) return <div className="login-screen"><ThemeToggle className="login-theme"/><div className="login-brand"><span className="brand-icon"><img src="/assets/onefrog-logo.png" alt=""/></span><strong>OneFrog<span>OPERATIONS CONSOLE</span></strong></div><form className="login-card" onSubmit={e => {e.preventDefault();login();}}><h1>进入你的运维工作台</h1><p>服务器、Bot 和密钥，一个地方管理。</p><Field label="账号"><input value={user} autoComplete="username" onChange={e => setUser(e.target.value)} required placeholder="管理员账号"/></Field><Field label="密码"><input type="password" value={password} autoComplete="current-password" onChange={e => setPassword(e.target.value)} required placeholder="输入登录密码"/></Field>{formError && <Notice error>{formError}</Notice>}<Button type="submit" variant="primary" busy={busy}>进入工作台<ArrowRight size={16}/></Button><small><LockKeyhole size={12}/>自托管 · 你的基础设施，由你掌控</small></form><footer>ONEFROG / OPERATIONS, SIMPLIFIED.</footer></div>;
  if(profile.must_change_credentials) return <SetupCredentials profile={profile} initialPassword={password} onDone={async next => {setProfile(next);setUser(next.username);setPassword(''); if(!next.must_change_credentials) await load();}} onLogout={logout}/>;
  if(migration?.paused&&dialog!=='settings') return <div className="migration-landing"><header><h1>备份与迁移</h1><div className="migration-actions"><ThemeToggle/><Button onClick={logout}>退出登录</Button></div></header><BackupMigration/></div>;
  if(profile.approval_choice_required) return <ApprovalChoice onDone={async mode => {setProfile({...profile,approval_choice_required:false});setChatConfig(c=>c?{...c,approval:{...c.approval,mode}}:c);await load();}} onLogout={logout}/>;

  const currentLabel = navigation.find(n => n.id === view)?.label;
  return <div className="app-shell">
    {mobileNav && <div className="nav-backdrop" onClick={() => setMobileNav(false)}/>}
    <div className={`navigation-shell ${mobileNav?'open':''}`} inert={Boolean(dialog)||(compact&&!mobileNav)}><aside className="sidebar"><a className="brand" href="#panel" onClick={e=>{e.preventDefault();go('panel');}}><span className="brand-icon"><img src="/assets/onefrog-logo.png" alt=""/></span><strong>OneFrog<span>YOUR CONTROL SPACE</span>{updateStatus?.current && <em className="brand-version">{updateStatus.current}</em>}</strong></a><nav aria-label="主导航">{navigation.map(n=><button key={n.id} title={n.label} className={`nav-item ${view===n.id?'active':''} ${n.id==='modules'||n.id==='servers'?'nav-separated':''}`} onClick={()=>go(n.id)} aria-current={view===n.id?'page':undefined}><n.icon size={18} strokeWidth={1.6}/><span>{n.label}</span>{(n.id==='chat'||n.id==='modules')&&<ChevronRight size={13} className="nav-chevron"/>}</button>)}</nav><div className="sidebar-bottom"><button className="nav-item" onClick={openSettings} title="设置"><Settings size={18} strokeWidth={1.6}/><span>设置</span></button><div className="account"><button className="account-edit" title="个人账号" onClick={()=>open('account')}><Avatar profile={profile}/><span><strong>{profile.display_name||profile.username}</strong><small>个人账号</small></span></button><IconButton label="退出登录" onClick={logout} disabled={sending}><LogOut size={16}/></IconButton></div></div></aside>
    {(view==='chat'||view==='modules')&&secondaryOpen&&<SecondaryNav key={view} view={view} bots={bots} sessions={sessions} mods={mods} activeBot={activeBot} session={session} module={activeModule} disabled={sending||loading||uploading} mode={chatMode} onBot={openBot} onSession={restoreChat} onNew={openGroupPicker} onAddBot={()=>open('agent')} onDelete={removeChat} onModule={id=>{setActiveModule(id);if(id==='services')setServiceFocus('');setMobileNav(false);}} onClose={()=>setSecondaryOpen(false)}/>}</div>
    <main className="app-main" inert={Boolean(dialog) || mobileNav}><UpdateBanner status={updateStatus}/><div className="topbar"><IconButton label="展开导航" className="mobile-menu" onClick={() => setMobileNav(true)}><Menu size={20}/></IconButton><span className="breadcrumb">OneFrog<ChevronRight size={13}/><strong>{currentLabel}</strong></span><div className="topbar-right"><span className="sync-time">{updated ? `更新于 ${updated}` : '尚未同步'}</span><ThemeToggle/><IconButton label="刷新数据" onClick={load} disabled={loading}><RefreshCw size={15} className={loading ? 'spin' : ''}/></IconButton><span className="topbar-divider"/><span className="connection"><span className={`live-dot ${pageError ? 'warn' : ''}`}/>{pageError ? '同步异常' : 'API 已连接'}</span></div></div>
    {pageError && <div className="global-error"><Notice error>{pageError}</Notice><IconButton label="关闭错误提示" onClick={() => setPageError('')}><X size={15}/></IconButton></div>}
    <div className={`view-container ${view === 'chat' ? 'chat-view' : ''}`}>
    {view === 'panel' && <Dashboard hosts={hosts} mods={mods} history={history} bots={bots} job={panelJob} sending={sending} onSelect={setSelected} onRun={runPanel} onAnswer={answerPanel} onProvide={answerPanelSecret} onDecideApproval={decidePanelApproval} onModules={() => open('modules')} onSettings={openSettings} onServers={() => go('servers')} onBots={() => go('agents')} onHistory={openHistory} onAssets={() => {setActiveModule('assets'); go('modules');}} onOpenModule={(id,serverId) => {if(id==='services')openServices(serverId);else {setActiveModule(id); go('modules');}}}/>}
    {view === 'chat' && <Chat onAddServer={()=>open('server')} controls={controls} onControls={setControls} settings={chatConfig} mode={chatMode} workspace={chatWorkspace} onOpenWorkspace={viewCode} projectTitle={projectTitle} attachments={attachments} onFiles={uploadFiles} onRemoveFile={id=>setAttachments(cur=>cur.filter(f=>f.id!==id))} uploading={uploading} activeBot={bots.find(b=>b.id===activeBot)} hosts={hosts} bots={bots} secrets={secrets} selected={selected} onSelect={setSelected} draft={draft} onDraft={setDraft} lines={lines} onSend={send} sending={sending||loading} onNew={openGroupPicker} sessions={sessions} session={session} onSession={restoreChat} onDeleteSession={removeChat} onNewTopic={()=>newChat(activeBot)} onNotice={setNotice} onAnswer={answerChoice} onProvide={answerSecret} onDecideApproval={decideChatApproval} onGoServer={() => go('servers')} onGoAsset={openAssetForServer} onGoHistory={name => openHistory('all', name)}/>}
    {view === 'workspace' && <Workspace hosts={hosts} target={workspaceTarget} onTarget={setWorkspaceTarget} sessions={sessions} activeTask={chatWorkspace} onReturnToTask={()=>go('chat')} onNewTask={startCodeTask} onResume={openCodeTask} sending={sending||uploading} onAddServer={()=>open('server')}/>}
    {view === 'servers' && <Servers addedServer={addedServer} hosts={hosts} selected={selected} onSelect={setSelected} onAdd={() => open('server')} onTest={testServer} testing={testing} onChat={() => openChat()} onOpenServices={openServices} onOpenAsset={openAssetForServer} onOpenHistory={name => openHistory('all', name)} onChanged={load}/>}
    {view === 'agents' && <Bots bots={bots} onAdd={()=>open('agent')} onChanged={load} onChat={id=>{openBot(id);openChat();}}/>}
    {view === 'secrets' && <Secrets secrets={secrets} onAdd={() => open('secret')} onRotate={openRotate} onDelete={removeSecret} onCopyRef={async ref => { try { await navigator.clipboard.writeText(ref); setNotice('已复制密钥引用 ' + ref); } catch { setPageError('复制失败'); } }} onUseInChat={ref => { openChat((draft ? draft + ' ' : '') + ref); }}/>}
    {view === 'modules' && activeModule === 'services' && <Services hosts={hosts} focusServer={serviceFocus} onServer={openServerById} onAsk={(id,text)=>{newChat();setSelected(id);openChat(text);}}/>}
    {view === 'modules' && activeModule === 'assets' && <Assets hosts={hosts} focusServer={assetFocusServer} onConsumedFocus={() => setAssetFocusServer(null)} onOpenServer={openServerById} onChatServer={(id, name) => { newChat(); setSelected(id); openChat('检查服务器 ' + name + ' 的运行状态和磁盘情况。'); }} onAsk={text => {const bot = bots.find(b => (b.id === 'module-builder' || b.name === '模块管理' || b.name === '模块构建') && b.enabled); if (bot) newChat(bot.id); go('chat'); setDraft(text);}}/>}
    {view === 'modules' && activeModule === 'develop' && <Develop bots={bots} settings={chatConfig} onOpenChat={text => {const bot = bots.find(b => (b.id === 'develop' || b.name === '自定义模块' || b.name === '二次开发') && b.enabled); if (bot) newChat(bot.id); go('chat'); setDraft(text);}} onOpenSettings={openSettings} onOpenModule={id => setActiveModule(id)} onChanged={load}/>}
    {view === 'modules' && activeModule === 'automations' && <Automations bots={bots} hosts={hosts} onOpenSession={async id => {let item = sessions.find(s => s.id === id); if (!item) {try {const list = await api.sessions(); setSessions(list); item = list.find(s => s.id === id);} catch (e) {setPageError(message(e)); return;}} if (item) {await restoreChat(item); go('chat');} else setPageError('这段对话已被删除');}}/>}
    {view === 'modules' && activeModule !== '' && activeModule !== 'assets' && activeModule !== 'services' && activeModule !== 'develop' && activeModule !== 'automations' && mods.find(m => m.id === activeModule)?.type === 'custom' && <CustomModulePage mod={mods.find(m => m.id === activeModule)!} onBack={() => setActiveModule('develop')} onAskDevelop={text => {const bot = bots.find(b => (b.id === 'develop' || b.name === '自定义模块' || b.name === '二次开发') && b.enabled); if (bot) newChat(bot.id); go('chat'); setDraft(text);}} onChanged={load}/>}
    {view === 'modules' && activeModule !== 'assets' && activeModule !== 'services' && activeModule !== 'develop' && activeModule !== 'automations' && mods.find(m => m.id === activeModule)?.type !== 'custom' && <Modules mods={activeModule?mods.filter(m=>m.id===activeModule):mods} onToggle={toggle} pending={switching} onOpen={id => {if(id==='services')openServices();else setActiveModule(id);}}/>}
    {view === 'history' && <HistoryPage history={history} initialFilter={historyFilter.filter} initialQuery={historyFilter.query} onOpenServer={openServerByName} onOpenBot={openBotByName} onUndo={id => {setRollback(id);open('rollback');}}/>}
    </div></main>
    {notice && <div className="toast" role="status"><Check size={16}/>{notice}<IconButton label="关闭通知" onClick={() => setNotice('')}><X size={14}/></IconButton></div>}
    {hostKey && <HostKeyDialog serverID={hostKey.id} result={hostKey.result} onClose={() => {setHostKey(null); load();}} onDone={msg => {setHostKey(null); setNotice(msg); load();}}/>}
    <ApprovalDialog/>
    {dialog === 'account' && <AccountDialog profile={profile} onClose={close} onSaved={p=>{setProfile(p);setUser(p.username);setNotice('账号资料已保存');setDialog(null);}}/>}
    {dialog === 'settings' && <SettingsPanel bots={bots} onUpdateStatus={setUpdateStatus} onClose={close} onSaved={() => {setNotice('设置已保存，下一轮任务生效');load();}}/>}
    {apiPrompt && dialog !== 'settings' && <Modal title="要先配置模型 API 吗？" note="还没有可用的 API 渠道。配好之后，Bot 才能对话和执行任务。之后也可以从左侧「设置」进入。" cancel="稍后再说" confirm="去配置" onClose={()=>{localStorage.setItem('onefrog-api-prompt','dismissed');setApiPrompt(false);}} onSubmit={()=>{localStorage.setItem('onefrog-api-prompt','dismissed');setApiPrompt(false);openSettings();}}/>}
    {dialog === 'modules' && <Modal title="定制面板模块" note="选择你想持续关注的内容，开关即时保存。" onClose={close} confirm="完成"><div className="module-picker">{mods.map(m => <div key={m.id}><span className="host-icon"><Blocks size={16}/></span><div><strong>{m.name}</strong><small>{m.desc}</small></div><button className={`switch ${m.enabled ? 'on' : ''}`} role="switch" aria-checked={m.enabled} aria-label={`显示${m.name}`} disabled={!!switching} onClick={() => toggle(m.id)}><i/></button></div>)}</div></Modal>}
    {dialog === 'server' && <AddServer onClose={close} onChanged={load} onDone={id=>{setSelected(id);setAddedServer(id);setNotice('服务器接入状态已更新');load();}}/>}
    {dialog === 'secret' && <Modal title="添加密钥" note="在这里录入敏感内容，Bot 只能按名称引用。" onClose={close} onSubmit={submit} busy={busy} error={formError}><Field label="密钥名称" hint="命令引用格式：{{secret:名称}}"><input required value={form.name} placeholder="例如 production-db-password" onChange={e => setForm({...form,name:e.target.value})}/></Field><SecretSource form={form} onForm={setForm}/><Field label="用途说明"><input value={form.description} placeholder="例如：生产数据库连接凭据" onChange={e => setForm({...form,description:e.target.value})}/></Field><div className="inline-help"><LockKeyhole size={15}/>关闭窗口会清空未保存的敏感内容。</div></Modal>}{dialog === 'rotate' && <Modal title="轮换密钥" note={'替换「' + form.name + '」的密文。名称不变，明文不会显示。'} onClose={close} onSubmit={submit} busy={busy} error={formError} confirm="保存新内容"><Field label="密钥名称"><input value={form.name} readOnly/></Field><SecretSource form={form} onForm={setForm} rotate/><Field label="用途说明"><input value={form.description} placeholder="可选，留空则清空说明" onChange={e => setForm({...form,description:e.target.value})}/></Field><div className="inline-help"><LockKeyhole size={15}/>提交后输入框会清空。模型仍然只能看到名称。</div></Modal>}
    {dialog === 'agent' && <Modal title="创建 Bot" note="描述分工，让主控知道何时把任务交给它。" onClose={close} onSubmit={submit} busy={busy} submitDisabled={botModelsLoading||!botModels} error={formError} confirm="创建 Bot"><Field label="Bot 名称"><input required placeholder="例如：数据库专家" value={form.name} onChange={e => setForm({...form,name:e.target.value})}/></Field><Field label="职责描述"><input required placeholder="例如：分析数据库性能与慢查询" value={form.description} onChange={e => setForm({...form,description:e.target.value})}/></Field>{botModelsLoading?<div className="loading-inline"><LoaderCircle size={16} className="spin"/>加载可选模型…</div>:botModels&&<><RoutePicker label="Bot 模型" note="跟随协作默认模型，或为此 Bot 单独选择渠道和模型；创建后可在设置中修改。" providers={botModels.providers} value={botRoute} onChange={setBotRoute} inherit/>{!botModels.providers.some(p=>p.enabled)&&<p className="model-hidden-note">尚无可用渠道，可先跟随默认配置，稍后在设置中配置。</p>}</>}<Field label="工作指令" hint="可选，用于约束这个 Bot 的工作方式。"><textarea placeholder="例如：先收集证据，只执行只读查询。" rows={4} value={form.system_prompt} onChange={e => setForm({...form,system_prompt:e.target.value})}/></Field></Modal>}
    {dialog === 'rollback' && <Modal title="撤回详情" note="当前版本仅支持记录撤回标记，暂不恢复服务器上的文件或服务。" onClose={close} confirm="知道了"><div className="rollback-detail"><Terminal size={18}/><code>{history.find(h => h.id === rollback)?.action}</code></div><p className="muted">实际恢复需要有效快照与恢复执行器。这里暂不提供恢复操作，避免将记录更新误认为服务器已恢复。</p></Modal>}
    {dialog === 'group' && <Modal title="新建群聊" note="每个群聊对应一个独立项目。主控负责派工，成员共享本项目的工作记忆。" onClose={close} onSubmit={startGroup} confirm="开始群聊"><Field label="项目名称（可选）"><input maxLength={60} value={groupName} onChange={e=>setGroupName(e.target.value)} placeholder="例如：生产环境迁移"/></Field><div className="group-picker">{bots.filter(b=>b.enabled).map(b=>{const lead=b.id===defaultBot(bots);const on=lead||groupPick.includes(b.id);return <label key={b.id} className={on?'on':''}><BotAvatar bots={bots} id={b.id} className="small-avatar"/><div><strong>{b.name}{lead&&<span className="tag">主控</span>}</strong><small>{b.description||'按主控分派完成任务'}</small></div><input type="checkbox" checked={on} disabled={lead} aria-label={(on?'移出 ':'加入 ')+b.name} onChange={e=>setGroupPick(cur=>e.target.checked?[...cur,b.id]:cur.filter(id=>id!==b.id))}/></label>;})}</div><p className="group-picker-count">已选 {bots.filter(b=>b.enabled&&(b.id===defaultBot(bots)||groupPick.includes(b.id))).length} / {bots.filter(b=>b.enabled).length} 个 Bot</p></Modal>}
  </div>;
}
