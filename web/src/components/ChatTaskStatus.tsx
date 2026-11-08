import { CircleAlert, CircleCheck, CircleDashed, LoaderCircle, Pause } from 'lucide-react';
import type { ChatTaskEntry, CompactTone } from '../lib/chatTaskPresentation';

/** Tone icon for a collapsed one-line task status. */
function ToneIcon({ tone }: { tone: CompactTone }) {
  if (tone === 'running') return <LoaderCircle size={13} className="spin"/>;
  if (tone === 'ok') return <CircleCheck size={13}/>;
  if (tone === 'danger') return <CircleAlert size={13}/>;
  if (tone === 'warn') return <Pause size={12}/>;
  return <CircleDashed size={13}/>;
}

/**
 * One-line status rows for short chat turns that do not earn the big live card.
 * The transcript above already carries the reply (and the real error), so this
 * only states the durable task's outcome — nothing is rendered twice. Purely
 * presentational: polling, approvals and stop controls live elsewhere.
 */
export default function ChatTaskStatus({ entries }: { entries: ChatTaskEntry[] }) {
  if (!entries.length) return null;
  return (
    <ul className="chat-task-status" aria-label="本对话的短任务状态">
      {entries.map(({ task, compact }) => (
        <li className={`chat-task-status-row ${compact.tone}`} key={task.id} title={task.goal || undefined}>
          <span className="chat-task-status-icon" aria-hidden="true"><ToneIcon tone={compact.tone}/></span>
          <span className="chat-task-status-text">{compact.text}</span>
        </li>
      ))}
    </ul>
  );
}
