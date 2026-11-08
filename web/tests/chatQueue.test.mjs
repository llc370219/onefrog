import { test } from 'node:test';
import assert from 'node:assert/strict';
import { chatQueue } from '../src/lib/chatQueue.ts';

const item = (id, status, fraction, extra = {}) => ({
  id, status, created_at: `2026-01-01T12:00:00${fraction}Z`, user_message_id: 'msg-' + id, ...extra,
});

test('an approval stays on the active turn while follow-ups retain arrival order', () => {
  const first = item('c', 'waiting_approval', '.123');
  const second = item('b', 'queued', '.123001');
  const third = item('a', 'queued', '.123002');
  const child = item('child', 'waiting_approval', '.123003', { parent_id: first.id });
  const result = chatQueue([third, second, child, first], [{ id: first.user_message_id }]);
  assert.equal(result.current, first);
  assert.deepEqual(result.currentTasks, [first, child]);
  assert.deepEqual(result.waiting, [second, third]);
  assert.equal(result.currentMessage, undefined);
});

test('accepted bubbles give way to the exact transcript message and cancelled queue entries disappear', () => {
  const first = item('first', 'completed', '');
  const next = item('next', 'running', '.0001');
  const removed = item('removed', 'cancelled', '.0002');
  const failed = item('failed', 'failed', '.0003');
  const tasks = [removed, next, first, failed];
  assert.equal(chatQueue(tasks, []).currentMessage, next);
  const result = chatQueue(tasks, [{ id: next.user_message_id }]);
  assert.equal(result.currentMessage, undefined);
  assert.deepEqual(result.waiting, []);
  assert.deepEqual(result.unsentErrors, [failed]);
});
