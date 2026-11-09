// 224 black-line doodle avatars in /assets/bot-avatars:
// bean figures (001-096), cat, frog and ghost (32 each).
export const BOT_AVATAR_COUNT = 224;

export const avatarURL = (n: number) =>
  `/assets/bot-avatars/avatar-${String(n + 1).padStart(3, '0')}.webp`;

export function slot(id: string): number {
  let h = 0x811c9dc5;
  for (let i = 0; i < id.length; i++) h = Math.imul(h ^ id.charCodeAt(i), 0x01000193);
  return (h >>> 0) % BOT_AVATAR_COUNT;
}

export function botAvatarById(id: string, fallbackSlot?: number): string {
  const n = fallbackSlot !== undefined ? fallbackSlot % BOT_AVATAR_COUNT : slot(id);
  return avatarURL(n);
}

export function BotAvatar({
  id,
  slotIndex,
  className = '',
  size = 28,
}: {
  id: string;
  slotIndex?: number;
  className?: string;
  size?: number;
}) {
  const src = botAvatarById(id, slotIndex);
  return (
    <span
      className={`inline-grid place-items-center shrink-0 overflow-hidden rounded-xl bg-white dark:bg-neutral-800 ring-1 ring-line2 shadow-2xs ${className}`}
      style={{ width: size, height: size }}
    >
      <img
        src={src}
        alt=""
        loading="lazy"
        decoding="async"
        draggable={false}
        className="size-[84%] object-contain"
      />
    </span>
  );
}
