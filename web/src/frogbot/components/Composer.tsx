import { ArrowUp, FileText, Loader2, Paperclip, Pencil, Square, Upload, X } from 'lucide-react';
import { useCallback, useEffect, useRef, useState } from 'react';
import { api, isImage, type Attachment } from '../api';
import { sizeOf } from '../lib/format';

type Pending = { key: string; name: string; size: number; mime: string; preview?: string; att?: Attachment; error?: string };
const LINE = 24, MAX_LINES = 8;

// 输入框：多行自动长高（最多 8 行），Enter 发送 / Shift+Enter 换行（输入法安全），
// 附件（按钮、粘贴图片、拖进来），草稿按机器人保存，↑ 编辑上一条，忙时发送键变停止键。
export default function Composer({ name, connected, chatBusy, working, lastUser, onSend, onEdit, onStopReply, onStopAll }: {
  name: string; connected: boolean; chatBusy: boolean; working: boolean;
  lastUser?: { id: string; text: string };
  onSend: (t: string, files: Attachment[]) => Promise<void>;
  onEdit: (id: string, t: string) => Promise<void>;
  onStopReply: () => void; onStopAll: () => void;
}) {
  const key = `frogbot-draft:${name}`;
  const [text, setText] = useState(() => localStorage.getItem(key) || '');
  const [files, setFiles] = useState<Pending[]>([]);
  const [editing, setEditing] = useState<string | null>(null);
  const [sending, setSending] = useState(false);
  const [drag, setDrag] = useState(false);
  const ref = useRef<HTMLTextAreaElement>(null);
  const picker = useRef<HTMLInputElement>(null);

  useEffect(() => { const t = localStorage.getItem(key); if (t && !text) setText(t); }, [key]); // eslint-disable-line react-hooks/exhaustive-deps
  useEffect(() => { if (editing) return; if (text) localStorage.setItem(key, text); else localStorage.removeItem(key); }, [text, key, editing]);
  useEffect(() => {
    const el = ref.current;
    if (!el) return;
    el.style.height = 'auto';
    el.style.height = Math.min(el.scrollHeight, LINE * MAX_LINES + 12) + 'px';
  }, [text]);

  const add = useCallback((list: FileList | File[]) => {
    for (const f of Array.from(list)) {
      const p: Pending = { key: Math.random().toString(36).slice(2), name: f.name || 'pasted.png', size: f.size, mime: f.type, preview: f.type.startsWith('image/') ? URL.createObjectURL(f) : undefined };
      setFiles((xs) => [...xs, p]);
      api.upload(f).then(
        (att) => setFiles((xs) => xs.map((x) => (x.key === p.key ? { ...x, att } : x))),
        (e) => setFiles((xs) => xs.map((x) => (x.key === p.key ? { ...x, error: (e as Error).message } : x))),
      );
    }
  }, []);

  // 整个页面都能拖文件进来
  useEffect(() => {
    let depth = 0;
    const has = (e: DragEvent) => Array.from(e.dataTransfer?.types || []).includes('Files');
    const enter = (e: DragEvent) => { if (has(e)) { depth++; setDrag(true); } };
    const leave = (e: DragEvent) => { if (has(e) && --depth <= 0) { depth = 0; setDrag(false); } };
    const over = (e: DragEvent) => { if (has(e)) e.preventDefault(); };
    const drop = (e: DragEvent) => { if (!has(e)) return; e.preventDefault(); depth = 0; setDrag(false); if (e.dataTransfer?.files.length) add(e.dataTransfer.files); ref.current?.focus(); };
    window.addEventListener('dragenter', enter); window.addEventListener('dragleave', leave);
    window.addEventListener('dragover', over); window.addEventListener('drop', drop);
    return () => { window.removeEventListener('dragenter', enter); window.removeEventListener('dragleave', leave); window.removeEventListener('dragover', over); window.removeEventListener('drop', drop); };
  }, [add]);

  const uploading = files.some((f) => !f.att && !f.error);
  const ready = files.filter((f) => f.att).map((f) => f.att!);
  const canSend = connected && !sending && !uploading && (text.trim().length > 0 || ready.length > 0);

  const send = async () => {
    if (!canSend) return;
    const t = text.trim();
    setSending(true);
    try {
      if (editing) await onEdit(editing, t);
      else await onSend(t, ready);
      setText(''); setFiles([]); setEditing(null); localStorage.removeItem(key);
    } catch { /* 错误由外层显示，草稿保留 */ } finally { setSending(false); ref.current?.focus(); }
  };
  const startEdit = () => { if (!lastUser) return; setEditing(lastUser.id); setText(lastUser.text); requestAnimationFrame(() => { const el = ref.current; if (el) { el.focus(); el.setSelectionRange(el.value.length, el.value.length); } }); };
  const cancelEdit = () => { setEditing(null); setText(localStorage.getItem(key) || ''); };

  useEffect(() => {
    const h = () => startEdit();
    window.addEventListener('frogbot:edit-last', h);
    return () => window.removeEventListener('frogbot:edit-last', h);
  });

  const stopMode = !canSend && !text.trim() && !files.length && (chatBusy || working);
  const placeholder = !connected ? '正在重新连接…' : working ? '任务在后台进行，可以继续和我说话…' : `给 ${name} 发消息，或交给它一件事…`;

  return (
    <div className="mx-auto w-full max-w-3xl px-3 pb-[max(10px,env(safe-area-inset-bottom))] sm:px-4">
      {drag && (
        <div className="pointer-events-none fixed inset-0 z-50 grid place-items-center bg-canvas/70 backdrop-blur-sm">
          <div className="flex flex-col items-center gap-2 rounded-3xl border-2 border-dashed border-accent/60 bg-s1 px-12 py-10 text-fg2">
            <Upload size={28} className="text-accent" /><span className="text-[15px] font-medium">松手即可添加附件</span><span className="text-xs text-fg4">文件会放进工作区的 uploads/ 目录</span>
          </div>
        </div>
      )}
      <div className={`rounded-[24px] border bg-composer shadow-[0_8px_30px_-16px_var(--shadow)] transition ${editing ? 'border-accent/50' : 'border-line2'} focus-within:border-accent/50 ${!connected ? 'opacity-70' : ''}`}>
        {editing && (
          <div className="flex items-center gap-2 border-b border-line px-4 py-1.5 text-[12px] text-fg3">
            <Pencil size={12} className="text-accent" />正在编辑上一条消息，发送后会替换它并重新回答
            <button onClick={cancelEdit} className="ml-auto rounded px-1.5 text-fg3 hover:text-fg">取消</button>
          </div>
        )}
        {files.length > 0 && (
          <ul className="flex flex-wrap gap-2 px-3 pt-3" aria-label="附件">
            {files.map((f) => (
              <li key={f.key} className={`group relative flex h-14 max-w-[220px] items-center gap-2 overflow-hidden rounded-xl border ${f.error ? 'border-danger/50' : 'border-line'} bg-s2 pr-7`}>
                {f.preview && isImage(f.name, f.mime) ? <img src={f.preview} alt="" className="h-full w-14 shrink-0 object-cover" /> : <span className="ml-2 grid size-9 shrink-0 place-items-center rounded-lg bg-s1 text-fg3"><FileText size={16} /></span>}
                <div className={`min-w-0 leading-tight ${f.preview ? '' : ''}`}>
                  <div className="truncate text-[12.5px] font-medium text-fg">{f.att?.name || f.name}</div>
                  <div className={`truncate text-[11px] ${f.error ? 'text-danger' : 'text-fg4'}`}>{f.error ? '上传失败：' + f.error : f.att ? sizeOf(f.size) : <span className="inline-flex items-center gap-1"><Loader2 size={10} className="animate-spin" />上传中</span>}</div>
                </div>
                <button onClick={() => setFiles((xs) => xs.filter((x) => x.key !== f.key))} aria-label={`移除 ${f.name}`} className="absolute right-1 top-1 grid size-5 place-items-center rounded-full bg-canvas/80 text-fg3 hover:text-fg"><X size={12} /></button>
              </li>
            ))}
          </ul>
        )}
        <div className="flex items-end gap-1.5 p-2">
          <input ref={picker} type="file" multiple hidden onChange={(e) => { if (e.target.files) add(e.target.files); e.target.value = ''; }} />
          <button onClick={() => picker.current?.click()} disabled={!connected} aria-label="添加附件" title="添加附件（也可以粘贴或拖进来）" className="grid size-9 shrink-0 place-items-center rounded-full text-fg3 transition hover:bg-s2 hover:text-fg disabled:opacity-50">
            <Paperclip size={18} />
          </button>
          <textarea
            id="composer" ref={ref} rows={1} value={text} disabled={!connected}
            aria-label="消息输入框"
            onChange={(e) => setText(e.target.value)}
            onPaste={(e) => { const fs = Array.from(e.clipboardData.files); if (fs.length) { e.preventDefault(); add(fs); } }}
            onKeyDown={(e) => {
              if (e.nativeEvent.isComposing || e.keyCode === 229) return; // 输入法选词中，不当作发送
              if (e.key === 'Enter' && !e.shiftKey) { e.preventDefault(); send(); }
              else if (e.key === 'ArrowUp' && !text && !files.length && lastUser) { e.preventDefault(); startEdit(); }
              else if (e.key === 'Escape' && editing) { e.preventDefault(); e.stopPropagation(); cancelEdit(); }
            }}
            placeholder={placeholder}
            className="max-h-[204px] min-h-[24px] flex-1 resize-none bg-transparent px-1 py-1.5 text-[15px] leading-6 text-fg outline-none placeholder:text-fg4 disabled:cursor-not-allowed"
          />
          {stopMode ? (
            <button onClick={chatBusy ? onStopReply : onStopAll} aria-label={chatBusy ? '停止回复' : '停止所有任务'} title={chatBusy ? '停止回复（Esc）' : '停止所有任务'}
              className="grid size-9 shrink-0 place-items-center rounded-full bg-fg text-canvas transition hover:opacity-85">
              <Square size={12} fill="currentColor" />
            </button>
          ) : (
            <button onClick={send} disabled={!canSend} aria-label={editing ? '保存并重新发送' : '发送'} title="发送（Enter）"
              className="grid size-9 shrink-0 place-items-center rounded-full bg-accent text-on-accent transition hover:bg-accent-hover disabled:bg-s3 disabled:text-fg4">
              {sending ? <Loader2 size={16} className="animate-spin" /> : <ArrowUp size={18} />}
            </button>
          )}
        </div>
      </div>
      <p className="mt-1.5 hidden text-center text-[11px] text-fg4 sm:block">Enter 发送 · Shift+Enter 换行 · ↑ 编辑上一条 · Esc 停止回复 · Ctrl/⌘ K 聚焦输入框</p>
    </div>
  );
}
