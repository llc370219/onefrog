import { Plus, Settings as SettingsIcon } from 'lucide-react';
import { BotAvatar } from './BotAvatar';

export type BotItem = {
  id: string;
  name: string;
  avatarSlot: number;
};

// 严格按照图一的 Bot 列表，图标使用 onefrog bot 库
export const DEFAULT_BOTS: BotItem[] = [
  { id: 'grok-ref', name: 'Grok Bot项目借鉴', avatarSlot: 0 },
  { id: 'onefrog', name: 'Onefrog', avatarSlot: 1 },
  { id: 'relay', name: '中转站', avatarSlot: 2 },
  { id: 'frog-co-im', name: '一只青蛙frog.co.im管理', avatarSlot: 3 },
  { id: 'server-mgmt', name: '服务器管理', avatarSlot: 4 },
  { id: 'grok-bot', name: 'Grok Bot', avatarSlot: 5 },
];

export default function Sidebar({
  activeBotId,
  onSelectBot,
  onOpenSettings,
  onNewChat,
}: {
  activeBotId: string;
  onSelectBot: (id: string, name: string) => void;
  onOpenSettings: () => void;
  onNewChat?: () => void;
}) {
  return (
    <aside
      className="flex h-full w-64 shrink-0 flex-col border-r border-[#e3e1dc] dark:border-[#2a2926] bg-[#f5f3f2] dark:bg-[#1c1b1a] select-none text-fg"
      style={{ width: '256px' }}
      aria-label="Bots 状态栏"
    >
      {/* 顶部 Bots 标题与 + 号，完全对齐图一样式 */}
      <div className="flex items-center justify-between px-3.5 pt-3.5 pb-2 text-[13px] font-medium text-fg4">
        <span>Bots</span>
        <button
          type="button"
          onClick={onNewChat}
          className="grid size-6 place-items-center rounded-md text-fg4 hover:text-fg hover:bg-black/5 dark:hover:bg-white/10 transition"
          title="新建 Bot 对话"
          aria-label="新建"
        >
          <Plus size={15} />
        </button>
      </div>

      {/* 仅保留 Bot 列表 */}
      <div className="flex-1 overflow-y-auto px-2 space-y-0.5 scrollbar-thin">
        {DEFAULT_BOTS.map((b) => {
          const active = activeBotId === b.id;
          return (
            <button
              key={b.id}
              onClick={() => onSelectBot(b.id, b.name)}
              className={`flex items-center gap-3 w-full px-2.5 py-2 rounded-2xl text-left text-[14px] transition ${
                active
                  ? 'bg-[#ebe9e9] dark:bg-white/10 font-medium text-fg shadow-2xs'
                  : 'text-fg2 hover:bg-black/4 dark:hover:bg-white/5 hover:text-fg'
              }`}
              aria-current={active ? 'true' : undefined}
            >
              <BotAvatar id={b.id} slotIndex={b.avatarSlot} size={28} />
              <span className="truncate flex-1 tracking-tight">{b.name}</span>
            </button>
          );
        })}
      </div>

      {/* 左下角设置：无分割线，图标与文字微调小更精致 */}
      <div className="px-2 pb-3 pt-1">
        <button
          type="button"
          onClick={onOpenSettings}
          className="group flex items-center gap-2.5 w-full px-2.5 py-1.5 rounded-xl text-left text-[12.5px] text-fg3 hover:bg-black/5 dark:hover:bg-white/10 hover:text-fg transition duration-150"
          title="设置"
          aria-label="设置"
        >
          <SettingsIcon
            size={15}
            className="text-fg4 group-hover:text-fg transition-colors duration-150 shrink-0"
            strokeWidth={1.7}
          />
          <span className="font-normal tracking-tight">设置</span>
        </button>
      </div>
    </aside>
  );
}
