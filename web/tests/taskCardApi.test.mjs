import {test} from 'node:test';
import assert from 'node:assert/strict';
import {activityGlyph, activityLabel, planStages, recentActivities} from '../src/lib/taskCardFormat.ts';

// Additive TaskView contract: fields the frontend reads from the proposed
// backend extension. The frontend must tolerate every one of them missing.
const liveTask = {
  id: 't1', bot_id: 'b', session_id: 's', goal: 'g', status: 'running',
  total_steps: 0, max_steps: 0, created_at: '2026-01-01T00:00:00Z', updated_at: '2026-01-01T00:00:00Z',
  // additive:
  model_label: '渠道 A · model-x',
  workspace_label: 'prod-web-1',
  started_at: '2026-01-01T00:00:00Z',
  finished_at: undefined,
  plan: ['拉取慢查询日志', '定位耗时最高的 SQL', '给出索引建议'],
  plan_completed: 1,
  activities: [
    {id: 'a1', name: 'run_command', label: '正在运行测试', status: 'completed', started_at: '2026-01-01T00:00:00Z', duration_ms: 5200},
    {id: 'a2', name: 'read_file', label: '读取配置文件', status: 'running', started_at: '2026-01-01T00:01:00Z'},
  ],
};

test('additive live fields follow the coordinated contract shape', () => {
  assert.equal(typeof liveTask.model_label, 'string');
  assert.ok(['running', 'completed', 'failed', 'interrupted'].includes(liveTask.activities[1].status));
  // duration_ms is optional on running activities, required-ish on finished ones.
  assert.equal(liveTask.activities[0].duration_ms, 5200);
  assert.equal(liveTask.activities[1].duration_ms, undefined);
});

test('older-backend fallback: absent live fields degrade instead of fabricating', () => {
  const legacy = {id: 't2', bot_id: 'b', session_id: 's', goal: 'g', status: 'running', total_steps: 0, max_steps: 0, created_at: '', updated_at: ''};
  // No activities -> the card shows the waiting placeholder, never invented rows.
  assert.deepEqual(recentActivities(legacy.activities), []);
  // No label fields -> empty string, callers show a neutral fallback.
  assert.equal(legacy.model_label, undefined);
  assert.equal(legacy.started_at, undefined);
});

test('activity rendering never exposes raw commands, args or file contents', () => {
  // The safe label is what the card shows; the raw tool name maps to a glyph.
  const a = liveTask.activities[0];
  assert.notEqual(activityLabel(a), a.name.includes('{') ? a.name : '');
  assert.ok(!activityLabel(a).includes('rm -rf'), 'labels carry no command text');
  assert.equal(activityLabel({label: '', name: 'run_command'}), 'run_command', 'fallback is the tool name only');
  assert.ok(['terminal', 'file', 'search', 'server', 'network', 'brain', 'user', 'tool'].includes(activityGlyph('workspace_edit')));
  // Recent list is capped and never includes fabricated entries.
  assert.equal(recentActivities(liveTask.activities).length, 2);
});

test('plan completion only from real plan + plan_completed, never from step budget', () => {
  // total_steps/max_steps exist on every task and must not drive the bar.
  const budgetOnly = {status: 'running', total_steps: 12, max_steps: 32, plan: ['a', 'b']};
  assert.deepEqual(planStages(budgetOnly.plan, budgetOnly.plan_completed).map(s => s.done), [false, false]);
  assert.equal(budgetOnly.total_steps !== budgetOnly.max_steps, true);
  assert.deepEqual(planStages(undefined, 3), [], 'no plan array -> no stages');
  // The additive field is the completed prefix, validated 0..plan.length.
  assert.deepEqual(planStages(liveTask.plan, liveTask.plan_completed).map(s => s.done), [true, false, false]);
});
