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

export type Mod = { id: string; name: string; desc: string; enabled: boolean; type?: string };
export type AssetInput = { server_id?: string; name: string; provider?: string; region?: string; plan?: string; cycle?: string; currency?: string; amount_cents: number; paid_at?: string; starts_at?: string; expires_at?: string; auto_renew?: boolean; status?: string; ip?: string; specs?: string; traffic_gb?: number; traffic_mode?: string; order_no?: string; account?: string; invoice_url?: string; contact?: string; tags?: string[]; notes?: string };
export type AssetRecord = AssetInput & { historical?:boolean; id: string; tags: string[]; days_total: number; days_left: number; elapsed_ratio: number; residual_cents: number; daily_cents: number; derived_status: string; created_at: string; updated_at: string };
export type AssetSummary = { subscriptions:number; historical:number; count: number; active: number; expiring: number; expired: number; auto_renew: number; monthly_cents: Record<string, number>; residual_cents: Record<string, number>; next_expiry: string; next_name: string };
export type AssetPage = { summary: AssetSummary; records: AssetRecord[] };
export type DevModuleRecord = { id: string; created_at: string; updated_at: string; data: Record<string, unknown> };
export type DevModuleData = { state: Record<string, unknown>; records: DevModuleRecord[]; record?: DevModuleRecord };
export type DevModuleRow = { id: string; name: string; description?: string; html: string; enabled: boolean; record_count: number; state_keys?: string[]; last_record_at?: string; created_at: string; updated_at: string };
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
  kind: "delegate" | "message" | "tool";
  bot?: string;
  target?: string;
  task?: string;
  server?: string;
  command?: string;
  ok?: boolean;
  output?: string;
};
export type ChoiceOption = { label: string; value: string };
export type ChoiceAnswer = { values?: string[]; custom?: string; answered_at?: string };
/** A question the bot asked with ask_user; answered once, then read-only. */
export type Choice = { prompt: string; options: ChoiceOption[]; multi_select?: boolean; allow_custom?: boolean; answer?: ChoiceAnswer };
export type ChoiceReply = { message_id: string; values?: string[]; custom?: string };
/** A password card. The value is never stored on the card or in the transcript. */
export type SecretRequest = { name: string; label: string; description?: string; provided_at?: string };
export type SecretReply = { message_id: string; value: string };
export type ApprovalCardData = {
  id: string;
  bot_name?: string;
  server_id?: string;
  server_name?: string;
  command: string;
  mode?: string;
  risk?: string;
  reasons?: string[];
  model_reason?: string;
  untrusted?: string;
  status: 'approved' | 'always' | 'denied' | 'timeout' | string;
  note?: string;
};
export type TurnItem = {
  kind: 'say' | 'step' | 'delegate' | 'message' | 'approval' | 'approval_pending' | string;
  bot?: string;
  text?: string;
  target?: string;
  task?: string;
  name?: string;
  command?: string;
  server_id?: string;
  ok?: boolean;
  output?: string;
  approval?: ApprovalCardData;
  pending_req?: ApprovalPrompt;
};
export type ChatLine = {
  who: "user" | "bot";
  text: string;
  id?: string;
  choice?: Choice;
  secret?: SecretRequest;
  pending?: boolean;
  /** Set when another bot wrote this line into the inbox with message_agent. */
  fromBot?: string;
  activeStatus?: string;
  steps?: ChatStep[];
  timeline?: TurnItem[];
  attachments?:Attachment[];
};
export type TokenUsage={requests:number;reported_calls:number;failed_calls:number;input_tokens:number;output_tokens:number;total_tokens:number};
export type UsageRow=TokenUsage&{bot_id:string;purpose:string;provider_id:string;model:string;updated_at:string};
export type Attachment={id:string;name:string;mime:string;size:number};
/** direct: one-to-one chat with a single bot and its own memory. team (default): the lead bot pulls other bots in. */
export type ChatMode='direct'|'team';
export type ChatOptions={approval_route?:ModelRoute;model_route?:ModelRoute;excluded_bot_ids?:string[];approval_mode?:string;attachment_ids?:string[];mode?:ChatMode};
export type ChatRequest=ChatOptions&{content:string;server_id?:string;bot_id?:string;session_id?:string;choice_answer?:ChoiceReply;secret_answer?:SecretReply};
export type ChatResult={session_id:string;text:string;message_id?:string;choice?:Choice;secret_request?:SecretRequest;timeline?:TurnItem[];answered?:boolean};
export type BotRow = { id:string;name:string;description:string;system_prompt?:string;enabled:boolean;model_route:ModelRoute;effective_route:ModelRoute;usage:TokenUsage;memory_count?:number };
export type BotMemory = { id:string;bot_id:string;title:string;body:string;source:string;created_at:string;updated_at:string };

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
  /** "stable" (正式版) or "beta" (测试版); older servers omit it. */
  channel?: 'stable' | 'beta';
  checked_at?: string;
  error?: string;
  install_dir?: string;
  project?: string;
  can_apply?: boolean;
  /** True when the host helper cannot switch a stable install onto a beta image. */
  helper_stale?: boolean;
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

export type ServiceItem = {kind: 'systemd' | 'docker' | 'ports'; name: string; state: string; detail: string; image?: string; ports?: string};
export type ServiceSource = {status: 'ok' | 'error' | 'unavailable'; detail: string; truncated: boolean};
export type ServiceView = {server_id: string; status: 'pending' | 'ok' | 'partial' | 'failed'; collected_at?: string; attempted_at?: string; error?: string; items: ServiceItem[]; sources: Record<string, ServiceSource>};
export type WatchTarget = {kind: ServiceItem['kind']; name: string};
export type WatchState = 'pending' | 'ok' | 'fault' | 'missing' | 'unknown';
export type WatchStatus = WatchTarget & {server_id: string; state: WatchState; detail: string; checked_at?: string; changed_at?: string};
export type ServiceWatch = {interval_seconds: number; servers: Record<string, WatchTarget[]>; status: WatchStatus[]};
export type AutomationKind = 'interval' | 'daily' | 'weekly' | 'once';
export type AutomationSchedule = {kind: AutomationKind; minutes?: number; time?: string; weekday?: number; at?: string};
export type AutomationMode = '' | 'smart' | 'model' | 'plan';
export type Automation = {id: string; name: string; prompt: string; bot_id: string; server_id: string; schedule: AutomationSchedule; approval_mode: AutomationMode; enabled: boolean; owner_id: string; next_run_at?: string; last_run_at?: string; last_status?: 'running' | 'success' | 'failed'; last_error?: string; last_summary?: string; session_id?: string; created_at: string; updated_at: string};
export type AutomationInput = Pick<Automation, 'name' | 'prompt' | 'bot_id' | 'server_id' | 'schedule' | 'approval_mode' | 'enabled'>;

export const api = {
 async automations():Promise<Automation[]>{return (await call('/api/automations'))?.items||[];},
 async createAutomation(body:AutomationInput):Promise<Automation>{return call('/api/automations',{method:'POST',body:JSON.stringify(body)});},
 async updateAutomation(id:string,body:AutomationInput):Promise<Automation>{return call('/api/automations/'+encodeURIComponent(id),{method:'PUT',body:JSON.stringify(body)});},
 async deleteAutomation(id:string){return call('/api/automations/'+encodeURIComponent(id),{method:'DELETE'});},
 async runAutomation(id:string):Promise<Automation>{return call('/api/automations/'+encodeURIComponent(id)+'/run',{method:'POST',body:'{}'});},
 async services():Promise<{nodes:ServiceView[]}>{return call('/api/services');},
 async serviceWatch():Promise<ServiceWatch>{return call('/api/services/watch');},
 async saveServiceWatch(body:{interval_seconds:number;servers:Record<string,WatchTarget[]>}):Promise<ServiceWatch>{return call('/api/services/watch',{method:'PUT',body:JSON.stringify(body)});},
 async scanServices(id:string):Promise<ServiceView>{return call('/api/servers/'+encodeURIComponent(id)+'/services/scan',{method:'POST',body:'{}'});},
 async oauth():Promise<OAuthView>{return call('/api/oauth');},
 async oauthStart(kind:string,consent_version:string,risk_acknowledged:boolean):Promise<OAuthLogin>{return call('/api/oauth/start',{method:'POST',body:JSON.stringify({kind,consent_version,risk_acknowledged})});},
 async oauthStatus(id:string):Promise<OAuthLogin>{return call('/api/oauth/login/'+encodeURIComponent(id));},
 async oauthCallback(id:string,url:string){return call('/api/oauth/login/'+encodeURIComponent(id)+'/callback',{method:'POST',body:JSON.stringify({url})});},
 async oauthCancel(id:string){return call('/api/oauth/login/'+encodeURIComponent(id)+'/cancel',{method:'POST'});},
 async oauthDisconnect(kind:string){return call('/api/oauth/'+encodeURIComponent(kind)+'/disconnect',{method:'POST'});},
 async enrollmentDefaults():Promise<{report_base:string;interval:number;configure:boolean;provinces?:Province[]}>{return call('/api/servers/onboarding/defaults');},
 async enroll(body:EnrollmentRequest):Promise<Enrollment>{return call('/api/servers/onboarding',{method:'POST',body:JSON.stringify(body)});},
 async enrollment(id:string):Promise<Enrollment>{return call('/api/servers/onboarding/'+encodeURIComponent(id));},
 async confirmEnrollment(id:string,fingerprint:string):Promise<Enrollment>{return call('/api/servers/onboarding/'+encodeURIComponent(id)+'/confirm',{method:'POST',body:JSON.stringify({fingerprint})});},
 async upload(file:File):Promise<Attachment>{const data=new FormData();data.append('file',file);const res=await fetch('/api/attachments',{method:'POST',credentials:'include',headers:{[CSRF_HEADER]:'1'},body:data});const body=await res.json();if(!res.ok)throw new Error(body.error||'上传失败');return body;},
 async updateBot(id:string,body:{name:string;description:string;system_prompt:string;model_route:ModelRoute}){return call('/api/bots/'+encodeURIComponent(id),{method:'PUT',body:JSON.stringify(body)});},
 async botEnabled(id:string,enabled:boolean){return call('/api/bots/'+encodeURIComponent(id)+'/enabled',{method:'PUT',body:JSON.stringify({enabled})});},
 async memories(id:string):Promise<BotMemory[]>{return (await call('/api/bots/'+encodeURIComponent(id)+'/memories'))||[];},
 async addMemory(id:string,body:{title:string;body:string}):Promise<BotMemory>{return call('/api/bots/'+encodeURIComponent(id)+'/memories',{method:'POST',body:JSON.stringify(body)});},
 async deleteMemory(id:string,memoryID:string){return call('/api/bots/'+encodeURIComponent(id)+'/memories/'+encodeURIComponent(memoryID),{method:'DELETE'});},
 async deleteBot(id:string){return call('/api/bots/'+encodeURIComponent(id),{method:'DELETE'});},
 async usage():Promise<UsageRow[]>{return call('/api/usage');},
  async monitor():Promise<ProbeView>{return call("/api/monitor");},
  async probeToken(id:string,opts:{province?:string;targets?:string[]}={}):Promise<{token:string;server_id:string;ping_targets:PingTarget[]}>{return call("/api/servers/"+encodeURIComponent(id)+"/probe/token",{method:"POST",body:JSON.stringify(opts)});},
  async probeProvince(id:string,province:string,pingIntervalSeconds=60):Promise<{message:string}>{return call('/api/servers/'+encodeURIComponent(id)+'/probe/province',{method:'POST',body:JSON.stringify({province,ping_interval_seconds:pingIntervalSeconds})});},
  async profile(body:{username:string;display_name:string;avatar:string;current_password:string;new_password:string}):Promise<Profile>{return call("/api/auth/profile",{method:"PUT",body:JSON.stringify(body)});},
  async sessions():Promise<ChatSession[]>{return (await call("/api/sessions"))||[];},
  async messages(id:string):Promise<{id:string;role:string;content:string;from_bot?:string;attachments?:Attachment[];choice?:Choice;secret_request?:SecretRequest;timeline?:TurnItem[]}[]>{return (await call("/api/sessions/"+encodeURIComponent(id)+"/messages"))||[];},
  async deleteSession(id:string){return call("/api/sessions/"+encodeURIComponent(id),{method:"DELETE"});},
  async updateStatus():Promise<UpdateStatus>{return call("/api/update");},
  async checkUpdate():Promise<UpdateStatus>{return call("/api/update/check",{method:"POST",body:"{}"});},
  async applyUpdate():Promise<{ok:boolean;version:string;message:string}>{return call("/api/update/apply",{method:"POST",body:"{}"});},
  async setUpdateCheck(enabled:boolean):Promise<UpdateStatus>{return call("/api/settings/update",{method:"PUT",body:JSON.stringify({enabled})});},
  async setUpdateChannel(channel:'stable'|'beta'):Promise<UpdateStatus>{return call("/api/settings/update",{method:"PUT",body:JSON.stringify({channel})});},
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
  async rotateSecret(id: string, body: { value: string; description?: string }) {
    return call("/api/secrets/" + encodeURIComponent(id), { method: "PUT", body: JSON.stringify(body) });
  },
  /** The server draws and encrypts the value; the response carries only metadata. */
  async generateSecret(body: { name: string; description?: string; length: number; charset: string }) {
    return call("/api/secrets/generate", { method: "POST", body: JSON.stringify(body) });
  },
  async regenerateSecret(id: string, body: { description?: string; length: number; charset: string }) {
    return call("/api/secrets/" + encodeURIComponent(id) + "/generate", { method: "POST", body: JSON.stringify(body) });
  },
  async deleteSecret(id: string) {
    return call("/api/secrets/" + encodeURIComponent(id), { method: "DELETE" });
  },
  async modules(): Promise<Mod[]> {
    const rows = (await call("/api/modules")) as { id: string; name: string; description: string; enabled: boolean; type?: string }[];
    return rows.map((item) => ({ id: item.id, name: item.name, desc: item.description, enabled: item.enabled, type: item.type }));
  },
  async enableModule(id: string, enabled: boolean) {
    return call("/api/modules/" + id + "/enable", { method: "POST", body: JSON.stringify({ enabled }) });
  },
  async assets(): Promise<AssetPage> {
    const data = (await call("/api/assets")) as AssetPage;
    return { summary: data.summary, records: data.records || [] };
  },
  async assetRates():Promise<import('./lib/subscriptions').ExchangeRates>{return call('/api/assets/rates');},
  async saveAsset(body: AssetInput, id = "") {
    return call("/api/assets" + (id ? "/" + encodeURIComponent(id) : ""), { method: id ? "PUT" : "POST", body: JSON.stringify(body) });
  },
  async deleteAsset(id: string) {
    return call("/api/assets/" + encodeURIComponent(id), { method: "DELETE" });
  },
  async devModules(): Promise<DevModuleRow[]> {
    return ((await call("/api/dev/modules")) as DevModuleRow[]) || [];
  },
  async createDevModule(body: { name: string; description?: string; html?: string; use_starter?: boolean; enabled?: boolean }) {
    return call("/api/dev/modules", { method: "POST", body: JSON.stringify(body) }) as Promise<DevModuleRow>;
  },
  async updateDevModule(id: string, body: { name?: string; description?: string; html?: string; enabled?: boolean }) {
    return call("/api/dev/modules/" + encodeURIComponent(id), { method: "PUT", body: JSON.stringify(body) }) as Promise<DevModuleRow>;
  },
  async deleteDevModule(id: string) {
    return call("/api/dev/modules/" + encodeURIComponent(id), { method: "DELETE" });
  },
  async devModuleData(id: string, body: { action: string; id?: string; state?: Record<string, unknown>; data?: Record<string, unknown> }) {
    return call("/api/dev/modules/" + encodeURIComponent(id) + "/data", { method: "POST", body: JSON.stringify(body) }) as Promise<DevModuleData>;
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
  chatLive(body: ChatRequest, handlers: ((step: ChatStep) => void) | LiveChatHandlers) {
    return liveChat(body, typeof handlers === "function" ? { onStep: handlers } : handlers);
  },
};

export type ApprovalDecision = { allow: boolean; always?: boolean };
export type LiveChatHandlers = {
  onStep?: (step: ChatStep) => void;
  onSay?: (bot: string, content: string) => void;
  onApproval?: (req: ApprovalPrompt) => Promise<ApprovalDecision>;
  onApprovalResolved?: (card: ApprovalCardData) => void;
  onApprovalTimeout?: (id: string) => void;
};
type ChatReply = ChatResult & {
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
  if (raw.kind === "delegate" || raw.kind === "message") {
    return { kind: raw.kind, bot: raw.bot, target: raw.target, task: raw.task };
  }
  return { kind: "tool", bot: raw.bot, server: raw.server_id, command: raw.command, ok: raw.ok, output: raw.output };
}

/** One server-side rule hit from the command classifier. */
export type RiskFinding = { category: string; label: string; detail: string };
/** A command the agent wants to run that needs the operator's decision (smart or manual approval mode). */
export type ApprovalPrompt = { id: string; bot: string; bot_name?: string; server_id: string; server_name: string; command: string; warning?: string; mode?: string; risk?: 'low'|'medium'|'high'; reasons?: string[]; findings?: RiskFinding[]; model_risk?: string; model_reason?: string; untrusted?: string; timeout_seconds: number };
/** Approval modes. "" = not chosen yet (behaves like smart). */
export type ApprovalMode = ''|'off'|'smart'|'manual'|'model'|'plan';
let approvalHandler: ((req: ApprovalPrompt) => Promise<boolean>) | null = null;
/** Registers the UI that asks the operator to allow or deny a command. */
export function setApprovalHandler(fn: ((req: ApprovalPrompt) => Promise<boolean>) | null) { approvalHandler = fn; }

function liveChat(body: ChatRequest, handlers: LiveChatHandlers) {
  return new Promise<ChatResult>((resolve, reject) => {
    let opened = false;
    let settled = false;
    let session = "";
    let text = "";
    let extra: Omit<ChatResult, "session_id" | "text"> = {};
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
          for (const step of res.steps || []) handlers.onStep?.(toStep(step));
          resolve({ session_id: res.session_id, text: res.text, message_id: res.message_id, choice: res.choice || undefined, secret_request: res.secret_request || undefined, timeline: res.timeline, answered: res.answered });
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
      resolve({ session_id: session, text, ...extra });
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
        const ask: Promise<ApprovalDecision> = handlers.onApproval
          ? handlers.onApproval(req)
          : approvalHandler
          ? approvalHandler(req).then((allow) => ({ allow }))
          : Promise.resolve({ allow: window.confirm("允许执行以下命令？\n" + req.command + ((req.reasons || []).length ? "\n⚠ " + (req.reasons || []).join("\n⚠ ") : req.warning ? "\n⚠ " + req.warning : "")) });
        ask.then(
          (dec) => {
            if (!settled) {
              ws.send(JSON.stringify({ type: "approval", id: req.id, allow: dec.allow, reason: dec.always ? "always" : "" }));
              arm(180000);
            }
          },
          () => { if (!settled) ws.send(JSON.stringify({ type: "approval", id: req.id, allow: false })); }
        );
        return;
      }
      if (msg.type === "approval_timeout") {
        handlers.onApprovalTimeout?.(msg.id);
        if (approvalHandler) approvalTimeout(msg.id);
        return;
      }
      if (msg.type === "say") {
        handlers.onSay?.(msg.bot || "", msg.content || "");
        return;
      }
      if (msg.type === "approval_resolved" && msg.approval) {
        handlers.onApprovalResolved?.(msg.approval as ApprovalCardData);
        return;
      }
      if (msg.type === "session") session = msg.session_id || "";
      if (msg.type === "delegate" || msg.type === "message" || msg.type === "tool") handlers.onStep?.(toStep({
        kind: msg.type,
        bot: msg.bot,
        target: msg.target,
        task: msg.task,
        command: msg.command,
        server_id: msg.server_id,
        ok: msg.ok,
        output: msg.output,
      }));
      if (msg.type === "assistant") { text = msg.content || ""; extra = { message_id: msg.message_id || undefined, choice: msg.choice || undefined, secret_request: msg.secret_request || undefined, timeline: msg.timeline || undefined, answered: !!msg.answered }; }
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
 approval:{mode:ApprovalMode;route:ModelRoute;instructions:string;smart_route?:ModelRoute};
 execution:{timeout_seconds:number;command_timeout_seconds:number;max_steps:number;context_messages:number};
};
export type ApprovalRow = {id:string;created_at:string;bot_name:string;server_name:string;command:string;status:string;review:{allow:boolean;reason:string;risk:string;model:string;provider_id:string;mode:string;findings?:RiskFinding[];model_reason?:string;untrusted?:string}};

export type PingMetric={method?:'tcp'|'icmp';measured_at?:string;next_check_at?:string;name:string;target:string;latency_ms:number|null;loss_percent:number|null;samples:number};
export type ProbeSample={received_at:string;hostname:string;os:string;arch:string;cpu_model:string;cpu_cores:number;cpu_percent:number;memory_used:number;memory_total:number;swap_used:number;swap_total:number;disk_used:number;disk_total:number;rx_bytes:number;tx_bytes:number;rx_rate:number;tx_rate:number;uptime_seconds:number;load:number[];tcp_connections:number;processes:number;pings:PingMetric[]};
export type Province={code:string;name:string};
/** This calendar month's interface traffic; since = when counting started in the month. */
export type ProbeTraffic={period:string;since:string;rx:number;tx:number;prev_period?:string;prev_rx:number;prev_tx:number};
export type PingTarget={name:string;host:string;port?:number};
export type ProbeNode={ping_interval_seconds?:number;server_id:string;status:'online'|'offline'|'pending';enrolled:boolean;province?:Province;traffic?:ProbeTraffic;latest:ProbeSample|null;history:ProbeSample[]};
export type ProbeView={nodes:ProbeNode[];stale_after_seconds:number;history_limit:number};

export type OAuthDefinition={kind:string;name:string;base_url:string;experimental:boolean;scopes:string;configured:boolean;setup_hint?:string;setup_doc?:string};
export type OAuthAccount={kind:string;name:string;status:string;email:string;expires_at:string;message?:string};
export type OAuthView={definitions:OAuthDefinition[];accounts:OAuthAccount[];consent_version:string;acknowledgements?:Record<string,{at:string;version:string}>};
export type OAuthLogin={id:string;kind:string;status:string;url?:string;redirect_uri:string;expires_at:string;message?:string};
export type EnrollmentRequest={request_id:string;server_id?:string;name:string;host:string;port:number;username:string;auth_type:string;secret:string;passphrase:string;role:string;configure:boolean;report_base:string;interval:number;targets:string[];province?:string};
export type Enrollment={id:string;server_id:string;status:string;steps:{id:string;label:string;status:string;message?:string}[];message?:string;fingerprint?:string};
