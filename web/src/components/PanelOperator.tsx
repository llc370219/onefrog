import { FormEvent, useState } from "react";
import { Bot, LoaderCircle } from "lucide-react";
import type { BotRow, ChatStep, Choice, SettingsState } from "../api";
import ChoiceCard from "./ChoiceCard";
import { Button } from "./ui";

export type PanelJob = {
  pending: boolean;
  prompt: string;
  text: string;
  error: string;
  steps: ChatStep[];
  sessionId: string;
  botId: string;
  messageId?: string;
  choice?: Choice;
  serverId?: string;
};

export function modelReady(settings: SettingsState | null, bots: BotRow[] = []) {
  if (!settings) return { ok: false, label: "正在读取模型配置" };
  const lead = bots.find((item) => item.id === "main" && item.enabled) || bots.find((item) => item.enabled && item.id !== "module-builder");
  const routed = lead?.effective_route;
  const route = routed?.provider_id && routed.model ? routed : settings.default;
  const provider = settings.providers.find((item) => item.id === route?.provider_id && item.enabled);
  if (!provider || !route?.model) return { ok: false, label: "模型还没接上" };
  if (provider.auth_required && !provider.auth_kind?.startsWith("oauth-") && !provider.has_api_key) {
    return { ok: false, label: "渠道还没有密钥" };
  }
  return { ok: true, label: provider.name + " · " + route.model };
}

function brief(text?: string) {
  const value = (text || "").trim();
  if (value.length <= 280) return value;
  return value.slice(0, 280) + "…";
}

const orders = [
  { label: "检查面板", text: "检查这个管理面板。调用 panel overview，并对每台已接入服务器调用 test_server。只汇报哪些在线、哪些要核对主机指纹、模型有没有接上。不要改模块，不要升级。" },
  { label: "处理告警", text: "处理面板上需要关注的事。离线或连不上的服务器用 panel test_server 说明原因。不要删除文件，不要清理磁盘，不要升级。" },
  { label: "检查更新", text: "调用 panel 的 check_update。只汇报有没有新版本，不要调用 apply_update。" },
];

export default function PanelOperator({ settings, bots, sending, job, onRun, onAnswer, onSettings }: {
  settings: SettingsState | null;
  bots: BotRow[];
  sending: boolean;
  job: PanelJob | null;
  onRun: (text: string, serverId?: string) => void;
  onAnswer: (values: string[], custom: string) => void;
  onSettings: () => void;
}) {
  const ready = modelReady(settings, bots);
  const [text, setText] = useState("");
  const busy = sending || !!job?.pending;
  function submit(event: FormEvent) {
    event.preventDefault();
    const next = text.trim();
    if (!next || busy) return;
    setText("");
    onRun("直接操作管理面板，不要只回复步骤：" + next);
  }
  return (
    <section className="panel-operator">
      <div className="panel-operator-head">
        <div className="launch-icon"><Bot size={23} strokeWidth={1.4} /></div>
        <div>
          <strong>{ready.ok ? "助理可以直接改这个面板" : ready.label}</strong>
          <p>{ready.ok ? ready.label + "。点一下就执行，结果留在面板上。" : "先接上渠道和模型。接上之前，这里不会把你送进对话。"}</p>
        </div>
        {!ready.ok && <Button variant="primary" onClick={onSettings}>去接模型</Button>}
      </div>
      {ready.ok && (
        <>
          <div className="operator-actions">
            {orders.map((item) => <Button key={item.label} disabled={busy} onClick={() => onRun(item.text)}>{item.label}</Button>)}
          </div>
          <form className="operator-form" onSubmit={submit}>
            <input aria-label="让助理操作面板" value={text} disabled={busy} placeholder="例如：启用磁盘对比，并检查有没有新版本" onChange={(event) => setText(event.target.value)} />
            <Button variant="primary" type="submit" busy={busy} disabled={!text.trim()}>执行</Button>
          </form>
        </>
      )}
      {job && (job.pending || job.error || job.text || job.steps.length > 0 || job.choice) && (
        <div className="operator-result" aria-live="polite">
          {job.pending && <p className="operator-pending"><LoaderCircle size={14} className="spin" />正在操作面板</p>}
          {job.error && <p className="operator-error">{job.error}</p>}
          {job.steps.map((step, index) => (
            <div className="operator-step" key={index}>
              <strong>{step.kind === "delegate" ? (step.bot || "") + " → " + (step.target || "") : step.command || step.task || "操作"}</strong>
              {step.output && <pre>{brief(step.output)}</pre>}
            </div>
          ))}
          {job.text && <p className="operator-text">{job.text}</p>}
          {job.choice && <ChoiceCard choice={job.choice} disabled={busy} onAnswer={onAnswer} />}
        </div>
      )}
    </section>
  );
}
