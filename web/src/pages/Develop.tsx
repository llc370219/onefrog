import { FormEvent, useCallback, useEffect, useRef, useState } from 'react';
import { ArrowLeft, ArrowRight, Blocks, Bot, Code2, Eraser, ExternalLink, LoaderCircle, Plus, Sparkles, Trash2 } from 'lucide-react';
import { api, type BotRow, type ChatStep, type Choice, type DevModuleRow, type Mod, type SettingsState } from '../api';
import ChoiceCard from '../components/ChoiceCard';
import CustomModuleFrame from '../components/CustomModuleFrame';
import { Button, Empty, Field, Modal, Notice, PageHeading } from '../components/ui';

const errText = (err: unknown) => (err instanceof Error ? err.message : '操作失败，请重试');
const fmtTime = (iso?: string) => (iso ? new Date(iso).toLocaleString('zh-CN', { month: '2-digit', day: '2-digit', hour: '2-digit', minute: '2-digit' }) : '暂无记录');

const quickPrompts = [
  { label: '打卡记述卡片', prompt: '创建一个打卡模块，功能简单：一张卡片点击即可打卡记录时间，支持填写简短记述备注，并列出最近打卡记录。' },
  { label: '待办与巡检清单', prompt: '创建一个运维待办清单模块，支持新增待办、点击勾选完成、删除条目，并统计已完成/未完成数量。' },
  { label: '常用入口导航', prompt: '创建一个常用书签记录模块，支持添加名称、地址备注与分组标签，支持搜索和删除。' },
  { label: '专注倒计时', prompt: '创建一个 25 分钟专注计时模块，支持开始、暂停、重置，并在每次完成后自动写入一条完成记录。' },
];

function devBotReady(settings: SettingsState | null, bots: BotRow[]) {
  if (!settings) return { ok: false, label: '正在读取模型配置', botId: '' };
  const devBot = bots.find(b => (b.id === 'develop' || b.name === '二次开发') && b.enabled)
    || bots.find(b => b.id === 'main' && b.enabled)
    || bots.find(b => b.enabled && b.id !== 'module-builder');
  if (!devBot) return { ok: false, label: '二次开发 Bot 未启用', botId: '' };
  const routed = devBot.effective_route;
  const route = routed?.provider_id && routed.model ? routed : settings.default;
  const provider = settings.providers.find(p => p.id === route?.provider_id && p.enabled);
  if (!provider || !route?.model) return { ok: false, label: '模型还没接上', botId: devBot.id };
  if (provider.auth_required && !provider.auth_kind?.startsWith('oauth-') && !provider.has_api_key) {
    return { ok: false, label: '渠道还没有密钥', botId: devBot.id };
  }
  return { ok: true, label: devBot.name + ' · ' + provider.name + ' / ' + route.model, botId: devBot.id };
}

export function DevelopGlance({ onOpenStudio, onOpenModule }: { onOpenStudio: () => void; onOpenModule: (id: string) => void }) {
  const [rows, setRows] = useState<DevModuleRow[] | null>(null);
  const [error, setError] = useState('');
  useEffect(() => {
    api.devModules().then(setRows).catch(() => setError('二次开发数据暂时不可用'));
  }, []);
  if (error) return <p className="card-foot danger">{error}</p>;
  if (!rows) return <p className="card-foot">正在读取二次开发模块…</p>;
  const totalRecords = rows.reduce((sum, r) => sum + (r.record_count || 0), 0);
  return (
    <div className="asset-glance">
      <div><span>已构建模块</span><strong>{rows.length}</strong></div>
      <div><span>面板启用</span><strong>{rows.filter(r => r.enabled).length}</strong></div>
      <div><span>累计数据记录</span><strong>{totalRecords}</strong></div>
      <div><span>运行隔离</span><strong>沙箱加密</strong></div>
      <p className="develop-glance-foot">
        {rows.length ? (
          <span>
            最近模块：{rows.slice(0, 4).map(r => (
              <button key={r.id} className="text-btn develop-inline-link" onClick={() => onOpenModule(r.id)}>{r.name}</button>
            ))}
          </span>
        ) : '还没有自定义模块，去二次开发工作台用一句话生成第一个模块。'}
        <button className="text-btn" onClick={onOpenStudio}>进入工作台<ArrowRight size={13}/></button>
      </p>
    </div>
  );
}

export function CustomModulePage({
  mod,
  onBack,
  onAskDevelop,
  onChanged,
}: {
  mod: Mod;
  onBack: () => void;
  onAskDevelop: (prompt: string) => void;
  onChanged: () => Promise<void>;
}) {
  const [detail, setDetail] = useState<DevModuleRow | null>(null);
  const [editing, setEditing] = useState(false);
  const [name, setName] = useState(mod.name);
  const [desc, setDesc] = useState(mod.desc);
  const [html, setHtml] = useState('');
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState('');

  const loadDetail = useCallback(async () => {
    try {
      const list = await api.devModules();
      const hit = list.find(item => item.id === mod.id);
      if (hit) {
        setDetail(hit);
        setName(hit.name);
        setDesc(hit.description || '');
        setHtml(hit.html);
      }
      setError('');
    } catch (err) {
      setError(errText(err));
    }
  }, [mod.id]);

  useEffect(() => { void loadDetail(); }, [loadDetail]);

  async function saveEdit() {
    setBusy(true); setError('');
    try {
      await api.updateDevModule(mod.id, { name: name.trim(), description: desc.trim(), html });
      setEditing(false);
      await loadDetail();
      await onChanged();
    } catch (err) {
      setError(errText(err));
    } finally {
      setBusy(false);
    }
  }

  return (
    <div className="page develop-page">
      <PageHeading
        eyebrow="MODULE / CUSTOM"
        title={detail?.name || mod.name}
        note={detail?.description || mod.desc || '二次开发自定义模块'}
        actions={
          <>
            <Button onClick={onBack}><ArrowLeft size={15}/>二次开发工作台</Button>
            <Button onClick={() => setEditing(true)}><Code2 size={15}/>编辑源码</Button>
            <Button variant="primary" onClick={() => onAskDevelop('请先 inspect 二次开发模块「' + (detail?.name || mod.name) + '」（id=' + mod.id + '），保留已有数据并按以下需求改造：')}>
              <Bot size={15}/>AI 继续改造
            </Button>
          </>
        }
      />
      {error && <Notice error>{error}</Notice>}
      <section className="card">
        <CustomModuleFrame
          moduleId={mod.id}
          title={detail?.name || mod.name}
          version={detail?.updated_at || '1'}
          onDataChanged={() => { void loadDetail(); }}
          onFixError={msg => onAskDevelop('二次开发模块「' + (detail?.name || mod.name) + '」（id=' + mod.id + '）运行时报错：' + msg + '。请先 inspect 查看源码并修复这个错误，保留已有记录。')}
        />
      </section>
      {editing && (
        <Modal title={'编辑模块 · ' + (detail?.name || mod.name)} note="修改源码不会清空这个模块已经保存的状态和记录。" wide onClose={() => setEditing(false)} onSubmit={saveEdit} busy={busy} error={error} submitDisabled={!name.trim() || !html.trim()}>
          <Field label="模块名称"><input value={name} maxLength={40} required onChange={e => setName(e.target.value)}/></Field>
          <Field label="功能说明"><input value={desc} maxLength={200} onChange={e => setDesc(e.target.value)} placeholder="一句话说明用途"/></Field>
          <Field label="界面与逻辑源码（HTML / CSS / JS）" hint="运行在隔离沙箱内，通过 window.onefrog.load / addRecord / saveState 读写加密数据。">
            <textarea className="mono code-editor" rows={14} required value={html} onChange={e => setHtml(e.target.value)}/>
          </Field>
        </Modal>
      )}
    </div>
  );
}

export default function Develop({
  bots,
  settings,
  onOpenChat,
  onOpenSettings,
  onOpenModule,
  onChanged,
}: {
  bots: BotRow[];
  settings: SettingsState | null;
  onOpenChat: (prompt: string) => void;
  onOpenSettings: () => void;
  onOpenModule: (id: string) => void;
  onChanged: () => Promise<void>;
}) {
  const [rows, setRows] = useState<DevModuleRow[]>([]);
  const [loaded, setLoaded] = useState(false);
  const [error, setError] = useState('');
  const [prompt, setPrompt] = useState('');
  const [building, setBuilding] = useState(false);
  const [steps, setSteps] = useState<ChatStep[]>([]);
  const [replyText, setReplyText] = useState('');
  const [choice, setChoice] = useState<{ messageId: string; card: Choice } | null>(null);
  const sessionRef = useRef('');

  const [modalMode, setModalMode] = useState<'create' | 'edit' | null>(null);
  const [editId, setEditId] = useState('');
  const [name, setName] = useState('');
  const [desc, setDesc] = useState('');
  const [html, setHtml] = useState('');
  const [useStarter, setUseStarter] = useState(true);
  const [saving, setSaving] = useState(false);
  const [formError, setFormError] = useState('');

  const ready = devBotReady(settings, bots);

  const reload = useCallback(async () => {
    try {
      setRows(await api.devModules());
      setError('');
    } catch (err) {
      setError(errText(err));
    } finally {
      setLoaded(true);
    }
  }, []);

  useEffect(() => { void reload(); }, [reload]);

  async function runBuild(text: string) {
    const trimmed = text.trim();
    if (!trimmed || building || !ready.ok) return;
    setBuilding(true);
    setError('');
    setReplyText('');
    setChoice(null);
    const trace: ChatStep[] = [];
    setSteps([]);
    try {
      const res = await api.chatLive(
        { content: trimmed, session_id: sessionRef.current, bot_id: ready.botId, approval_mode: 'default' },
        step => { trace.push(step); setSteps([...trace]); }
      );
      sessionRef.current = res.session_id || sessionRef.current;
      setReplyText(res.text);
      if (res.choice && res.message_id) {
        setChoice({ messageId: res.message_id, card: res.choice });
      }
      setPrompt('');
      await reload();
      await onChanged();
    } catch (err) {
      setError(errText(err));
    } finally {
      setBuilding(false);
    }
  }

  async function answerBuildChoice(values: string[], custom: string) {
    if (!choice || building || !sessionRef.current) return;
    setBuilding(true);
    const cardID = choice.messageId;
    setChoice({ ...choice, card: { ...choice.card, answer: { values, custom: custom.trim() || undefined } } });
    const trace: ChatStep[] = [];
    setSteps([]);
    try {
      const res = await api.chatLive(
        { content: '', session_id: sessionRef.current, bot_id: ready.botId, approval_mode: 'default', choice_answer: { message_id: cardID, values, custom: custom.trim() || undefined } },
        step => { trace.push(step); setSteps([...trace]); }
      );
      setReplyText(res.text);
      setChoice(res.choice && res.message_id ? { messageId: res.message_id, card: res.choice } : null);
      await reload();
      await onChanged();
    } catch (err) {
      setError(errText(err));
    } finally {
      setBuilding(false);
    }
  }

  function submitPrompt(e: FormEvent) {
    e.preventDefault();
    void runBuild(prompt);
  }

  function openCreate() {
    setEditId('');
    setName('');
    setDesc('');
    setHtml('');
    setUseStarter(true);
    setFormError('');
    setModalMode('create');
  }

  function openEdit(row: DevModuleRow) {
    setEditId(row.id);
    setName(row.name);
    setDesc(row.description || '');
    setHtml(row.html);
    setUseStarter(false);
    setFormError('');
    setModalMode('edit');
  }

  async function submitModal() {
    setSaving(true); setFormError('');
    try {
      if (modalMode === 'create') {
        await api.createDevModule({
          name: name.trim(),
          description: desc.trim(),
          html: useStarter && !html.trim() ? undefined : html,
          use_starter: useStarter && !html.trim(),
          enabled: true,
        });
      } else if (modalMode === 'edit' && editId) {
        await api.updateDevModule(editId, {
          name: name.trim(),
          description: desc.trim(),
          html,
        });
      }
      setModalMode(null);
      await reload();
      await onChanged();
    } catch (err) {
      setFormError(errText(err));
    } finally {
      setSaving(false);
    }
  }

  async function toggleEnabled(row: DevModuleRow) {
    try {
      await api.enableModule(row.id, !row.enabled);
      await reload();
      await onChanged();
    } catch (err) {
      setError(errText(err));
    }
  }

  async function clearData(row: DevModuleRow) {
    if (!window.confirm('清空模块「' + row.name + '」保存的全部记录与状态？界面代码会保留。')) return;
    try {
      await api.devModuleData(row.id, { action: 'clear_records' });
      await reload();
    } catch (err) {
      setError(errText(err));
    }
  }

  async function removeModule(row: DevModuleRow) {
    if (!window.confirm('删除二次开发模块「' + row.name + '」及其 ' + row.record_count + ' 条记录？此操作不可恢复。')) return;
    try {
      await api.deleteDevModule(row.id);
      await reload();
      await onChanged();
    } catch (err) {
      setError(errText(err));
    }
  }

  return (
    <div className="page develop-page">
      <PageHeading
        eyebrow="MODULE / DEVELOP"
        title="二次开发"
        note="用一句话让二次开发 Bot 生成自定义交互模块，或直接编辑源码；生成的模块自动挂载到「模块」目录和「面板」首页。"
        actions={
          <>
            <Button onClick={() => onOpenChat(prompt || '帮我开发一个自定义模块：')}><Bot size={15}/>进入对话细调</Button>
            <Button variant="primary" onClick={openCreate}><Plus size={15}/>新建模块</Button>
          </>
        }
      />

      <section className="panel-operator develop-builder">
        <div className="panel-operator-head">
          <div className="launch-icon"><Sparkles size={21} strokeWidth={1.5}/></div>
          <div>
            <strong>{ready.ok ? 'AI 二次开发构建器' : ready.label}</strong>
            <p>{ready.ok ? ready.label + ' · 创建即生效，修改或删除已有模块会先弹出审批。模块在隔离沙箱运行，数据加密保存。' : '先配置可用模型渠道，或点击右上角「新建模块」使用内置模板与源码创建。'}</p>
          </div>
          {!ready.ok && <Button variant="primary" onClick={onOpenSettings}>去接模型</Button>}
        </div>
        {ready.ok && (
          <>
            <div className="operator-actions">
              {quickPrompts.map(item => (
                <Button key={item.label} disabled={building} onClick={() => void runBuild(item.prompt)}>{item.label}</Button>
              ))}
            </div>
            <form className="operator-form" onSubmit={submitPrompt}>
              <input
                aria-label="描述你想开发的模块"
                value={prompt}
                disabled={building}
                placeholder="例如：创建一个打卡的模块，功能简单，一个卡片点击记述"
                onChange={e => setPrompt(e.target.value)}
              />
              <Button variant="primary" type="submit" busy={building} disabled={!prompt.trim()}>生成模块</Button>
            </form>
          </>
        )}
        {(building || steps.length > 0 || replyText || choice) && (
          <div className="operator-result" aria-live="polite">
            {building && <p className="operator-pending"><LoaderCircle size={14} className="spin"/>正在生成模块…</p>}
            {steps.map((s, idx) => (
              <div className="operator-step" key={idx}>
                <strong>{s.command || s.task || '执行步骤'}</strong>
                {s.output && <pre>{s.output.slice(0, 280)}</pre>}
              </div>
            ))}
            {replyText && <p className="operator-text">{replyText}</p>}
            {choice && <ChoiceCard choice={choice.card} disabled={building} onAnswer={answerBuildChoice}/>}
          </div>
        )}
      </section>

      {error && <Notice error>{error}</Notice>}

      <div className="section-label">
        <span><span className="live-dot"/>已构建模块 <small>{rows.length} 个</small></span>
        <span className="muted">开关控制是否同时展示在面板首页</span>
      </div>

      {loaded && !rows.length ? (
        <Empty
          title="还没有二次开发模块"
          body="在上方输入需求（如「创建一个打卡模块，点击记录时间和备注」）一键生成，或手动创建模板模块。"
          action={<Button onClick={openCreate}><Plus size={15}/>新建第一个模块</Button>}
        />
      ) : (
        <div className="develop-grid">
          {rows.map(row => (
            <section className="card develop-card" key={row.id}>
              <header className="card-heading">
                <div>
                  <h2><Blocks size={16}/>{row.name}</h2>
                  <small className="muted">{row.description || '自定义交互模块'} · {row.record_count} 条记录 · 最近更新 {fmtTime(row.updated_at)}</small>
                </div>
                <div className="develop-card-controls">
                  <span className="muted">{row.enabled ? '面板显示' : '仅模块页'}</span>
                  <button
                    role="switch"
                    aria-checked={row.enabled}
                    aria-label={'在面板显示 ' + row.name}
                    className={'switch ' + (row.enabled ? 'on' : '')}
                    onClick={() => void toggleEnabled(row)}
                  >
                    <i/>
                  </button>
                </div>
              </header>
              <CustomModuleFrame
                moduleId={row.id}
                title={row.name}
                version={row.updated_at}
                onDataChanged={() => { void reload(); }}
                onFixError={msg => onOpenChat('二次开发模块「' + row.name + '」（id=' + row.id + '）报错：' + msg + '。请先 inspect 查看源码并修复，保留已有数据。')}
              />
              <footer className="develop-card-foot">
                <div className="row-actions">
                  <Button onClick={() => onOpenModule(row.id)}><ExternalLink size={13}/>独立打开</Button>
                  <Button onClick={() => onOpenChat('请先 inspect 二次开发模块「' + row.name + '」（id=' + row.id + '），保留已有数据并按以下需求升级：')}><Bot size={13}/>AI 改造</Button>
                  <Button onClick={() => openEdit(row)}><Code2 size={13}/>源码</Button>
                </div>
                <div className="row-actions">
                  {row.record_count > 0 && <Button onClick={() => void clearData(row)}><Eraser size={13}/>清空数据</Button>}
                  <Button className="danger-btn" onClick={() => void removeModule(row)}><Trash2 size={13}/>删除</Button>
                </div>
              </footer>
            </section>
          ))}
        </div>
      )}

      {modalMode && (
        <Modal
          title={modalMode === 'create' ? '新建二次开发模块' : '编辑模块源码'}
          note={modalMode === 'create' ? '可直接使用内置打卡记述模板，或粘贴自定义 HTML / CSS / JS。' : '修改界面代码会保留模块已保存的状态与记录。'}
          wide
          onClose={() => setModalMode(null)}
          onSubmit={submitModal}
          busy={saving}
          error={formError}
          confirm={modalMode === 'create' ? '创建模块' : '保存修改'}
          submitDisabled={!name.trim() || (!useStarter && !html.trim())}
        >
          <Field label="模块名称"><input value={name} maxLength={40} required placeholder="例如：打卡" onChange={e => setName(e.target.value)}/></Field>
          <Field label="功能说明"><input value={desc} maxLength={200} placeholder="例如：点击卡片记录打卡时间与备注" onChange={e => setDesc(e.target.value)}/></Field>
          {modalMode === 'create' && (
            <label className="starter-check">
              <input type="checkbox" checked={useStarter} onChange={e => setUseStarter(e.target.checked)}/>
              <span>源码留空时使用内置「点击打卡记述卡片」模板初始化</span>
            </label>
          )}
          <Field label="界面与逻辑源码（HTML / CSS / JS）" hint="内置 window.onefrog.load() / addRecord() / updateRecord() / deleteRecord() / saveState()，数据加密保存在服务端。">
            <textarea
              className="mono code-editor"
              rows={12}
              value={html}
              required={!useStarter}
              placeholder={useStarter ? '留空将自动填入可点击记述的打卡卡片模板，也可直接编写自定义 HTML + <script>…' : ''}
              onChange={e => setHtml(e.target.value)}
            />
          </Field>
        </Modal>
      )}
    </div>
  );
}

