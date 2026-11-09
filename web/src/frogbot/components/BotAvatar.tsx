import type { BotProfile } from '../api';

// 头像和 OneFrog「Bot」页一致：由页面按 Bot 列表算好放进 profile.avatar。
const fallback = '/assets/bot-avatars/avatar-001.webp';

export const avatarOf = (bot: Pick<BotProfile, 'avatar'>) => bot.avatar || fallback;

export function BotAvatar({ bot, className = '', size = 28 }: { bot: Pick<BotProfile, 'avatar'>; className?: string; size?: number }) {
  return (
    <span
      className={`inline-grid place-items-center shrink-0 overflow-hidden rounded-xl bg-s1 ring-1 ring-line2 shadow-2xs ${className}`}
      style={{ width: size, height: size }}
    >
      <img src={avatarOf(bot)} alt="" loading="lazy" decoding="async" draggable={false} className="size-[84%] object-contain" />
    </span>
  );
}
