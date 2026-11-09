import { Check, ShieldAlert, ShieldCheck, ShieldX, TimerOff } from 'lucide-react';
import { useState } from 'react';
import { useActiveBot, type Approval } from '../api';

const riskLabel: Record<string, string> = { danger: '高风险', exec: '中风险', write: '低风险', read: '只读' };
const toolLabel: Record<string, string> = { shell: '执行命令', run_onefrog_command: '在服务器上执行命令', run_command: '在服务器上执行命令', inspect_services: '查看服务器服务', connect_server: '接入服务器', install_probe: '安装探针', panel: '调整面板', manage_modules: '管理模块', manage_module_record: '维护模块数据', generate_secret: '生成密钥', schedule_create: '创建定时计划', schedule_pause: '暂停定时计划', schedule_resume: '恢复定时计划', schedule_delete: '删除定时计划', schedule_update: '修改定时计划', write_file: '写入文件', edit_file: '修改文件', read_file: '读取文件', list_dir: '查看目录', grep: '搜索', delete_secret: '删除密钥' };

// refs 把 {{secret:NAME}} 占位符高亮出来：卡片上永远只显示名字，不显示值。
function refs(text: unknown) {
  return String(text ?? '').split(/(\{\{secret:[A-Z][A-Z0-9_]*\}\})/).map((p, i) =>
    p.startsWith('{{secret:') ? <span key={i} className="secret-ref" title="密钥：执行时才替换成真实值">🔑{p.slice(9, -2)}</span> : p);
}

// Detail 把"到底要做什么"原样展示出来：命令、文件路径和内容、修改前后。
function Detail({ a }: { a: Approval }) {
  const g = a.args as Record<string, string>;
  const box = 'mt-3 max-h-64 overflow-auto rounded-xl border border-line bg-code px-3.5 py-2.5 font-mono text-[12.5px] leading-relaxed whitespace-pre-wrap break-all';
  if (a.tool_name === 'run_onefrog_command') return <pre className={box}><span className="select-none text-fg4">服务器 {g.server_id}{'\n'}$ </span>{refs(g.command)}</pre>;
  if (a.once_only) {
    // OneFrog 的审批：目标服务器、要执行的内容和触发确认的原因
    const reasons = Array.isArray(a.args.reasons) ? (a.args.reasons as string[]) : [];
    return (
      <>
        <pre className={box}><span className="select-none text-fg4">{g.server || g.server_id}{g.server && g.server_id && g.server !== g.server_id ? `（${g.server_id}）` : ''}{'\n'}$ </span>{refs(g.command)}</pre>
        {reasons.length > 0 && <ul className="mt-2 list-disc space-y-0.5 pl-5 text-[12.5px] text-fg3">{reasons.map((r, i) => <li key={i}>{r}</li>)}</ul>}
      </>
    );
  }
  if (a.tool_name === 'shell') return <pre className={box}><span className="select-none text-fg4">$ </span>{refs(g.command)}</pre>;
  if (a.tool_name === 'delete_secret') return <pre className={box}>删除密钥 <span className="secret-ref">{g.name}</span>（删除后无法恢复）</pre>;
  if (a.tool_name === 'edit_file') return (
    <div className={box}>
      <div className="mb-1 text-fg3">{g.path}</div>
      {String(g.old_string ?? '').split('\n').map((l, i) => <div key={'o' + i} className="text-danger">- {l}</div>)}
      {String(g.new_string ?? '').split('\n').map((l, i) => <div key={'n' + i} className="text-ok">+ {l}</div>)}
    </div>
  );
  if (a.tool_name === 'write_file') return (
    <div className={box}><div className="mb-1 text-fg3">{g.path}</div>{refs(String(g.content ?? '').split('\n').slice(0, 14).join('\n'))}</div>
  );
  return <pre className={box}>{JSON.stringify(a.args, null, 2)}</pre>;
}

export default function ApprovalCard({ a, taskTitle, fromChat }: { a: Approval; taskTitle?: string; fromChat?: boolean }) {
  const { api } = useActiveBot();
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState('');
  const decide = async (d: 'once' | 'session' | 'deny') => {
    setBusy(true); setErr('');
    try { await api.decide(a.id, d); } catch (e) { setErr((e as Error).message); setBusy(false); }
  };

  if (a.status !== 'pending') {
    const done = {
      allow: { icon: <ShieldCheck size={15} />, tone: 'text-ok', text: a.scope === 'session' ? '已批准（本任务内相同操作）' : '已批准一次' },
      deny: { icon: <ShieldX size={15} />, tone: 'text-danger', text: '已拒绝' },
      expired: { icon: <TimerOff size={15} />, tone: 'text-fg3', text: '已失效' },
    }[a.status];
    return (
      <div className="rise flex items-center gap-2 rounded-xl border border-line bg-s1/60 px-3.5 py-2 text-[13px]">
        <span className={done.tone}>{done.icon}</span>
        <span className={done.tone}>{done.text}</span>
        <code className="min-w-0 flex-1 truncate font-mono text-[12px] text-fg3">{toolLabel[a.tool_name] || a.tool_name} · {(a.args as Record<string, string>).command || (a.args as Record<string, string>).path}</code>
      </div>
    );
  }

  return (
    <div className="rise rounded-2xl border border-warn/35 bg-s1 p-4 shadow-[0_6px_24px_-12px_var(--shadow)]" role="region" aria-label="需要你的批准">
      <div className="flex items-start gap-3">
        <span className="grid size-8 shrink-0 place-items-center rounded-full bg-warn-soft text-warn"><ShieldAlert size={17} /></span>
        <div className="min-w-0 flex-1">
          <div className="flex flex-wrap items-center gap-2">
            <span className="text-[15px] font-semibold">需要你的批准</span>
            <span className="rounded-full bg-warn-soft px-2 py-px text-[11px] font-medium text-warn">{riskLabel[a.risk] || a.risk}</span>
          </div>
          <p className="mt-0.5 text-[13px] text-fg3">{taskTitle ? `任务「${taskTitle}」想要${toolLabel[a.tool_name] || a.tool_name}` : fromChat ? `我想直接${toolLabel[a.tool_name] || a.tool_name}` : toolLabel[a.tool_name]}{a.reason && !a.once_only ? `：${a.reason}` : ''}</p>
        </div>
      </div>
      <Detail a={a} />
      <div className="mt-3.5 flex flex-wrap gap-2">
        <button disabled={busy} onClick={() => decide('once')} className="inline-flex items-center gap-1.5 rounded-full bg-accent px-4 py-2 text-[13px] font-medium text-on-accent transition hover:bg-accent-hover disabled:opacity-50"><Check size={14} />批准一次</button>
        {!fromChat && !a.once_only && a.tool_name !== 'run_onefrog_command' && <button disabled={busy} onClick={() => decide('session')} className="rounded-full border border-line2 px-4 py-2 text-[13px] font-medium text-fg2 transition hover:bg-s2 disabled:opacity-50">本任务内批准相同操作</button>}
        <button disabled={busy} onClick={() => decide('deny')} className="rounded-full px-4 py-2 text-[13px] font-medium text-danger transition hover:bg-danger-soft disabled:opacity-50">拒绝</button>
      </div>
      {err && <p className="mt-2 text-xs text-danger">{err}</p>}
      <p className="mt-2.5 text-[11.5px] text-fg4">只批准上面这一条具体操作；参数变了需要重新确认。不处理会在 10 分钟后自动拒绝。</p>
    </div>
  );
}
