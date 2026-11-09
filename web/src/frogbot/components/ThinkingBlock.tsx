import { Brain, ChevronDown } from 'lucide-react';
import { useEffect, useLayoutEffect, useRef, useState } from 'react';

const secs = (ms: number) => Math.max(1, Math.round(ms / 1000));

// ThinkingBlock 显示聊天层的思考过程：
// 思考中 —— 展开、流式滚动、计时；开始回答后自动收起成「已思考 N 秒」，点一下可以再展开。
export default function ThinkingBlock({ text, live = false, startedAt, endedAt, ms }: {
  text: string; live?: boolean; startedAt?: number; endedAt?: number; ms?: number;
}) {
  const thinking = live && !endedAt;
  const [userOpen, setUserOpen] = useState<boolean | null>(null);
  const open = userOpen ?? thinking;
  const [now, setNow] = useState(Date.now());
  useEffect(() => {
    if (!thinking) return;
    const t = setInterval(() => setNow(Date.now()), 250);
    return () => clearInterval(t);
  }, [thinking]);
  useEffect(() => { if (!thinking) setUserOpen(null); }, [thinking]); // 思考结束：回到默认（收起）

  const body = useRef<HTMLDivElement>(null);
  const [overflow, setOverflow] = useState(false);
  useLayoutEffect(() => {
    const el = body.current;
    if (!el) return;
    if (thinking) el.scrollTop = el.scrollHeight;
    const o = el.scrollHeight > el.clientHeight + 2;
    if (o !== overflow) setOverflow(o);
  });

  const elapsed = ms ?? ((endedAt ?? now) - (startedAt ?? now));
  const label = thinking ? `思考中… ${secs(elapsed)} 秒` : `已思考 ${secs(elapsed)} 秒`;

  return (
    <div className="mb-2">
      <button onClick={() => setUserOpen(!open)} aria-expanded={open}
        className="group inline-flex items-center gap-1.5 rounded-full py-0.5 pr-1 text-[13px] text-fg3 transition hover:text-fg2">
        <Brain size={14} className={thinking ? 'pulse-dot text-accent' : 'text-fg4'} />
        <span className={thinking ? 'shimmer' : ''}>{label}</span>
        <ChevronDown size={14} className={`text-fg4 transition-transform ${open ? 'rotate-180' : ''}`} />
      </button>
      {open && text && (
        <div ref={body}
          className={`think-body mt-1.5 overflow-y-auto border-l-2 border-line2 pl-3 text-[13px] leading-relaxed whitespace-pre-wrap text-fg3 ${thinking ? 'max-h-36' : 'max-h-80'} ${thinking && overflow ? 'think-fade' : ''}`}>
          {text}{thinking && <span className="caret-inline" />}
        </div>
      )}
    </div>
  );
}
