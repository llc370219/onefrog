/*
 * Offline visual fixture for the durable task UI. It is NOT part of the
 * production app: no auth, no router, no API/network calls. It renders the
 * real TaskPanel/Chat/Dashboard components with local fixture state and the
 * real index.css, so light/dark and 390px/desktop layouts can be checked.
 *
 * Open with: npx vite tests/runtime-preview.html
 */
import { StrictMode, useEffect, useMemo, useState } from 'react';
import { createRoot } from 'react-dom/client';
import '../src/index.css';
import Chat from '../src/pages/Chat';
import Dashboard from '../src/pages/Dashboard';
import Develop from '../src/pages/Develop';
import TaskPanel from '../src/components/TaskPanel';
import { setThemePref, useTheme } from '../src/theme';
import { api, type BotRow, type ChatLine, type ChatSession, type Host, type Mod, type SettingsState, type TaskActivity, type TaskApprovalMeta, type TaskView } from '../src/api';
import { createDevelopMock, installDevelopMock } from './developMock';

const now = Date.now();
const at = (minutesAgo: number) => new Date(now - minutesAgo * 60000).toISOString();

// SYNTHETIC FIXTURE ONLY: additive live-card fields below (model_label,
// activities, started_at…) exist to aid visual QA of the compact card. They are
// fake values authored here; the real values come from the backend TaskView.
const activitiesOf = (rows: Array<[string, string, TaskActivity['status'], number | undefined]>): TaskActivity[] => rows.map(([name, label, status, durationMs], i) => ({
  id: 'act-' + i, name, label, status,
  started_at: at(20 - i), ...(durationMs != null ? {duration_ms: durationMs} : {}),
}));

const bots: BotRow[] = [
  { id: 'main', name: '主控', description: '调度与总控', enabled: true, model_route: { provider_id: 'p', model: 'm' }, effective_route: { provider_id: 'p', model: 'm' }, usage: { requests: 0, reported_calls: 0, failed_calls: 0, input_tokens: 0, output_tokens: 0, total_tokens: 0 } },
  { id: 'ops', name: '运维专家', description: '服务器排障', enabled: true, model_route: { provider_id: 'p', model: 'm' }, effective_route: { provider_id: 'p', model: 'm' }, usage: { requests: 0, reported_calls: 0, failed_calls: 0, input_tokens: 0, output_tokens: 0, total_tokens: 0 } },
  { id: 'db', name: '数据库专家', description: '慢查询分析', enabled: true, model_route: { provider_id: 'p', model: 'm' }, effective_route: { provider_id: 'p', model: 'm' }, usage: { requests: 0, reported_calls: 0, failed_calls: 0, input_tokens: 0, output_tokens: 0, total_tokens: 0 } },
];

const approval: TaskApprovalMeta = { id: 'apr-1', command: 'systemctl restart nginx', reasons: ['会短暂中断在线用户的连接', '属于高危变更，执行后会写入操作记录'], status: 'pending', created_at: at(2) };
const decided: TaskApprovalMeta = { id: 'apr-0', command: 'df -h', reasons: ['只读检查'], status: 'allowed', note: '用户已批准', created_at: at(9), decided_at: at(8) };

const parentTask: TaskView = {
  id: 'task-parent', bot_id: 'main', session_id: 'sess-1', goal: '排查生产环境磁盘告警并给出处置方案',
  plan: ['确认告警来源和影响范围', '只读检查磁盘与日志占用', '把数据库慢查询分析交给数据库 Bot', '汇总结论，等待确认后再清理'],
  plan_completed: 2,
  progress: ['已读取磁盘使用率 91%', '已定位 /var/log 下 4.2 GB 的旧日志'],
  next: '等待你批准重启 nginx 前先确认到日志清理方案', status: 'waiting_approval',
  total_steps: 3, max_steps: 32, created_at: at(12), updated_at: at(1), deadline: new Date(now + 3600_000).toISOString(),
  pending: { kind: 'approval', approval_id: 'apr-1' }, approvals: [approval, decided],
  model_label: 'GLM · glm-4.7', workspace_label: 'prod-web-1', started_at: at(12),
  activities: activitiesOf([
    ['list_services', '查看运行中的服务', 'completed', 1200],
    ['read_file', '读取磁盘使用记录', 'completed', 3400],
    ['run_command', '正在运行磁盘占用分析', 'running', undefined],
  ]),
};

const childTask: TaskView = {
  id: 'task-child', parent_id: 'task-parent', bot_id: 'db', session_id: 'sess-2',
  goal: '分析慢查询并给出索引建议', plan: ['拉取慢查询日志', '定位耗时最高的三条 SQL'], plan_completed: 1,
  progress: ['已拿到近 24 小时慢查询统计'], next: '正在对比索引命中率', status: 'running',
  total_steps: 2, max_steps: 32, created_at: at(6), updated_at: at(1),
  model_label: 'GLM · glm-4.7', started_at: at(6),
  activities: activitiesOf([
    ['query_memory', '检索数据库运维记忆', 'completed', 900],
    ['read_file', '读取慢查询日志', 'running', undefined],
  ]),
};

const doneTask: TaskView = {
  id: 'task-done', bot_id: 'ops', session_id: 'sess-1', goal: '检查服务器证书到期时间',
  status: 'completed', total_steps: 4, max_steps: 32, created_at: at(40), updated_at: at(30),
  result: '证书还有 47 天到期，自动续期任务正常，无需人工处理。',
  model_label: 'GLM · glm-4.7', started_at: at(40), finished_at: at(30),
  activities: activitiesOf([
    ['run_command', '查询证书有效期', 'completed', 2100],
    ['run_command', '核对续期任务状态', 'completed', 1500],
  ]),
};

const failedTask: TaskView = {
  id: 'task-failed', bot_id: 'ops', session_id: 'sess-1', goal: '重启测试环境服务',
  status: 'failed', total_steps: 1, max_steps: 32, created_at: at(70), updated_at: at(60),
  last_error: 'SSH 连接超时：10.0.0.9:22 无法访问',
};

const queuedTask: TaskView = {
  id: 'task-queued', bot_id: 'main', session_id: 'sess-1', goal: '汇总本周变更记录',
  status: 'queued', total_steps: 0, max_steps: 32, created_at: at(0), updated_at: at(0),
  model_label: 'GLM · glm-4.7',
};

const pausedTask: TaskView = {
  id: 'task-paused', bot_id: 'main', session_id: 'sess-1', goal: '批量重启前先核对变更清单',
  status: 'paused', total_steps: 2, max_steps: 32, created_at: at(15), updated_at: at(4),
  model_label: 'GLM · glm-4.7',
};

// Older-backend fallback shape: none of the additive live fields are present.
const legacyTask: TaskView = {
  id: 'task-legacy', bot_id: 'ops', session_id: 'sess-1', goal: '旧版后端任务（无实时字段）',
  status: 'running', total_steps: 1, max_steps: 32, created_at: at(8), updated_at: at(2),
  next: '正在检查服务状态',
};

const userTask: TaskView = {
  id: 'task-user', bot_id: 'main', session_id: 'sess-1', goal: '确认是否清理 3 天前的访问日志',
  status: 'waiting_user', total_steps: 2, max_steps: 32, created_at: at(20), updated_at: at(5),
  pending: { kind: 'user' }, progress: ['统计出 3 天前日志共 1.1 GB'],
};

const budgetTask: TaskView = {
  id: 'task-budget', bot_id: 'ops', session_id: 'sess-1', goal: '深度巡检全部服务',
  status: 'budget_exhausted', total_steps: 32, max_steps: 32, created_at: at(90), updated_at: at(80),
  last_error: '总步骤预算用完，任务还没有完成',
};

const sessions: ChatSession[] = [{ id: 'sess-1', bot_id: 'main', title: '生产环境磁盘告警', preview: '排查磁盘告警', updated_at: at(1) }];
const hosts: Host[] = [{ id: 'srv-1', name: 'prod-web-1', host: '10.0.0.11', up: true, disk: 91, load: '0.42', role: 'web', status: 'online', username: 'root', port: 22, auth_type: 'password' }];
const mods: Mod[] = [{ id: 'alerts', name: '告警', desc: '离线与磁盘告警', enabled: true }];
function Toolbar({ width, onWidth }: { width: number | 'auto'; onWidth: (w: number | 'auto') => void }) {
  const { pref, theme } = useTheme();
  return (
    <div className="preview-toolbar" role="group" aria-label="预览控制">
      <strong>离线界面预览</strong>
      <span>无登录、无网络请求，仅本地夹具数据</span>
      <div>
        <button type="button" className={pref === 'light' ? 'active' : ''} onClick={() => setThemePref('light')}>浅色</button>
        <button type="button" className={pref === 'dark' ? 'active' : ''} onClick={() => setThemePref('dark')}>深色</button>
        <button type="button" className={pref === 'system' ? 'active' : ''} onClick={() => setThemePref('system')}>跟随系统（{theme}）</button>
      </div>
      <div>
        <button type="button" className={width === 390 ? 'active' : ''} onClick={() => onWidth(390)}>容器宽 390px</button>
        <button type="button" className={width === 'auto' ? 'active' : ''} onClick={() => onWidth('auto')}>占满宽度</button>
      </div>
    </div>
  );
}

function TaskCardsFixture() {
  const [tasks, setTasks] = useState<TaskView[]>([parentTask, childTask, queuedTask, legacyTask, userTask, pausedTask, doneTask, failedTask, budgetTask]);
  const [busy, setBusy] = useState('');
  const busyFor = (id: string, action: string) => { setBusy(id + ':' + action); window.setTimeout(() => setBusy(''), 400); };
  const patch = (id: string, fn: (t: TaskView) => TaskView) => setTasks(cur => cur.map(t => t.id === id ? fn(t) : t));

  function action(id: string, act: 'pause' | 'resume' | 'cancel') {
    busyFor(id, act);
    patch(id, t => ({ ...t, status: act === 'pause' ? 'paused' : act === 'resume' ? 'queued' : 'cancelled', updated_at: new Date().toISOString() }));
  }
  function guide(id: string, content: string) {
    busyFor(id, 'guide');
    patch(id, t => ({ ...t, progress: [...(t.progress || []), '收到引导：' + content], updated_at: new Date().toISOString() }));
  }
  function decide(taskID: string, approvalID: string, allow: boolean) {
    busyFor(taskID, approvalID);
    patch(taskID, t => ({
      ...t,
      status: 'queued',
      pending: undefined,
      approvals: (t.approvals || []).map(a => a.id === approvalID ? { ...a, status: allow ? 'allowed' : 'denied', decided_at: new Date().toISOString(), note: allow ? '用户已批准' : '用户在任务卡片中拒绝' } : a),
    }));
  }
  function dismiss(id: string) { setTasks(cur => cur.filter(t => t.id !== id)); }
  const parentOf = (t: TaskView) => tasks.find(x => x.id === t.parent_id);

  return (
    <section className="preview-section">
      <h2>持久任务卡片 · 全部状态</h2>
      <p>真实 TaskPanel 组件；按钮只改本地夹具状态。含父任务与其子任务（不同 Bot、不同对话）。</p>
      <TaskPanel tasks={tasks} bots={bots} parentOf={parentOf} busy={busy} onAction={action} onGuide={guide} onApproval={decide} onDismiss={dismiss}/>
    </section>
  );
}

function ChatFixture() {
  const [tasks, setTasks] = useState<TaskView[]>([parentTask, childTask]);
  const [draft, setDraft] = useState('清理 /var/log 里 3 天前的旧日志，先只读确认。');
  const [lines, setLines] = useState<ChatLine[]>(lines0());
  const [busy, setBusy] = useState('');
  const [notice, setNotice] = useState('');
  const [state, setState] = useState({ model: { provider_id: '', model: '' }, excluded: [] as string[], approval: 'default' });
  const patch = (id: string, fn: (t: TaskView) => TaskView) => setTasks(cur => cur.map(t => t.id === id ? fn(t) : t));
  const controls = useMemo(() => state, [state]);

  function send() {
    if (!draft.trim()) return;
    const id = 'task-new-' + tasks.length;
    const task: TaskView = { id, user_message_id: 'chat-' + id, bot_id: 'main', session_id: 'sess-1', goal: draft.trim(), status: 'queued', total_steps: 0, max_steps: 32, created_at: new Date().toISOString(), updated_at: new Date().toISOString() };
    setTasks(cur => [task, ...cur]);
    setDraft('');
    setNotice('已接受消息（夹具）');
  }
  function action(id: string, act: 'pause' | 'resume' | 'cancel' | 'withdraw') {
    setBusy(id + ':' + act);
    window.setTimeout(() => setBusy(''), 400);
    patch(id, t => ({ ...t, status: act === 'pause' ? 'paused' : act === 'resume' ? 'queued' : 'cancelled' }));
  }

  return (
    <section className="preview-section preview-chat">
      <h2>对话页 · 动画回复</h2>
      <p>真实 Chat 组件；等待动效显示在对话中，审批和停止回复仍可操作。</p>
      <button className="text-btn" type="button" onClick={() => {
        const pending = tasks.filter(t => !t.parent_id && ['running', 'queued', 'waiting_approval'].includes(t.status)).reverse();
        const [current, next] = pending;
        if (!current) return;
        setTasks(cur => cur.map(t => t.id === current.id || t.parent_id === current.id
          ? { ...t, status: 'completed' } : t.id === next?.id ? { ...t, status: 'running' } : t));
        setLines(cur => [...cur, { who: 'bot', text: '排查完成，日志轮转正常。\n\n没有改动服务器配置。' },
          ...(next ? [{ who: 'user' as const, id: next.user_message_id, text: next.goal }] : [])]);
      }}>模拟收到最终回复</button>
      <div className="preview-frame">
        <Chat
          onAddServer={() => {}} controls={controls} onControls={next => setState(next as typeof state)} settings={null}
          mode="team" projectTitle="生产环境磁盘告警" onOpenWorkspace={() => {}} attachments={[]} onFiles={() => {}} onRemoveFile={() => {}} uploading={false}
          activeBot={bots[0]} hosts={hosts} bots={bots} secrets={[]} selected="srv-1" onSelect={() => {}}
          draft={draft} onDraft={setDraft} lines={lines} onSend={send} sending={false} onNew={() => {}}
          sessions={sessions} session="sess-1" onSession={() => {}} onDeleteSession={() => {}} onNewTopic={() => {}}
          onNotice={setNotice} onAnswer={() => {}} onProvide={() => {}}
          tasks={tasks} taskBusy={busy} onTaskAction={action}
          onTaskApproval={(id, approvalID, allow) => patch(id, t => ({ ...t, status: 'queued', pending: undefined, approvals: (t.approvals || []).map(a => a.id === approvalID ? { ...a, status: allow ? 'allowed' : 'denied' } : a) }))}
        />
      </div>
      {notice && <p className="preview-note" role="status">{notice}</p>}
    </section>
  );
}

function lines0(): ChatLine[] {
  return [
    { who: 'user', text: '排查生产环境磁盘告警并给出处置方案' },
    { who: 'bot', text: '已确认磁盘使用率 91%，主要占用在 /var/log。清理前需要你确认。', timeline: [
      { kind: 'step', bot: '主控', server_id: 'srv-1', command: 'df -h', ok: true },
      { kind: 'delegate', bot: '主控', target: '数据库专家', task: '分析慢查询并给出索引建议' },
    ] },
  ];
}

function DashboardFixture() {
  const [tasks, setTasks] = useState<TaskView[]>([parentTask]);
  const [busy, setBusy] = useState('');
  const patch = (id: string, fn: (t: TaskView) => TaskView) => setTasks(cur => cur.map(t => t.id === id ? fn(t) : t));
  return (
    <section className="preview-section preview-dashboard">
      <h2>面板页 · 动画回复</h2>
      <p>真实 Dashboard 组件；面板请求使用相同的回复动画和审批控件。</p>
      <div className="preview-frame">
        <Dashboard
          hosts={hosts} mods={mods} history={[]} bots={bots}
          job={{ pending: true, prompt: '检查生产环境磁盘', text: '', error: '', activeStatus: '执行中：读取磁盘占用', steps: [], sessionId: 'sess-panel', botId: 'main', taskID: parentTask.id }}
          tasks={tasks} taskBusy={busy} sending={false}
          onSelect={() => {}} onRun={() => {}} onAnswer={() => {}} onProvide={() => {}}
          onModules={() => {}} onSettings={() => {}} onServers={() => {}} onBots={() => {}} onHistory={() => {}} onAssets={() => {}} onOpenModule={() => {}}
          onTaskAction={(id, act) => { setBusy(id + ':' + act); window.setTimeout(() => setBusy(''), 400); patch(id, t => ({ ...t, status: act === 'pause' ? 'paused' : act === 'resume' ? 'queued' : 'cancelled' })); }}
          onTaskApproval={(id, approvalID, allow) => patch(id, t => ({ ...t, status: 'queued', pending: undefined, approvals: (t.approvals || []).map(a => a.id === approvalID ? { ...a, status: allow ? 'allowed' : 'denied' } : a) }))}
        />
      </div>
    </section>
  );
}

/**
 * Develop page driven by a mocked, fully offline API. It exercises the real
 * page: POST /api/tasks with the develop bot, polling of GET /api/tasks/:id,
 * approval + question-card answers, and restore after a "refresh" through
 * GET /api/tasks?session_id=. Nothing here touches the network.
 */
const developSettings: SettingsState = {
  providers: [{ id: 'p', name: '本地夹具渠道', base_url: 'https://example.invalid/v1', enabled: true, auth_required: false, models: ['fixture-model'], has_api_key: true }],
  default: { provider_id: 'p', model: 'fixture-model' },
  worker: { provider_id: 'p', model: 'fixture-model' },
  bot_routes: {},
  approval: { mode: 'smart', route: { provider_id: 'p', model: 'fixture-model' }, instructions: '' },
  execution: { timeout_seconds: 600, command_timeout_seconds: 120, max_steps: 32, context_messages: 20 },
};

function DevelopFixture() {
  const [reloadKey, setReloadKey] = useState(0);
  const [log, setLog] = useState<string[]>([]);
  const [loseResponse, setLoseResponse] = useState(false);
  // The fake runtime is installed once at module scope (see the bottom of this
  // file), NOT from an effect here: StrictMode runs mount → cleanup → mount, so
  // an effect-owned mock would restore the real backend in between and the
  // page's mount fetch would hit it.
  const mock = developMock;
  const say = (line: string) => setLog(cur => [...cur.slice(-11), line]);
  useEffect(() => mock.subscribe(say), [mock]);

  const resetFixture = () => {
    mock.reset();
    try { localStorage.removeItem('onefrog-develop-session'); } catch { /* ignore */ }
    setLoseResponse(false);
    setLog([]);
    setReloadKey(k => k + 1);
  };

  return (
    <section className="preview-section preview-develop">
      <h2>自定义模块页 · 持久构建任务</h2>
      <p>真实 Develop 组件 + 完全离线的 API 夹具：创建任务、轮询进度、回答卡片、批准写入、完成，以及刷新后从 GET /api/tasks 恢复。</p>
      <p className="preview-note">这里是本地夹具，不是真实后端：任务、模块列表和模块数据都由本页内存里的假运行时提供，生成的「打卡」模块只是占位 HTML，不会写入服务器，也不会真的在沙箱里运行。</p>
      <div className="preview-toolbar" role="group" aria-label="Develop 夹具控制">
        <button type="button" onClick={resetFixture}>重置夹具</button>
        <button type="button" onClick={() => { setReloadKey(k => k + 1); say('模拟刷新页面：组件重新挂载，仅靠 localStorage 会话 id + GET /api/tasks 恢复'); }}>模拟刷新（重连）</button>
        <button type="button" className={loseResponse ? 'active' : ''} onClick={() => { mock.loseNextResponse(); setLoseResponse(true); say('下一次提交会先保存任务再丢失响应，验证同一 client_request_id 只重试一次'); }}>下次提交丢失响应</button>
      </div>
      <div className="preview-frame">
        <Develop key={reloadKey} bots={bots} settings={developSettings} onOpenChat={() => {}} onOpenSettings={() => {}} onOpenModule={() => {}} onChanged={async () => {}}/>
      </div>
      <div className="preview-note" role="status">
        <strong>任务 API 调用记录（夹具）</strong>
        {log.length ? <ul>{log.map((line, i) => <li key={i}>{line}</li>)}</ul> : <p>还没有调用。输入需求点「生成模块」。</p>}
      </div>
    </section>
  );
}

function Preview() {
  const [width, setWidth] = useState<number | 'auto'>('auto');
  const [page, setPage] = useState<'tasks' | 'chat' | 'dashboard' | 'develop'>('tasks');
  return (
    <div className="preview-shell">
      <Toolbar width={width} onWidth={setWidth}/>
      <p className="preview-note">“容器宽 390px”只约束内容容器，不会触发 CSS 媒体查询；真实手机布局请把浏览器/设备视口设为 390px（媒体查询断点 600/760px）。</p>
      <div className="preview-tabs" role="tablist" aria-label="预览页面">
        {([['tasks', '任务卡片'], ['chat', '对话页'], ['dashboard', '面板页'], ['develop', '自定义模块页']] as const).map(([id, label]) => (
          <button key={id} type="button" role="tab" aria-selected={page === id} className={page === id ? 'active' : ''} onClick={() => setPage(id)}>{label}</button>
        ))}
      </div>
      <div className="preview-viewport" style={width === 'auto' ? undefined : { width: width + 'px' }}>
        {page === 'tasks' && <TaskCardsFixture/>}
        {page === 'chat' && <ChatFixture/>}
        {page === 'dashboard' && <DashboardFixture/>}
        {page === 'develop' && <DevelopFixture/>}
      </div>
    </div>
  );
}

/**
 * The Develop fixture swaps the whole `api` surface for an offline fake. This
 * happens ONCE, at module scope, before the first render — never inside a
 * component effect. Under StrictMode React runs setup → cleanup → setup, so an
 * effect-owned mock is uninstalled between the two mounts and the page's mount
 * fetch (`GET /api/dev/modules`) reaches the real backend and shows
 * "请求失败". Owning it here keeps the fake installed across the whole
 * StrictMode replay, and the teardown only runs when the page really unloads.
 */
const developMock = createDevelopMock();
const restoreDevelopApi = installDevelopMock(api, developMock);
window.addEventListener('pagehide', restoreDevelopApi, { once: true });

createRoot(document.getElementById('root')!).render(<StrictMode><Preview/></StrictMode>);
