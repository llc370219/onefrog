import { useCallback, useEffect, useRef, useState } from 'react';
import { CalendarClock, MessageSquare, Pencil, Play, Plus, Trash2 } from 'lucide-react';
import { api, type Automation, type AutomationInput, type AutomationKind, type AutomationMode, type BotRow, type Host, type TaskView } from '../api';
import { Button, Empty, Field, Modal, Notice, PageHeading } from '../components/ui';
import { taskStatusText, useTaskPoll } from '../lib/taskFeed';
import TaskPanel from '../components/TaskPanel';

const weekdays = ['周日', '周一', '周二', '周三', '周四', '周五', '周六'];
const kinds: { id: AutomationKind; label: string }[] = [{ id: 'daily', label: '每天' }, { id: 'weekly', label: '每周' }, { id: 'interval', label: '固定间隔' }, { id: 'once', label: '只运行一次' }];
const modes: { id: AutomationMode; label: string }[] = [{ id: '', label: '跟随设置（关闭审批时改用智能审批）' }, { id: 'smart', label: '智能审批 · 有风险的命令不执行' }, { id: 'plan', label: '仅规划 · 不执行命令' }];
/**
 * `waiting` means the run is parked, not finished: the runtime reports it while
 * it waits for an operator. The exact reason comes from the linked task, so the
 * label never claims an approval when the task is waiting for an answer.
 */
const statusNames: Record<string, [string, string]> = { running: ['运行中', 'warn'], waiting: ['等待你处理', 'warn'], success: ['上次成功', 'ok'], failed: ['上次失败', 'danger'] };
const waitingText = (task?: TaskView) => {
  if (!task) return '等待你处理（还没完成）';
  switch (task.pending?.kind) {
    case 'approval': return '等待你批准一项操作（还没完成）';
    case 'user': return '等待你回答问题（还没完成）';
    case 'job': return '等待外部任务完成（还没完成）';
    default: return '等待你处理（还没完成）';
  }
};
const errText = (e: unknown, fallback: string) => e instanceof Error ? e.message : fallback;
const fmt = (time?: string) => time ? new Date(time).toLocaleString([], { month: '2-digit', day: '2-digit', hour: '2-digit', minute: '2-digit', hour12: false }) : '—';
const pad = (n: number) => String(n).padStart(2, '0');
/** Formats an ISO time for a datetime-local input, in the browser's zone. */
const localInput = (time?: string) => { if (!time) return ''; const d = new Date(time); return Number.isNaN(d.getTime()) ? '' : `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}T${pad(d.getHours())}:${pad(d.getMinutes())}`; };

export function scheduleText(s: Automation['schedule']) {
  if (s.kind === 'interval') { const m = s.minutes || 0; return m % 1440 === 0 ? `每 ${m / 1440} 天` : m % 60 === 0 ? `每 ${m / 60} 小时` : `每 ${m} 分钟`; }
  if (s.kind === 'daily') return `每天 ${s.time}`;
  if (s.kind === 'weekly') return `每${weekdays[s.weekday ?? 0]} ${s.time}`;
  return `一次 · ${fmt(s.at)}`;
}
/** Dashboard card: counts plus the next scheduled runs. */
export function AutomationGlance({ onOpen }: { onOpen: () => void }) {
  const [rows, setRows] = useState<Automation[] | null>(null);
  const [error, setError] = useState('');
  useEffect(() => {
    let active = true;
    const refresh = () => api.automations().then(list => { if (active) { setRows(list); setError(''); } }).catch(() => { if (active) setError('自动化数据暂时不可用'); });
    void refresh();
    const timer = window.setInterval(() => { if (!document.hidden) void refresh(); }, 30000);
    return () => { active = false; clearInterval(timer); };
  }, []);
  if (error) return <p className="card-foot danger">{error}</p>;
  if (!rows) return <p className="card-foot">正在读取自动化…</p>;
  const on = rows.filter(a => a.enabled);
  const next = on.filter(a => a.next_run_at).sort((a, b) => a.next_run_at!.localeCompare(b.next_run_at!))[0];
  return <div className="asset-glance">
    <div><span>已启用</span><strong>{on.length}<small className="muted"> / {rows.length}</small></strong></div>
    <div><span>运行中</span><strong>{rows.filter(a => a.last_status === 'running').length}{rows.some(a => a.last_status === 'waiting') && <small className="muted"> · 待处理 {rows.filter(a => a.last_status === 'waiting').length}</small>}</strong></div>
    <div><span>上次失败</span><strong className={rows.some(a => a.last_status === 'failed') ? 'danger' : ''}>{rows.filter(a => a.last_status === 'failed').length}</strong></div>
    <div><span>下一次</span><strong className="mono">{next ? fmt(next.next_run_at) : '—'}</strong></div>
    <p className="develop-glance-foot"><span>{next ? next.name + ' · ' + scheduleText(next.schedule) : rows.length ? '没有待运行的自动化' : '还没有自动化，可以让 Bot 每天定时巡检。'}</span><button className="text-btn" onClick={onOpen}>管理自动化</button></p>
  </div>;
}

type Form = { name: string; prompt: string; bot_id: string; server_id: string; kind: AutomationKind; minutes: string; time: string; weekday: string; at: string; approval_mode: AutomationMode; enabled: boolean };
const blank = (bot: string): Form => ({ name: '', prompt: '', bot_id: bot, server_id: '', kind: 'daily', minutes: '60', time: '09:00', weekday: '1', at: '', approval_mode: '', enabled: true });
const fromItem = (a: Automation): Form => ({ name: a.name, prompt: a.prompt, bot_id: a.bot_id, server_id: a.server_id, kind: a.schedule.kind, minutes: String(a.schedule.minutes || 60), time: a.schedule.time || '09:00', weekday: String(a.schedule.weekday ?? 1), at: localInput(a.schedule.at), approval_mode: a.approval_mode, enabled: a.enabled });
const inputOf = (a: Automation, enabled = a.enabled): AutomationInput => ({ name: a.name, prompt: a.prompt, bot_id: a.bot_id, server_id: a.server_id, schedule: a.schedule, approval_mode: a.approval_mode, enabled });

function toInput(f: Form): AutomationInput {
  const schedule: AutomationInput['schedule'] = { kind: f.kind };
  if (f.kind === 'interval') { const m = Number(f.minutes); if (!Number.isInteger(m) || m < 15 || m > 10080) throw new Error('间隔必须是 15–10080 分钟的整数'); schedule.minutes = m; }
  if (f.kind === 'daily' || f.kind === 'weekly') { if (!/^\d{2}:\d{2}$/.test(f.time)) throw new Error('请选择运行时间'); schedule.time = f.time; }
  if (f.kind === 'weekly') schedule.weekday = Number(f.weekday);
  if (f.kind === 'once') { const d = new Date(f.at); if (!f.at || Number.isNaN(d.getTime())) throw new Error('请选择运行时间'); schedule.at = d.toISOString(); }
  if (!f.name.trim()) throw new Error('请填写名称');
  if (!f.prompt.trim()) throw new Error('请填写要 Bot 做的事');
  if (!f.bot_id) throw new Error('请选择 Bot');
  return { name: f.name.trim(), prompt: f.prompt.trim(), bot_id: f.bot_id, server_id: f.server_id, schedule, approval_mode: f.approval_mode, enabled: f.enabled };
}

// 自动化到点后进入这个 Bot 的 AI 助理后台队列（task_auto…），在 AI 助理里能看到过程和审批卡片。
// 'frog-bot' 是 0.2 之前单个 Frog Bot 的旧目标，现在由主控执行。
const LEGACY_FROG = 'frog-bot';
const inAssistant = (taskID?: string) => !!taskID && taskID.startsWith('task_auto');

export default function Automations({ bots, hosts, onOpenAssistant }: { bots: BotRow[]; hosts: Host[]; onOpenAssistant: (botId?: string) => void }) {
  const [items, setItems] = useState<Automation[] | null>(null);
  const [error, setError] = useState('');
  const [editing, setEditing] = useState<Automation | 'new' | null>(null);
  const [form, setForm] = useState<Form>(blank(''));
  const [formError, setFormError] = useState('');
  const [saving, setSaving] = useState(false);
  const [working, setWorking] = useState('');
  const [removing, setRemoving] = useState<Automation | null>(null);
  // Linked durable tasks of runs that are still active, so a `waiting` run
  // shows the real reason and the operator can open it to approve or answer.
  const [tasks, setTasks] = useState<Record<string, TaskView>>({});
  const [taskBusy, setTaskBusy] = useState<string[]>([]);
  const mounted = useRef(true);
  const enabledBots = bots.filter(b => b.enabled);
  const firstBot = (enabledBots.find(b => b.id === 'main') || enabledBots[0])?.id || '';
  const load = useCallback(async () => {
    try { const list = await api.automations(); if (mounted.current) { setItems(list); setError(''); } }
    catch (e) { if (mounted.current) setError(errText(e, '读取自动化失败')); }
  }, []);
  const anyRunning = !!items?.some(a => a.last_status === 'running' || a.last_status === 'waiting');
  const linked = (items || []).filter(a => a.task_id && !inAssistant(a.task_id) && (a.last_status === 'running' || a.last_status === 'waiting'));
  const linkedKey = linked.map(a => a.task_id + ':' + a.last_status).join('|');
  useTaskPoll(linked.length > 0, 5000, async isCurrent => {
    const rows = await Promise.all(linked.map(async a => {
      try { return [a.task_id!, await api.task(a.task_id!)] as const; } catch { return null; }
    }));
    if (!isCurrent()) return;
    const next: Record<string, TaskView> = {};
    for (const row of rows) if (row) next[row[0]] = row[1];
    setTasks(next);
  }, [linkedKey]);
  useEffect(() => { mounted.current = true; void load(); return () => { mounted.current = false; }; }, [load]);
  // Poll fast while a run is in progress so its result shows up promptly.
  useEffect(() => {
    const timer = window.setInterval(() => { if (!document.hidden) void load(); }, anyRunning ? 3000 : 30000);
    return () => clearInterval(timer);
  }, [load, anyRunning]);
  // A classic Bot's run is handled right here: there is no separate chat
  // page any more, so its approvals, pause and stop live on the card.
  async function control(id: string, key: string, call: () => Promise<TaskView>) {
    if (taskBusy.some(k => k.startsWith(id + ':'))) return;
    setTaskBusy(cur => [...cur, id + ':' + key]);
    try { const t = await call(); if (mounted.current) setTasks(cur => ({ ...cur, [t.id]: t })); void load(); }
    catch (e) { if (mounted.current) setError(errText(e, '操作失败')); }
    finally { if (mounted.current) setTaskBusy(cur => cur.filter(k => k !== id + ':' + key)); }
  }
  const replace = (a: Automation) => setItems(old => (old || []).some(x => x.id === a.id) ? (old || []).map(x => x.id === a.id ? a : x) : [...(old || []), a]);
  function openNew() { setForm(blank(firstBot)); setFormError(''); setEditing('new'); }
  function openEdit(a: Automation) { setForm(fromItem(a)); setFormError(''); setEditing(a); }
  async function save() {
    setFormError('');
    let body: AutomationInput;
    try { body = toInput(form); } catch (e) { setFormError(errText(e, '请检查表单')); return; }
    setSaving(true);
    try {
      const saved = editing === 'new' || !editing ? await api.createAutomation(body) : await api.updateAutomation(editing.id, body);
      if (!mounted.current) return; replace(saved); setEditing(null);
    } catch (e) { if (mounted.current) setFormError(errText(e, '保存失败')); }
    finally { if (mounted.current) setSaving(false); }
  }
  async function act(a: Automation, fn: () => Promise<unknown>) {
    if (working) return; setWorking(a.id); setError('');
    try { await fn(); } catch (e) { if (mounted.current) setError(errText(e, '操作失败')); }
    finally { if (mounted.current) { setWorking(''); void load(); } }
  }
  async function remove() {
    if (!removing) return; const a = removing; setSaving(true);
    try { await api.deleteAutomation(a.id); if (mounted.current) { setItems(old => (old || []).filter(x => x.id !== a.id)); setRemoving(null); } }
    catch (e) { if (mounted.current) setFormError(errText(e, '删除失败')); }
    finally { if (mounted.current) setSaving(false); }
  }
  const leader = bots.find(b => b.id === 'main');
  const botName = (id: string) => id === LEGACY_FROG ? (leader?.name || '主控') : bots.find(b => b.id === id)?.name || '已删除的 Bot';
  const hostName = (id: string) => !id ? '不指定服务器' : hosts.find(h => h.id === id)?.name || '已删除的服务器';
  const set = <K extends keyof Form>(key: K, value: Form[K]) => setForm(f => ({ ...f, [key]: value }));
  const list = items || [];
  return <div className="page automations-page"><PageHeading title="自动化" note="让 Bot 按计划在后台执行任务，结果显示在卡片上并写入变更记录。" actions={<Button variant="primary" disabled={!enabledBots.length} onClick={openNew}><Plus size={14}/>新建自动化</Button>}/>
    {error && <Notice error>{error}</Notice>}
    <p className="automation-note"><CalendarClock size={13}/>到点后任务进入对应 Bot 的 AI 助理后台运行。指定的服务器上按审批设置执行，其他服务器和有风险的操作会在 AI 助理里弹出审批卡片，10 分钟内没处理会自动拒绝。时间按 OneFrog 服务器所在时区计算。</p>
    {items === null ? <Empty title="正在读取自动化"/> : !list.length ? <Empty title="还没有自动化" body="例如每天早上让 Bot 检查磁盘和服务状态，或每小时巡检一次日志里的错误。" action={enabledBots.length ? <Button variant="primary" onClick={openNew}><Plus size={14}/>新建自动化</Button> : undefined}/> :
      <div className="automation-list">{list.map(a => {
        const st = a.last_status ? statusNames[a.last_status] : undefined;
        const busy = working === a.id;
        return <section className={`card automation-item${a.enabled ? '' : ' paused'}`} key={a.id}>
          <header className="automation-head">
            <div className="automation-title"><h2><CalendarClock size={16}/><span title={a.name}>{a.name}</span></h2><small className="muted">{scheduleText(a.schedule)} · {botName(a.bot_id)} · {hostName(a.server_id)}{a.approval_mode === 'plan' ? ' · 仅规划' : ''}</small></div>
            <button className={`switch ${a.enabled ? 'on' : ''}`} role="switch" aria-checked={a.enabled} aria-label={`启用自动化 ${a.name}`} disabled={!!working} onClick={() => void act(a, async () => replace(await api.updateAutomation(a.id, inputOf(a, !a.enabled))))}><i/></button>
          </header>
          <p className="automation-prompt">{a.prompt}</p>
          <div className="automation-meta">
            {a.last_status === 'waiting'
              ? <span className={`status ${st?.[1] || 'warn'}`}><i/>{waitingText(tasks[a.task_id || ''])}</span>
              : st ? <span className={`status ${st[1]}`}><i/>{st[0]}</span> : <span className="status muted"><i/>尚未运行</span>}
            <span>下次 <strong className="mono">{a.enabled && a.next_run_at ? fmt(a.next_run_at) : a.enabled ? '—' : '已暂停'}</strong></span>
            <span>上次 <strong className="mono">{fmt(a.last_run_at)}</strong></span>
          </div>
          {a.last_status === 'failed' && a.last_error && <p className="automation-result danger">{a.last_error}</p>}
          {a.last_status === 'waiting' && <p className="automation-result">{a.task_id && tasks[a.task_id] ? taskStatusText(tasks[a.task_id]) : '这次运行还没结束，等待你处理；可在下方任务卡片里批准、拒绝或停止。'}</p>}
          {!inAssistant(a.task_id) && a.task_id && tasks[a.task_id] && (a.last_status === 'running' || a.last_status === 'waiting') && <TaskPanel tasks={[tasks[a.task_id]]} bots={bots} busy={taskBusy} onAction={(id, action) => void control(id, action, () => api.taskAction(id, action))} onGuide={(id, content) => void control(id, 'guide', () => api.guideTask(id, content))} onApproval={(id, approvalID, allow) => void control(id, approvalID, () => api.decideTaskApproval(id, approvalID, allow, allow ? '' : '用户在自动化卡片中拒绝'))}/>}
          {a.last_status !== 'failed' && a.last_status !== 'waiting' && a.last_summary && <p className="automation-result">{a.last_summary}</p>}
          <footer className="automation-actions">
            <Button busy={busy || a.last_status === 'running'} disabled={!!working} onClick={() => void act(a, async () => replace(await api.runAutomation(a.id)))}><Play size={13}/>立即运行</Button>
            {(inAssistant(a.task_id) || a.bot_id === LEGACY_FROG) && <Button onClick={() => onOpenAssistant(a.bot_id === LEGACY_FROG ? undefined : a.bot_id)}><MessageSquare size={13}/>{a.last_status === 'waiting' ? '查看并处理任务' : '在 AI 助理中查看'}</Button>}
            <Button variant="ghost" onClick={() => openEdit(a)}><Pencil size={13}/>编辑</Button>
            <Button variant="ghost" disabled={!!working} onClick={() => { setFormError(''); setRemoving(a); }}><Trash2 size={13}/>删除</Button>
          </footer>
        </section>;
      })}</div>}
    {editing && <Modal wide title={editing === 'new' ? '新建自动化' : '编辑自动化'} note="Bot 会在设定时间收到下面的指令并在后台完成，结果显示在本页卡片上，过程和审批在 AI 助理里查看。" onClose={() => !saving && setEditing(null)} onSubmit={() => void save()} busy={saving} error={formError}>
      <Field label="名称"><input value={form.name} maxLength={40} onChange={e => set('name', e.target.value)} placeholder="例如：每日磁盘巡检"/></Field>
      <Field label="要 Bot 做的事" hint="写清楚检查什么、发现问题时怎么处理。需要审批的修改不会在后台执行。"><textarea value={form.prompt} rows={5} onChange={e => set('prompt', e.target.value)} placeholder="检查磁盘使用率和失败的 systemd 服务，超过 85% 或有失败服务时说明原因和建议。"/></Field>
      <div className="form-grid automation-form">
        <Field label="Bot"><select value={form.bot_id} onChange={e => set('bot_id', e.target.value)}>{form.bot_id === LEGACY_FROG && <option value={LEGACY_FROG}>{botName(LEGACY_FROG)}</option>}{form.bot_id !== LEGACY_FROG && !enabledBots.some(b => b.id === form.bot_id) && <option value={form.bot_id} disabled>{form.bot_id ? botName(form.bot_id) + '（已停用）' : '选择 Bot'}</option>}{enabledBots.map(b => <option key={b.id} value={b.id}>{b.name}</option>)}</select></Field>
        <Field label="服务器"><select value={form.server_id} onChange={e => set('server_id', e.target.value)}><option value="">不指定服务器</option>{form.server_id && !hosts.some(h => h.id === form.server_id) && <option value={form.server_id} disabled>已删除的服务器</option>}{hosts.map(h => <option key={h.id} value={h.id}>{h.name}</option>)}</select></Field>
        <Field label="频率"><select value={form.kind} onChange={e => set('kind', e.target.value as AutomationKind)}>{kinds.map(k => <option key={k.id} value={k.id}>{k.label}</option>)}</select></Field>
        {form.kind === 'interval' && <Field label="间隔（分钟）" hint="15 分钟到 7 天"><input type="number" min={15} max={10080} step={1} value={form.minutes} onChange={e => set('minutes', e.target.value)}/></Field>}
        {form.kind === 'daily' && <Field label="时间"><input type="time" value={form.time} onChange={e => set('time', e.target.value)}/></Field>}
        {form.kind === 'weekly' && <Field label="星期与时间"><span className="automation-weekly"><select aria-label="星期" value={form.weekday} onChange={e => set('weekday', e.target.value)}>{weekdays.map((d, i) => <option key={d} value={i}>{d}</option>)}</select><input type="time" aria-label="时间" value={form.time} onChange={e => set('time', e.target.value)}/></span></Field>}
        {form.kind === 'once' && <Field label="运行时间"><input type="datetime-local" value={form.at} onChange={e => set('at', e.target.value)}/></Field>}
      </div>
      <Field label="审核方式"><select value={form.approval_mode} onChange={e => set('approval_mode', e.target.value as AutomationMode)}>{!modes.some(m => m.id === form.approval_mode) && <option value={form.approval_mode}>替我审批 · 独立模型复核</option>}{modes.map(m => <option key={m.id} value={m.id}>{m.label}</option>)}</select></Field>
      <label className="automation-enable"><input type="checkbox" checked={form.enabled} onChange={e => set('enabled', e.target.checked)}/>保存后启用</label>
    </Modal>}
    {removing && <Modal title="删除自动化" note={`删除「${removing.name}」后不再运行。已经产生的对话和变更记录会保留。`} confirm="删除" busy={saving} error={formError} onClose={() => !saving && setRemoving(null)} onSubmit={() => void remove()}/>}
  </div>;
}
