import {test} from 'node:test';
import assert from 'node:assert/strict';
import {
  activityDurationText, activityGlyph, activityLabel, elapsedAnchor, elapsedRunning,
  activityRender, activityRowTime, currentActivityText, formatDuration, formatElapsed,
  isTerminal, planProgressLabel, planStages, recentActivities, statusLine, taskBusyId,
  ACTIVITY_SHOWN,
} from '../src/lib/taskCardFormat.ts';

// The helpers only read a few fields; a plain object stands in for TaskView in
// this value-level .mjs test (type imports cannot be stripped in .mjs).
const task = (status, extra = {}) => ({
  id: 't1', bot_id: 'b', session_id: 's', goal: 'g', status,
  total_steps: 0, max_steps: 0, created_at: '2026-01-01T00:00:00Z', updated_at: '2026-01-01T00:00:00Z',
  ...extra,
});

const act = (extra = {}) => ({
  id: 'a1', name: 'run_command', label: '正在运行测试', status: 'completed',
  started_at: '2026-01-01T00:00:00Z', ...extra,
});

test('formatDuration renders compact Chinese units and never negative/invalid values', () => {
  assert.equal(formatDuration(400), '<1 秒');
  assert.equal(formatDuration(65_000), '1 分 5 秒');
  assert.equal(formatDuration(3_600_000), '1 小时 0 分');
  assert.equal(formatDuration(-5), '');
  assert.equal(formatDuration(Number.NaN), '');
});

test('formatElapsed computes from started_at and freezes at the anchor', () => {
  const start = '2026-01-01T00:00:00Z';
  assert.equal(formatElapsed(start, Date.parse(start) + 90_000), '1 分 30 秒');
  // Missing/invalid start never renders as NaN.
  assert.equal(formatElapsed(undefined, Date.now()), '');
  assert.equal(formatElapsed('not-a-date', Date.now()), '');
  assert.equal(formatElapsed(start, Number.NaN), '');
});

test('elapsed freezes on paused and terminal states (updated_at fallback), ticks otherwise', () => {
  const finished = '2026-01-01T00:10:00Z';
  assert.equal(elapsedAnchor(task('completed', {finished_at: finished})), finished);
  assert.equal(elapsedAnchor(task('failed', {finished_at: finished})), finished);
  assert.equal(elapsedAnchor(task('cancelled', {finished_at: finished})), finished);
  assert.equal(elapsedAnchor(task('budget_exhausted', {finished_at: finished})), finished);
  // Contract has no paused_at: the pause freeze anchor is updated_at.
  const pausedAt = '2026-01-01T00:05:00Z';
  assert.equal(elapsedAnchor(task('paused', {updated_at: pausedAt})), pausedAt);
  // Terminal without finished_at also freezes at updated_at, never wall clock.
  assert.equal(elapsedAnchor(task('completed')), task('completed').updated_at);
  assert.equal(elapsedAnchor(task('failed', {updated_at: pausedAt})), pausedAt);
  assert.equal(elapsedAnchor(task('running')), undefined);
  assert.equal(elapsedAnchor(task('waiting_user')), undefined, 'waiting tasks keep counting');
  assert.equal(isTerminal('completed'), true);
  assert.equal(isTerminal('running'), false);
  assert.equal(elapsedRunning('paused'), false);
  assert.equal(elapsedRunning('completed'), false);
  assert.equal(elapsedRunning('waiting_job'), true);
  assert.equal(elapsedRunning('queued'), true);
});

test('paused elapsed survives reload: anchor is updated_at, not the viewer clock', () => {
  // Simulates refresh/remount: a paused task is formatted with a fresh now()
  // far in the future — the frozen value must be unchanged.
  const started = '2026-01-01T00:00:00Z';
  const pausedAt = '2026-01-01T00:02:00Z';
  const t = task('paused', {started_at: started, updated_at: pausedAt});
  const before = formatElapsed(started, 0, elapsedAnchor(t));
  const after = formatElapsed(started, Date.parse('2026-01-01T01:00:00Z'), elapsedAnchor(t));
  assert.equal(before, '2 分');
  assert.equal(after, '2 分', 'remount/refresh must not resume ticking on a paused task');
  // Resume keeps counting from the real start again.
  const resumed = task('running', {started_at: started, updated_at: pausedAt});
  assert.equal(elapsedAnchor(resumed), undefined);
});

test('plan stages: numbered 1..N from real plan, completed prefix from plan_completed only', () => {
  const plan = ['确认告警来源', '检查磁盘占用', '汇总结论'];
  // 0 of 2 done… actually 0 completed: nothing bright.
  assert.deepEqual(planStages(plan, 0).map(s => s.done), [false, false, false]);
  // 2 completed prefix: first two bright, last pending.
  const two = planStages(plan, 2);
  assert.deepEqual(two.map(s => s.done), [true, true, false]);
  assert.deepEqual(two.map(s => s.index), [1, 2, 3]);
  assert.equal(two[1].text, '检查磁盘占用');
  // All N completed.
  assert.deepEqual(planStages(plan, 3).map(s => s.done), [true, true, true]);
  // Undefined completion means 0 — never fake progress.
  assert.deepEqual(planStages(plan, undefined).map(s => s.done), [false, false, false]);
  // Replan shrank the plan below the old count: clamp, never overflow.
  assert.deepEqual(planStages(plan, 9).map(s => s.done), [true, true, true]);
  // Negative and fractional counts never mark stages done.
  assert.deepEqual(planStages(plan, -1).map(s => s.done), [false, false, false]);
  assert.deepEqual(planStages(plan, 1.9).map(s => s.done), [true, false, false]);
  // Missing plan renders no stages (card shows 尚未生成计划 placeholder).
  assert.deepEqual(planStages(undefined, 2), []);
  assert.deepEqual(planStages([], 2), []);
  // Many steps keep their 1..N numbering.
  const many = planStages(Array.from({length: 12}, (_, i) => '步骤' + (i + 1)), 7);
  assert.equal(many.length, 12);
  assert.equal(many[6].done, true);
  assert.equal(many[7].done, false);
  assert.equal(many[11].index, 12);
  // Summary line.
  // Label above the strip: current = completed+1 clamped; done → 第 N 步.
  assert.equal(planProgressLabel(two), '第 3 步 / 共 3 步');
  assert.equal(planProgressLabel(planStages(plan, 0)), '第 1 步 / 共 3 步');
  assert.equal(planProgressLabel(planStages(plan, 3)), '第 3 步 / 共 3 步');
  assert.equal(planProgressLabel([]), '');
  // Step budgets are never a progress source.
  const budget = task('running', {total_steps: 7, max_steps: 32, plan});
  assert.deepEqual(planStages(budget.plan, budget.plan_completed).map(s => s.done), [false, false, false]);
});

test('activities: glyph mapping, safe labels, actual durations, running state', () => {
  assert.equal(activityGlyph('run_command'), 'terminal');
  assert.equal(activityGlyph('read_file'), 'file');
  assert.equal(activityGlyph('ask_user'), 'user');
  assert.equal(activityGlyph('workspace_edit'), 'network');
  assert.equal(activityGlyph('totally_unknown_tool'), 'tool');
  assert.equal(activityLabel({label: '正在运行测试', name: 'run_command'}), '正在运行测试');
  assert.equal(activityLabel({label: '  ', name: 'search_code'}), 'search_code');
  assert.equal(activityLabel({label: '', name: ''}), '工具活动');
  assert.equal(activityDurationText(act({status: 'running'})), '进行中');
  assert.equal(activityDurationText(act({status: 'interrupted', duration_ms: undefined})), '结果未知');
  assert.equal(activityDurationText(act({duration_ms: 4200})), '4 秒');
  assert.equal(activityDurationText(act({started_at: '2026-01-01T00:00:00Z', finished_at: '2026-01-01T00:01:00Z'})), '1 分');
  assert.equal(activityDurationText(act({started_at: '2026-01-01T00:00:00Z'})), '', 'no finish, no fabricated duration');
});

test('recentActivities shows only the last few, newest on top, and tolerates absent data', () => {
  const many = Array.from({length: 8}, (_, i) => act({id: 'a' + i}));
  const recent = recentActivities(many, ACTIVITY_SHOWN);
  assert.equal(recent.length, ACTIVITY_SHOWN);
  assert.equal(recent[0].id, 'a7', 'newest first');
  assert.equal(recentActivities(undefined).length, 0);
  assert.equal(recentActivities([]).length, 0);
});

test('status lines are plain OneFrog Chinese labels without invented detail', () => {
  assert.match(statusLine({status: 'queued'}), /排队/);
  assert.equal(statusLine({status: 'running'}), '正在执行');
  // statusLine never presents next prose as current execution; the headline
  // helper adds the 下一步 prefix when it does surface next.
  assert.equal(statusLine({status: 'running', next: '读取日志'}), '正在执行');
  assert.equal(statusLine({status: 'paused'}), '已暂停，时间停在暂停那一刻');
  assert.equal(statusLine({status: 'budget_exhausted'}), '步数预算用尽，任务没有完成');
});

test('running headline prefers the latest running activity label over stale next prose', () => {
  const acts = [
    {id: '1', name: 'read_file', label: '读取配置文件', status: 'completed', started_at: '2026-01-01T00:00:00Z'},
    {id: '2', name: 'run_command', label: '正在运行测试', status: 'running', started_at: '2026-01-01T00:01:00Z'},
  ];
  assert.equal(currentActivityText({status: 'running', next: '下一步：清理日志'}, acts), '正在运行测试');
  // No running activity: next prose is planning, shown prefixed — never as the
  // current action.
  assert.equal(currentActivityText({status: 'running', next: '清理日志'}, []), '下一步：清理日志');
  assert.equal(currentActivityText({status: 'running', next: '下一步：清理日志'}, []), '下一步：下一步：清理日志');
  assert.equal(currentActivityText({status: 'running'}, []), '思考与规划中');
  // Non-running states go through statusLine; waiting_approval never shows tool prose.
  assert.equal(currentActivityText({status: 'waiting_approval', next: 'x'}, acts), '等待你批准一项操作');
});

test('activity rows stop animating and relabel when the parent is not running', () => {
  // Paused parent: a persisted running activity renders static and honest.
  const pausedRender = activityRender('paused');
  assert.deepEqual(pausedRender, {kind: 'stale', label: '已暂停'});
  assert.equal(activityRowTime({id: 'a', name: 'x', label: 'l', status: 'running', started_at: '2026-01-01T00:00:00Z'}, pausedRender), '已暂停');
  // Waiting parents get their own truthful labels.
  assert.equal(activityRender('waiting_approval').label, '等待审批');
  assert.equal(activityRender('waiting_user').label, '等待你回答');
  assert.equal(activityRender('waiting_job').label, '等待外部任务');
  // Terminal parents: the inflight row never claims it completed.
  assert.deepEqual(activityRender('completed'), {kind: 'stale', label: '结果待确认'});
  assert.deepEqual(activityRender('failed'), {kind: 'stale', label: '结果待确认'});
  // Running parent keeps the real spinner semantics and real duration.
  assert.deepEqual(activityRender('running'), {kind: 'active'});
  assert.equal(activityRowTime({id: 'a', name: 'x', label: 'l', status: 'running', started_at: '2026-01-01T00:00:00Z'}, activityRender('running')), '进行中');
  // Completed rows always show their actual duration regardless of parent.
  assert.equal(activityRowTime({id: 'a', name: 'x', label: 'l', status: 'completed', started_at: '2026-01-01T00:00:00Z', duration_ms: 4200}, pausedRender), '4 秒');
});

test('busy key scoping: any in-flight action disables all controls of that task only', () => {
  assert.equal(taskBusyId('t1:pause', 't1'), 't1');
  assert.equal(taskBusyId('t1:cancel', 't1'), 't1');
  assert.equal(taskBusyId('t1:apr-1', 't1'), 't1');
  assert.equal(taskBusyId('t1:guide', 't1'), 't1');
  // Another task's busy state never disables this one.
  assert.equal(taskBusyId('t2:pause', 't1'), undefined);
  assert.equal(taskBusyId(undefined, 't1'), undefined);
  // Prefix collisions cannot leak across ids.
  assert.equal(taskBusyId('t12:pause', 't1'), undefined);
});
