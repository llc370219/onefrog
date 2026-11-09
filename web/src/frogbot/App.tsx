import { Eye, FolderSearch, ListTodo, Sparkles, SquareTerminal, WifiOff } from 'lucide-react';
import { useEffect, useLayoutEffect, useMemo, useRef, useState, type ReactNode } from 'react';
import { api, isImage, isOpen, useBot, type Attachment, type Info, type Msg, type State } from './api';
import ApprovalCard from './components/ApprovalCard';
import Artifact from './components/Artifact';
import Composer from './components/Composer';
import Header, { Avatar } from './components/Header';
import { AssistantBubbles } from './components/Markdown';
import MessageActions, { Reactions } from './components/MessageActions';
import OptionCard from './components/OptionCard';
import SecretCard from './components/SecretCard';
import TaskDock from './components/TaskDock';
import ScheduleDock, { useSchedules } from './components/ScheduleDock';
import ThinkingBlock from './components/ThinkingBlock';
import WorkingCard from './components/WorkingCard';

const NOTIFY_KEY = 'frogbot-notify';

export default function App() {
  const s = useBot();
  const [schedules, setSchedules] = useSchedules(s.ready);
  // SSE 里的计划变更合并进本地列表
  useEffect(() => {
    const list = Object.values(s.schedules);
    if (list.length) setSchedules(list);
  }, [s.schedules, setSchedules]);
  const [info, setInfo] = useState<Info>({ name: 'Frog', chat_model: '', agent_model: '', policy: '', demo: false });
  const [error, setError] = useState('');
  const [notify, setNotify] = useState(() => localStorage.getItem(NOTIFY_KEY) === '1' && typeof Notification !== 'undefined' && Notification.permission === 'granted');
  const [unread, setUnread] = useState(0);
  useEffect(() => { api.info().then(setInfo).catch(() => {}); }, []);

  const tasks = Object.values(s.tasks);
  const running = tasks.filter((t) => t.status === 'running');
  const queued = tasks.filter((t) => t.status === 'queued').sort((a, b) => a.prio - b.prio);
  const pos = useMemo(() => Object.fromEntries(queued.map((t, i) => [t.id, i + 1])), [queued.map((t) => t.id).join()]); // eslint-disable-line react-hooks/exhaustive-deps
  const waiting = Object.values(s.approvals).filter((a) => a.status === 'pending').length;
  const secretWaiting = Object.values(s.msgs).some((m) => m.secret?.status === 'pending');
  const working = running.length + queued.length > 0;
  const status = waiting ? `等你确认 ${waiting} 个操作` : secretWaiting ? '等你填写密钥'
    : running.length ? `正在处理 ${running.length} 个任务${queued.length ? `，${queued.length} 个排队` : ''}`
    : queued.length ? `${queued.length} 个任务排队中`
    : s.busy ? (s.thinking && !s.thinkEnd ? '正在思考…' : '正在回复…') : '在线';
  const stallMs = Math.min(((info.stall_sec || 720) * 1000) / 2, 180_000);

  // 标签页标题：在工作时带一个标记；页面在后台时累计未读
  useEffect(() => {
    document.title = `${unread ? `(${unread}) ` : ''}${waiting ? '⚠ ' : working || s.busy ? '● ' : ''}${info.name}`;
  }, [unread, working, s.busy, waiting, info.name]);
  const seen = useRef(0);
  useEffect(() => {
    const delta = s.news - seen.current;
    seen.current = s.news;
    if (delta <= 0 || !document.hidden) return;
    setUnread((n) => n + delta);
    if (notify) try { new Notification(info.name, { body: s.lastNews.slice(0, 140), icon: '/frog.png', tag: 'frogbot' }); } catch { /* 浏览器不支持 */ }
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
  }, [s.busy]);

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
    <div className="flex h-full flex-col">
      <Header name={info.name} status={status} working={working || s.busy} demo={info.demo} connected={s.connected || !s.ready} notify={notify} onNotify={toggleNotify} />
      {!s.connected && s.ready && (
        <div className="flex items-center justify-center gap-2 border-b border-line bg-s2 px-4 py-1.5 text-[12.5px] text-fg3" role="status">
          <WifiOff size={13} />连接中断，正在重新连接。任务在服务器上照常进行，连上后会自动补齐。
        </div>
      )}
      <div ref={scroller} onScroll={(e) => { const el = e.currentTarget; stick.current = el.scrollHeight - el.scrollTop - el.clientHeight < 80; }} className="flex-1 overflow-y-auto">
        <main className="mx-auto flex w-full max-w-3xl flex-col gap-5 px-4 pb-8 pt-6" aria-live="off">
          {s.ready && s.items.length === 0 && <Welcome name={info.name} onPick={(t) => send(t).catch(() => {})} />}
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
        <ScheduleDock schedules={schedules} onChange={setSchedules} onError={setError} />
        <TaskDock tasks={tasks} pos={pos} />
        {error && <p className="mx-auto mb-1.5 max-w-3xl px-4 text-center text-xs text-danger" role="alert">{error}<button onClick={() => setError('')} className="ml-2 text-fg4 underline">知道了</button></p>}
        <Composer name={info.name} connected={s.connected} chatBusy={s.busy} working={working}
          lastUser={editId && lastUser ? { id: lastUser.id, text: lastUser.text } : undefined}
          onSend={send} onEdit={edit} onStopReply={() => api.stopReply().catch(() => {})} onStopAll={stopAll} />
      </div>
    </div>
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
  return (
    <AssistantRow>
      <div className="group">
        {m.thinking && <ThinkingBlock text={m.thinking} ms={m.thinking_ms} />}
        {m.text && <AssistantBubbles text={m.text} />}
        {m.files && m.files.length > 0 && <div className="mt-2"><Files files={m.files} /></div>}
        {m.tools && m.tools.length > 0 && (
          <div className="mt-1.5 flex flex-wrap gap-1.5">
            {m.tools.map((t) => <span key={t} className="inline-flex items-center gap-1 rounded-full bg-s2 px-2 py-0.5 text-[11.5px] text-fg3"><Eye size={11} className="text-fg4" />{t}</span>)}
          </div>
        )}
        {m.card && <div className={m.text ? 'mt-3' : ''}><OptionCard card={m.card} /></div>}
        {m.secret && <div className={m.text ? 'mt-3' : ''}><SecretCard req={m.secret} /></div>}
        <Reactions m={m} />
        {(m.text || m.card) && <MessageActions m={m} canRegenerate={canRegenerate} onError={onError} />}
      </div>
    </AssistantRow>
  );
}

function AssistantRow({ children }: { children: ReactNode }) {
  return (
    <div className="rise flex gap-3">
      <div className="pt-0.5"><Avatar size={28} /></div>
      <div className="min-w-0 flex-1 pt-0.5">{children}</div>
    </div>
  );
}

const Indented = ({ children }: { children: ReactNode }) => <div className="sm:pl-10">{children}</div>;

function Welcome({ name, onPick }: { name: string; onPick: (t: string) => void }) {
  const ideas = [
    { icon: FolderSearch, title: '整理工作区', text: '看看工作区里有什么，写一份 notes.md 总结' },
    { icon: SquareTerminal, title: '写代码并运行', text: '写一个 hello.go 并运行它' },
    { icon: ListTodo, title: '先记个待办', text: '帮我记个待办：明天整理 README，先别开始' },
    { icon: ListTodo, title: '定个提醒', text: '帮我创建一个明天上午 9 点的单次提醒：整理 README' },
  ];
  return (
    <div className="rise flex flex-col items-center px-2 pt-[9vh] text-center">
      <Avatar size={72} />
      <h1 className="mt-5 font-serif text-[30px] font-semibold tracking-tight">你好，我是 {name}</h1>
      <p className="mt-2 max-w-md text-[14.5px] leading-relaxed text-fg3">
        直接聊天，或者把事情交给我。耗时的活在后台做，你随时可以继续说话、插队、暂停；有风险的操作会先问你。
      </p>
      <div className="mt-8 grid w-full max-w-2xl gap-2.5 sm:grid-cols-3">
        {ideas.map(({ icon: Icon, title, text }) => (
          <button key={text} onClick={() => onPick(text)} className="group flex flex-col gap-1.5 rounded-2xl border border-line bg-s1 p-4 text-left transition hover:-translate-y-px hover:border-accent/40 hover:shadow-[0_6px_20px_-12px_var(--shadow)]">
            <Icon size={18} className="text-accent" />
            <span className="text-[13.5px] font-medium text-fg">{title}</span>
            <span className="text-[12.5px] leading-snug text-fg3 group-hover:text-fg2">{text}</span>
          </button>
        ))}
      </div>
      <p className="mt-6 flex items-center gap-1.5 text-[12px] text-fg4"><Sparkles size={12} />可以粘贴图片或拖文件进来</p>
    </div>
  );
}
