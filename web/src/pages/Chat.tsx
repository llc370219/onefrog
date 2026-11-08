import { useEffect, useLayoutEffect, useRef, useState } from 'react';
import { ArrowUp, Blocks, BookOpen, CodeXml, CalendarClock, Code2, HardDrive, History, Inbox, KeyRound, LoaderCircle, LockKeyhole, Plus, Server, ShieldCheck, Sparkles, Paperclip, Send, Trash2, Users, Wallet } from 'lucide-react';
import { api, type BotMemory, type ProjectNote, type BotRow, type ChatLine, type ChatMode, type ChatSession, type Host, type Attachment, type SecretRow, type SettingsState, type TaskView, type WorkspaceTarget } from '../api';
import ChatControls, { type ChatControlState } from '../components/ChatControls';
import Attachments from '../components/Attachments';
import TurnStream from '../components/TurnStream';
import TaskReplyControls, { replyFailure, replyWaitingStatus } from '../components/TaskReplyControls';
import { Button, IconButton, Modal, Status } from '../components/ui';
import { BotAvatar } from '../components/BotAvatar';
import { AvatarStack } from '../components/SecondaryNav';
import { groupMembers, groupSessions } from '../lib/chatGroups';
import { nearChatBottom } from '../lib/chatScroll';
import { chatQueue, composerChoiceAnswer, pendingChatCard } from '../lib/chatQueue';
import { taskBusyId } from '../lib/taskCardFormat';

function when(value: string) {
  const d = new Date(value);
  return d.toDateString() === new Date().toDateString()
    ? d.toLocaleTimeString('zh-CN', { hour: '2-digit', minute: '2-digit' })
    : d.toLocaleDateString('zh-CN', { month: 'numeric', day: 'numeric' });
}

export default function Chat({
  onAddServer,
  controls,
  onControls,
  settings,
  mode,
  projectTitle,
  workspace,
  onOpenWorkspace,
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
  onCorrect,
  sending,
  submitting,
  onNew,
  onNewTopic,
  sessions = [],
  session = '',
  onSession,
  onDeleteSession,
  onNotice,
  onAnswer,
  onProvide,
  onGoServer,
  onGoAsset,
  onGoHistory,
  tasks = [],
  taskBusy,
  onTaskAction,
  onTaskApproval,
}: {
  onAddServer: () => void;
  controls: ChatControlState;
  onControls: (s: ChatControlState) => void;
  settings: SettingsState | null;
  mode: ChatMode;
  projectTitle: string;
  workspace?: WorkspaceTarget;
  onOpenWorkspace: () => void;
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
  onCorrect?: (taskID: string) => void;
  sending: boolean;
  submitting?: boolean;
  onNew: () => void;
  onNewTopic?: () => void;
  sessions?: ChatSession[];
  session?: string;
  onSession?: (s: ChatSession) => void;
  onDeleteSession?: (s: ChatSession) => void;
  onNotice: (s: string) => void;
  onAnswer: (line: ChatLine, values: string[], custom: string, fromComposer?: boolean) => void;
  onProvide: (line: ChatLine, value: string) => void;
  onGoServer?: () => void;
  onGoAsset?: (host: Host) => void;
  onGoHistory?: (hostName: string) => void;
  tasks?: TaskView[];
  taskBusy?: string | string[];
  onTaskAction: (id: string, action: 'pause' | 'resume' | 'cancel' | 'withdraw') => void;
  onTaskApproval: (taskID: string, approvalID: string, allow: boolean) => void;
}) {
  const scroll = useRef<HTMLDivElement>(null);
  const followBottom = useRef(true);
  const fileInput = useRef<HTMLInputElement>(null);
  const question = useRef<HTMLElement>(null);
  const queue = chatQueue(tasks, lines);
  const cardID = queue.current?.status === 'waiting_user' ? queue.current.pending?.card_message_id : undefined;
  const card = pendingChatCard(queue.current, lines);
  const choice = card?.choice;
  const answer = choice ? composerChoiceAnswer(choice, draft) : undefined;
  const canCorrect = Boolean(onCorrect && queue.current && (
    queue.current.status === 'running' || (queue.current.status === 'queued' && queue.current.started_at && !queue.current.pending)
  ));
  const canSend = !sending && !uploading && Boolean(activeBot?.enabled && (draft.trim() || attachments.length));
  const sendLabel = choice ? '回答当前问题' : queue.current ? '排到下一轮' : '发送指令';
  const replyPending = Boolean(submitting || queue.current);
  const replyError = replyPending || queue.latest?.user_message_id ? '' : replyFailure(queue.latest);
  const lastUser = lines.reduce((last, line, index) => line.who === 'user' ? index : last, -1);
  const conversationKey = session || `new:${mode}:${activeBot?.id || ''}`;
  const previousConversation = useRef<string | undefined>(undefined);
  // A different conversation starts at its latest message. Polls and card
  // updates in the same conversation must respect a reader who scrolled up.
  useLayoutEffect(() => {
    const previous = previousConversation.current;
    previousConversation.current = conversationKey;
    // The first send promotes a new chat from a temporary key to its durable
    // session id. It is the same conversation, so preserve a manual scroll.
    if (previous?.startsWith('new:') && session) return;
    followBottom.current = true;
    const container = scroll.current;
    if (container) container.scrollTop = container.scrollHeight;
  }, [conversationKey]);
  useLayoutEffect(() => {
    const container = scroll.current;
    if (container && followBottom.current) container.scrollTop = container.scrollHeight;
  });
  function sendFromComposer(nextTurn = false) {
    if (sending || uploading || !activeBot?.enabled || (!draft.trim() && !attachments.length)) return;
    if (!nextTurn && choice && card) {
      if (attachments.length || !answer) {
        onNotice(attachments.length ? '当前问题只能用文字回答；附件可排到下一轮' : '请在问题卡中选择，或输入有效回答');
        return;
      }
      followBottom.current = true;
      onAnswer(card, answer.values, answer.custom, true);
      return;
    }
    followBottom.current = true;
    onSend();
  }

  function showQuestion() {
    if (!scroll.current || !question.current) return;
    followBottom.current = false;
    scroll.current.scrollTop += question.current.getBoundingClientRect().top - scroll.current.getBoundingClientRect().top - 12;
  }

  function pendingMessage(task: TaskView, label: string, removable = false) {
    return <article className="message user" key={task.user_message_id}>
      <div className="user-message-content">
        <div className="user-text">{task.goal}</div>
        <Attachments files={task.chat_attachments || []}/>
        <div className="reply-actions" role="status">
          <span>{label}</span>
          {removable && <button type="button" className="text-btn" disabled={!!taskBusyId(taskBusy, task.id)}
            onClick={() => onTaskAction(task.id, 'withdraw')}>取消排队</button>}
        </div>
      </div>
    </article>;
  }

  const [memoryState, setMemoryState] = useState<{ bot: string; rows: BotMemory[] } | null>(null);
  const memoryBot = mode === 'direct' ? activeBot?.id || '' : '';
  useEffect(() => {
    if (!memoryBot) return;
    let live = true;
    api.memories(memoryBot).then(rows => { if (live) setMemoryState({ bot: memoryBot, rows }); }).catch(() => { if (live) setMemoryState({ bot: memoryBot, rows: [] }); });
    return () => { live = false; };
  }, [memoryBot, lines.length]);
  const memories = memoryState && memoryState.bot === memoryBot ? memoryState.rows : null;

  const [projectNotes, setProjectNotes] = useState<{ id: string; rows: ProjectNote[]; error?: string } | null>(null);
  const [memoryOpen, setMemoryOpen] = useState(false);
  const projectID = mode === 'team' ? session : '';
  useEffect(() => {
    if (!projectID || sending) return;
    let active = true;
    api.projectNotes(projectID).then(rows => {
      if (active) setProjectNotes({ id: projectID, rows });
    }).catch(() => {
      if (active) setProjectNotes({ id: projectID, rows: [], error: '工作记忆暂时无法读取。' });
    });
    return () => { active = false; };
  }, [projectID, sending]);
  const notes = projectID && projectNotes?.id === projectID ? projectNotes : null;
  const title = projectTitle || sessions.find(s => s.id === session)?.title || '隔离项目';

  const current = hosts.find(h => h.id === selected);
  const members = workspace && activeBot ? [activeBot] : mode === 'team' ? groupMembers(bots, activeBot?.id || '', controls.excluded) : [];
  const topics = mode === 'direct' && activeBot ? groupSessions(sessions).byBot[activeBot.id] || [] : [];
  const isDevelop = activeBot?.id === 'develop' || activeBot?.name === '自定义模块' || activeBot?.name === '二次开发';
  const isModMgr = activeBot?.id === 'module-builder' || activeBot?.name === '模块管理' || activeBot?.name === '模块构建';
  const welcomeTitle = workspace ? '交给我一个代码任务' : isDevelop ? '想创建什么自定义模块？' : isModMgr ? '管理模块与资产台账' : '今天，先处理什么？';
  const welcomeNote = workspace ? '告诉我改什么、怎么验收。写入文件和运行命令前会请你确认。' : isDevelop
    ? '描述你想要的交互卡片或小工具，自定义模块 Bot 会一步步编写界面并挂载到面板与模块目录。'
    : isModMgr
    ? '查看或开关面板模块、维护资产与订阅台账，或管理自定义模块里的数据记录。'
    : mode === 'direct'
    ? `只和「${activeBot?.name || 'Bot'}」一对一对话。它带着自己的记忆独立干活，能操作服务器和面板；别的 Bot 更熟悉时，它会自己找对方协助。`
    : `这是一个 ${members.length} 个 Bot 的隔离项目。主控「${activeBot?.name || '主控'}」按任务分派给项目里的 Bot，每条回复都标出是谁说的；会话和工作记忆只保留在这个项目里，不读取任何 Bot 私有记忆，也不会写入任何 Bot 的长期记忆。你可以在右侧随时剔除或拉入 Bot。`;

  const suggestions = workspace
    ? [
        { icon: Code2, title: '了解仓库', text: '阅读仓库说明和关键目录，简要介绍结构与验证方法。' },
        { icon: HardDrive, title: '审查当前差异', text: '检查当前 Git 差异，报告问题和需要验证的地方。' },
        { icon: ShieldCheck, title: '运行相关测试', text: '查看仓库说明，找出合适的测试命令，并在我批准后运行。' },
      ]
    : isDevelop
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
          <div className="conversation-title">
            {mode === 'direct'
              ? <h1>{activeBot && <BotAvatar bots={bots} id={activeBot.id} className="heading-avatar"/>}{activeBot?.name || 'Bot'} <span className="tag">单聊</span></h1>
              : <h1><AvatarStack bots={bots} members={members}/><span className="project-name" title={title}>{title}</span> <span className="tag">{members.length} 个 Bot</span></h1>}
          </div>
          {mode === 'direct'
            ? <Button onClick={onNewTopic || onNew} disabled={sending}><Plus size={15}/>新话题</Button>
            : <div className="conversation-actions">
              <IconButton label="项目工作记忆" onClick={() => setMemoryOpen(true)}><BookOpen size={16}/></IconButton>
              {workspace ? <Button onClick={onOpenWorkspace}><CodeXml size={15}/>查看代码</Button> : <Button onClick={onNew} disabled={sending}><Users size={15}/>新建隔离项目</Button>}
            </div>}
        </header>
        {workspace && <div className="chat-workspace-path mono" title={workspace.root}>{current?.name || workspace.server_id} · {workspace.root}</div>}
        <div className="chat-scroll" ref={scroll} onScroll={() => {
          if (scroll.current) followBottom.current = nearChatBottom(scroll.current);
        }}>
          {!lines.length && !replyPending && !replyError && !queue.unsentErrors.length ? (
            <div className="chat-welcome">
              {mode === 'direct' && activeBot ? <BotAvatar bots={bots} id={activeBot.id} className="welcome-icon"/> : <div className="welcome-icon welcome-group"><AvatarStack bots={bots} members={members} max={5}/></div>}
              
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
                <article className={`message ${line.who}`} key={line.id || i} ref={cardID && line.id === cardID ? question : undefined}>
                  {line.who === 'user' ? (
                    <div className={line.fromBot ? 'user-message-content from-bot' : 'user-message-content'}>
                      {line.fromBot && <div className="from-bot-label"><Send size={12}/>来自 Bot {bots.find(b => b.id === line.fromBot)?.name || line.fromBot}</div>}
                      {line.text && <div className="user-text">{line.text}</div>}
                      <Attachments files={line.attachments || []}/>
                      {replyFailure(line.id ? queue.tasksByMessage.get(line.id) : undefined) && <div className="reply-actions">
                        <span>{replyFailure(line.id ? queue.tasksByMessage.get(line.id) : undefined)}</span>
                      </div>}
                    </div>
                  ) : (
                    <div className="bot-turn">
                      {mode === 'direct' && (
                        <div className="speaker">
                          {activeBot && <BotAvatar bots={bots} id={activeBot.id} className="speaker-avatar"/>}
                          <strong>{activeBot?.name || 'Bot'}</strong>
                        </div>
                      )}
                      <TurnStream
                        timeline={line.timeline || []}
                        text={line.text}
                        bots={bots}
                        group={mode === 'team'}
                        lead={activeBot}
                        pending={line.pending}
                        activeStatus={line.activeStatus}
                        hosts={hosts}
                        choice={line.choice}
                        secret={line.secret}
                        stale={line.choice || line.secret ? !cardID || line.id !== cardID : i < lines.length - 1}
                        disabled={sending}
                        showCompletion={i < lastUser || (!replyPending && !replyError)}
                        animateFinal={i === lines.length - 1 && !line.pending}
                        onAnswerChoice={(values, custom) => onAnswer(line, values, custom)}
                        onProvideSecret={value => onProvide(line, value)}
                        onCopy={copy}
                      />
                    </div>
                  )}
                </article>
              ))}
              {queue.currentMessage && pendingMessage(queue.currentMessage, '已发送')}
              {(replyPending || replyError) && <article className="message bot">
                <div className="bot-turn">
                  <div className="speaker">
                    {activeBot && <BotAvatar bots={bots} id={activeBot.id} className="speaker-avatar"/>}
                    <strong>{activeBot?.name || 'Bot'}</strong>
                  </div>
                  {replyPending ? <>
                    <TaskReplyControls tasks={queue.currentTasks} bots={bots} busy={taskBusy} onAction={onTaskAction} onApproval={onTaskApproval}/>
                    <TurnStream pending waitingStatus={cardID && !card ? '未显示待答问题，可刷新对话或停止本轮' : replyWaitingStatus(queue.currentTasks)}/>
                  </> : <p className="operator-error" role="status">{replyError}</p>}
                </div>
              </article>}
              {queue.waiting.map(task => pendingMessage(task, '已排队 · 上一轮结束后回复', true))}
              {queue.unsentErrors.map(task => pendingMessage(task, replyFailure(task)))}
            </div>
          )}
        </div>
        <div className="composer-area">
          <ChatControls
            workspace={Boolean(workspace)}
            onAddServer={onAddServer}
            hosts={hosts}
            bots={bots}
            selected={selected}
            onServer={onSelect}
            botID={activeBot?.id || ''}
            mode={mode}
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
              sendFromComposer();
            }}
          >
            {card && <div className="composer-reply-context">
              <span>{choice ? '回答：' + choice.prompt : '请在问题卡中安全填写凭据'}</span>
              <button className="text-btn" type="button" onClick={showQuestion}>查看问题</button>
            </div>}
            <Attachments files={attachments} onRemove={onRemoveFile} disabled={sending || uploading}/>
            <textarea
              aria-label="任务指令"
              placeholder={choice ? (choice.allow_custom ? '输入文字回答当前问题…' : '输入选项编号，或在问题卡中选择…') : queue.current ? '发消息，或纠正正在进行的回复…' : '发消息，或上传图片、配置、日志…'}
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
                  sendFromComposer();
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
              {canCorrect && <button type="button" className="text-btn" disabled={!canSend || !draft.trim() || !!attachments.length}
                title="把文字交给当前轮，在下一步生效"
                onClick={() => {
                  followBottom.current = true;
                  if (queue.current) onCorrect?.(queue.current.id);
                }}>纠正当前轮</button>}
              {choice && <button type="button" className="text-btn" disabled={!canSend}
                onClick={() => sendFromComposer(true)}>排到下一轮</button>}
              <button className="send-btn" type="submit" aria-label={sendLabel} disabled={!canSend || Boolean(choice && (!answer || attachments.length))} title={sendLabel}>
                {sending ? <LoaderCircle size={18} className="spin"/> : <ArrowUp size={19}/>}
              </button>
            </div>
          </form>
          <p className="composer-note"><LockKeyhole size={11}/>凭据通过加密卡片录入 · 高危操作在对话内确认 · Enter {choice ? '回答当前问题' : queue.current ? '排到下一轮' : '发送'}</p>
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
        {mode === 'direct' ? (
          <>
          <div className="context-section">
            <h3>关于这个 Bot</h3>
            {activeBot ? (
              <div className="direct-bot">
                <div className="team-member">
                  <BotAvatar bots={bots} id={activeBot.id} className="small-avatar"/>
                  <div>
                    <strong>{activeBot.name}<span>独立对话</span></strong>
                    <p>{activeBot.description || '还没有填写分工说明'}</p>
                  </div>
                </div>
                <div className="direct-memory">
                  <span className="eyebrow">自己的记忆 · {memories === null ? '…' : memories.length} 条</span>
                  {memories?.slice(0, 4).map(m => <small key={m.id} title={m.body}>{m.title}</small>)}
                  {memories?.length === 0 && <small className="muted wrap">还没有记忆，聊天中学到的事会记在这里。</small>}
                </div>
                <p className="muted">它独立完成任务，只读取自己的记忆。需要时会自己找其他 Bot 协助，过程显示在对话里。</p>
              </div>
            ) : <p className="muted">未选择 Bot。</p>}
          </div>
          <div className="context-section">
            <h3>历史话题 <span className="tag">{topics.length}</span></h3>
            {topics.length ? (
              <div className="topic-list">
                {topics.map(s => (
                  <div className={`topic-row ${session === s.id ? 'selected' : ''}`} key={s.id}>
                    <button type="button" onClick={() => onSession?.(s)} disabled={sending || session === s.id} aria-current={session === s.id ? 'true' : undefined}>
                      <strong>{s.id.startsWith('inbox-') && <Inbox size={11}/>}{s.id.startsWith('auto-') && <CalendarClock size={11}/>}{s.title}</strong>
                      <time>{when(s.updated_at)}</time>
                    </button>
                    {onDeleteSession && <IconButton label={`删除话题 ${s.title}`} disabled={sending} onClick={() => onDeleteSession(s)}><Trash2 size={12}/></IconButton>}
                  </div>
                ))}
              </div>
            ) : <p className="muted">还没有历史，发出第一条消息后会保存在这里。</p>}
          </div>
          </>
        ) : (
          <>
          <div className="context-section">
            <h3>项目工作记忆 {notes && !notes.error && <span className="tag">{notes.rows.length}</span>}</h3>
            <button type="button" className="text-btn" onClick={() => setMemoryOpen(true)}><BookOpen size={13}/>查看笔记</button>
          </div>
          {!workspace && <div className="context-section">
            <h3>项目成员 <span className="tag">{members.length}</span></h3>
            <p className="muted">主控按需分派任务给项目里的 Bot，可随时剔除或拉入。</p>
            {bots.filter(b => b.enabled).sort((a, b) => Number(b.id === activeBot?.id) - Number(a.id === activeBot?.id)).map(b => {
              const lead = b.id === activeBot?.id;
              const out = !lead && controls.excluded.includes(b.id);
              return (
                <div className={`team-member ${out ? 'excluded' : ''}`} key={b.id}>
                  <BotAvatar bots={bots} id={b.id} className="small-avatar"/>
                  <div>
                    <strong>{b.name}<span>{lead ? '主控' : out ? '已剔除' : '协作'}</span></strong>
                    <p>{b.description || '按主控分派完成任务'}</p>
                  </div>
                  {!lead && (
                    <button
                      type="button"
                      className="member-toggle"
                      disabled={sending}
                      aria-label={(out ? '拉入 ' : '剔除 ') + b.name}
                      onClick={() => onControls({ ...controls, excluded: out ? controls.excluded.filter(id => id !== b.id) : [...controls.excluded, b.id] })}
                    >
                      {out ? '拉入' : '剔除'}
                    </button>
                  )}
                </div>
              );
            })}
          </div>}
          </>
        )}
        {!workspace && secrets.length > 0 && (
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
      {mode === 'team' && memoryOpen && <Modal title="项目工作记忆" note={title} onClose={() => setMemoryOpen(false)} confirm="完成">
        {notes?.error && <p className="muted" role="status">{notes.error}</p>}
        {!notes && <p className="muted">{projectID ? '正在读取…' : '开始对话后，可以让 Bot 记下项目的决定和进度。'}</p>}
        {notes && !notes.error && !notes.rows.length && <p className="muted">还没有笔记。让 Bot 记下以后需要用到的结论。</p>}
        <div className="project-notes">{notes?.rows.map(note => <details key={note.id}><summary>{note.title}</summary><p>{note.body}</p></details>)}</div>
      </Modal>}
    </div>
  );
}
