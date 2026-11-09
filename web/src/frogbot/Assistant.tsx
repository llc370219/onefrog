import { Bell, BellOff, Check, ChevronDown, ShieldAlert, Eye, FolderSearch, ListTodo, LoaderCircle, Server, ShieldCheck, ShieldQuestion, Users, WifiOff, X } from 'lucide-react';
import { useEffect, useLayoutEffect, useMemo, useRef, useState, type ReactNode } from 'react';
import { api, BotContext, createBotAPI, isImage, isOpen, useActiveBot, useBot, type Attachment, type BotProfile, type Info, type Msg, type ScopeServer, type State } from './api';
import ApprovalCard from './components/ApprovalCard';
import Artifact from './components/Artifact';
import Composer from './components/Composer';
import { avatarOf, BotAvatar } from './components/BotAvatar';
import { AssistantBubbles } from './components/Markdown';
import MessageActions, { Reactions } from './components/MessageActions';
import OptionCard from './components/OptionCard';
import SecretCard from './components/SecretCard';
import TaskDock from './components/TaskDock';
import ThinkingBlock from './components/ThinkingBlock';
import WorkingCard from './components/WorkingCard';

const NOTIFY_KEY = 'frogbot-notify';
const LAST_BOT_KEY = 'onefrog-assistant-bot';
export const draftKey = (frogID: string) => `frogbot-draft:bot:${frogID}`;

type HostBot = { id: string; name: string; description?: string; enabled?: boolean };

/**
 * AI 助理页：每个启用的 OneFrog Bot（自定义模块 Bot 除外）都是一个独立的助理，
 * 有自己的对话、后台任务、记忆和工作区。主控排在第一位。
 * Bot 的新建、改名、模型和停用都在「Bot」页管理，这里只负责对话。
 */
export default function Assistant({ hostBots, avatar, openBot, draft, onManageBots }: {
  hostBots: HostBot[];
  avatar: (hostID: string) => string;
  /** OneFrog bot id to open; empty opens the last used bot or the leader. */
  openBot?: string;
  /** Text to put in the composer of the opened bot (n changes each time). */
  draft?: { text: string; n: number };
  onManageBots: () => void;
}) {
  const [profiles, setProfiles] = useState<BotProfile[]>([]);
  const [loadError, setLoadError] = useState('');
  const [attempt, setAttempt] = useState(0);
  const [active, setActive] = useState(() => localStorage.getItem(LAST_BOT_KEY) || 'main');
  const [picker, setPicker] = useState(false);
  const hostKey = hostBots.map((b) => `${b.id}:${b.name}:${b.enabled}`).join('|');
  useEffect(() => {
    const request = new AbortController();
    api.bots(request.signal).then((list) => {
      if (!Array.isArray(list) || !list.length) throw new Error('AI 助理没有启动，请刷新重试');
      setProfiles(list); setLoadError('');
    }).catch((error: Error) => { if (!request.signal.aborted) setLoadError(error.message); });
    return () => request.abort();
  }, [attempt, hostKey]);

  // 从其他页面带着 Bot 或草稿打开
  useEffect(() => {
    if (!profiles.length) return;
    const target = openBot ? profiles.find((p) => p.hostId === openBot) : undefined;
    const id = target?.id || (profiles.some((p) => p.id === active) ? active : profiles[0].id);
    if (draft?.text) { try { localStorage.setItem(draftKey(id), draft.text); } catch { /* 存储已满或被禁用 */ } }
    setActive(id);
  }, [profiles, openBot, draft?.n]); // eslint-disable-line react-hooks/exhaustive-deps
  useEffect(() => { try { localStorage.setItem(LAST_BOT_KEY, active); } catch { /* ignore */ } }, [active]);

  const bots = useMemo(() => profiles.map((p) => ({ ...p, avatar: avatar(p.hostId || p.id) })), [profiles, avatar]);
  const bot = bots.find((b) => b.id === active) || bots[0];
  const pick = (id: string) => { setActive(id); setPicker(false); };

  if (!bot) {
    return (
      <div className="assistant">
        <main className="grid flex-1 place-content-center gap-3 px-4 text-center text-sm text-fg3" role="status">
          {loadError || '正在加载 AI 助理…'}
          {loadError && <button className="underline" onClick={() => { setLoadError(''); setAttempt((n) => n + 1); }}>重试</button>}
        </main>
      </div>
    );
  }
  const list = (
    <ul className="as-bot-list" aria-label="助理">
      {bots.map((b) => (
        <li key={b.id}>
          <button onClick={() => pick(b.id)} aria-current={b.id === bot.id ? 'true' : undefined} className="as-bot-item">
            <BotAvatar bot={b} size={32} />
            <span className="min-w-0 flex-1 text-left">
              <span className="flex items-center gap-1.5"><strong className="truncate">{b.name}</strong>{b.leader && <em className="as-tag">主控</em>}</span>
              {b.role && <small className="block truncate">{b.role}</small>}
            </span>
          </button>
        </li>
      ))}
    </ul>
  );
  return (
    <div className="assistant">
      {bots.length > 1 && (
        <aside className="as-bots">
          <header><Users size={14} />助理<button onClick={onManageBots} className="ml-auto text-[12px] text-fg4 hover:text-fg">管理</button></header>
          {list}
        </aside>
      )}
      {picker && bots.length > 1 && (
        <div className="as-picker-layer" onClick={() => setPicker(false)}>
          <div className="as-picker" onClick={(e) => e.stopPropagation()} role="dialog" aria-label="选择助理">
            <header><strong>选择助理</strong><button onClick={() => setPicker(false)} aria-label="关闭" className="as-icon-btn"><X size={15} /></button></header>
            {list}
            <button onClick={() => { setPicker(false); onManageBots(); }} className="mt-2 w-full rounded-xl px-3 py-2 text-left text-[13px] text-fg3 hover:bg-s2">在「Bot」页新建或调整助理</button>
          </div>
        </div>
      )}
      <BotView key={bot.id + ':' + (draft?.n || 0)} bot={bot} many={bots.length > 1} onPick={() => setPicker(true)} />
    </div>
  );
}

function BotView({ bot, many, onPick }: { bot: BotProfile; many: boolean; onPick: () => void }) {
  const botAPI = useMemo(() => createBotAPI(bot.id), [bot.id]);
  return <BotContext.Provider value={{ bot, api: botAPI }}><ChatView many={many} onPick={onPick} /></BotContext.Provider>;
}

function ChatView({ many, onPick }: { many: boolean; onPick: () => void }) {
  const { bot, api } = useActiveBot();
  const activeBotName = bot.name;
  const s = useBot(bot.id);
  const [info, setInfo] = useState<Info | null>(null);
  const [error, setError] = useState('');
  const [notify, setNotify] = useState(() => localStorage.getItem(NOTIFY_KEY) === '1' && typeof Notification !== 'undefined' && Notification.permission === 'granted');
  const [unread, setUnread] = useState(0);
  useEffect(() => {
    const request = new AbortController();
    api.info(request.signal).then(setInfo).catch(() => {});
    return () => request.abort();
  }, [api]);

  const tasks = Object.values(s.tasks);
  const running = tasks.filter((t) => t.status === 'running');
  const queued = tasks.filter((t) => t.status === 'queued').sort((a, b) => a.prio - b.prio);
  const pos = useMemo(() => Object.fromEntries(queued.map((t, i) => [t.id, i + 1])), [queued.map((t) => t.id).join()]); // eslint-disable-line react-hooks/exhaustive-deps
  const waiting = Object.values(s.approvals).filter((a) => a.status === 'pending').length;
  const working = running.length + queued.length > 0;
  const stallMs = Math.min(((info?.stall_sec || 720) * 1000) / 2, 180_000);

  const seen = useRef(0);
  useEffect(() => {
    const delta = s.news - seen.current;
    seen.current = s.news;
    if (delta <= 0 || !document.hidden) return;
    setUnread((n) => n + delta);
    if (notify) try { new Notification(activeBotName, { body: s.lastNews.slice(0, 140), icon: avatarOf(bot), tag: `onefrog-assistant-${bot.id}` }); } catch { /* 浏览器不支持 */ }
  }, [s.news]); // eslint-disable-line react-hooks/exhaustive-deps
  useEffect(() => {
    const v = () => { if (!document.hidden) setUnread(0); };
    document.addEventListener('visibilitychange', v);
    return () => document.removeEventListener('visibilitychange', v);
  }, []);
  const toggleNotify = async () => {
    if (notify) { setNotify(false); localStorage.removeItem(NOTIFY_KEY); return; }
    if (typeof Notification === 'undefined') { setError('这个浏览器不支持系统通知'); return; }
    const p = Notification.permission === 'granted' ? 'granted' : await Notification.requestPermission();
    if (p === 'granted') { setNotify(true); localStorage.setItem(NOTIFY_KEY, '1'); } else setError('浏览器没有允许通知');
  };

  // 快捷键：Esc 停止回复，Ctrl/⌘+K 聚焦输入框
  useEffect(() => {
    const h = (e: KeyboardEvent) => {
      if ((e.metaKey || e.ctrlKey) && e.key.toLowerCase() === 'k') { e.preventDefault(); document.getElementById('composer')?.focus(); }
      else if (e.key === 'Escape' && s.busy) { e.preventDefault(); api.stopReply().catch(() => {}); }
    };
    window.addEventListener('keydown', h);
    return () => window.removeEventListener('keydown', h);
  }, [api, s.busy]);

  // 新内容出现时，如果用户本来就在底部，就跟着滚到底
  const scroller = useRef<HTMLDivElement>(null);
  const stick = useRef(true);
  useLayoutEffect(() => {
    const el = scroller.current;
    if (el && stick.current) el.scrollTop = el.scrollHeight;
  });

  const wrap = async <T,>(p: Promise<T>) => {
    setError('');
    try { await p; stick.current = true; } catch (e) { setError((e as Error).message); throw e; }
  };
  const send = (t: string, files: Attachment[] = []) => wrap(api.send(t, files));
  const edit = (id: string, t: string) => wrap(api.edit(id, t));
  const stopAll = () => {
    const n = tasks.filter(isOpen).length;
    if (n > 1 && !confirm(`停止全部 ${n} 个任务（包括排队和暂停的）？`)) return;
    api.stop().catch((e) => setError((e as Error).message));
  };

  // 只有最后一轮可以重新生成 / 编辑
  const visible = s.items.filter((it) => it.type === 'msg').map((it) => s.msgs[it.id]).filter(Boolean);
  const lastUser = [...visible].reverse().find((m) => m.role === 'user');
  const lastReply = [...visible].reverse().find((m) => m.role === 'assistant');
  const regenId = !s.busy && lastUser && lastReply && lastReply.created_at >= lastUser.created_at ? lastReply.id : '';
  const editId = !s.busy && lastUser ? lastUser.id : '';

  return (
    <>
      <div className="flex flex-1 flex-col h-full min-w-0 overflow-hidden relative">
        <div className="as-head">
          <button onClick={many ? onPick : undefined} disabled={!many} className="as-head-bot" aria-label={many ? '切换助理' : undefined}>
            <BotAvatar bot={bot} size={26} />
            <span className="min-w-0 text-left"><strong className="block truncate">{bot.name}</strong>{bot.role && <small className="block truncate">{bot.role}</small>}</span>
            {many && <ChevronDown size={14} className="as-head-chevron" />}
          </button>
          <div className="ml-auto flex items-center gap-1">
            {waiting > 0 && <span className="as-chip bg-warn-soft text-warn" title={`${waiting} 个操作等你确认`}><ShieldAlert size={12} />{waiting}<span className="as-chip-long"> 个操作等你确认</span></span>}
            {unread > 0 && <span className="as-chip bg-accent-soft text-accent" title={`${unread} 条新消息`}>{unread}<span className="as-chip-long"> 条新消息</span></span>}
            <ScopePicker />
            <button onClick={toggleNotify} className="as-icon-btn" title={notify ? '通知已开启（点击关闭）' : '开启系统通知'} aria-label={notify ? '关闭系统通知' : '开启系统通知'} aria-pressed={notify}>
              {notify ? <Bell size={15} /> : <BellOff size={15} />}
            </button>
          </div>
        </div>

        {!s.connected && s.ready && (
          <div className="flex items-center justify-center gap-2 border-b border-line bg-s2 px-4 py-1.5 text-[12.5px] text-fg3" role="status">
            <WifiOff size={13} />连接中断，正在重新连接。任务在服务器上照常进行，连上后会自动补齐。
          </div>
        )}

        <div ref={scroller} onScroll={(e) => { const el = e.currentTarget; stick.current = el.scrollHeight - el.scrollTop - el.clientHeight < 80; }} className="flex-1 overflow-y-auto">
          <main className="mx-auto flex w-full max-w-3xl flex-col gap-5 px-4 pb-8 pt-8" aria-live="off">
            {s.ready && s.items.length === 0 && <Welcome name={activeBotName} onPick={(t) => send(t).catch(() => {})} />}
            {!s.ready && <div className="flex justify-center pt-[20vh]"><span className="typing"><i /><i /><i /></span></div>}
            <Timeline s={s} pos={pos} stallMs={stallMs} regenId={regenId} editId={editId} onError={setError} />
            {s.busy && (
              <AssistantRow>
                {s.thinking && <ThinkingBlock live text={s.thinking} startedAt={s.thinkStart} endedAt={s.thinkEnd || undefined} />}
                {s.streaming ? <AssistantBubbles text={s.streaming} streaming />
                  : !(s.thinking && !s.thinkEnd) && <div className="flex items-center gap-2 pt-1.5"><span className="typing"><i /><i /><i /></span>{s.toolLabel && <span className="text-[12.5px] text-fg4">{s.toolLabel.replace(/^(查看了|读取了|搜索了|请求|派出了|写入了|停止了|暂停了|恢复了|排上了)/, (m) => '正在' + m.replace('了', ''))}…</span>}</div>}
              </AssistantRow>
            )}
          </main>
        </div>

        <div className="dock relative">
          <TaskDock tasks={tasks} pos={pos} />
          {error && <p className="mx-auto mb-1.5 max-w-3xl px-4 text-center text-xs text-danger" role="alert">{error}<button onClick={() => setError('')} className="ml-2 text-fg4 underline">知道了</button></p>}
          <Composer name={activeBotName} connected={s.connected && s.ready} chatBusy={s.busy} working={working}
            lastUser={editId && lastUser ? { id: lastUser.id, text: lastUser.text } : undefined}
            onSend={send} onEdit={edit} onStopReply={() => api.stopReply().catch(() => {})} onStopAll={stopAll} />
        </div>
      </div>

    </>
  );
}

function Timeline({ s, pos, stallMs, regenId, editId, onError }: { s: State; pos: Record<string, number>; stallMs: number; regenId: string; editId: string; onError: (e: string) => void }) {
  return (
    <>
      {s.items.map((it) => {
        if (it.type === 'task') return s.tasks[it.id] ? <Indented key={it.id}><WorkingCard task={s.tasks[it.id]} pos={pos[it.id]} stallMs={stallMs} /></Indented> : null;
        if (it.type === 'approval') {
          const a = s.approvals[it.id];
          return <Indented key={it.id}><ApprovalCard a={a} taskTitle={s.tasks[a.task_id]?.title} fromChat={a.task_id.startsWith('chat_')} /></Indented>;
        }
        const m = s.msgs[it.id];
        return m ? <Message key={it.id} m={m} canRegenerate={m.id === regenId} canEdit={m.id === editId} onError={onError} /> : null;
      })}
    </>
  );
}

function Files({ files, right }: { files: Attachment[]; right?: boolean }) {
  const imgs = files.filter((f) => isImage(f.name, f.mime)), others = files.filter((f) => !isImage(f.name, f.mime));
  return (
    <div className={`mb-1.5 flex flex-col gap-1.5 ${right ? 'items-end' : ''}`}>
      {imgs.length > 0 && <div className={`flex flex-wrap gap-1.5 ${right ? 'justify-end' : ''}`}>{imgs.map((f) => <Artifact key={f.path} path={f.path} mime={f.mime} />)}</div>}
      {others.map((f) => <Artifact key={f.path} path={f.path} size={f.size} mime={f.mime} compact />)}
    </div>
  );
}

function Message({ m, canRegenerate, canEdit, onError }: { m: Msg; canRegenerate: boolean; canEdit: boolean; onError: (e: string) => void }) {
  if (m.role === 'user') {
    return (
      <div className="rise group flex flex-col items-end">
        {m.files && m.files.length > 0 && <Files files={m.files} right />}
        {m.text && <div className="max-w-[85%] whitespace-pre-wrap break-words rounded-[20px] rounded-br-md bg-bubble px-4 py-2.5 text-[15px] leading-relaxed text-fg">{m.text}</div>}
        <Reactions m={m} />
        <MessageActions m={m} canEdit={canEdit} onError={onError} />
      </div>
    );
  }
  const details = (
    <>
      {m.files && m.files.length > 0 && <div className="mt-2"><Files files={m.files} /></div>}
      {m.tools && m.tools.length > 0 && (
        <div className="mt-1.5 flex flex-wrap gap-1.5">
          {m.tools.map((t) => <span key={t} className="inline-flex items-center gap-1 rounded-full bg-s2 px-2 py-0.5 text-[11.5px] text-fg3"><Eye size={11} className="text-fg4" />{t}</span>)}
        </div>
      )}
      {m.card && <div className={m.text ? 'mt-3' : ''}><OptionCard card={m.card} /></div>}
      {m.secret && <div className={m.text ? 'mt-3' : ''}><SecretCard req={m.secret} /></div>}
    </>
  );
  return (
    <AssistantRow footer={
      <>
        {m.text && details}
        <Reactions m={m} />
        {(m.text || m.card) && <MessageActions m={m} canRegenerate={canRegenerate} onError={onError} />}
      </>
    }>
      {m.thinking && <ThinkingBlock text={m.thinking} ms={m.thinking_ms} />}
      {m.text ? <AssistantBubbles text={m.text} /> : details}
    </AssistantRow>
  );
}

function AssistantRow({ children, footer }: { children: ReactNode; footer?: ReactNode }) {
  const { bot } = useActiveBot();
  return (
    <div className="rise group grid grid-cols-[auto_minmax(0,1fr)] gap-x-3">
      <div className="self-end pt-0.5"><BotAvatar bot={bot} /></div>
      <div className="pt-0.5">{children}</div>
      {footer && <div className="col-start-2">{footer}</div>}
    </div>
  );
}

const Indented = ({ children }: { children: ReactNode }) => <div className="sm:pl-10">{children}</div>;

function Welcome({ name, onPick }: { name: string; onPick: (t: string) => void }) {
  const { bot } = useActiveBot();
  const ideas = [
    { icon: Server, title: '看看服务器', text: '看看 OneFrog 里有哪些服务器，检查一下磁盘和内存' },
    { icon: FolderSearch, title: '巡检服务', text: '检查服务器上正在运行的服务和端口，有异常告诉我' },
    { icon: ListTodo, title: '先记个待办', text: '帮我记个待办：明天整理备份策略，先别开始' },
    { icon: ListTodo, title: '定个巡检', text: '每天上午 9 点检查一次所有服务器的磁盘占用' },
  ];
  return (
    <div className="rise flex flex-col items-center px-2 pt-[9vh] text-center">
      <BotAvatar bot={bot} size={72} />
      <h1 className="mt-5 font-serif text-[30px] font-semibold tracking-tight">你好，我是 {name}</h1>
      <p className="mt-2 max-w-md text-[14.5px] leading-relaxed text-fg3">
        {bot.role ? `${bot.role}。` : 'OneFrog 的 AI 助理。'}我会把要动手的事交给后台任务去做，刷新页面也不会中断。你在右上角选中的服务器上，普通命令直接执行；有风险的操作和其他服务器，会先请你确认。
      </p>
      <div className="mt-8 grid w-full max-w-xl grid-cols-1 gap-2.5 sm:grid-cols-2">
        {ideas.map((id) => (
          <button key={id.title} onClick={() => onPick(id.text)}
            className="group flex flex-col items-start rounded-2xl border border-line bg-s1 p-4 text-left transition hover:border-line2 hover:bg-s2 hover:shadow-xs">
            <span className="grid size-8 place-items-center rounded-xl bg-s2 text-fg2 transition group-hover:bg-s1"><id.icon size={16} /></span>
            <span className="mt-3 text-[14.5px] font-medium text-fg">{id.title}</span>
            <span className="mt-1 text-xs text-fg3">{id.text}</span>
          </button>
        ))}
      </div>
    </div>
  );
}

// ScopePicker 选择这个助理可以直接操作的服务器。
function ScopePicker() {
  const { api } = useActiveBot();
  const [open, setOpen] = useState(false);
  const [servers, setServers] = useState<ScopeServer[] | null>(null);
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState('');
  const box = useRef<HTMLDivElement>(null);
  useEffect(() => {
    const request = new AbortController();
    api.scope(request.signal).then((r) => setServers(r.servers)).catch(() => {});
    return () => request.abort();
  }, [api, open]);
  useEffect(() => {
    if (!open) return;
    const close = (e: MouseEvent) => { if (box.current && !box.current.contains(e.target as Node)) setOpen(false); };
    const esc = (e: KeyboardEvent) => { if (e.key === 'Escape') { e.stopPropagation(); setOpen(false); } };
    document.addEventListener('mousedown', close); document.addEventListener('keydown', esc, true);
    return () => { document.removeEventListener('mousedown', close); document.removeEventListener('keydown', esc, true); };
  }, [open]);
  const picked = (servers || []).filter((s) => s.selected);
  const toggle = async (id: string) => {
    if (!servers || busy) return;
    const next = servers.filter((s) => (s.id === id ? !s.selected : s.selected)).map((s) => s.id);
    setBusy(true); setErr('');
    try { setServers((await api.setScope(next)).servers); } catch (e) { setErr((e as Error).message); } finally { setBusy(false); }
  };
  const label = picked.length === 0 ? '未选服务器' : picked.length === 1 ? picked[0].name : `${picked.length} 台服务器`;
  return (
    <div className="relative" ref={box}>
      <button onClick={() => setOpen(!open)} aria-expanded={open} className="as-scope-btn" title="选择这个助理可以直接操作的服务器">
        <Server size={14} /><span className="max-w-[120px] truncate">{label}</span><ChevronDown size={12} />
      </button>
      {open && (
        <div className="as-scope-pop" role="dialog" aria-label="可直接操作的服务器">
          <p className="px-1 pb-2 text-[12px] leading-relaxed text-fg3">选中的服务器上，普通命令按「设置 → 审批」直接执行；没选中的服务器，每条命令都会先请你确认。</p>
          {servers === null ? <div className="flex items-center gap-2 px-1 py-2 text-[13px] text-fg3"><LoaderCircle size={14} className="animate-spin" />加载中…</div>
            : servers.length === 0 ? <p className="px-1 py-2 text-[13px] text-fg3">还没有服务器，可以在「服务器」页接入。</p>
              : (
                <ul className="max-h-72 overflow-y-auto">
                  {servers.map((s) => (
                    <li key={s.id}>
                      <button disabled={busy} onClick={() => void toggle(s.id)} className="as-scope-row" aria-pressed={s.selected}>
                        <span className={`as-check ${s.selected ? 'is-on' : ''}`}>{s.selected && <Check size={12} />}</span>
                        <span className="min-w-0 flex-1 text-left"><span className="block truncate text-[13px] text-fg">{s.name}</span><span className="block truncate text-[11.5px] text-fg4">{s.role || s.id}</span></span>
                        {s.fingerprint_confirmed ? <ShieldCheck size={14} className="text-ok" aria-label="SSH 指纹已确认" /> : <ShieldQuestion size={14} className="text-warn" aria-label="SSH 指纹未确认" />}
                      </button>
                    </li>
                  ))}
                </ul>
              )}
          {err && <p className="px-1 pt-2 text-xs text-danger">{err}</p>}
        </div>
      )}
    </div>
  );
}
