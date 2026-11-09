import { Check, X } from 'lucide-react';
import { useState } from 'react';
import { useActiveBot, type Card } from '../api';

export default function OptionCard({ card }: { card: Card }) {
  const { api } = useActiveBot();
  const [busy, setBusy] = useState(false);
  const pending = card.status === 'pending';
  const act = async (fn: () => Promise<unknown>) => { setBusy(true); try { await fn(); } finally { setBusy(false); } };
  return (
    <div className={`rise rounded-2xl border bg-s1 p-4 ${pending ? 'border-accent/30' : 'border-line opacity-80'}`}>
      <div className="flex items-start gap-2">
        <p className="flex-1 text-[15px] font-medium">{card.question}</p>
        {pending && (
          <button disabled={busy} onClick={() => act(() => api.dismiss(card.id))} title="关闭（不选择）" aria-label="关闭" className="-mr-1 -mt-1 grid size-7 place-items-center rounded-full text-fg4 hover:bg-s2 hover:text-fg"><X size={15} /></button>
        )}
      </div>
      <div className="mt-3 flex flex-wrap gap-2">
        {card.options.map((o) => {
          const chosen = card.answer === o;
          return (
            <button key={o} disabled={!pending || busy} onClick={() => act(() => api.answer(card.id, o))}
              className={`inline-flex items-center gap-1.5 rounded-full border px-3.5 py-1.5 text-[13px] transition ${chosen ? 'border-accent bg-accent-soft text-accent' : pending ? 'border-line2 text-fg2 hover:border-accent hover:text-accent' : 'border-line text-fg4'}`}>
              {chosen && <Check size={13} />}{o}
            </button>
          );
        })}
      </div>
      {card.status === 'dismissed' && <p className="mt-2 text-xs text-fg4">已关闭，未选择</p>}
    </div>
  );
}
