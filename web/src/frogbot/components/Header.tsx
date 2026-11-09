import { Bell, BellOff } from 'lucide-react';
export default function Header({ name, status, working, demo, connected, notify, onNotify }: { name: string; status: string; working: boolean; demo: boolean; connected: boolean; notify: boolean; onNotify: () => void }) {
  return (
    <header className="sticky top-0 z-20 border-b border-line bg-canvas/85 backdrop-blur-md">
      <div className="mx-auto flex h-14 max-w-3xl items-center gap-3 px-4">
        <Avatar size={34} />
        <div className="min-w-0 flex-1 leading-tight">
          <div className="flex items-center gap-2">
            <span className="truncate font-serif text-[17px] font-semibold tracking-tight">{name}</span>
            {demo && <span className="rounded-full border border-warn/40 bg-warn-soft px-2 py-px text-[11px] font-medium text-warn">演示 · 假模型</span>}
          </div>
          <div className="flex items-center gap-1.5 text-xs text-fg3">
            <span className={`inline-block size-1.5 rounded-full ${!connected ? 'bg-danger' : working ? 'bg-accent working-dot' : 'bg-ok'}`} />
            <span className="truncate">{connected ? status : '连接中断，正在重新连接…'}</span>
          </div>
        </div>
        <button onClick={onNotify} className="grid size-9 place-items-center rounded-full text-fg3 transition hover:bg-s2 hover:text-fg"
          title={notify ? '页面不在前台时会弹系统通知（点一下关闭）' : '开启系统通知：页面不在前台时，任务完成或需要确认会提醒你'} aria-label={notify ? '关闭系统通知' : '开启系统通知'} aria-pressed={notify}>
          {notify ? <Bell size={17} /> : <BellOff size={17} />}
        </button>
      </div>
    </header>
  );
}

export function Avatar({ size = 28 }: { size?: number }) {
  return (
    <span className="grid shrink-0 place-items-center overflow-hidden rounded-full bg-white ring-1 ring-line2" style={{ width: size, height: size }}>
      <img src="/frog-bot.png" alt="" className="size-[88%] object-contain" />
    </span>
  );
}
