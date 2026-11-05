import type { BotRow } from '../api';

// 224 black-line doodle avatars in web/public/assets/bot-avatars:
// bean figures (001-096), 旺 cat, cat, frog and ghost (32 each).
export const BOT_AVATAR_COUNT = 224;

const avatarURL = (n: number) => `/assets/bot-avatars/avatar-${String(n + 1).padStart(3, '0')}.webp`;

// FNV-1a: a stable pseudo-random slot per bot id, so an avatar survives reloads
// without storing anything on the server.
function slot(id: string): number {
  let h = 0x811c9dc5;
  for (let i = 0; i < id.length; i++) h = Math.imul(h ^ id.charCodeAt(i), 0x01000193);
  return (h >>> 0) % BOT_AVATAR_COUNT;
}

const cache = new WeakMap<BotRow[], Map<string, string>>();

// Bots arrive ordered by creation time. Each takes its hashed slot, or the next
// free one on a collision, so up to 224 bots never share an avatar and adding a
// bot never changes the avatars of older ones.
function assign(bots: BotRow[]): Map<string, string> {
  let map = cache.get(bots);
  if (map) return map;
  map = new Map();
  const used = new Set<number>();
  for (const b of bots) {
    let n = slot(b.id);
    if (used.size < BOT_AVATAR_COUNT) while (used.has(n)) n = (n + 1) % BOT_AVATAR_COUNT;
    used.add(n);
    map.set(b.id, avatarURL(n));
  }
  cache.set(bots, map);
  return map;
}

export function botAvatar(bots: BotRow[], id: string): string {
  return assign(bots).get(id) || avatarURL(slot(id));
}
