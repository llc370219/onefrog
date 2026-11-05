import type { BotRow } from '../api';
import { botAvatar } from '../lib/botAvatar';

export function BotAvatar({ bots, id, className = '' }: { bots: BotRow[]; id: string; className?: string }) {
  return <span className={`bot-avatar ${className}`.trim()}><img src={botAvatar(bots, id)} alt="" loading="lazy" decoding="async" draggable={false}/></span>;
}
