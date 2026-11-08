import { useState } from 'react';
import { Check } from 'lucide-react';
import type { Choice } from '../api';
import { Button } from './ui';

/** Text the operator "says" by answering; mirrors the server's wording. */
export function choiceAnswerText(choice: Choice, values: string[], custom: string) {
  const labels = choice.options.filter(o => values.includes(o.value)).map(o => o.label);
  let text = labels.length ? '我的选择：' + labels.join('、') : '';
  if (custom.trim()) text += (text ? '\n补充：' : '') + custom.trim();
  return text;
}

/** Grok-style lettered option card (A / B / C rows with right checkmark). */
export default function ChoiceCard({ choice, disabled, stale, onAnswer }: { choice: Choice; disabled?: boolean; stale?: boolean; onAnswer: (values: string[], custom: string) => void }) {
  const [picked, setPicked] = useState<string[]>([]);
  const [custom, setCustom] = useState('');
  const answer = choice.answer;
  const multi = !!choice.multi_select;
  const skipped = !answer && !!stale;
  const locked = !!answer || !!disabled || skipped;
  const chosen = answer?.values || picked;

  const chooseOption = (value: string) => {
    if (locked) return;
    if (!multi) {
      setPicked([value]);
      onAnswer([value], '');
      return;
    }
    setPicked(cur => (cur.includes(value) ? cur.filter(v => v !== value) : [...cur, value]));
  };

  const canSubmitCustomOrMulti = (multi && picked.length > 0) || (!!choice.allow_custom && custom.trim().length > 0);

  return (
    <div className={`grok-card choice-inline-card${answer || skipped ? ' answered' : ''}`} role="group" aria-label={choice.prompt}>
      <div className="grok-card-head">
        <strong>{choice.prompt}</strong>
        {multi && !answer && <span className="grok-pill muted">可多选</span>}
        {skipped && <span className="grok-pill muted">已跳过</span>}
      </div>
      <div className="choice-rows">
        {(choice.options || []).map((o, idx) => {
          const on = chosen.includes(o.value);
          const letter = String.fromCharCode(65 + idx);
          return (
            <button
              type="button"
              key={o.value}
              className={`choice-row${on ? ' on' : ''}`}
              disabled={locked}
              aria-pressed={on}
              onClick={() => chooseOption(o.value)}
            >
              <span className="choice-letter">{letter}</span>
              <span className="choice-label">{o.label}</span>
              {on && <Check size={16} className="choice-check"/>}
            </button>
          );
        })}
      </div>
      {answer?.custom && (
        <div className="choice-row on custom-answered">
          <span className="choice-letter">✎</span>
          <span className="choice-label">{answer.custom}</span>
          <Check size={16} className="choice-check"/>
        </div>
      )}
      {!answer && !skipped && (multi || choice.allow_custom) && (
        <form
          className="choice-submit"
          onSubmit={e => {
            e.preventDefault();
            if (canSubmitCustomOrMulti && !locked) {
              onAnswer(picked, choice.allow_custom ? custom : '');
            }
          }}
        >
          {choice.allow_custom && (
            <input
              value={custom}
              maxLength={500}
              disabled={locked}
              onChange={e => setCustom(e.target.value)}
              placeholder={multi ? '补充说明（可选）' : '或者直接输入你的想法，回车确认…'}
              aria-label="自定义回答"
            />
          )}
          {(multi || custom.trim().length > 0) && (
            <Button type="submit" variant="primary" disabled={!canSubmitCustomOrMulti || locked}>
              {multi ? '确认选择' : '发送'}
            </Button>
          )}
        </form>
      )}
    </div>
  );
}
