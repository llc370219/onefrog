import {test} from 'node:test';
import assert from 'node:assert/strict';
import {readFileSync} from 'node:fs';
import {
  LONG_TASK_THRESHOLD_MS,
  cardTasks, chatTaskClockActive, chatTaskCompact, chatTaskMode, compactTasks,
  errorSummary, isDelegatedTask, isLongTask, isMultiStepPlan,
  presentChatTasks, taskElapsedMs, taskNeedsOperator,
} from '../src/lib/chatTaskPresentation.ts';

// Value-level .mjs test: a plain object stands in for TaskView.
const T0 = '2026-01-01T00:00:00Z';
const at = ms => new Date(Date.parse(T0) + ms).toISOString();
const task = (status, extra = {}) => ({
  id: 't1', bot_id: 'b', session_id: 's', goal: 'g', status,
  total_steps: 0, max_steps: 0, created_at: T0, updated_at: T0,
  ...extra,
});

test('a normal short turn is a one-line status, not the big card', () => {
  const running = task('running', {started_at: T0});
  assert.equal(chatTaskMode(running, Date.parse(T0) + 2_000), 'compact');
  assert.deepEqual(chatTaskCompact(running, Date.parse(T0) + 2_000), {text: '正在处理…', tone: 'running'});
  // 14.999s is still short: the threshold only fires at 15s of real time.
  assert.equal(chatTaskMode(running, Date.parse(T0) + LONG_TASK_THRESHOLD_MS - 1), 'compact');
});

test('a completed short turn stays compact and shows its real duration', () => {
  const done = task('completed', {started_at: T0, finished_at: at(4_000)});
  assert.equal(chatTaskMode(done, Date.parse(T0) + 60 * 60_000), 'compact');
  assert.deepEqual(chatTaskCompact(done, Date.parse(T0) + 60 * 60_000), {text: '已完成 · 用时 4 秒', tone: 'ok'});
});

test('a failed short turn never disappears silently', () => {
  const failed = task('failed', {started_at: T0, finished_at: at(3_000), last_error: '  运行时连接被拒绝\n请稍后重试  '});
  assert.equal(chatTaskMode(failed, Date.parse(T0) + 10_000), 'compact');
  assert.deepEqual(chatTaskCompact(failed, Date.parse(T0) + 10_000), {text: '任务失败：运行时连接被拒绝 请稍后重试', tone: 'danger'});
  // A backend that sends no detail still produces a readable sentence.
  const bare = task('failed', {started_at: T0, finished_at: at(1_000)});
  const line = chatTaskCompact(bare, Date.parse(T0) + 10_000);
  assert.equal(line.tone, 'danger');
  assert.match(line.text, /^任务失败：/);
  assert.ok(!line.text.includes('undefined'));
});

test('long tasks get the card, and only from the real 15s threshold', () => {
  const running = task('running', {started_at: T0});
  assert.equal(isLongTask(running, Date.parse(T0) + LONG_TASK_THRESHOLD_MS - 1), false);
  assert.equal(isLongTask(running, Date.parse(T0) + LONG_TASK_THRESHOLD_MS), true);
  assert.equal(chatTaskMode(running, Date.parse(T0) + LONG_TASK_THRESHOLD_MS), 'card');
  // created_at is the fallback when the backend never sent started_at.
  const legacy = task('running');
  assert.equal(chatTaskMode(legacy, Date.parse(T0) + LONG_TASK_THRESHOLD_MS), 'card');
  // Longer work that already finished keeps its card until dismissed.
  const longDone = task('completed', {started_at: T0, finished_at: at(40_000)});
  assert.equal(chatTaskMode(longDone, Date.parse(T0) + 10 * 60_000), 'card');
});

test('terminal and paused clocks freeze on server timestamps, never the browser clock', () => {
  const done = task('completed', {started_at: T0, finished_at: at(2_000), updated_at: at(2_000)});
  // Simulate a browser left open for an hour: the task stays short.
  assert.equal(taskElapsedMs(done, Date.parse(T0) + 3_600_000), 2_000);
  assert.equal(chatTaskMode(done, Date.parse(T0) + 3_600_000), 'compact');
  // finished_at missing -> updated_at is the freeze point.
  const noFinish = task('failed', {started_at: T0, updated_at: at(5_000)});
  assert.equal(taskElapsedMs(noFinish, Date.parse(T0) + 3_600_000), 5_000);
  assert.equal(chatTaskMode(noFinish, Date.parse(T0) + 3_600_000), 'compact');
  // A paused task freezes at the pause write.
  const paused = task('paused', {started_at: T0, updated_at: at(8_000)});
  assert.equal(taskElapsedMs(paused, Date.parse(T0) + 3_600_000), 8_000);
  // Live tasks do advance with real time.
  const running = task('running', {started_at: T0});
  assert.equal(taskElapsedMs(running, Date.parse(T0) + 20_000), 20_000);
});

test('refresh/remount gives the same decision: no stored state, pure inputs only', () => {
  const long = task('running', {started_at: T0});
  // Same server data, later wall clock: still a card, because the runtime says so.
  assert.equal(chatTaskMode(long, Date.parse(T0) + 16_000), chatTaskMode(long, Date.parse(T0) + 9_999_999));
  const short = task('completed', {started_at: T0, finished_at: at(1_000)});
  const first = presentChatTasks([short], Date.parse(T0) + 2_000);
  const afterReload = presentChatTasks([short], Date.parse(T0) + 7_200_000);
  assert.deepEqual(afterReload, first, 'a reload must not change a finished short task into a long one');
  // The module is stateless: nothing is written to localStorage/sessionStorage.
  const src = readFileSync(new URL('../src/lib/chatTaskPresentation.ts', import.meta.url), 'utf8');
  assert.ok(!/localStorage|sessionStorage/.test(src), 'presentation must not persist to the browser');
});

test('approval, ask_user, external job and pause show the card immediately', () => {
  const now = Date.parse(T0) + 500;
  for (const status of ['waiting_user', 'waiting_approval', 'waiting_job', 'paused']) {
    const t = task(status, {started_at: T0, updated_at: at(200), activities: [{id: 'a', name: 'run_command', label: 'x', status: 'completed', started_at: T0}]});
    assert.equal(taskNeedsOperator(t), true, status);
    assert.equal(chatTaskMode(t, now), 'card', status);
  }
  // A pending approval on an otherwise running short task also needs the card.
  const approving = task('running', {started_at: T0, approvals: [{id: 'apr', status: 'pending'}]});
  assert.equal(taskNeedsOperator(approving), true);
  assert.equal(chatTaskMode(approving, Date.parse(T0) + 1_000), 'card');
  // A decided approval is history, not a blocker.
  const decided = task('running', {started_at: T0, approvals: [{id: 'apr', status: 'allowed'}]});
  assert.equal(taskNeedsOperator(decided), false);
  assert.equal(chatTaskMode(decided, Date.parse(T0) + 1_000), 'compact');
});

test('an explicit multi-step plan or a delegated child task uses the card', () => {
  const now = Date.parse(T0) + 1_000;
  assert.equal(isMultiStepPlan(task('running', {plan: ['确认告警', '检查磁盘']})), true);
  assert.equal(chatTaskMode(task('running', {started_at: T0, plan: ['确认告警', '检查磁盘']}), now), 'card');
  // One step, blank steps or no plan are not multi-step work.
  assert.equal(isMultiStepPlan(task('running', {plan: ['确认告警']})), false);
  assert.equal(isMultiStepPlan(task('running', {plan: ['确认告警', '   ']})), false);
  assert.equal(isMultiStepPlan(task('running')), false);
  assert.equal(chatTaskMode(task('running', {started_at: T0, plan: ['确认告警']}), now), 'compact');
  // A delegated child task may need approvals elsewhere: always a card.
  const child = task('running', {started_at: T0, parent_id: 'parent-1'});
  assert.equal(isDelegatedTask(child), true);
  assert.equal(chatTaskMode(child, now), 'card');
});

test('waiting tasks stay visible even when old or missing optional metadata', () => {
  const stale = task('waiting_user', {started_at: T0, updated_at: at(1_000)});
  for (const status of ['waiting_user', 'waiting_approval', 'waiting_job', 'paused']) {
    assert.equal(chatTaskMode({...stale, status}, Date.parse(T0) + 24 * 60 * 60_000), 'card', status);
  }
  assert.equal(chatTaskMode({...stale, approvals: [{id: 'apr', status: 'pending'}]}, Date.parse(T0) + 24 * 60 * 60_000), 'card');
});

test('a clock only runs while a task can still cross the threshold', () => {
  assert.equal(chatTaskClockActive(task('running')), true);
  assert.equal(chatTaskClockActive(task('queued')), true);
  for (const status of ['paused', 'waiting_user', 'waiting_approval', 'waiting_job', 'completed', 'failed', 'cancelled', 'budget_exhausted']) {
    assert.equal(chatTaskClockActive(task(status)), false, status);
  }
});

test('older backends without additive fields degrade instead of crashing', () => {
  const legacy = {id: 't9', bot_id: 'b', session_id: 's', goal: 'g', status: 'running', total_steps: 0, max_steps: 0, created_at: T0, updated_at: T0};
  const now = Date.parse(T0) + 1_000;
  assert.equal(chatTaskMode(legacy, now), 'compact');
  assert.deepEqual(chatTaskCompact(legacy, now), {text: '正在处理…', tone: 'running'});
  // No timestamps at all: no elapsed value, still a safe decision.
  const empty = {...legacy, created_at: '', updated_at: ''};
  assert.equal(taskElapsedMs(empty, now), undefined);
  assert.equal(chatTaskMode(empty, now), 'compact');
  assert.equal(chatTaskCompact(empty, Number.NaN).tone, 'running');
  // Unknown status still renders a readable line.
  assert.equal(chatTaskCompact(task('weird'), now).tone, 'muted');
});

test('presentChatTasks keeps live and failed tasks, but completed replies stay in transcript only', () => {
  const now = Date.parse(T0) + 1_000;
  const short = task('completed', {id: 'short', started_at: T0, finished_at: at(1_000)});
  const long = task('running', {id: 'long', started_at: T0});
  const ask = task('waiting_approval', {id: 'ask', started_at: T0});
  const entries = presentChatTasks([short, long, ask], now);
  assert.deepEqual(entries.map(e => [e.task.id, e.mode]), [['short', 'compact'], ['long', 'compact'], ['ask', 'card']]);
  assert.deepEqual(cardTasks(entries).map(t => t.id), ['ask']);
  assert.deepEqual(compactTasks(entries).map(e => e.task.id), ['long']);
  // Threshold crossing flips only that task, at the same wall clock + 15s.
  const later = presentChatTasks([short, long, ask], now + LONG_TASK_THRESHOLD_MS);
  assert.deepEqual(cardTasks(later).map(t => t.id), ['long', 'ask']);
  const longDone = task('completed', {id: 'long-done', started_at: T0, finished_at: at(40_000)});
  const failed = task('failed', {id: 'failed', started_at: T0, finished_at: at(2_000), last_error: '网络超时'});
  const finished = presentChatTasks([short, longDone, failed], now + 60_000);
  assert.deepEqual(cardTasks(finished), [], 'even a long successful task must not leave a final-answer card');
  assert.deepEqual(compactTasks(finished).map(e => e.task.id), ['failed'], 'a failed task still shows its error');
});

test('errorSummary is single-line, bounded and never fabricates text', () => {
  assert.equal(errorSummary(undefined), '');
  assert.equal(errorSummary('   '), '');
  assert.equal(errorSummary('a\n\nb\tc'), 'a b c');
  const long = errorSummary('x'.repeat(400), 20);
  assert.equal(long.length, 20);
  assert.ok(long.endsWith('…'));
});
