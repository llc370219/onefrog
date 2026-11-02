import { FormEvent, useState } from "react";
import { Bot } from "lucide-react";
import type { ApprovalDecision, BotRow, ChatStep, Choice, Host, SecretRequest, SettingsState, TurnItem } from "../api";
import TurnStream from "./TurnStream";
import { Button } from "./ui";

export type PanelJob = {
  pending: boolean;
  prompt: string;
  text: string;
  error: string;
  activeStatus?: string;
  steps: ChatStep[];
  timeline?: TurnItem[];
  sessionId: string;
  botId: string;
  messageId?: string;
  choice?: Choice;
  secret?: SecretRequest;
  serverId?: string;
};

export function modelReady(settings: SettingsState | null, bots: BotRow[] = []) {
  if (!settings) return { ok: false, label: "正在读取模型配置" };
  const lead = bots.find((item) => item.id === "main" && item.enabled) || bots.find((item) => item.enabled && item.id !== "module-builder" && item.id !== "develop");
  const routed = lead?.effective_route;
  const route = routed?.provider_id && routed.model ? routed : settings.default;
  const provider = settings.providers.find((item) => item.id === route?.provider_id && item.enabled);
  if (!provider || !route?.model) return { ok: false, label: "模型还没接上" };
  if (provider.auth_required && !provider.auth_kind?.startsWith("oauth-") && !provider.has_api_key) {
    return { ok: false, label: "渠道还没有密钥" };
  }
  return { ok: true, label: provider.name + " · " + route.model };
}

const orders = [
  { label: "检查面板", text: "检查这个管理面板。调用 panel overview，并对每台已接入服务器调用 test_server。只汇报哪些在线、哪些要核对主机指纹、模型有没有接上。不要改模块，不要升级。" },
  { label: "处理告警", text: "处理面板上需要关注的事。离线或连不上的服务器用 panel test_server 说明原因。不要删除文件，不要清理磁盘，不要升级。" },
  { label: "检查更新", text: "调用 panel 的 check_update。只汇报有没有新版本，不要调用 apply_update。" },
];

export default function PanelOperator({
  settings,
  bots,
  hosts = [],
  sending,
  job,
  onRun,
  onAnswer,
  onProvide,
  onDecideApproval,
  onSettings,
}: {
  settings: SettingsState | null;
  bots: BotRow[];
  hosts?: Host[];
  sending: boolean;
  job: PanelJob | null;
  onRun: (text: string, serverId?: string) => void;
  onAnswer: (values: string[], custom: string) => void;
  onProvide: (value: string) => void;
  onDecideApproval?: (id: string, dec: ApprovalDecision) => void;
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
    onRun("直接操作管理面板，一步步反馈：" + next);
  }

  const hasOutput = Boolean(
    job && (job.pending || job.error || job.text || (job.timeline && job.timeline.length > 0) || job.choice || job.secret)
  );

  return (
    <section className="panel-operator">
      <div className="panel-operator-head">
        <div className="launch-icon"><Bot size={22} strokeWidth={1.5} /></div>
        <div>
          <strong>{ready.ok ? "助理可以直接改这个面板" : ready.label}</strong>
          <p>{ready.ok ? ready.label + " · 点一下就逐步执行，遇到审批或密钥需求会直接弹出卡片。" : "先接上渠道和模型。接上之前，这里不会把你送进对话。"}</p>
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
      {hasOutput && job && (
        <div className="operator-result" aria-live="polite">
          {job.error && <p className="operator-error">{job.error}</p>}
          {!job.error && (
            <TurnStream
              timeline={job.timeline || []}
              text={job.text}
              pending={job.pending}
              activeStatus={job.activeStatus}
              hosts={hosts}
              choice={job.choice}
              secret={job.secret}
              disabled={busy}
              animateFinal={!job.pending}
              onDecideApproval={onDecideApproval}
              onAnswerChoice={onAnswer}
              onProvideSecret={onProvide}
            />
          )}
        </div>
      )}
    </section>
  );
}
