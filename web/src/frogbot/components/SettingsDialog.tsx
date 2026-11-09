import { KeyRound, Shield, Sliders, X, Laptop, Moon, Sun, Bell, BellOff, Info as InfoIcon } from 'lucide-react';
import { useEffect, useState } from 'react';
import { api, type Info } from '../api';

type Tab = 'general' | 'model' | 'secrets' | 'about';

export default function SettingsDialog({
  open,
  onClose,
  info,
  notify,
  onToggleNotify,
}: {
  open: boolean;
  onClose: () => void;
  info: Info;
  notify: boolean;
  onToggleNotify: () => void;
}) {
  const [tab, setTab] = useState<Tab>('general');
  const [theme, setTheme] = useState<'system' | 'light' | 'dark'>(() => {
    return (localStorage.getItem('frogbot-theme') as 'system' | 'light' | 'dark') || 'system';
  });
  const [secrets, setSecrets] = useState<Array<{ name: string; description?: string; updated_at?: string }>>([]);
  const [loadingSecrets, setLoadingSecrets] = useState(false);

  useEffect(() => {
    if (!open) return;
    if (tab === 'secrets') {
      setLoadingSecrets(true);
      api.secrets?.()
        .then((list) => setSecrets(list || []))
        .catch(() => {})
        .finally(() => setLoadingSecrets(false));
    }
  }, [open, tab]);

  if (!open) return null;

  const setAppTheme = (t: 'system' | 'light' | 'dark') => {
    setTheme(t);
    const root = document.documentElement;
    if (t === 'system') {
      delete root.dataset.theme;
      localStorage.removeItem('frogbot-theme');
    } else {
      root.dataset.theme = t;
      localStorage.setItem('frogbot-theme', t);
    }
  };

  return (
    <div className="fixed inset-0 z-50 flex items-center justify-center bg-black/45 backdrop-blur-xs p-4 animate-in fade-in duration-150">
      <div
        className="relative flex h-[560px] w-full max-w-2xl flex-col overflow-hidden rounded-2xl border border-line bg-canvas shadow-2xl"
        role="dialog"
        aria-modal="true"
        aria-labelledby="settings-title"
      >
        {/* Header */}
        <div className="flex h-14 items-center justify-between border-b border-line px-6">
          <div className="flex items-center gap-2.5">
            <Sliders size={18} className="text-accent" />
            <h2 id="settings-title" className="text-[16px] font-semibold text-fg">系统与偏好设置</h2>
          </div>
          <button
            onClick={onClose}
            className="grid size-8 place-items-center rounded-lg text-fg3 transition hover:bg-s2 hover:text-fg"
            aria-label="关闭"
          >
            <X size={17} />
          </button>
        </div>

        {/* Content body with side tabs */}
        <div className="flex flex-1 min-h-0">
          {/* Sidebar tabs */}
          <div className="w-44 border-r border-line bg-s2/50 p-3 flex flex-col gap-1">
            <button
              onClick={() => setTab('general')}
              className={`flex items-center gap-2.5 w-full px-3 py-2 rounded-xl text-left text-xs font-medium transition ${
                tab === 'general' ? 'bg-s1 text-fg shadow-xs border border-line/60' : 'text-fg3 hover:bg-s2 hover:text-fg'
              }`}
            >
              <Sliders size={15} />
              通用外观
            </button>
            <button
              onClick={() => setTab('model')}
              className={`flex items-center gap-2.5 w-full px-3 py-2 rounded-xl text-left text-xs font-medium transition ${
                tab === 'model' ? 'bg-s1 text-fg shadow-xs border border-line/60' : 'text-fg3 hover:bg-s2 hover:text-fg'
              }`}
            >
              <Shield size={15} />
              模型与策略
            </button>
            <button
              onClick={() => setTab('secrets')}
              className={`flex items-center gap-2.5 w-full px-3 py-2 rounded-xl text-left text-xs font-medium transition ${
                tab === 'secrets' ? 'bg-s1 text-fg shadow-xs border border-line/60' : 'text-fg3 hover:bg-s2 hover:text-fg'
              }`}
            >
              <KeyRound size={15} />
              密钥保险箱
            </button>
            <button
              onClick={() => setTab('about')}
              className={`flex items-center gap-2.5 w-full px-3 py-2 rounded-xl text-left text-xs font-medium transition ${
                tab === 'about' ? 'bg-s1 text-fg shadow-xs border border-line/60' : 'text-fg3 hover:bg-s2 hover:text-fg'
              }`}
            >
              <InfoIcon size={15} />
              关于系统
            </button>
          </div>

          {/* Tab content panel */}
          <div className="flex-1 overflow-y-auto p-6 text-sm text-fg2">
            {tab === 'general' && (
              <div className="flex flex-col gap-6">
                <div>
                  <h3 className="text-xs font-semibold uppercase tracking-wider text-fg4 mb-3">主题模式</h3>
                  <div className="grid grid-cols-3 gap-2.5">
                    {[
                      { id: 'system', label: '跟随系统', icon: Laptop },
                      { id: 'light', label: '浅色模式', icon: Sun },
                      { id: 'dark', label: '深色模式', icon: Moon },
                    ].map((item) => (
                      <button
                        key={item.id}
                        onClick={() => setAppTheme(item.id as any)}
                        className={`flex flex-col items-center gap-2 p-3 rounded-xl border text-xs font-medium transition ${
                          theme === item.id
                            ? 'border-accent bg-accent-soft text-accent ring-1 ring-accent'
                            : 'border-line bg-s1 text-fg3 hover:bg-s2 hover:text-fg'
                        }`}
                      >
                        <item.icon size={20} />
                        {item.label}
                      </button>
                    ))}
                  </div>
                </div>

                <div>
                  <h3 className="text-xs font-semibold uppercase tracking-wider text-fg4 mb-3">系统通知</h3>
                  <div className="flex items-center justify-between rounded-xl border border-line bg-s1 p-3.5">
                    <div className="flex items-center gap-3">
                      <div className="grid size-8 place-items-center rounded-lg bg-s2 text-fg3">
                        {notify ? <Bell size={17} /> : <BellOff size={17} />}
                      </div>
                      <div>
                        <div className="text-xs font-medium text-fg">后台桌面弹窗通知</div>
                        <div className="text-[11.5px] text-fg4">页面在后台时，任务完成或等待审批时会提醒你</div>
                      </div>
                    </div>
                    <button
                      onClick={onToggleNotify}
                      className={`relative inline-flex h-6 w-11 shrink-0 cursor-pointer rounded-full border-2 border-transparent transition-colors duration-200 ease-in-out ${
                        notify ? 'bg-accent' : 'bg-s3'
                      }`}
                    >
                      <span className={`pointer-events-none inline-block size-5 transform rounded-full bg-white shadow-sm ring-0 transition duration-200 ease-in-out ${notify ? 'translate-x-5' : 'translate-x-0'}`} />
                    </button>
                  </div>
                </div>
              </div>
            )}

            {tab === 'model' && (
              <div className="flex flex-col gap-4">
                <div className="rounded-xl border border-line bg-s1 p-4">
                  <div className="text-xs font-semibold uppercase tracking-wider text-fg4 mb-3">当前运行模型</div>
                  <div className="grid grid-cols-2 gap-3 text-xs">
                    <div className="rounded-lg bg-s2/60 p-2.5">
                      <div className="text-fg4 mb-1">聊天层模型（快模型）</div>
                      <div className="font-mono font-medium text-fg">{info.chat_model || '默认模型'}</div>
                    </div>
                    <div className="rounded-lg bg-s2/60 p-2.5">
                      <div className="text-fg4 mb-1">Agent 模型（强模型）</div>
                      <div className="font-mono font-medium text-fg">{info.agent_model || '默认模型'}</div>
                    </div>
                  </div>
                </div>

                <div className="rounded-xl border border-line bg-s1 p-4">
                  <div className="text-xs font-semibold uppercase tracking-wider text-fg4 mb-3">策略与并发</div>
                  <div className="flex flex-col gap-2.5 text-xs">
                    <div className="flex items-center justify-between py-1 border-b border-line/50">
                      <span className="text-fg3">审批拦截策略 (Policy)</span>
                      <span className="font-mono font-medium text-fg">{info.policy || 'on-request'}</span>
                    </div>
                    <div className="flex items-center justify-between py-1 border-b border-line/50">
                      <span className="text-fg3">最大并发后台任务</span>
                      <span className="font-mono font-medium text-fg">{info.max_concurrent || 4} 个</span>
                    </div>
                    <div className="flex items-center justify-between py-1 border-b border-line/50">
                      <span className="text-fg3">无响应卡住超时判定</span>
                      <span className="font-mono font-medium text-fg">{info.stall_sec ? Math.round(info.stall_sec / 60) : 12} 分钟</span>
                    </div>
                    <div className="flex items-center justify-between py-1">
                      <span className="text-fg3">系统运行环境时区</span>
                      <span className="font-mono font-medium text-fg">{info.timezone || 'Asia/Shanghai'}</span>
                    </div>
                  </div>
                </div>
              </div>
            )}

            {tab === 'secrets' && (
              <div className="flex flex-col gap-3">
                <p className="text-xs text-fg4">
                  密钥保存在本地加密凭据库中，明文永远不会进入对话记录、SSE 流或前端网页。
                </p>
                {loadingSecrets ? (
                  <div className="py-12 text-center text-xs text-fg4">正在加载密钥列表…</div>
                ) : secrets.length === 0 ? (
                  <div className="rounded-xl border border-dashed border-line p-8 text-center text-xs text-fg4">
                    暂未录入密钥凭据
                  </div>
                ) : (
                  <div className="flex flex-col gap-2">
                    {secrets.map((sec) => (
                      <div key={sec.name} className="flex items-center justify-between rounded-xl border border-line bg-s1 p-3">
                        <div className="flex items-center gap-2.5">
                          <KeyRound size={15} className="text-accent" />
                          <div>
                            <div className="font-mono text-xs font-medium text-fg">{sec.name}</div>
                            {sec.description && <div className="text-[11px] text-fg4">{sec.description}</div>}
                          </div>
                        </div>
                        <span className="rounded-md bg-ok/10 px-2 py-0.5 text-[11px] font-medium text-ok">已安全加密</span>
                      </div>
                    ))}
                  </div>
                )}
              </div>
            )}

            {tab === 'about' && (
              <div className="flex flex-col gap-4 text-xs">
                <div className="flex items-center gap-3 rounded-xl border border-line bg-s1 p-4">
                  <div className="flex size-10 shrink-0 items-center justify-center rounded-xl bg-accent-soft text-accent font-bold text-lg">
                    🐸
                  </div>
                  <div>
                    <h4 className="text-sm font-semibold text-fg">{info.name || 'Frog Bot'}</h4>
                    <p className="text-fg4 mt-0.5">单个 AI 助理 · Onefrog AI 架构独立版</p>
                  </div>
                </div>
                <div className="rounded-xl border border-line bg-s1 p-4 leading-relaxed text-fg3 space-y-2">
                  <p>• 遵循 AGPL-3.0 开源协议，所有对话与执行数据均保存在本地工作区。</p>
                  <p>• 左侧状态栏管理各个 Bot，设置常驻左下角，顶部黑白切换与通知悬浮于页面右上角。</p>
                </div>
              </div>
            )}
          </div>
        </div>

        {/* Footer */}
        <div className="flex h-14 items-center justify-end border-t border-line bg-s2/30 px-6">
          <button
            onClick={onClose}
            className="rounded-xl bg-accent px-5 py-2 text-xs font-medium text-on-accent transition hover:bg-accent-hover shadow-xs"
          >
            完成
          </button>
        </div>
      </div>
    </div>
  );
}
