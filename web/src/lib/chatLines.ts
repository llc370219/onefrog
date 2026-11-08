import type { ChatLine } from '../api';

export type MessageRow = {
  id: string;
  role: string;
  content: string;
  from_bot?: string;
  attachments?: ChatLine['attachments'];
  choice?: ChatLine['choice'];
  secret_request?: ChatLine['secret'];
  timeline?: ChatLine['timeline'];
};

/**
 * Turns stored messages into chat lines, hiding the automatic follow-ups sent
 * after a choice or secret card. Shared by the chat, dashboard and develop
 * views so every screen reads the same durable transcript.
 */
export function chatLines(rows: MessageRow[]): ChatLine[] {
  const visible = rows.filter((r, idx) => {
    if (r.role !== 'user' && r.role !== 'assistant') return false;
    if (r.role === 'user' && idx > 0) {
      const prev = rows[idx - 1];
      const autoChoice = Boolean(prev.role === 'assistant' && prev.choice?.answer && r.content.startsWith('我的选择：'));
      const autoSecret = Boolean(
        prev.role === 'assistant' &&
          prev.secret_request?.provided_at &&
          r.content.startsWith('已把密钥「') &&
          r.content.endsWith('模型看不到明文。')
      );
      if (autoChoice || autoSecret) return false;
    }
    return true;
  });
  // Inbox messages carry a "[来自 X（id=…）] " prefix for the model; the UI shows a label instead.
  return visible.map(r => ({
    who: r.role === 'user' ? 'user' : 'bot',
    text: r.from_bot ? r.content.replace(/^\[来自 [^\]\n]*\] /, '') : r.content,
    attachments: r.attachments,
    id: r.id,
    fromBot: r.from_bot || undefined,
    choice: r.choice || undefined,
    secret: r.secret_request || undefined,
    timeline: r.timeline || undefined,
  }));
}
