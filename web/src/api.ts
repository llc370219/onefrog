export type Profile = {id:string;username:string;display_name:string;avatar:string;must_change_credentials?:boolean;approval_choice_required?:boolean};
export type ChatSession = {id:string;bot_id:string;title:string;preview:string;updated_at:string;options?:ChatOptions};
export type Host = {
  id: string;
  name: string;
  host: string;
  up: boolean;
  disk: number;
  load: string;
  role: string;
  status: string;
  username: string;
  port: number;
  auth_type: string;
};

export type Mod = { id: string; name: string; desc: string; enabled: boolean };
export type AssetInput = { server_id?: string; name: string; provider?: string; region?: string; plan?: string; cycle?: string; currency?: string; amount_cents: number; paid_at?: string; starts_at?: string; expires_at?: string; auto_renew?: boolean; status?: string; ip?: string; specs?: string; order_no?: string; account?: string; invoice_url?: string; contact?: string; tags?: string[]; notes?: string };
export type AssetRecord = AssetInput & { id: string; tags: string[]; days_total: number; days_left: number; elapsed_ratio: number; residual_cents: number; daily_cents: number; derived_status: string; created_at: string; updated_at: string };
export type AssetSummary = { count: number; active: number; expiring: number; expired: number; auto_renew: number; monthly_cents: Record<string, number>; residual_cents: Record<string, number>; next_expiry: string; next_name: string };
export type AssetPage = { summary: AssetSummary; records: AssetRecord[] };
export type Hist = {
  id: string;
  time: string;
  bot: string;
  server: string;
  action: string;
  ok: boolean;
  undo: boolean;
  blocked: boolean;
  status: string;
};
export type SecretRow = { id: string; name: string; desc: string };
export type ChatStep = {
  kind: "delegate" | "tool";
  bot?: string;
  target?: string;
  task?: string;
  server?: string;
  command?: string;
  ok?: boolean;
  output?: string;
};
export type ChatLine = {
  who: "user" | "bot";
  text: string;
  pending?: boolean;
  steps?: ChatStep[];
  attachments?:Attachment[];
};
export type TokenUsage={requests:number;reported_calls:number;failed_calls:number;input_tokens:number;output_tokens:number;total_tokens:number};
export type UsageRow=TokenUsage&{bot_id:string;purpose:string;provider_id:string;model:string;updated_at:string};
export type Attachment={id:string;name:string;mime:string;size:number};
export type ChatOptions={approval_route?:ModelRoute;model_route?:ModelRoute;excluded_bot_ids?:string[];approval_mode?:string;attachment_ids?:string[]};
export type ChatRequest=ChatOptions&{content:string;server_id?:string;bot_id?:string;session_id?:string};
export type BotRow = { id:string;name:string;description:string;system_prompt?:string;enabled:boolean;model_route:ModelRoute;effective_route:ModelRoute;usage:TokenUsage };

type ServerJSON = {
  id: string;
  name: string;
  host: string;
  status: string;
  role: string;
  disk_percent: number | null;
  load: string;
  username: string;
  port: number;
  auth_type: string;
};

// Sent on every API request; the server rejects state-changing requests without it (CSRF defence).
const CSRF_HEADER = "X-OneFrog-CSRF";

/** Result of the GitHub release check (GET /api/update). */
export type UpdateStatus = {
  current: string;
  latest?: string;
  url?: string;
  notes?: string;
  published_at?: string;
  available: boolean;
  enabled: boolean;
  disabled_by?: 'env' | 'settings' | 'dev' | 'repo';
  repo: string;
  checked_at?: string;
  error?: string;
  install_dir?: string;
  project?: string;
};

async function call(path: string, init?: RequestInit) {
  const res = await fetch(path, {
    credentials: "include",
    ...init,
    headers: { "Content-Type": "application/json", [CSRF_HEADER]: "1", ...(init?.headers || {}) },
  });
  if (res.status === 204) return null;
  const text = await res.text();
  let data;
  try { data = text ? JSON.parse(text) : null; } catch { throw new Error("服务暂时不可用，请稍后重试"); }
  if (!res.ok) {
    const err = new Error(data?.error || "请求失败");
    (err as Error & { status?: number }).status = res.status;
    throw err;
  }
  return data;
}

function hm(iso: string) {
  const d = new Date(iso);
  const h = String(d.getHours()).padStart(2, "0");
  const m = String(d.getMinutes()).padStart(2, "0");
  return h + ":" + m;
}

function mapServer(item: ServerJSON): Host {
  return {
    id: item.id,
    name: item.name,
    host: item.host,
    status: item.status,
    up: item.status === "online",
    disk: item.disk_percent ?? 0,
    load: item.load || "—",
    role: item.role || "",
    username: item.username,
    port: item.port,
    auth_type: item.auth_type,
  };
}

export const api = {
 async oauth():Promise<OAuthView>{return call('/api/oauth');},
 async oauthStart(kind:string,consent_version:string,risk_acknowledged:boolean):Promise<OAuthLogin>{return call('/api/oauth/start',{method:'POST',body:JSON.stringify({kind,consent_version,risk_acknowledged})});},
 async oauthStatus(id:string):Promise<OAuthLogin>{return call('/api/oauth/login/'+encodeURIComponent(id));},
 async oauthCallback(id:string,url:string){return call('/api/oauth/login/'+encodeURIComponent(id)+'/callback',{method:'POST',body:JSON.stringify({url})});},
 async oauthCancel(id:string){return call('/api/oauth/login/'+encodeURIComponent(id)+'/cancel',{method:'POST'});},
 async oauthDisconnect(kind:string){return call('/api/oauth/'+encodeURIComponent(kind)+'/disconnect',{method:'POST'});},
 async enrollmentDefaults():Promise<{report_base:string;interval:number;configure:boolean}>{return call('/api/servers/onboarding/defaults');},
 async enroll(body:EnrollmentRequest):Promise<Enrollment>{return call('/api/servers/onboarding',{method:'POST',body:JSON.stringify(body)});},
 async enrollment(id:string):Promise<Enrollment>{return call('/api/servers/onboarding/'+encodeURIComponent(id));},
 async confirmEnrollment(id:string,fingerprint:string):Promise<Enrollment>{return call('/api/servers/onboarding/'+encodeURIComponent(id)+'/confirm',{method:'POST',body:JSON.stringify({fingerprint})});},
 async upload(file:File):Promise<Attachment>{const data=new FormData();data.append('file',file);const res=await fetch('/api/attachments',{method:'POST',credentials:'include',headers:{[CSRF_HEADER]:'1'},body:data});const body=await res.json();if(!res.ok)throw new Error(body.error||'上传失败');return body;},
 async updateBot(id:string,body:{name:string;description:string;system_prompt:string;model_route:ModelRoute}){return call('/api/bots/'+encodeURIComponent(id),{method:'PUT',body:JSON.stringify(body)});},
 async botEnabled(id:string,enabled:boolean){return call('/api/bots/'+encodeURIComponent(id)+'/enabled',{method:'PUT',body:JSON.stringify({enabled})});},
 async usage():Promise<UsageRow[]>{return call('/api/usage');},
  async monitor():Promise<ProbeView>{return call("/api/monitor");},
  async probeToken(id:string):Promise<{token:string;server_id:string}>{return call("/api/servers/"+encodeURIComponent(id)+"/probe/token",{method:"POST",body:"{}"});},
  async profile(body:{username:string;display_name:string;avatar:string;current_password:string;new_password:string}):Promise<Profile>{return call("/api/auth/profile",{method:"PUT",body:JSON.stringify(body)});},
  async sessions():Promise<ChatSession[]>{return (await call("/api/sessions"))||[];},
  async messages(id:string):Promise<{role:string;content:string;attachments?:Attachment[]}[]>{return (await call("/api/sessions/"+encodeURIComponent(id)+"/messages"))||[];},
  async updateStatus():Promise<UpdateStatus>{return call("/api/update");},
  async checkUpdate():Promise<UpdateStatus>{return call("/api/update/check",{method:"POST",body:"{}"});},
  async setUpdateCheck(enabled:boolean):Promise<UpdateStatus>{return call("/api/settings/update",{method:"PUT",body:JSON.stringify({enabled})});},
  async approvalMode():Promise<{mode:ApprovalMode;effective_mode:ApprovalMode;choice_required:boolean}>{return call("/api/settings/approval");},
  async setApprovalMode(mode:ApprovalMode):Promise<{mode:ApprovalMode}>{return call("/api/settings/approval",{method:"PUT",body:JSON.stringify({mode})});},
  async me():Promise<Profile> {
    return call("/api/auth/me");
  },
  async login(username: string, password: string): Promise<Profile> {
    return call("/api/auth/login", { method: "POST", body: JSON.stringify({ username, password }) });
  },
  async logout() { return call("/api/auth/logout", { method: "POST" }); },
  async servers(): Promise<Host[]> {
    return ((await call("/api/servers")) as ServerJSON[]).map(mapServer);
  },
  async createServer(body: { name: string; host: string; role?: string; port?: number; username?: string; auth_type?: string; secret?: string }) {
    return call("/api/servers", { method: "POST", body: JSON.stringify({ port: 22, username: "root", auth_type: "password", ...body }) });
  },
  async deleteServer(id: string, uninstallProbe: boolean): Promise<{ deleted: boolean; probe: { status: 'removed' | 'absent' | 'skipped' | 'failed' | string; detail: string } }> {
    return call("/api/servers/" + encodeURIComponent(id) + (uninstallProbe ? "?uninstall_probe=1" : ""), { method: "DELETE" });
  },
  async testServer(id: string, fingerprint?: string): Promise<ServerTestResult> {
    return call("/api/servers/" + encodeURIComponent(id) + "/test", { method: "POST", body: JSON.stringify(fingerprint ? { fingerprint } : {}) });
  },
  async resetHostKey(id: string): Promise<{ ok: boolean }> {
    return call("/api/servers/" + encodeURIComponent(id) + "/host-key/reset", { method: "POST", body: "{}" });
  },
  async secrets(): Promise<SecretRow[]> {
    const rows = (await call("/api/secrets")) as { id: string; name: string; description: string }[];
    return rows.map((item) => ({ id: item.id, name: item.name, desc: item.description }));
  },
  async createSecret(body: { name: string; value: string; description?: string }) {
    return call("/api/secrets", { method: "POST", body: JSON.stringify(body) });
  },
  async modules(): Promise<Mod[]> {
    const rows = (await call("/api/modules")) as { id: string; name: string; description: string; enabled: boolean }[];
    return rows.map((item) => ({ id: item.id, name: item.name, desc: item.description, enabled: item.enabled }));
  },
  async enableModule(id: string, enabled: boolean) {
    return call("/api/modules/" + id + "/enable", { method: "POST", body: JSON.stringify({ enabled }) });
  },
  async assets(): Promise<AssetPage> {
    const data = (await call("/api/assets")) as AssetPage;
    return { summary: data.summary, records: data.records || [] };
  },
  async saveAsset(body: AssetInput, id = "") {
    return call("/api/assets" + (id ? "/" + encodeURIComponent(id) : ""), { method: id ? "PUT" : "POST", body: JSON.stringify(body) });
  },
  async deleteAsset(id: string) {
    return call("/api/assets/" + encodeURIComponent(id), { method: "DELETE" });
  },
  async history(): Promise<Hist[]> {
    const rows = (await call("/api/history")) as {
      id: string;
      bot_name: string;
      server_name: string;
      action: string;
      status: string;
      can_rollback: boolean;
      created_at: string;
    }[];
    return rows.map((item) => ({
      id: item.id,
      time: hm(item.created_at),
      bot: item.bot_name || "—",
      server: item.server_name || "—",
      action: item.action,
      ok: item.status === "success" || item.status === "rolled_back",
      undo: item.can_rollback,
      blocked: item.status === "blocked",
      status: item.status,
    }));
  },
  async rollback(id: string) {
    return call("/api/history/" + id + "/rollback", { method: "POST", body: "{}" });
  },
  async settings(): Promise<SettingsState> { return call("/api/settings"); },
  async saveSettings(body: SettingsState): Promise<SettingsState> { return call("/api/settings", {method:"PUT",body:JSON.stringify(body)}); },
  async providerModels(body: ProviderSettings): Promise<{models:string[]}> {return call("/api/settings/providers/models",{method:"POST",body:JSON.stringify(body)});},
  async testProvider(body: ProviderSettings & {model:string}): Promise<{ok:boolean;latency_ms:number;model:string}> {return call("/api/settings/providers/test",{method:"POST",body:JSON.stringify(body)});},
  async approvals(): Promise<ApprovalRow[]> {return call("/api/approvals");},
  async createBot(body: { name: string; description: string; system_prompt: string; model_route?: ModelRoute }) {
    return call("/api/bots", { method: "POST", body: JSON.stringify(body) });
  },
  async bots(): Promise<BotRow[]> {
    return (await call("/api/bots")) as BotRow[];
  },
  async chat(body: ChatRequest) {
    return call("/api/chat", { method: "POST", body: JSON.stringify(body) }) as Promise<ChatReply>;
  },
  chatLive(body: ChatRequest, onStep: (step: ChatStep) => void) {
    return liveChat(body, onStep);
  },
};

type ChatReply = {
  session_id: string;
  text: string;
  steps?: {
    kind?: string;
    bot?: string;
    target?: string;
    task?: string;
    command?: string;
    server_id?: string;
    ok?: boolean;
    output?: string;
  }[];
};

function toStep(raw: NonNullable<ChatReply["steps"]>[number]): ChatStep {
  if (raw.kind === "delegate") {
    return { kind: "delegate", bot: raw.bot, target: raw.target, task: raw.task };
  }
  return { kind: "tool", bot: raw.bot, server: raw.server_id, command: raw.command, ok: raw.ok, output: raw.output };
}

/** One server-side rule hit from the command classifier. */
export type RiskFinding = { category: string; label: string; detail: string };
/** A command the agent wants to run that needs the operator's decision (smart or manual approval mode). */
export type ApprovalPrompt = { id: string; bot: string; server_id: string; server_name: string; command: string; warning?: string; mode?: string; risk?: 'low'|'medium'|'high'; reasons?: string[]; findings?: RiskFinding[]; model_risk?: string; model_reason?: string; untrusted?: string; timeout_seconds: number };
/** Approval modes. "" = not chosen yet (behaves like smart). */
export type ApprovalMode = ''|'off'|'smart'|'manual'|'model'|'plan';
let approvalHandler: ((req: ApprovalPrompt) => Promise<boolean>) | null = null;
/** Registers the UI that asks the operator to allow or deny a command. */
export function setApprovalHandler(fn: ((req: ApprovalPrompt) => Promise<boolean>) | null) { approvalHandler = fn; }

function liveChat(body: ChatRequest, onStep: (step: ChatStep) => void) {
  return new Promise<{ session_id: string; text: string }>((resolve, reject) => {
    let opened = false;
    let settled = false;
    let session = "";
    let text = "";
    const proto = location.protocol === "https:" ? "wss" : "ws";
    const ws = new WebSocket(proto + "://" + location.host + "/ws/chat");
    let timer = 0;
    const arm = (ms: number) => { window.clearTimeout(timer); timer = window.setTimeout(() => fail(new Error("模型没有在时间内返回")), ms); };
    const fail = (err: Error) => {
      if (settled) return;
      settled = true;
      window.clearTimeout(timer);
      ws.close();
      if (!opened) {
        api.chat(body).then((res) => {
          for (const step of res.steps || []) onStep(toStep(step));
          resolve({ session_id: res.session_id, text: res.text });
        }, reject);
        return;
      }
      reject(err);
    };
    const ok = () => {
      if (settled) return;
      settled = true;
      window.clearTimeout(timer);
      ws.close();
      resolve({ session_id: session, text });
    };
    arm(180000);
    ws.onopen = () => {
      opened = true;
      ws.send(JSON.stringify(body));
    };
    ws.onerror = () => fail(new Error("连接中断"));
    ws.onclose = () => { if (!settled) fail(new Error("连接已关闭，请检查操作记录后重试")); };
    ws.onmessage = (ev) => {
      let msg;
      try { msg = JSON.parse(String(ev.data)); } catch { fail(new Error("收到无效的服务响应")); return; }
      if (msg.type === "approval_request") {
        const req = msg as ApprovalPrompt;
        arm((req.timeout_seconds || 300) * 1000 + 180000);
        const ask = approvalHandler ? approvalHandler(req) : Promise.resolve(window.confirm("允许执行以下命令？\n" + req.command + ((req.reasons || []).length ? "\n⚠ " + (req.reasons || []).join("\n⚠ ") : req.warning ? "\n⚠ " + req.warning : "")));
        ask.then((allow) => { if (!settled) { ws.send(JSON.stringify({ type: "approval", id: req.id, allow })); arm(180000); } }, () => { if (!settled) ws.send(JSON.stringify({ type: "approval", id: req.id, allow: false })); });
        return;
      }
      if (msg.type === "approval_timeout") { approvalHandler && approvalTimeout(msg.id); return; }
      if (msg.type === "session") session = msg.session_id || "";
      if (msg.type === "delegate" || msg.type === "tool") onStep(toStep({
        kind: msg.type,
        bot: msg.bot,
        target: msg.target,
        task: msg.task,
        command: msg.command,
        server_id: msg.server_id,
        ok: msg.ok,
        output: msg.output,
      }));
      if (msg.type === "assistant") text = msg.content || "";
      if (msg.type === "error") fail(new Error(msg.message || "发送失败"));
      if (msg.type === "done") ok();
    };
  });
}

let approvalTimeoutHandler: ((id: string) => void) | null = null;
function approvalTimeout(id: string) { approvalTimeoutHandler?.(id); }
/** Called when the server gave up waiting for a decision. */
export function setApprovalTimeoutHandler(fn: ((id: string) => void) | null) { approvalTimeoutHandler = fn; }

export type ServerTestResult = { ok: boolean; status: "online" | "offline" | "host_key_unknown" | "host_key_mismatch" | string; detail: string; fingerprint?: string; stored_fingerprint?: string };

export type ModelRoute = {provider_id:string;model:string};
export type ProviderSettings = {auth_kind?:string;id:string;name:string;base_url:string;enabled:boolean;auth_required:boolean;models:string[];hidden_models?:string[];has_api_key:boolean;api_key?:string;clear_api_key?:boolean};
export type SettingsState = {
 disabled_bots?:string[];
 providers:ProviderSettings[];
 default:ModelRoute;
 worker:ModelRoute;
 bot_routes:Record<string,ModelRoute>;
 approval:{mode:ApprovalMode;route:ModelRoute;instructions:string};
 execution:{timeout_seconds:number;command_timeout_seconds:number;max_steps:number;context_messages:number};
};
export type ApprovalRow = {id:string;created_at:string;bot_name:string;server_name:string;command:string;status:string;review:{allow:boolean;reason:string;risk:string;model:string;provider_id:string;mode:string;findings?:RiskFinding[];model_reason?:string;untrusted?:string}};

export type PingMetric={name:string;target:string;latency_ms:number|null;loss_percent:number|null;samples:number};
export type ProbeSample={received_at:string;hostname:string;os:string;arch:string;cpu_model:string;cpu_cores:number;cpu_percent:number;memory_used:number;memory_total:number;swap_used:number;swap_total:number;disk_used:number;disk_total:number;rx_bytes:number;tx_bytes:number;rx_rate:number;tx_rate:number;uptime_seconds:number;load:number[];tcp_connections:number;processes:number;pings:PingMetric[]};
export type ProbeNode={server_id:string;status:'online'|'offline'|'pending';enrolled:boolean;latest:ProbeSample|null;history:ProbeSample[]};
export type ProbeView={nodes:ProbeNode[];stale_after_seconds:number;history_limit:number};

export type OAuthDefinition={kind:string;name:string;base_url:string;experimental:boolean;scopes:string;configured:boolean;setup_hint?:string;setup_doc?:string};
export type OAuthAccount={kind:string;name:string;status:string;email:string;expires_at:string;message?:string};
export type OAuthView={definitions:OAuthDefinition[];accounts:OAuthAccount[];consent_version:string;acknowledgements?:Record<string,{at:string;version:string}>};
export type OAuthLogin={id:string;kind:string;status:string;url?:string;redirect_uri:string;expires_at:string;message?:string};
export type EnrollmentRequest={request_id:string;server_id?:string;name:string;host:string;port:number;username:string;auth_type:string;secret:string;passphrase:string;role:string;configure:boolean;report_base:string;interval:number;targets:string[]};
export type Enrollment={id:string;server_id:string;status:string;steps:{id:string;label:string;status:string;message?:string}[];message?:string;fingerprint?:string};
