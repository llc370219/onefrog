/*
 * Pure, offline fake of the durable-task API for the Develop build flow. It is
 * shared by the visual fixture and the behavioural tests, so what the fixture
 * shows is the same state machine the tests assert. No network, no React.
 */
import type { ChatRequest, DevModuleData, DevModuleRow, TaskApprovalMeta, TaskView, TurnItem } from '../src/api';
import type { MessageRow } from '../src/lib/chatLines';

export const BUILD_SESSION = 'sess-develop';
export const BUILT_MODULE_ID = 'mod-checkin';

/** The api surface the Develop page touches. Kept structural so tests can pass a plain object. */
type ApiTarget = Pick<typeof import('../src/api').api,
  'devModules' | 'createDevModule' | 'updateDevModule' | 'deleteDevModule' | 'enableModule' | 'devModuleData' |
  'tasks' | 'task' | 'messages' | 'createTask' | 'taskAction' | 'guideTask' | 'decideTaskApproval'>;

const API_KEYS = [
  'devModules', 'createDevModule', 'updateDevModule', 'deleteDevModule', 'enableModule', 'devModuleData',
  'tasks', 'task', 'messages', 'createTask', 'taskAction', 'guideTask', 'decideTaskApproval',
] as const;

/**
 * Points an api object at the fake runtime. This is deliberately NOT tied to a
 * component effect: under StrictMode React mounts, cleans up and mounts again,
 * so an effect cleanup would restore the real backend between the two mounts
 * and the page would fetch it ("请求失败"). The fixture installs this once
 * before the first render and restores it only when the fixture is really gone.
 */
export function installDevelopMock(target: ApiTarget, mock: DevelopMock): () => void {
  const saved = Object.fromEntries(API_KEYS.map(k => [k, target[k]]));
  for (const key of API_KEYS) {
    (target as Record<string, unknown>)[key] = mock[key];
  }
  return () => { Object.assign(target, saved); };
}

/** Minimal stand-in for the module the runtime writes on a successful build. */
const builtModuleHTML = '<div class="checkin">打卡卡片</div><script>window.onefrog.addRecord({at:Date.now()})</script>';

const approvalMeta: TaskApprovalMeta = {
  id: 'apr-dev', command: '写入自定义模块「打卡」的源码',
  reasons: ['会修改一个已存在模块的界面代码'], status: 'pending', created_at: new Date(0).toISOString(),
};
const buildSteps: TurnItem[] = [
  { kind: 'step', bot: '自定义模块', command: 'inspect 自定义模块「打卡」', ok: true },
  { kind: 'step', bot: '自定义模块', command: '读取已有 12 条记录，保留数据结构', ok: true },
];

export type DevelopState = {
  task: TaskView | null;
  ticks: number;
  rows: MessageRow[];
  pendingCard: boolean;
  calls: string[];
  /** Custom modules the fake runtime has stored, like /api/dev/modules. */
  modules: DevModuleRow[];
  /** Per-module encrypted data handed to the sandboxed frame on init. */
  moduleData: Record<string, DevModuleData>;
};

export type DevelopMock = {
  state: DevelopState;
  reset: () => void;
  /** Next create loses its response AFTER the task is stored, like a dropped connection. */
  loseNextResponse: () => void;
  /** Every fake-runtime call, for the visible fixture log. */
  subscribe: (fn: (line: string) => void) => () => void;
  tasks: () => Promise<TaskView[]>;
  task: (id: string) => Promise<TaskView>;
  messages: () => Promise<MessageRow[]>;
  createTask: (body: ChatRequest) => Promise<TaskView>;
  taskAction: (id: string, action: 'pause' | 'resume' | 'cancel') => Promise<TaskView>;
  guideTask: (id: string, content: string) => Promise<TaskView>;
  decideTaskApproval: (id: string, approvalID: string, allow: boolean) => Promise<TaskView>;
  devModules: () => Promise<DevModuleRow[]>;
  createDevModule: (body: { name: string; description?: string; html?: string; use_starter?: boolean; enabled?: boolean }) => Promise<DevModuleRow>;
  updateDevModule: (id: string, body: { name?: string; description?: string; html?: string; enabled?: boolean }) => Promise<DevModuleRow>;
  deleteDevModule: (id: string) => Promise<void>;
  enableModule: (id: string, enabled: boolean) => Promise<void>;
  devModuleData: (id: string, body: { action: string; id?: string; state?: Record<string, unknown>; data?: Record<string, unknown> }) => Promise<DevModuleData>;
};

const now = () => new Date().toISOString();

export function createDevelopMock(): DevelopMock {
  const state: DevelopState = { task: null, ticks: 0, rows: [], pendingCard: false, calls: [], modules: [], moduleData: {} };
  let lose = false;
  const listeners = new Set<(line: string) => void>();
  const say = (line: string) => { state.calls.push(line); listeners.forEach(fn => fn(line)); };
  const need = () => {
    if (!state.task) throw Object.assign(new Error('任务不存在'), { status: 404 });
    return state.task;
  };
  const reset = () => {
    state.task = null; state.ticks = 0; state.rows = []; state.pendingCard = false; state.calls = [];
    state.modules = []; state.moduleData = {}; lose = false;
  };

  const emptyData = (): DevModuleData => ({ state: {}, records: [] });

  /** Persists the generated module so the module list is truthful after a build. */
  function saveBuiltModule() {
    if (state.modules.some(m => m.id === BUILT_MODULE_ID)) return;
    const stamp = now();
    state.modules = [{
      id: BUILT_MODULE_ID, name: '打卡', description: '点击卡片记录打卡时间与备注',
      html: builtModuleHTML, enabled: true, record_count: 0,
      created_at: stamp, updated_at: stamp,
    }, ...state.modules];
    state.moduleData[BUILT_MODULE_ID] = emptyData();
    say('写入自定义模块「打卡」· id=' + BUILT_MODULE_ID);
  }

  /** One poll step of the fake runtime. */
  function advance(): TaskView {
    const cur = state.task!;
    const ticks = state.ticks + 1;
    let next = cur;
    if (cur.status === 'queued' && ticks >= 1) {
      next = { ...cur, status: 'running', next: '正在生成打卡卡片界面', total_steps: 1, updated_at: now() };
    } else if (cur.status === 'running' && ticks >= 2 && !state.pendingCard) {
      next = { ...cur, status: 'waiting_user', pending: { kind: 'user' }, total_steps: 2, updated_at: now() };
      state.rows = [...state.rows, {
        id: 'msg-choice', role: 'assistant', content: '这个打卡模块想保留原来的记录结构，还是重新建一份？',
        timeline: buildSteps,
        choice: { prompt: '数据怎么处理？', options: [{ label: '保留已有记录', value: 'keep' }, { label: '重新建一份', value: 'reset' }], allow_custom: true },
      }];
      state.pendingCard = true;
      state.ticks = ticks;
      state.task = next;
      return next;
    } else if (cur.status === 'waiting_approval') {
      // A pending approval parks the task. Only an explicit operator decision
      // moves it: allowed continues, denied ends the run truthfully.
      const approval = (cur.approvals || []).find(a => a.id === cur.pending?.approval_id) || (cur.approvals || [])[0];
      if (approval?.status === 'allowed') {
        next = { ...cur, status: 'running', next: '已获批准，正在写入源码', pending: undefined, total_steps: 3, updated_at: now() };
        state.ticks = 0;
      } else if (approval?.status === 'denied') {
        next = { ...cur, status: 'cancelled', pending: undefined, next: undefined, last_error: '你在审批卡片里拒绝了写入源码，任务已停止', updated_at: now() };
        state.ticks = ticks;
      } else {
        state.ticks = ticks;
        return cur;
      }
      state.task = next;
      return next;
    } else if (cur.status === 'running' && ticks >= 6) {
      saveBuiltModule();
      next = {
        ...cur, status: 'completed', next: '构建完成，模块已挂载到模块页与面板', total_steps: 4, updated_at: now(),
        result: '已生成「打卡」模块：点击卡片记录时间与备注，保留原有 12 条记录。',
      };
      state.rows = [...state.rows, { id: 'msg-done', role: 'assistant', content: next.result || '', timeline: buildSteps }];
    }
    state.ticks = ticks;
    state.task = next;
    return next;
  }

  return {
    state,
    reset,
    loseNextResponse() { lose = true; },
    subscribe(fn) { listeners.add(fn); return () => { listeners.delete(fn); }; },
    async tasks() { return state.task ? [state.task] : []; },
    async messages() { return state.rows; },
    async task(id) {
      if (!state.task || state.task.id !== id) throw Object.assign(new Error('任务不存在'), { status: 404 });
      return advance();
    },
    async createTask(body) {
      const cur = state.task;
      if (body.choice_answer) {
        // Answering a card is the same task API with a choice_answer body.
        const base = need();
        const next: TaskView = {
          ...base, status: 'waiting_approval', total_steps: 3, updated_at: now(),
          pending: { kind: 'approval', approval_id: approvalMeta.id }, approvals: [{ ...approvalMeta, created_at: now() }],
        };
        say('POST /api/tasks · 回答卡片 message_id=' + body.choice_answer.message_id + ' · id=' + body.client_request_id);
        state.task = next;
        state.ticks = 5;
        state.rows = [...state.rows, { id: 'msg-user-choice', role: 'user', content: '我的选择：' + (body.choice_answer.values || []).join('、') }];
        return next;
      }
      // A repeated client_request_id resolves to the stored task: no second run.
      if (cur && body.client_request_id && cur.id === 'task-build-' + body.client_request_id) {
        say('服务端按 client_request_id 去重，返回已接受的同一个任务（没有第二次执行）');
        return cur;
      }
      const task: TaskView = {
        id: 'task-build-' + (body.client_request_id || 'x'), bot_id: body.bot_id || 'develop', session_id: BUILD_SESSION,
        goal: body.content, status: 'queued', total_steps: 0, max_steps: 32, created_at: now(), updated_at: now(),
      };
      // Stored FIRST; the response may then be lost, which is the case the
      // client-side single retry has to survive.
      state.task = task;
      state.ticks = 0;
      state.pendingCard = false;
      state.rows = [...state.rows, { id: 'msg-user-' + state.rows.length, role: 'user', content: body.content }];
      if (lose) {
        lose = false;
        say('任务已保存，但第一次 POST 的响应丢失（模拟断网）· id=' + body.client_request_id);
        throw new Error('网络中断');
      }
      say('POST /api/tasks · bot=' + task.bot_id + ' · id=' + body.client_request_id);
      return task;
    },
    async taskAction(_id, action) {
      const cur = need();
      const status = action === 'pause' ? 'paused' : action === 'resume' ? 'running' : 'cancelled';
      const next: TaskView = { ...cur, status, pending: action === 'cancel' ? undefined : cur.pending, updated_at: now() };
      say('POST /api/tasks/:id/' + action);
      state.task = next;
      return next;
    },
    async guideTask(_id, content) {
      const cur = need();
      const next: TaskView = { ...cur, progress: [...(cur.progress || []), '收到引导：' + content], updated_at: now() };
      say('POST /api/tasks/:id/guidance');
      state.task = next;
      return next;
    },
    async decideTaskApproval(_id, approvalID, allow) {
      const cur = need();
      const next: TaskView = {
        ...cur,
        pending: cur.pending?.kind === 'user' ? { kind: 'approval', approval_id: approvalID } : cur.pending,
        approvals: (cur.approvals || []).map(a => a.id === approvalID ? { ...a, status: allow ? 'allowed' : 'denied', decided_at: now() } : a),
        updated_at: now(),
      };
      say('POST /api/tasks/:id/approvals/' + approvalID + ' · allow=' + allow);
      state.task = next;
      return next;
    },
    // The module APIs the Develop page calls on mount and after a build. The
    // page reads /api/dev/modules before anything else, so the fixture must
    // answer it or the empty page shows "请求失败".
    async devModules() { return state.modules; },
    async createDevModule(body) {
      const stamp = now();
      const row: DevModuleRow = {
        id: 'mod-' + (state.modules.length + 1), name: body.name, description: body.description,
        html: body.html || '<div>内置模板</div>', enabled: body.enabled !== false, record_count: 0,
        created_at: stamp, updated_at: stamp,
      };
      state.modules = [row, ...state.modules];
      state.moduleData[row.id] = emptyData();
      say('POST /api/dev/modules · ' + row.name);
      return row;
    },
    async updateDevModule(id, body) {
      const cur = state.modules.find(m => m.id === id);
      if (!cur) throw Object.assign(new Error('模块不存在'), { status: 404 });
      const next = { ...cur, ...body, updated_at: now() };
      state.modules = state.modules.map(m => m.id === id ? next : m);
      say('PUT /api/dev/modules/' + id);
      return next;
    },
    async deleteDevModule(id) {
      state.modules = state.modules.filter(m => m.id !== id);
      delete state.moduleData[id];
      say('DELETE /api/dev/modules/' + id);
    },
    async enableModule(id, enabled) {
      state.modules = state.modules.map(m => m.id === id ? { ...m, enabled, updated_at: now() } : m);
      say('POST /api/modules/' + id + '/enable · ' + enabled);
    },
    async devModuleData(id, body) {
      const cur = state.moduleData[id] || emptyData();
      if (body.action === 'get') return cur;
      if (body.action === 'clear_records') {
        state.moduleData[id] = { state: {}, records: [] };
        state.modules = state.modules.map(m => m.id === id ? { ...m, record_count: 0, updated_at: now() } : m);
        return state.moduleData[id];
      }
      // add/update/delete/save_state all just persist what the frame sent.
      const records = body.action === 'add' && body.data
        ? [...cur.records, { id: 'rec-' + (cur.records.length + 1), created_at: now(), updated_at: now(), data: body.data }]
        : cur.records;
      const next: DevModuleData = { state: body.state ? { ...cur.state, ...body.state } : cur.state, records };
      state.moduleData[id] = next;
      state.modules = state.modules.map(m => m.id === id ? { ...m, record_count: records.length, updated_at: now() } : m);
      say('POST /api/dev/modules/' + id + '/data · ' + body.action);
      return next;
    },
  };
}
