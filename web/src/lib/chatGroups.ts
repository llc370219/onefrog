import type { BotRow, ChatSession } from '../api';

/** A one-to-one chat with a single bot. Inbox and automation sessions belong to one bot too. */
export function isDirect(s: Pick<ChatSession, 'id' | 'options'>): boolean {
  return s.options?.mode === 'direct' || s.id.startsWith('inbox-') || s.id.startsWith('auto-');
}

/** Splits sessions into group chats and each bot's own one-to-one history, keeping the newest first. */
export function groupSessions(sessions: ChatSession[]): { groups: ChatSession[]; byBot: Record<string, ChatSession[]> } {
  const groups: ChatSession[] = [];
  const byBot: Record<string, ChatSession[]> = {};
  for (const s of sessions) {
    if (!isDirect(s)) {
      groups.push(s);
      continue;
    }
    (byBot[s.bot_id] ||= []).push(s);
  }
  const newest = (a: ChatSession, b: ChatSession) => b.updated_at.localeCompare(a.updated_at);
  groups.sort(newest);
  for (const list of Object.values(byBot)) list.sort(newest);
  return { groups, byBot };
}

/** The bot's most recent one-to-one chat, which reopens when the user clicks that bot. */
export function latestDirect(sessions: ChatSession[], botId: string): ChatSession | undefined {
  return groupSessions(sessions).byBot[botId]?.[0];
}

/** Maps a timeline speaker (stored by name) back to its bot. */
export function speakerOf(bots: BotRow[], name?: string): BotRow | undefined {
  if (!name) return undefined;
  return bots.find(b => b.name === name) || bots.find(b => b.id === name);
}

/** Bots taking part in a group chat: every enabled bot except the excluded ones, lead first. */
export function groupMembers(bots: BotRow[], lead: string, excluded: string[] = []): BotRow[] {
  return bots
    .filter(b => b.enabled && (b.id === lead || !excluded.includes(b.id)))
    .sort((a, b) => Number(b.id === lead) - Number(a.id === lead));
}
