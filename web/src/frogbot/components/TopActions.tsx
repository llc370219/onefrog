import { Bell, BellOff } from 'lucide-react';

// 嵌入 OneFrog：主题跟随面板（面板切换时由 main.tsx 同步），这里只保留通知开关。
export default function TopActions({ notify, onNotify }: { notify: boolean; onNotify: () => void }) {
  return (
    <div className="fixed top-3 right-4 z-40 flex items-center gap-1.5 rounded-full border border-line bg-canvas/80 px-2 py-1 shadow-xs backdrop-blur-md">
      <button
        onClick={onNotify}
        className="grid size-8 place-items-center rounded-full text-fg3 transition hover:bg-s2 hover:text-fg"
        title={notify ? '通知已开启（点击关闭）' : '开启系统通知'}
        aria-label={notify ? '关闭系统通知' : '开启系统通知'}
        aria-pressed={notify}
      >
        {notify ? <Bell size={16} /> : <BellOff size={16} />}
      </button>
    </div>
  );
}
