import { useEffect, useState } from 'react';
import type { Step, Task } from '../api';

// 共享的"当前时间"：每秒刷新一次（只有需要时才开定时器）
export function useNow(active = true, every = 1000) {
  const [now, setNow] = useState(Date.now());
  useEffect(() => { if (!active) return; setNow(Date.now()); const t = setInterval(() => setNow(Date.now()), every); return () => clearInterval(t); }, [active, every]);
  return now;
}

export const dur = (ms: number) => {
  const s = Math.max(0, Math.round(ms / 1000));
  if (s < 60) return `${s} 秒`;
  const m = Math.floor(s / 60);
  if (m < 60) return `${m} 分 ${s % 60} 秒`;
  return `${Math.floor(m / 60)} 小时 ${m % 60} 分`;
};
export const clock = (ms: number) => { const s = Math.max(0, Math.floor(ms / 1000)); const m = Math.floor(s / 60); return `${m}:${String(s % 60).padStart(2, '0')}`; };
export const ago = (ms: number) => (ms < 60_000 ? `${Math.max(1, Math.round(ms / 1000))} 秒前` : `${Math.round(ms / 60_000)} 分钟前`);
export const base = (p: string) => p.split('/').filter(Boolean).pop() || p;
export const sizeOf = (n: number) => (n < 1024 ? `${n} B` : n < 1 << 20 ? `${(n / 1024).toFixed(0)} KB` : `${(n / (1 << 20)).toFixed(1)} MB`);

export function localTime(iso: string | number) {
  const d = new Date(iso);
  const today = new Date();
  const same = d.toDateString() === today.toDateString();
  return d.toLocaleString('zh-CN', same ? { hour: '2-digit', minute: '2-digit' } : { month: 'numeric', day: 'numeric', hour: '2-digit', minute: '2-digit' });
}

// 把一次工具调用翻译成人话，例如 "读取 README.md"、"运行测试"
export function describe(tool: string, args: Record<string, unknown>): { verb: string; target: string } {
  const a = args as Record<string, string>;
  switch (tool) {
    case 'read_file': return { verb: '读取', target: base(a.path || '') };
    case 'write_file': return { verb: '写入', target: base(a.path || '') };
    case 'edit_file': return { verb: '修改', target: base(a.path || '') };
    case 'list_dir': return { verb: '查看目录', target: a.path && a.path !== '.' ? a.path : '工作区' };
    case 'grep': return { verb: '搜索', target: `「${a.pattern || ''}」` };
    case 'shell': {
      const c = (a.command || '').trim();
      const kinds: [RegExp, string][] = [
        [/\b(go test|npm (run )?test|pytest|cargo test|jest|vitest)\b/, '运行测试'],
        [/\b(go build|npm run build|make|cargo build|tsc)\b/, '构建项目'],
        [/\b(go run|node|python3?|\.\/)\S*/, '运行程序'],
        [/\b(npm|pnpm|yarn|pip3?|go get|apt(-get)?) (i|install|add)\b/, '安装依赖'],
        [/^git\b/, 'Git 操作'],
        [/\b(curl|wget)\b/, '访问网络'],
        [/^(ls|tree|find)\b/, '查看文件'],
        [/^(cat|head|tail|less|wc)\b/, '查看内容'],
        [/^(rm|mv|cp|mkdir)\b/, '整理文件'],
      ];
      const k = kinds.find(([re]) => re.test(c));
      return { verb: k ? k[1] : '执行命令', target: c.length > 60 ? c.slice(0, 57) + '…' : c };
    }
    default: return { verb: tool, target: '' };
  }
}

export const stepText = (s: Step) => { const d = describe(s.tool, s.args); return `${d.verb}${d.target && s.tool !== 'shell' ? ' ' + d.target : ''}`; };

// 任务"已经干了多久"：之前各段累计 + 当前这段
export const elapsedOf = (t: Task, now: number) => (t.duration ?? t.activeMs + (t.started ? now - t.started : 0));

export function nowDoing(t: Task) {
  if (t.awaiting > 0) return '等你确认一个操作';
  if (t.pausing) return '正在暂停（做完这一步就停）';
  const cur = [...t.steps].reverse().find((s) => !s.done);
  if (cur) return '正在' + stepText(cur);
  if (t.text) return '正在整理结果';
  if (t.thinking) return '正在思考下一步';
  return t.steps.length ? '正在想下一步' : '正在看要做什么';
}
