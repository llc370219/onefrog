import { test } from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { nearChatBottom } from '../src/lib/chatScroll.ts';

test('a reader who scrolls up is outside the follow range', () => {
  const container = { scrollHeight: 2000, clientHeight: 500, scrollTop: 500 };
  assert.equal(nearChatBottom(container), false);
  assert.equal(nearChatBottom({ ...container, scrollTop: 1404 }), true);
  assert.equal(nearChatBottom({ ...container, scrollTop: 1403 }), false);
});

test('no overflow and a returned-to-bottom reader follow new activity', () => {
  assert.equal(nearChatBottom({ scrollTop: 0, scrollHeight: 300, clientHeight: 500 }), true);
  assert.equal(nearChatBottom({ scrollTop: 1500, scrollHeight: 2000, clientHeight: 500 }), true);
});

test('chat scroll is scoped to its container and not to document scrollIntoView', () => {
  const source = readFileSync(new URL('../src/pages/Chat.tsx', import.meta.url), 'utf8');
  assert.match(source, /followBottom.current = nearChatBottom/);
  assert.ok(source.includes('if (container && followBottom.current)'));
  assert.ok(source.includes('if (sending || uploading || !activeBot?.enabled'), 'a rejected send must not resume following');
  assert.ok(source.includes('}, [conversationKey]'), 'a different conversation should open at its latest message');
  assert.ok(source.includes("if (previous?.startsWith('new:') && session) return;"), 'a new chat gaining its session id is not a navigation');
  assert.doesNotMatch(source, /scrollIntoView/);
});

test('task polling keeps checking for the final assistant message after completion', () => {
  const source = readFileSync(new URL('../src/App.tsx', import.meta.url), 'utf8');
  assert.ok(source.includes('if(!sendingRef.current&&isCurrent())await loadTaskLines(sid);'));
  assert.ok(source.includes('if(key===linesKey.current&&!localCardAnswers.current.size)return;'));
});
