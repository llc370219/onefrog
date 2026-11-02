import { useEffect, useRef } from 'react';
import { ArrowUp, Blocks, Bot, Code2, HardDrive, History, KeyRound, LoaderCircle, LockKeyhole, Plus, Server, ShieldCheck, Sparkles, Paperclip, Wallet } from 'lucide-react';
import type { ApprovalDecision, BotRow, ChatLine, Host, Attachment, SecretRow, SettingsState } from '../api';
import ChatControls, { type ChatControlState } from '../components/ChatControls';
import Attachments from '../components/Attachments';
import TurnStream from '../components/TurnStream';
import { Button, IconButton, Status } from '../components/ui';

export default function Chat({
  onAddServer,
  controls,
  onControls,
  settings,
  onBot,
  attachments,
  onFiles,
  onRemoveFile,
  uploading,
  activeBot,
  hosts,
  bots,
  secrets = [],
  selected,
  onSelect,
  draft,
  onDraft,
  lines,
  onSend,
  sending,
  onNew,
  onNotice,
  onAnswer,
  onProvide,
  onDecideApproval,
  onGoServer,
  onGoAsset,
  onGoHistory,
}: {
  onAddServer: () => void;
  controls: ChatControlState;
  onControls: (s: ChatControlState) => void;
  settings: SettingsState | null;
  onBot: (id: string) => void;
  attachments: Attachment[];
  onFiles: (files: File[]) => void;
  onRemoveFile: (id: string) => void;
  uploading: boolean;
  activeBot?: BotRow;
  hosts: Host[];
  bots: BotRow[];
  secrets?: SecretRow[];
  selected: string;
  onSelect: (id: string) => void;
  draft: string;
  onDraft: (s: string) => void;
  lines: ChatLine[];
  onSend: () => void;
  sending: boolean;
  onNew: () => void;
  onNotice: (s: string) => void;
  onAnswer: (line: ChatLine, values: string[], custom: string) => void;
  onProvide: (line: ChatLine, value: string) => void;
  onDecideApproval: (id: string, dec: ApprovalDecision) => void;
  onGoServer?: () => void;
  onGoAsset?: (host: Host) => void;
  onGoHistory?: (hostName: string) => void;
}) {
  const end = useRef<HTMLDivElement>(null);
  const fileInput = useRef<HTMLInputElement>(null);
  useEffect(() => {
    end.current?.scrollIntoView({ block: 'end', behavior: 'smooth' });
  }, [lines]);

  const current = hosts.find(h => h.id === selected);
  const isDevelop = activeBot?.id === 'develop' || activeBot?.name === '自定义模块' || activeBot?.name === '二次开发';
  const isModMgr = activeBot?.id === 'module-builder' || activeBot?.name === '模块管理' || activeBot?.name === '模块构建';
  const welcomeTitle = isDevelop ? '想创建什么自定义模块？' : isModMgr ? '管理模块与资产台账' : '今天，先处理什么？';
  const welcomeNote = isDevelop
    ? '描述你想要的交互卡片或小工具，自定义模块 Bot 会一步步编写界面并挂载到面板与模块目录。'
    : isModMgr
    ? '查看或开关面板模块、维护资产与订阅台账，或管理自定义模块里的数据记录。'
    : '直接把地址、账号或任务发给 Bot。遇到选项、加密凭据或高危操作时，会在对话中直接弹出交互卡片逐步确认。';

  const suggestions = isDevelop
    ? [
        { icon: Sparkles, title: '创建打卡模块', text: '创建一个打卡的模块，功能简单，一个卡片点击记述时间和备注，并显示最近记录。' },
        { icon: Code2, title: '创建运维待办清单', text: '创建一个运维待办清单模块，支持新增事项、勾选完成、删除条目，并显示完成进度。' },
        { icon: Blocks, title: '查看与改造模块', text: '列出当前已有的自定义模块，并说明每个模块的功能和记录数量。' },
      ]
    : isModMgr
    ? [
        { icon: Wallet, title: '盘点资产与到期', text: '请列出资产与订阅里即将到期的服务器，并说明剩余价值和是否自动续费。' },
        { icon: Blocks, title: '查看与开关模块', text: '列出当前面板上的全部内置模块和自定义模块，并说明哪些已在面板启用。' },
        { icon: ShieldCheck, title: '登记新服务器资费', text: '帮我在资产与订阅里登记一台服务器：\n名称：\n提供商：\n年付金额（元）：\n到期日：' },
      ]
    : [
        { icon: Server, title: '接入服务器', text: '接入这台服务器，登录成功后加入服务器页面：\n地址：\n端口：22\n用户：root\n密码：' },
        { icon: HardDrive, title: '分析磁盘占用', text: '查看当前服务器磁盘占用，分析占用最高的目录，不做删除。' },
        { icon: ShieldCheck, title: '查看可用能力', text: '介绍你和其他 Bot 的分工，暂时不要执行命令。' },
      ];

  async function copy(text: string) {
    try {
      await navigator.clipboard.writeText(text);
      onNotice('已复制回复');
    } catch {
      onNotice('复制失败，请手动选择文本');
    }
  }

  return (
    <div className="chat-layout">
      <div className="conversation">
        <header className="conversation-heading">
          <div>
            <span className="eyebrow">AI WORKSPACE</span>
            <h1>AI 助理 <span className="tag">{activeBot?.name || '主控'}</span></h1>
          </div>
          <Button onClick={onNew} disabled={sending}><Plus size={15}/>新对话</Button>
        </header>
        <div className="chat-scroll">
          {!lines.length ? (
            <div className="chat-welcome">
              <div className="welcome-icon"><Bot size={30} strokeWidth={1.3}/></div>
              <span className="eyebrow">{isDevelop ? 'CUSTOM MODULES' : isModMgr ? 'MODULE & ASSET MANAGER' : 'YOUR OPERATIONS COPILOT'}</span>
              <h2>{welcomeTitle}</h2>
              <p>{welcomeNote}</p>
              <div className="suggestions">
                {suggestions.map(({ icon: Icon, title, text }) => (
                  <button key={title} onClick={() => onDraft(text)}>
                    <Icon size={18} strokeWidth={1.4}/>
                    <strong>{title}</strong>
                    <span>填入指令<ArrowUp size={12}/></span>
                  </button>
                ))}
              </div>
            </div>
          ) : (
            <div className="messages">
              {lines.map((line, i) => (
                <article className={`message ${line.who}`} key={i}>
                  {line.who === 'user' ? (
                    <div className="user-message-content">
                      {line.text && <div className="user-text">{line.text}</div>}
                      <Attachments files={line.attachments || []}/>
                    </div>
                  ) : (
                    <div className="bot-turn">
                      <div className="speaker">
                        <Bot size={15}/>
                        <strong>{activeBot?.name || '主控'}</strong>
                      </div>
                      <TurnStream
                        timeline={line.timeline || []}
                        text={line.text}
                        pending={line.pending}
                        activeStatus={line.activeStatus}
                        hosts={hosts}
                        choice={line.choice}
                        secret={line.secret}
                        stale={i < lines.length - 1}
                        disabled={sending}
                        animateFinal={i === lines.length - 1 && !line.pending}
                        onDecideApproval={onDecideApproval}
                        onAnswerChoice={(values, custom) => onAnswer(line, values, custom)}
                        onProvideSecret={value => onProvide(line, value)}
                        onCopy={copy}
                      />
                    </div>
                  )}
                </article>
              ))}
            </div>
          )}
          <div ref={end}/>
        </div>
        <div className="composer-area">
          <ChatControls
            onAddServer={onAddServer}
            hosts={hosts}
            bots={bots}
            selected={selected}
            onServer={onSelect}
            botID={activeBot?.id || ''}
            onBot={onBot}
            state={controls}
            onChange={onControls}
            settings={settings}
            disabled={sending || uploading}
          />
          <form
            className="composer"
            onDragOver={e => e.preventDefault()}
            onDrop={e => {
              e.preventDefault();
              if (!sending && !uploading) onFiles(Array.from(e.dataTransfer.files));
            }}
            onSubmit={e => {
              e.preventDefault();
              onSend();
            }}
          >
            <Attachments files={attachments} onRemove={onRemoveFile} disabled={sending || uploading}/>
            <textarea
              aria-label="任务指令"
              placeholder="输入指令，按步骤交互对话或上传图片、配置、日志…"
              value={draft}
              onChange={e => onDraft(e.target.value)}
              onPaste={e => {
                const files = Array.from(e.clipboardData.files);
                if (files.length && !sending && !uploading) {
                  e.preventDefault();
                  onFiles(files);
                }
              }}
              onKeyDown={e => {
                if (e.key === 'Enter' && !e.shiftKey && !e.nativeEvent.isComposing) {
                  e.preventDefault();
                  onSend();
                }
              }}
              rows={3}
            />
            <div className="composer-tools">
              <input
                ref={fileInput}
                className="attachment-file-input"
                type="file"
                multiple
                aria-label="上传文件或图片"
                accept="image/png,image/jpeg,image/webp,application/pdf,.pdf,text/*,.md,.log,.json,.yaml,.yml,.toml,.ini,.conf,.csv,.go,.py,.js,.ts,.tsx,.sh,.sql,.xml"
                disabled={sending || uploading}
                onChange={e => {
                  const files = Array.from(e.target.files || []);
                  e.target.value = '';
                  onFiles(files);
                }}
              />
              <IconButton label="添加文件或图片" onClick={() => fileInput.current?.click()} disabled={sending || uploading}>
                {uploading ? <LoaderCircle size={17} className="spin"/> : <Paperclip size={17}/>}
              </IconButton>
              <span className="attachment-hint" title="最多 4 个附件；图片/PDF 每个 5 MB，UTF-8 文本每个 256 KB，合计 12 MB。">
                {uploading ? '正在加密上传…' : attachments.length ? `${attachments.length}/4 个附件` : '图片 / PDF / 配置 / 日志'}
              </span>
              <button className="send-btn" type="submit" aria-label="发送指令" disabled={(!draft.trim() && !attachments.length) || sending || uploading || !activeBot?.enabled}>
                {sending ? <LoaderCircle size={18} className="spin"/> : <ArrowUp size={19}/>}
              </button>
            </div>
          </form>
          <p className="composer-note"><LockKeyhole size={11}/>凭据通过加密卡片录入 · 高危操作在对话内联卡片确认 · Enter 发送</p>
        </div>
      </div>
      <aside className="context-panel">
        <div className="context-section">
          <span className="eyebrow">当前上下文</span>
          <h3>目标服务器</h3>
          {current ? (
            <div className="context-host">
              <Server size={20}/>
              <strong className="mono">{current.name}</strong>
              <small className="mono">{current.host}</small>
              <Status status={current.status}/>
              <div><span>角色</span><span>{current.role || '通用服务器'}</span></div>
              <div className="context-jumps">
                {onGoServer && <button type="button" className="text-btn" onClick={onGoServer}><Server size={12}/>探针</button>}
                {onGoAsset && <button type="button" className="text-btn" onClick={() => onGoAsset(current)}><Wallet size={12}/>资费</button>}
                {onGoHistory && <button type="button" className="text-btn" onClick={() => onGoHistory(current.name)}><History size={12}/>记录</button>}
              </div>
            </div>
          ) : (
            <p className="muted">未指定目标，可直接提问。</p>
          )}
        </div>
        <div className="context-section">
          <h3>参与 Bot <span className="tag">{bots.filter(b => b.enabled && !controls.excluded.includes(b.id)).length}</span></h3>
          {bots.filter(b => b.enabled && !controls.excluded.includes(b.id)).map(b => (
            <button type="button" className="team-member team-member-btn" key={b.id} onClick={() => onBot(b.id)} disabled={sending} title={`切换到 ${b.name}`}>
              <span className="small-avatar"><Bot size={16}/></span>
              <div>
                <strong>{b.name}<span>{b.id === activeBot?.id ? '主控' : '协作'}</span></strong>
                <p>{b.description || '按主控分派完成任务'}</p>
              </div>
            </button>
          ))}
        </div>
        {secrets.length > 0 && (
          <div className="context-section">
            <h3>可用密钥引用 <span className="tag">{secrets.length}</span></h3>
            <div className="context-secrets">
              {secrets.slice(0, 8).map(s => (
                <button
                  type="button"
                  key={s.id}
                  className="context-secret-chip mono"
                  disabled={sending}
                  title="点击插入引用到输入框（不会暴露明文）"
                  onClick={() => onDraft((draft ? draft + ' ' : '') + '{{secret:' + s.name + '}}')}
                >
                  <KeyRound size={11}/>{s.name}
                </button>
              ))}
            </div>
          </div>
        )}
        <div className="context-section safety-note">
          <ShieldCheck size={18}/>
          <h3>凭据在执行端解密</h3>
          <p>Bot 通过名称引用密钥，执行记录中的敏感内容会打码。</p>
        </div>
      </aside>
    </div>
  );
}
