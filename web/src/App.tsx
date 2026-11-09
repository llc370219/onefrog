import { useCallback, useEffect, useRef, useState } from 'react';
import { ArrowRight, Blocks, Bot, Check, ChevronRight, Code2, History, KeyRound, LayoutDashboard, LoaderCircle, LockKeyhole, LogOut, Menu, MessageSquare, RefreshCw, Server, Settings, Terminal, X } from 'lucide-react';
import { ThemeToggle } from './components/ThemeToggle';
import { api, taskIsActive, type BotRow, type ChatLine, type ChatMode, type ChatRequest, type Hist, type Host, type Mod, type SecretRow, type SecretRequest, type SettingsState, type ServerTestResult, type ModelRoute, type Profile, type ChatSession, type Attachment, type UpdateStatus, type Choice, type WorkspaceTarget, type TaskView } from './api';
import { Button, Field, IconButton, Modal, Notice } from './components/ui';
import { RoutePicker } from "./components/ModelPicker";
import SettingsPanel from "./pages/Settings";
import BackupMigration, { migrationStatus, type MigrationStatus } from './components/BackupMigration';
import AccountDialog, { Avatar } from './components/AccountDialog';
import SetupCredentials from './components/SetupCredentials';
import ApprovalChoice from './components/ApprovalChoice';
import { UpdateBanner } from './components/UpdateInfo';
import SecondaryNav from './components/SecondaryNav';
import { chatLines, type MessageRow } from './lib/chatLines';
import { clientRequestID, createTask as submitDurableTask, taskKeyOf as taskKeyOfList, taskStatusText, useTaskPoll } from './lib/taskFeed';
import Dashboard from './pages/Dashboard';
import type { PanelJob } from './components/PanelTaskResult';
import Chat from './pages/Chat';
import Bots from './pages/Bots';
import AddServer from './components/AddServer';
import { HostKeyDialog } from './components/SafetyDialogs';
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

type View = 'panel' | 'frog-bot' | 'chat' | 'workspace' | 'servers' | 'agents' | 'secrets' | 'modules' | 'history';
type Dialog = 'account' | 'settings' | 'modules' | 'server' | 'secret' | 'rotate' | 'agent' | 'rollback' | 'group' | null;
const navigation = [{id:'panel',label:'面板',icon:LayoutDashboard}, {id:'frog-bot',label:'AI 助理',icon:MessageSquare}, {id:'chat',label:'运维会话',icon:Terminal}, {id:'workspace',label:'代码工作区',icon:Code2}, {id:'modules',label:'模块',icon:Blocks}, {id:'servers',label:'服务器',icon:Server}, {id:'agents',label:'Bot',icon:Bot}, {id:'secrets',label:'密钥保险箱',icon:KeyRound}, {id:'history',label:'变更记录',icon:History}] as const;
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
  // The durable task the dashboard card is following (created by runPanel).
  const panelTask = useRef('');
  const [panelTasks, setPanelTasks] = useState<TaskView[]>([]);
  // Durable tasks of the open session. The runtime keeps running without this
  // page, so the UI only creates a task, polls its real state and reads the
  // transcript; it never fakes a finished reply.
  const [sessionTasks,setSessionTasks] = useState<TaskView[]>([]);
  // Every task of the operator (all sessions), so a child task running in
  // another session still shows its own approvals and Bot label.
  const [allTasks,setAllTasks] = useState<TaskView[]>([]);
  // In-flight task controls, keyed per task (value 'id:action'). A ref is the
  // synchronous guard, a state copy re-renders only the affected cards; other
  // tasks' controls stay interactive while one is in flight.
  const taskBusyRef = useRef<Map<string,string>>(new Map());
  const [taskBusy,setTaskBusy] = useState<string[]>([]);
  // Only written at the exact moments the open session changes (never synced
  // from render state), so an async poll or late control response can always
  // tell which chat it belongs to.
  const sessionRef = useRef('');
  const sessionTasksRef = useRef<TaskView[]>([]); sessionTasksRef.current = sessionTasks;
  // Signature of the task list last polled; a change is when new transcript
  // rows may exist, so only then is the session read again.
  const taskKey = useRef('');
  // Terminal tasks stay visible with their real result/error until dismissed.
  // Answers typed into a choice/secret card, re-applied if a poll reloads the
  // transcript before the server has stored the answer.
  const localCardAnswers = useRef<Map<string,{choice?:Choice['answer'];secret?:string}>>(new Map());
  // Signature of the transcript last painted, so a poll that finds no change
  // does not replace `lines` (which would re-scroll and fight the reader).
  const linesKey = useRef('');
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
        if(active&&request===chatLoad.current&&!sendingRef.current)setLines(chatLines(mergeLocalAnswers(rows)));
      }catch{/* the next tick retries */}
    };
    const timer=window.setInterval(tick,10000);
    return()=>{active=false;window.clearInterval(timer);};
  },[authed,view,session,sending,loading,uploading]);
  // Reloads the transcript of one session, keeping card answers this tab just
  // sent so a poll cannot make an answered card look unanswered again.
  function mergeLocalAnswers(rows:MessageRow[]):MessageRow[]{
    if(!localCardAnswers.current.size)return rows;
    const merged=rows.map(row=>{
      const local=localCardAnswers.current.get(row.id);
      if(!local)return row;
      const next={...row};
      if(local.choice&&next.choice&&!next.choice.answer)next.choice={...next.choice,answer:local.choice};
      if(local.secret&&next.secret_request&&!next.secret_request.provided_at)next.secret_request={...next.secret_request,provided_at:local.secret};
      return next;
    });
    // The server confirmed an answer once it is stored: stop replaying it locally.
    for(const id of [...localCardAnswers.current.keys()]){
      const row=merged.find(r=>r.id===id);
      if(!row){continue;}
      const local=localCardAnswers.current.get(id);
      if((local?.choice&&row.choice?.answer)||(local?.secret&&row.secret_request?.provided_at))localCardAnswers.current.delete(id);
    }
    return merged;
  }
  async function loadTaskLines(sid:string){
    const request=chatLoad.current;
    try{
      const rows=await api.messages(sid);
      if(request!==chatLoad.current||sid!==sessionRef.current)return;
      const key=rows.length+':'+(rows[rows.length-1]?.id||'');
      if(key===linesKey.current&&!localCardAnswers.current.size)return;
      linesKey.current=key;
      setLines(chatLines(mergeLocalAnswers(rows)));
    }catch{/* the next poll retries */}
  }
  function taskKeyOf(sid:string,items:TaskView[]){return sid+'#'+taskKeyOfList(items);}
  /** Dashboard wording differs slightly: it points at the card below. */
  function panelStatusText(t:TaskView){
    const text=taskStatusText(t);
    return t.status==='waiting_user'?'等待你在下方卡片回答…':text;
  }
  function applyTasks(sid:string,items:TaskView[]){
    // A task list of another session must never enter the open chat.
    if(sid!==sessionRef.current)return;
    sessionTasksRef.current=items;
    taskKey.current=taskKeyOf(sid,items);
    setSessionTasks(items);
  }
  /** Puts a task returned by a create/control call first without waiting for the poll. */
  function adoptTask(task:TaskView){
    const sid=task.session_id||sessionRef.current;
    applyTasks(sid,[task,...sessionTasksRef.current.filter(t=>t.id!==task.id)]);
    // Even a task of another session updates the global list so its card and
    // approvals stay current while it runs elsewhere.
    setAllTasks(cur => cur.some(t=>t.id===task.id) ? cur.map(t=>t.id===task.id?task:t) : [task,...cur]);
  }
  /** Applies one task returned by a control call to the visible list. */
  function mergeTask(task:TaskView){
    if(task.id===panelTask.current)setPanelTasks([task]);
    const sid=task.session_id||sessionRef.current;
    const cur=sessionTasksRef.current;
    applyTasks(sid,cur.some(t=>t.id===task.id)?cur.map(t=>t.id===task.id?task:t):[task,...cur]);
    // A child task may belong to another session; keep the global list in
    // step so its card and approvals stay current.
    setAllTasks(cur => cur.some(t=>t.id===task.id) ? cur.map(t=>t.id===task.id?task:t) : [task,...cur]);
  }
  // Poll the durable tasks of the open session. The runtime owns the work, so
  // a refresh or reconnect finds the same task again through this list.
  // Keep tasks that already belong to this session (a just-created task);
  // only a list from a different session is dropped.
  useEffect(()=>{
    if(view!=='chat'||!session)return;
    if(sessionTasksRef.current.some(t=>t.session_id!==session)){
      sessionTasksRef.current=[];
      setSessionTasks([]);
      taskKey.current='';
    }
  },[view,session]);
  // Poll the durable tasks of the open session. The runtime owns the work, so
  // a refresh or reconnect finds the same task again through this list.
  useTaskPoll(Boolean(authed&&!profile.must_change_credentials&&view==='chat'&&session),4000,async(isCurrent)=>{
    const sid=sessionRef.current;
    if(!sid||sendingRef.current)return;
    const revision=chatLoad.current;
    const previousKey=taskKey.current;
    const items=await api.tasks(sid);
    // A response requested before a send, navigation or task action may
    // predate the accepted bubble or undo an already confirmed cancellation.
    if(!isCurrent()||sid!==sessionRef.current||revision!==chatLoad.current||
      previousKey!==taskKey.current||sendingRef.current)return;
    const key=taskKeyOf(sid,items);
    const changed=key!==taskKey.current;
    if(changed)applyTasks(sid,items);
    // Completion is recorded before writeFinal appends the assistant reply.
    // A one-shot read on the terminal transition can miss that write; keep
    // checking the transcript. loadTaskLines skips unchanged rows by key, so
    // this polling never repaints or re-scrolls a reader of older messages.
    if(!sendingRef.current&&isCurrent())await loadTaskLines(sid);
  },[authed,profile.must_change_credentials,view,session]);
  // Global task list: a child task can run in another session, so approvals
  // and Bot labels are read across sessions, not only for the open chat.
  useTaskPoll(Boolean(authed&&!profile.must_change_credentials&&view==='chat'),8000,async(isCurrent)=>{
    const items=await api.tasks();
    if(!isCurrent())return;
    setAllTasks(items);
  },[authed,profile.must_change_credentials,view]);
  // The dashboard card follows its durable task; results come from the task
  // and the session transcript, never from a stream that a reload can lose.
  useTaskPoll(Boolean(authed&&view==='panel'),4000,async(isCurrent)=>{
    const id=panelTask.current;
    if(!id)return;
    const task=await api.task(id);
    if(!isCurrent()||panelTask.current!==id)return;
    setPanelTasks(cur => cur.some(t=>t.id===task.id) ? cur.map(t=>t.id===task.id?task:t) : [...cur,task]);
    setPanelJob(cur=>{
      if(!cur||cur.taskID!==id)return cur;
      const pending=taskIsActive(task);
      const status=panelStatusText(task);
      const replyPending=task.status==='waiting_user';
      if(cur.pending===pending&&cur.activeStatus===status&&cur.text===(task.result||'')&&(cur.error||'')===(task.status==='failed'?(task.last_error||'任务失败'):''))return cur;
      return {...cur,pending,activeStatus:status,text:task.result||cur.text,
        error:task.status==='failed'?(task.last_error||'任务失败'):'',
        taskID:task.id, sessionId:task.session_id||cur.sessionId,
        choice:replyPending?cur.choice:undefined};
    });
    // A waiting task asked a question through the transcript: read it so
    // the real choice/secret card appears, then the operator can answer.
    if(task.status==='waiting_user'&&panelSession.current){
      const rows=await api.messages(panelSession.current);
      if(!isCurrent()||panelTask.current!==id)return;
      const visible=chatLines(rows);
      const last=[...visible].reverse().find(l=>l.who==='bot'&&(l.choice||l.secret));
      if(last)setPanelJob(cur=>cur&&cur.taskID===id?{...cur,messageId:last.id,choice:last.choice,secret:last.secret}:cur);
    }
  },[authed,view,panelJob?.taskID]);
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
  function newChat(botID?:string){if(sendingRef.current||uploadRef.current)return;chatLoad.current++;localCardAnswers.current.clear();taskKey.current='';linesKey.current='';sessionRef.current='';sessionTasksRef.current=[];setChatMode(botID?'direct':'team');setActiveBot(botID||defaultBot(bots));setSession('');setSessionTasks([]);setProjectTitle('');setChatWorkspace(undefined);setLines([]);setDraft('');setSelected('');setAttachments([]);setControls({model:{provider_id:'',model:''},excluded:[],approval:'default'});setMobileNav(false);}
  /** Clicking a bot reopens its latest one-to-one chat, so leaving and coming back keeps the history. */
  function openBot(id:string){const hit=latestDirect(sessions,id);if(hit)restoreChat(hit);else newChat(id);}
  /** A new group chat starts by asking which bots join; every enabled bot is ticked by default. */
  function openGroupPicker(){if(sendingRef.current||uploadRef.current)return;open('group');setGroupName('');setGroupPick(bots.filter(b=>b.enabled).map(b=>b.id));}
  function startGroup(){const lead=defaultBot(bots);const enabled=bots.filter(b=>b.enabled).map(b=>b.id);newChat();setProjectTitle(groupName.trim());setControls(c=>({...c,excluded:enabled.filter(id=>id!==lead&&!groupPick.includes(id))}));setDialog(null);if(view!=='chat')go('chat');}
  async function removeChat(item:ChatSession){if(sendingRef.current||uploadRef.current)return;if(!window.confirm('删除对话「'+item.title+'」？会删除消息和项目工作记忆，不能恢复。'))return;try{await api.deleteSession(item.id);setSessions(cur=>cur.filter(s=>s.id!==item.id));if(session===item.id){chatLoad.current++;localCardAnswers.current.clear();taskKey.current='';linesKey.current='';sessionRef.current='';sessionTasksRef.current=[];setSession('');setSessionTasks([]);setProjectTitle('');setChatWorkspace(undefined);setLines([]);setDraft('');setAttachments([]);setSelected('');}setNotice('已删除对话');}catch(err){setPageError(message(err));}}
  async function restoreChat(item:ChatSession){if(sendingRef.current||uploadRef.current)return;const request=++chatLoad.current;const prevSession=sessionRef.current;// Switch identity and clear the task list before anything async can return.
    sessionRef.current=item.id;sessionTasksRef.current=[];setSessionTasks([]);localCardAnswers.current.clear();taskKey.current='';linesKey.current='';setLoading(true);
    try{const rows=await api.messages(item.id);if(request!==chatLoad.current)return;const mode=sessionMode(item);linesKey.current=rows.length+':'+(rows[rows.length-1]?.id||'');setChatMode(mode);setActiveBot(item.bot_id);setChatWorkspace(item.options?.workspace);setSelected(item.options?.workspace?.server_id||'');setSession(item.id);setProjectTitle(mode==='team'?item.title:'');setLines(chatLines(rows));setDraft('');setAttachments([]);setControls({model:item.options?.model_route||{provider_id:'',model:''},excluded:mode==='direct'?[]:(item.options?.excluded_bot_ids||[]).filter(id=>bots.some(b=>b.id===id)),approval:item.options?.approval_mode==='rules'?'off':item.options?.approval_mode||'default',review:item.options?.approval_route});setMobileNav(false);}catch(e){if(request===chatLoad.current){sessionRef.current=prevSession;sessionTasksRef.current=[];setSessionTasks([]);setPageError(message(e));}}finally{if(request===chatLoad.current)setLoading(false);}}
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

  /** Pauses, resumes or cancels a durable task; the runtime owns the actual work. */
  async function taskAction(id:string, action:'pause'|'resume'|'cancel'|'withdraw') {
    if(taskBusyRef.current.has(id)) return;
    taskBusyRef.current.set(id,id+':'+action);
    setTaskBusy(cur => [...cur,id+':'+action]);
    try {
      const task = await api.taskAction(id,action);
      mergeTask(task);
      setNotice(action==='withdraw' ? '已取消排队' : action==='pause' ? '任务已暂停' : action==='resume' ? '任务已继续' : '回复已停止');
    } catch(err) { setPageError(message(err)); }
    finally {
      // Clears only this task's entry: other in-flight controls stay busy.
      taskBusyRef.current.delete(id);
      setTaskBusy(cur => cur.filter(entry => entry !== id+':'+action));
    }
  }
  /** Records a durable approval decision for a parked task. */
  async function taskApproval(id:string, approvalID:string, allow:boolean) {
    if(taskBusyRef.current.has(id)) return;
    taskBusyRef.current.set(id,id+':'+approvalID);
    setTaskBusy(cur => [...cur,id+':'+approvalID]);
    try {
      const task = await api.decideTaskApproval(id,approvalID,allow,allow?'':'用户在任务卡片中拒绝');
      mergeTask(task);
      setNotice(allow ? '已批准，任务继续执行' : '已拒绝该操作');
    } catch(err) { setPageError(message(err)); }
    finally {
      taskBusyRef.current.delete(id);
      setTaskBusy(cur => cur.filter(entry => entry !== id+':'+approvalID));
    }
  }

  async function send() {
    if(sendingRef.current || uploadRef.current || loading || (!draft.trim()&&!attachments.length)) return;
    if(!bots.some(b=>b.id===activeBot&&b.enabled)){setPageError('请先选取一个已启用的 Bot');return;}
    chatLoad.current++;
    const request = chatLoad.current;
    sendingRef.current = true;setSending(true);
    const originalDraft = draft;
    const text = draft.trim() || '请查看我发来的附件。';const sentAttachments=[...attachments];
    const sid = session;
    setDraft('');setAttachments([]);
    // One stable client request id identifies this submission, so a retry of
    // the identical body is deduplicated by the runtime instead of running
    // twice.
    const requestID = clientRequestID();
    setLines(cur => [...cur,{id:requestID,who:'user',text,attachments:sentAttachments}]);
    const body:ChatRequest = {content:text,server_id:selected,session_id:sid,project_title:chatMode==='team'&&!sid?projectTitle:undefined,...turnOptions(),attachment_ids:sentAttachments.map(f=>f.id),client_request_id:requestID,ordered_chat:true};
    // 202 only means the task was accepted: the runtime finishes it in the
    // background, so the composer is released right away and progress is read
    // from the task, never invented here. This helper is the only submission
    // call: on a lost response it resends the identical body once.
    const result = await submitDurableTask(body);
    if(result.ok){
      const next = result.task.session_id || sid;
      if(request===chatLoad.current){
        // Still the same chat: enter the (possibly new) session before adopting.
        if(next){sessionRef.current=next;setSession(next);}
        adoptTask(result.task);
        // The durable task represents a queued bubble until the transcript
        // includes its exact message id. Replace the optimistic copy.
        if(result.task.user_message_id)setLines(cur=>cur.filter(line=>line.id!==requestID));
        if(sid&&sid===next)await loadTaskLines(next);
      } else {
        // The operator already moved to another chat: record globally only.
        setAllTasks(cur => cur.some(t=>t.id===result.task.id) ? cur.map(t=>t.id===result.task.id?result.task:t) : [result.task,...cur]);
      }
      if(result.attempts>1)setNotice('网络中断，但任务已创建，正在后台执行');
    } else if(request===chatLoad.current){
      setLines(cur => cur.filter(line=>line.id!==requestID));
      setDraft(current=>current ? originalDraft+'\n'+current : originalDraft);setAttachments(sentAttachments);
      const detail=message(result.error);
      setPageError(result.uncertain
        ? detail+'。这次提交是否已被接收无法确认，刷新页面核对任务列表后再决定重发，避免重复执行。'
        : detail);
    } else {
      setPageError(message(result.error));
    }
    sendingRef.current=false;setSending(false);api.sessions().then(setSessions).catch(()=>{});
  }
  /** Send text into the exact active turn, never via the next-turn queue. */
  async function correctTurn(taskID: string) {
    if(sendingRef.current||uploadRef.current||loading||!draft.trim()||attachments.length)return;
    const text = draft;
    sendingRef.current=true;setSending(true);setPageError('');
    try {
      const updated = await api.guideTask(taskID, text, true);
      mergeTask(updated);
      setDraft(current=>current===text?'':current);
      setNotice('纠正已送达当前轮，将在下一步生效');
    } catch(err) {
      const status=(err as {status?:number}|null)?.status;
      setPageError(!status||status>=500 ? message(err)+'。无法确认是否已送达，请核对对话后再发送。' : message(err));
    } finally {
      sendingRef.current=false;setSending(false);
    }
  }
  /** Answers an ask_user card once; the answer resumes the same turn. */
  async function answerChoice(card: ChatLine, values: string[], custom: string, fromComposer = false) {
    if(sendingRef.current || uploadRef.current || loading || !card.id || !card.choice || card.choice.answer || !session) return;
    chatLoad.current++;
    const request = chatLoad.current;
    const sid = session, cardID = card.id;
    const answer:Choice['answer'] = {values,custom:custom.trim()||undefined};
    const mark = (value?:Choice['answer']) => setLines(cur => cur.map(l => l.id === cardID && l.choice ? {...l,choice:{...l.choice,answer:value}} : l));
    mark(answer);
    localCardAnswers.current.set(cardID,{choice:answer});
    sendingRef.current = true;setSending(true);
    const requestID = clientRequestID();
    const body:ChatRequest = {content:'',server_id:selected,session_id:sid,...turnOptions(),choice_answer:{message_id:cardID,values,custom:custom.trim()||undefined},client_request_id:requestID};
    // The backend resumes the task this card came from; the response is a
    // TaskView, so keep following that same task. The helper resends the
    // identical answer once on a lost response, never on a rejection.
    const result = await submitDurableTask(body);
    if(result.ok){
      if(fromComposer)setDraft(current=>current===draft?'':current);
      if(request===chatLoad.current){sessionRef.current=result.task.session_id||sid;setSession(result.task.session_id||sid);}
      await adoptTask(result.task);
    } else if(request===chatLoad.current){
      localCardAnswers.current.delete(cardID);
      mark(undefined);
      const detail=message(result.error);
      setPageError(result.uncertain
        ? detail+'。这个回答是否已被接收无法确认，刷新页面核对任务和卡片状态后再作答。'
        : detail);
    } else {
      setPageError(message(result.error));
    }
    sendingRef.current=false;setSending(false);api.sessions().then(setSessions).catch(()=>{});
  }
  async function answerSecret(card: ChatLine, value: string) {
    if(sendingRef.current || uploadRef.current || loading || !card.id || !card.secret || card.secret.provided_at || !session) return;
    chatLoad.current++;
    const request = chatLoad.current;
    const sid = session, cardID = card.id;
    const mark = (provided?: string) => setLines(cur => cur.map(l => l.id === cardID && l.secret ? {...l,secret:{...l.secret,provided_at:provided}} : l));
    const ack = new Date().toISOString();
    mark(ack);
    // Only the vault acknowledgement is kept; the plaintext lives in this
    // call and was already cleared from the card.
    localCardAnswers.current.set(cardID,{secret:ack});
    sendingRef.current = true;setSending(true);
    const requestID = clientRequestID();
    const body:ChatRequest = {content:'',server_id:selected,session_id:sid,...turnOptions(),secret_answer:{message_id:cardID,value},client_request_id:requestID};
    const result = await submitDurableTask(body);
    if(result.ok){
      if(request===chatLoad.current){sessionRef.current=result.task.session_id||sid;setSession(result.task.session_id||sid);}
      await adoptTask(result.task);
    } else if(request===chatLoad.current){
      localCardAnswers.current.delete(cardID);
      mark(undefined);
      const detail=message(result.error);
      setPageError(result.uncertain
        ? detail+'。这项密钥是否已保存无法确认，刷新页面核对卡片状态后再提交。'
        : detail);
    } else {
      setPageError(message(result.error));
    }
    sendingRef.current=false;setSending(false);api.sessions().then(setSessions).catch(()=>{});
  }
  async function runPanel(prompt: string, serverId?: string) {
    if (sendingRef.current || uploadRef.current || loading) return;
    const botID = defaultBot(bots);
    if (!bots.some(b => b.id === botID && b.enabled)) { setPageError('请先启用主控 Bot'); return; }
    if (serverId) setSelected(serverId);
    // The dashboard runs the same durable task as the chat: this request only
    // creates it, and the panel follows the task and its transcript.
    sendingRef.current = true; setSending(true);
    const requestID = clientRequestID();
    const body:ChatRequest = { content: prompt, server_id: serverId, session_id: panelSession.current, mode: 'direct', bot_id: botID, approval_mode: 'default', client_request_id: requestID };
    setPanelJob({ pending: true, prompt, text: '', error: '', activeStatus: '已提交持久任务，排队执行…', steps: [], timeline: [], sessionId: panelSession.current, botId: botID, serverId, taskID: '' });
    // One submission call: a lost response is retried once with the same id.
    const result = await submitDurableTask(body);
    if (result.ok) {
      const task = result.task;
      panelSession.current = task.session_id || panelSession.current;
      panelTask.current = task.id;
      setPanelTasks([task]);
      setPanelJob(cur => cur ? { ...cur, pending: taskIsActive(task), error: task.status === 'failed' ? task.last_error || '任务失败' : '', text: task.result || '', sessionId: panelSession.current, taskID: task.id, activeStatus: panelStatusText(task) } : cur);
      if (result.attempts > 1) setNotice('网络中断，但任务已创建，正在后台执行');
      await load();
    } else {
      setPanelJob(cur => cur ? { ...cur, pending: false, error: message(result.error) } : cur);
    }
    sendingRef.current = false; setSending(false);
  }
  async function answerPanel(values: string[], custom: string) {
    await answerPanelCard({ choice_answer: { message_id: panelJob?.messageId || '', values, custom: custom.trim() || undefined } }, { values, custom: custom.trim() || undefined });
  }
  async function answerPanelSecret(value: string) {
    const original = panelJob?.secret;
    await answerPanelCard({ secret_answer: { message_id: panelJob?.messageId || '', value } }, undefined, original);
  }
  /** Answers a dashboard card by resuming its durable task, not a websocket turn. */
  async function answerPanelCard(answer: Pick<ChatRequest,'choice_answer'|'secret_answer'>, choice?:Choice['answer'], secret?:SecretRequest) {
    const job = panelJob;
    if (sendingRef.current || uploadRef.current || loading || !job?.messageId || !panelSession.current) return;
    const isChoice = Boolean(answer.choice_answer);
    const isSecret = Boolean(answer.secret_answer);
    if (isChoice && (!job.choice || job.choice.answer)) return;
    if (isSecret && (!job.secret || job.secret.provided_at)) return;
    sendingRef.current = true; setSending(true);
    const requestID = clientRequestID();
    const body:ChatRequest = { content: '', server_id: job.serverId, session_id: panelSession.current, mode: 'direct', bot_id: job.botId || defaultBot(bots), approval_mode: 'default', ...answer, client_request_id: requestID };
    setPanelJob(cur => cur ? {...cur, pending: true, error: '', activeStatus: '已提交回答，任务继续执行…',
      choice: choice&&cur.choice ? {...cur.choice, answer:choice} : cur.choice,
      secret: secret ? {...secret, provided_at: new Date().toISOString()} : cur.secret} : cur);
    // Same single path: the answer is resent once only if its receipt is unknown.
    const result = await submitDurableTask(body);
    if (result.ok) {
      const task = result.task;
      panelTask.current = task.id;
      setPanelTasks([task]);
      panelSession.current = task.session_id || panelSession.current;
      setPanelJob(cur => cur ? {...cur, taskID: task.id, sessionId: panelSession.current, activeStatus: panelStatusText(task)} : cur);
    } else {
      setPanelJob(cur => cur ? {...cur, pending: false, error: message(result.error), choice: job.choice, secret: job.secret} : cur);
    }
    sendingRef.current = false; setSending(false);
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
  async function logout() {try {await api.logout();chatLoad.current++;localCardAnswers.current.clear();taskKey.current='';linesKey.current='';sessionRef.current='';sessionTasksRef.current=[];setPanelTasks([]);setAllTasks([]);setAuthed(false);setLines([]);setSession('');setSessionTasks([]);setWorkspaceTarget(undefined);setChatWorkspace(undefined);setProjectTitle('');setPassword('');setNotice('');setDialog(null);setPanelJob(null);panelSession.current='';panelTask.current='';} catch(err) {setPageError(message(err));}}

  if(authed === null) return <div className="boot-screen"><LoaderCircle className="spin" size={23}/><span>连接工作台…</span></div>;
  if(!authed) return <div className="login-screen"><ThemeToggle className="login-theme"/><div className="login-brand"><span className="brand-icon"><img src="/assets/onefrog-logo.png" alt=""/></span><strong>OneFrog<span>OPERATIONS CONSOLE</span></strong></div><form className="login-card" onSubmit={e => {e.preventDefault();login();}}><h1>进入你的运维工作台</h1><p>服务器、Bot 和密钥，一个地方管理。</p><Field label="账号"><input value={user} autoComplete="username" onChange={e => setUser(e.target.value)} required placeholder="管理员账号"/></Field><Field label="密码"><input type="password" value={password} autoComplete="current-password" onChange={e => setPassword(e.target.value)} required placeholder="输入登录密码"/></Field>{formError && <Notice error>{formError}</Notice>}<Button type="submit" variant="primary" busy={busy}>进入工作台<ArrowRight size={16}/></Button><small><LockKeyhole size={12}/>自托管 · 你的基础设施，由你掌控</small></form><footer>ONEFROG / OPERATIONS, SIMPLIFIED.</footer></div>;
  if(profile.must_change_credentials) return <SetupCredentials profile={profile} initialPassword={password} onDone={async next => {setProfile(next);setUser(next.username);setPassword(''); if(!next.must_change_credentials) await load();}} onLogout={logout}/>;
  if(migration?.paused&&dialog!=='settings') return <div className="migration-landing"><header><h1>备份与迁移</h1><div className="migration-actions"><ThemeToggle/><Button onClick={logout}>退出登录</Button></div></header><BackupMigration/></div>;
  if(profile.approval_choice_required) return <ApprovalChoice onDone={async mode => {setProfile({...profile,approval_choice_required:false});setChatConfig(c=>c?{...c,approval:{...c.approval,mode}}:c);await load();}} onLogout={logout}/>;

  const currentLabel = navigation.find(n => n.id === view)?.label;
  // Include delegated children so approvals in other sessions stay actionable.
  const roots = sessionTasks.filter(t => !t.session_id || t.session_id === session);
  const ownIDs = new Set(roots.map(t => t.id));
  const children = allTasks.filter(t => t.parent_id && ownIDs.has(t.parent_id));
  const chatTasks = [...roots, ...children.filter(t => !ownIDs.has(t.id))];
  return <div className="app-shell">
    {mobileNav && <div className="nav-backdrop" onClick={() => setMobileNav(false)}/>}
    <div className={`navigation-shell ${mobileNav?'open':''}`} inert={Boolean(dialog)||(compact&&!mobileNav)}><aside className="sidebar"><a className="brand" href="#panel" onClick={e=>{e.preventDefault();go('panel');}}><span className="brand-icon"><img src="/assets/onefrog-logo.png" alt=""/></span><strong>OneFrog<span>YOUR CONTROL SPACE</span>{updateStatus?.current && <em className="brand-version">{updateStatus.current}</em>}</strong></a><nav aria-label="主导航">{navigation.map(n=><button key={n.id} title={n.label} className={`nav-item ${view===n.id?'active':''} ${n.id==='modules'||n.id==='servers'?'nav-separated':''}`} onClick={()=>go(n.id)} aria-current={view===n.id?'page':undefined}><n.icon size={18} strokeWidth={1.6}/><span>{n.label}</span>{(n.id==='chat'||n.id==='modules')&&<ChevronRight size={13} className="nav-chevron"/>}</button>)}</nav><div className="sidebar-bottom"><button className="nav-item" onClick={openSettings} title="设置"><Settings size={18} strokeWidth={1.6}/><span>设置</span></button><div className="account"><button className="account-edit" title="个人账号" onClick={()=>open('account')}><Avatar profile={profile}/><span><strong>{profile.display_name||profile.username}</strong><small>个人账号</small></span></button><IconButton label="退出登录" onClick={logout} disabled={sending}><LogOut size={16}/></IconButton></div></div></aside>
    {(view==='chat'||view==='modules')&&secondaryOpen&&<SecondaryNav key={view} view={view} bots={bots} sessions={sessions} mods={mods} activeBot={activeBot} session={session} module={activeModule} disabled={sending||loading||uploading} mode={chatMode} onBot={openBot} onSession={restoreChat} onNew={openGroupPicker} onAddBot={()=>open('agent')} onDelete={removeChat} onModule={id=>{setActiveModule(id);if(id==='services')setServiceFocus('');setMobileNav(false);}} onClose={()=>setSecondaryOpen(false)}/>}</div>
    <main className="app-main" inert={Boolean(dialog) || mobileNav}><UpdateBanner status={updateStatus}/><div className="topbar"><IconButton label="展开导航" className="mobile-menu" onClick={() => setMobileNav(true)}><Menu size={20}/></IconButton><span className="breadcrumb">OneFrog<ChevronRight size={13}/><strong>{currentLabel}</strong></span><div className="topbar-right"><span className="sync-time">{updated ? `更新于 ${updated}` : '尚未同步'}</span><ThemeToggle/><IconButton label="刷新数据" onClick={load} disabled={loading}><RefreshCw size={15} className={loading ? 'spin' : ''}/></IconButton><span className="topbar-divider"/><span className="connection"><span className={`live-dot ${pageError ? 'warn' : ''}`}/>{pageError ? '同步异常' : 'API 已连接'}</span></div></div>
    {pageError && <div className="global-error"><Notice error>{pageError}</Notice><IconButton label="关闭错误提示" onClick={() => setPageError('')}><X size={15}/></IconButton></div>}
    <div className={`view-container ${view === 'chat' ? 'chat-view' : ''}`}>
    {view === 'panel' && <Dashboard hosts={hosts} mods={mods} history={history} bots={bots} job={panelJob} tasks={panelTasks} taskBusy={taskBusy} sending={sending} onSelect={setSelected} onRun={runPanel} onAnswer={answerPanel} onProvide={answerPanelSecret} onModules={() => open('modules')} onSettings={openSettings} onServers={() => go('servers')} onBots={() => go('agents')} onHistory={openHistory} onAssets={() => {setActiveModule('assets'); go('modules');}} onOpenModule={(id,serverId) => {if(id==='services')openServices(serverId);else {setActiveModule(id); go('modules');}}} onTaskAction={taskAction} onTaskApproval={taskApproval}/>}
    {view === 'frog-bot' && <iframe className="frog-bot-frame" title="Frog Bot AI 助理" src="/frog-bot.html"/>}
    {view === 'chat' && <Chat onAddServer={()=>open('server')} controls={controls} onControls={setControls} settings={chatConfig} mode={chatMode} workspace={chatWorkspace} onOpenWorkspace={viewCode} projectTitle={projectTitle} attachments={attachments} onFiles={uploadFiles} onRemoveFile={id=>setAttachments(cur=>cur.filter(f=>f.id!==id))} uploading={uploading} activeBot={bots.find(b=>b.id===activeBot)} hosts={hosts} bots={bots} secrets={secrets} selected={selected} onSelect={setSelected} draft={draft} onDraft={setDraft} lines={lines} onSend={send} onCorrect={correctTurn} sending={sending||loading} submitting={sending} onNew={openGroupPicker} sessions={sessions} session={session} onSession={restoreChat} onDeleteSession={removeChat} onNewTopic={()=>newChat(activeBot)} onNotice={setNotice} onAnswer={answerChoice} onProvide={answerSecret} onGoServer={() => go('servers')} onGoAsset={openAssetForServer} onGoHistory={name => openHistory('all', name)} tasks={chatTasks} taskBusy={taskBusy} onTaskAction={taskAction} onTaskApproval={taskApproval}/>}
    {view === 'workspace' && <Workspace hosts={hosts} target={workspaceTarget} onTarget={setWorkspaceTarget} sessions={sessions} activeTask={chatWorkspace} onReturnToTask={()=>go('chat')} onNewTask={startCodeTask} onResume={openCodeTask} sending={sending||uploading} onAddServer={()=>open('server')}/>}
    {view === 'servers' && <Servers addedServer={addedServer} hosts={hosts} selected={selected} onSelect={setSelected} onAdd={() => open('server')} onTest={testServer} testing={testing} onChat={() => openChat()} onOpenServices={openServices} onOpenAsset={openAssetForServer} onOpenHistory={name => openHistory('all', name)} onChanged={load}/>}
    {view === 'agents' && <Bots bots={bots} onAdd={()=>open('agent')} onChanged={load} onChat={id=>{openBot(id);openChat();}}/>}
    {view === 'secrets' && <Secrets secrets={secrets} onAdd={() => open('secret')} onRotate={openRotate} onDelete={removeSecret} onCopyRef={async ref => { try { await navigator.clipboard.writeText(ref); setNotice('已复制密钥引用 ' + ref); } catch { setPageError('复制失败'); } }} onUseInChat={ref => { openChat((draft ? draft + ' ' : '') + ref); }}/>}
    {view === 'modules' && activeModule === 'services' && <Services hosts={hosts} focusServer={serviceFocus} onServer={openServerById} onAsk={(id,text)=>{newChat();setSelected(id);openChat(text);}}/>}
    {view === 'modules' && activeModule === 'assets' && <Assets hosts={hosts} focusServer={assetFocusServer} onConsumedFocus={() => setAssetFocusServer(null)} onOpenServer={openServerById} onChatServer={(id, name) => { newChat(); setSelected(id); openChat('检查服务器 ' + name + ' 的运行状态和磁盘情况。'); }} onAsk={text => {const bot = bots.find(b => (b.id === 'module-builder' || b.name === '模块管理' || b.name === '模块构建') && b.enabled); if (bot) newChat(bot.id); go('chat'); setDraft(text);}}/>}
    {view === 'modules' && activeModule === 'develop' && <Develop bots={bots} settings={chatConfig} onOpenChat={text => {const bot = bots.find(b => (b.id === 'develop' || b.name === '自定义模块' || b.name === '二次开发') && b.enabled); if (bot) newChat(bot.id); go('chat'); setDraft(text);}} onOpenSettings={openSettings} onOpenModule={id => setActiveModule(id)} onChanged={load}/>}
    {view === 'modules' && activeModule === 'automations' && <Automations bots={bots} hosts={hosts} onOpenSession={async (id, botId) => {if (botId === 'frog-bot') {go('frog-bot'); return;} let item = sessions.find(s => s.id === id); if (!item) {try {const list = await api.sessions(); setSessions(list); item = list.find(s => s.id === id);} catch (e) {setPageError(message(e)); return;}} if (item) {await restoreChat(item); go('chat');} else setPageError('这段对话已被删除');}}/>}
    {view === 'modules' && activeModule !== '' && activeModule !== 'assets' && activeModule !== 'services' && activeModule !== 'develop' && activeModule !== 'automations' && mods.find(m => m.id === activeModule)?.type === 'custom' && <CustomModulePage mod={mods.find(m => m.id === activeModule)!} onBack={() => setActiveModule('develop')} onAskDevelop={text => {const bot = bots.find(b => (b.id === 'develop' || b.name === '自定义模块' || b.name === '二次开发') && b.enabled); if (bot) newChat(bot.id); go('chat'); setDraft(text);}} onChanged={load}/>}
    {view === 'modules' && activeModule !== 'assets' && activeModule !== 'services' && activeModule !== 'develop' && activeModule !== 'automations' && mods.find(m => m.id === activeModule)?.type !== 'custom' && <Modules mods={activeModule?mods.filter(m=>m.id===activeModule):mods} onToggle={toggle} pending={switching} onOpen={id => {if(id==='services')openServices();else setActiveModule(id);}}/>}
    {view === 'history' && <HistoryPage history={history} initialFilter={historyFilter.filter} initialQuery={historyFilter.query} onOpenServer={openServerByName} onOpenBot={openBotByName} onUndo={id => {setRollback(id);open('rollback');}}/>}
    </div></main>
    {notice && <div className="toast" role="status"><Check size={16}/>{notice}<IconButton label="关闭通知" onClick={() => setNotice('')}><X size={14}/></IconButton></div>}
    {hostKey && <HostKeyDialog serverID={hostKey.id} result={hostKey.result} onClose={() => {setHostKey(null); load();}} onDone={msg => {setHostKey(null); setNotice(msg); load();}}/>}
    {dialog === 'account' && <AccountDialog profile={profile} onClose={close} onSaved={p=>{setProfile(p);setUser(p.username);setNotice('账号资料已保存');setDialog(null);}}/>}
    {dialog === 'settings' && <SettingsPanel bots={bots} onUpdateStatus={setUpdateStatus} onClose={close} onSaved={() => {setNotice('设置已保存，下一轮任务生效');load();}}/>}
    {apiPrompt && dialog !== 'settings' && <Modal title="要先配置模型 API 吗？" note="还没有可用的 API 渠道。配好之后，Bot 才能对话和执行任务。之后也可以从左侧「设置」进入。" cancel="稍后再说" confirm="去配置" onClose={()=>{localStorage.setItem('onefrog-api-prompt','dismissed');setApiPrompt(false);}} onSubmit={()=>{localStorage.setItem('onefrog-api-prompt','dismissed');setApiPrompt(false);openSettings();}}/>}
    {dialog === 'modules' && <Modal title="定制面板模块" note="选择你想持续关注的内容，开关即时保存。" onClose={close} confirm="完成"><div className="module-picker">{mods.map(m => <div key={m.id}><span className="host-icon"><Blocks size={16}/></span><div><strong>{m.name}</strong><small>{m.desc}</small></div><button className={`switch ${m.enabled ? 'on' : ''}`} role="switch" aria-checked={m.enabled} aria-label={`显示${m.name}`} disabled={!!switching} onClick={() => toggle(m.id)}><i/></button></div>)}</div></Modal>}
    {dialog === 'server' && <AddServer onClose={close} onChanged={load} onDone={id=>{setSelected(id);setAddedServer(id);setNotice('服务器接入状态已更新');load();}}/>}
    {dialog === 'secret' && <Modal title="添加密钥" note="在这里录入敏感内容，Bot 只能按名称引用。" onClose={close} onSubmit={submit} busy={busy} error={formError}><Field label="密钥名称" hint="命令引用格式：{{secret:名称}}"><input required value={form.name} placeholder="例如 production-db-password" onChange={e => setForm({...form,name:e.target.value})}/></Field><SecretSource form={form} onForm={setForm}/><Field label="用途说明"><input value={form.description} placeholder="例如：生产数据库连接凭据" onChange={e => setForm({...form,description:e.target.value})}/></Field><div className="inline-help"><LockKeyhole size={15}/>关闭窗口会清空未保存的敏感内容。</div></Modal>}{dialog === 'rotate' && <Modal title="轮换密钥" note={'替换「' + form.name + '」的密文。名称不变，明文不会显示。'} onClose={close} onSubmit={submit} busy={busy} error={formError} confirm="保存新内容"><Field label="密钥名称"><input value={form.name} readOnly/></Field><SecretSource form={form} onForm={setForm} rotate/><Field label="用途说明"><input value={form.description} placeholder="可选，留空则清空说明" onChange={e => setForm({...form,description:e.target.value})}/></Field><div className="inline-help"><LockKeyhole size={15}/>提交后输入框会清空。模型仍然只能看到名称。</div></Modal>}
    {dialog === 'agent' && <Modal title="创建 Bot" note="描述分工，让主控知道何时把任务交给它。" onClose={close} onSubmit={submit} busy={busy} submitDisabled={botModelsLoading||!botModels} error={formError} confirm="创建 Bot"><Field label="Bot 名称"><input required placeholder="例如：数据库专家" value={form.name} onChange={e => setForm({...form,name:e.target.value})}/></Field><Field label="职责描述"><input required placeholder="例如：分析数据库性能与慢查询" value={form.description} onChange={e => setForm({...form,description:e.target.value})}/></Field>{botModelsLoading?<div className="loading-inline"><LoaderCircle size={16} className="spin"/>加载可选模型…</div>:botModels&&<><RoutePicker label="Bot 模型" note="跟随协作默认模型，或为此 Bot 单独选择渠道和模型；创建后可在设置中修改。" providers={botModels.providers} value={botRoute} onChange={setBotRoute} inherit/>{!botModels.providers.some(p=>p.enabled)&&<p className="model-hidden-note">尚无可用渠道，可先跟随默认配置，稍后在设置中配置。</p>}</>}<Field label="工作指令" hint="可选，用于约束这个 Bot 的工作方式。"><textarea placeholder="例如：先收集证据，只执行只读查询。" rows={4} value={form.system_prompt} onChange={e => setForm({...form,system_prompt:e.target.value})}/></Field></Modal>}
    {dialog === 'rollback' && <Modal title="撤回详情" note="当前版本仅支持记录撤回标记，暂不恢复服务器上的文件或服务。" onClose={close} confirm="知道了"><div className="rollback-detail"><Terminal size={18}/><code>{history.find(h => h.id === rollback)?.action}</code></div><p className="muted">实际恢复需要有效快照与恢复执行器。这里暂不提供恢复操作，避免将记录更新误认为服务器已恢复。</p></Modal>}
    {dialog === 'group' && <Modal title="新建隔离项目" note="每个隔离项目有独立的会话和工作记忆，只在本项目内保留；不读取任何 Bot 私有记忆，项目内容也不会写入任何 Bot 的长期记忆。主控负责派工。" onClose={close} onSubmit={startGroup} confirm="开始"><Field label="项目名称（可选）"><input maxLength={60} value={groupName} onChange={e=>setGroupName(e.target.value)} placeholder="例如：生产环境迁移"/></Field><div className="group-picker">{bots.filter(b=>b.enabled).map(b=>{const lead=b.id===defaultBot(bots);const on=lead||groupPick.includes(b.id);return <label key={b.id} className={on?'on':''}><BotAvatar bots={bots} id={b.id} className="small-avatar"/><div><strong>{b.name}{lead&&<span className="tag">主控</span>}</strong><small>{b.description||'按主控分派完成任务'}</small></div><input type="checkbox" checked={on} disabled={lead} aria-label={(on?'移出 ':'加入 ')+b.name} onChange={e=>setGroupPick(cur=>e.target.checked?[...cur,b.id]:cur.filter(id=>id!==b.id))}/></label>;})}</div><p className="group-picker-count">已选 {bots.filter(b=>b.enabled&&(b.id===defaultBot(bots)||groupPick.includes(b.id))).length} / {bots.filter(b=>b.enabled).length} 个 Bot</p></Modal>}
  </div>;
}
