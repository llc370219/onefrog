import { useState } from 'react';
import { Check, CircleHelp } from 'lucide-react';
import type { Choice } from '../api';
import { Button } from './ui';

/** Text the operator "says" by answering; mirrors the server's wording. */
export function choiceAnswerText(choice: Choice, values: string[], custom: string) {
  const labels = choice.options.filter(o => values.includes(o.value)).map(o => o.label);
  let text = labels.length ? '我的选择：' + labels.join('、') : '';
  if (custom.trim()) text += (text ? '\n补充：' : '') + custom.trim();
  return text;
}

/** Card for an ask_user question. Labels are rendered as plain text. */
export default function ChoiceCard({ choice, disabled, stale, onAnswer }: { choice: Choice; disabled?: boolean; stale?: boolean; onAnswer: (values: string[], custom: string) => void }) {
  const [picked, setPicked] = useState<string[]>([]);
  const [custom, setCustom] = useState('');
  const answer = choice.answer;
  const multi = !!choice.multi_select;
  const skipped = !answer && !!stale;
  const locked = !!answer || !!disabled || skipped;
  const chosen = answer?.values || picked;
  const toggle = (value: string) => {
    if (locked) return;
    if (!multi && !choice.allow_custom) { onAnswer([value], ''); return; }
    setPicked(cur => multi ? (cur.includes(value) ? cur.filter(v => v !== value) : [...cur, value]) : (cur[0] === value ? [] : [value]));
  };
  const ready = picked.length > 0 || (!!choice.allow_custom && custom.trim().length > 0);
  const needsSubmit = multi || !!choice.allow_custom;
  return <div className={'choice-card' + (answer || skipped ? ' answered' : '')} role="group" aria-label={choice.prompt}>
    <div className="choice-prompt"><CircleHelp size={16} /><span>{choice.prompt}</span>{multi && !answer && <small>可多选</small>}</div>
    <div className="choice-options">{choice.options.map(o => {
      const on = chosen.includes(o.value);
      return <button type="button" key={o.value} className={on ? 'on' : ''} disabled={locked} aria-pressed={on} onClick={() => toggle(o.value)}>
        {multi ? <span className="choice-box">{on && <Check size={11} />}</span> : on ? <Check size={14} /> : null}<span>{o.label}</span>
      </button>;
    })}</div>
    {answer ? <>{answer.custom && <p className="choice-custom-answer"><Check size={13} /><span>{answer.custom}</span></p>}<p className="choice-done">已回答</p></>
      : skipped ? <p className="choice-done">已跳过，对话已继续</p>
      : needsSubmit && <form className="choice-submit" onSubmit={e => { e.preventDefault(); if (ready && !locked) onAnswer(picked, choice.allow_custom ? custom : ''); }}>
        {choice.allow_custom && <input value={custom} maxLength={500} disabled={locked} onChange={e => setCustom(e.target.value)} placeholder="其他想法（可选）" aria-label="自定义回答" />}
        <Button type="submit" variant="primary" disabled={!ready || locked}>提交选择</Button>
      </form>}
  </div>;
}
