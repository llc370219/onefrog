# 免责声明 / Disclaimer

[中文](#中文) · [English](#english)

## 中文

### 1. 与任何服务提供方无关

OneFrog 是独立的开源项目，与 OpenAI、Google 或任何模型、云服务提供方**没有隶属、合作或背书关系**。文中和界面中出现的 OpenAI、ChatGPT、Codex、Google、Gemini、Antigravity 等名称及商标归其各自所有者所有，仅用于说明兼容性。

### 2. 第三方账号登录：风险自负

「设置 → 模型渠道」中的 OAuth 登录（ChatGPT / Codex、Google Antigravity）**不是**这些提供方为第三方应用开放的官方接入方式：

- 登录时使用的是**其他产品的官方客户端身份**（OAuth client ID 以及与之匹配的请求标识），调用的是**非公开的内部接口**（例如 `chatgpt.com/backend-api/codex`、`cloudcode-pa.googleapis.com/v1internal`）。
- 这种用法**可能违反**提供方的服务条款或使用政策。Google Antigravity 的条款明确禁止在其产品之外使用其服务，2026 年已有用户报告因此被封号。
- 你的账号可能因此被**限制、暂停或永久封禁**，订阅额度可能被收回；接口也可能随时变化或失效。
- OneFrog 不提供 Google 客户端凭据。要使用 Google 登录，你必须自行配置 `OF_GOOGLE_OAUTH_CLIENT_ID` / `OF_GOOGLE_OAUTH_CLIENT_SECRET`，并对其来源和使用负责。

开始登录前，界面会弹出风险说明，必须勾选「我已了解风险并自行承担后果」才能继续；确认的版本和时间会保存在本地并写入变更记录。**如果你不能接受上述风险，请只使用提供方官方 API Key 的 OpenAI 兼容渠道。**

### 3. 你对服务器和 AI 执行的命令负责

OneFrog 会让大语言模型生成并通过 SSH 在你的服务器上执行命令。模型可能出错、误解指令或被输入内容（日志、网页、附件等）误导。

- 默认模式下每条命令都需要你在网页上逐条确认；关键字规则只是提示，**不能**识别所有危险写法。「替我审批」由另一个模型判断，同样不是安全沙盒。
- 建议为 OneFrog 创建权限受限的专用 SSH 用户，不要使用 root；先在非生产环境试用；保持备份。
- 你需要自行确认使用方式符合所在地法律法规、所在单位的规定，以及服务器、云服务和模型提供方的条款。

### 4. 无担保

OneFrog 按 GNU Affero General Public License v3.0 发布。依照该许可证第 15、16 条，本软件**按"原样"提供，不附带任何明示或暗示的担保**；在适用法律允许的最大范围内，版权持有人和贡献者不对因使用或无法使用本软件造成的任何损失（包括数据丢失、服务中断、账号封禁或第三方索赔）承担责任。完整条款见 [LICENSE](LICENSE)。

## English

### 1. Not affiliated with any provider

OneFrog is an independent open-source project. It is **not affiliated with, endorsed by or sponsored by** OpenAI, Google or any model or cloud provider. OpenAI, ChatGPT, Codex, Google, Gemini, Antigravity and other names and trademarks belong to their respective owners and are used only to describe compatibility.

### 2. Third-party account logins are at your own risk

The OAuth logins under "Settings → Model providers" (ChatGPT / Codex, Google Antigravity) are **not** official integrations offered by those providers to third-party apps:

- They sign in with **another product's official client identity** (its OAuth client ID and matching request identifiers) and call **unofficial internal endpoints** (for example `chatgpt.com/backend-api/codex` and `cloudcode-pa.googleapis.com/v1internal`).
- This **may violate** the provider's terms of service or usage policies. The Google Antigravity terms explicitly forbid using the service outside its own products, and account bans for doing so were reported in 2026.
- Your account may be **restricted, suspended or permanently banned**, and subscription quota may be revoked. The endpoints may change or stop working at any time.
- OneFrog does not ship Google client credentials. To enable Google login you must configure `OF_GOOGLE_OAUTH_CLIENT_ID` / `OF_GOOGLE_OAUTH_CLIENT_SECRET` yourself and you are responsible for where they come from and how they are used.

Before a login starts, the UI shows a risk notice and you must tick "I understand the risks and accept the consequences" to continue. The acknowledged version and time are stored locally and written to the change history. **If you cannot accept these risks, use only OpenAI-compatible providers with an official API key.**

### 3. You are responsible for your servers and for the commands the AI runs

OneFrog lets large language models generate commands and run them on your servers over SSH. Models can be wrong, misunderstand instructions, or be manipulated by the content they read (logs, web pages, attachments).

- By default every command must be approved by you in the web UI. The keyword rules are only a warning and **cannot** catch every dangerous command. "Approve for me" mode relies on another model and is not a sandbox either.
- Use a dedicated SSH user with limited privileges instead of root, try OneFrog on non-production machines first, and keep backups.
- You are responsible for complying with applicable laws, your organization's policies, and the terms of your server, cloud and model providers.

### 4. No warranty

OneFrog is released under the GNU Affero General Public License v3.0. As stated in sections 15 and 16 of that license, the software is provided **"as is", without warranty of any kind**, express or implied. To the maximum extent permitted by applicable law, the copyright holders and contributors are not liable for any damages arising from the use of or inability to use the software, including data loss, service outages, account bans or third-party claims. See [LICENSE](LICENSE) for the full terms.
