# OneFrog

<p align="center"><img src="web/public/assets/onefrog-logo.jpg" width="96" alt="OneFrog logo"></p>

OneFrog 是一个自托管的 **AI 运维面板**：把你的 Linux 服务器通过 SSH 接进来，用对话让 Bot 查看状态、执行命令、汇总结果；同时提供服务器探针、密钥保险箱、资产与订阅记账等模块。

> ⚠️ OneFrog 会让 AI 在你的服务器上执行命令。使用前请阅读 [免责声明 / DISCLAIMER](DISCLAIMER.md)。

## 功能

- **AI 助理与多 Bot 协作**：主控 Bot 理解需求、分派任务，协作 Bot 通过 SSH 在你手动选中的服务器上执行命令，过程和结果显示在对话里。
- **命令审批**：首次登录时由你选择「智能审批」（只读命令直接执行，有风险的命令弹窗等你确认）或「关闭审批」，之后可随时修改；另有逐条确认、替我审批、仅规划等高级模式。
- **SSH 主机指纹校验**：首次连接显示指纹并由你确认；指纹变化时拒绝连接，不会自动覆盖。
- **密钥保险箱**：Bot 只拿到密钥名称，命令里写 `{{secret:名称}}`，执行时才在服务端替换，输出中的明文会被打码。
- **模型渠道**：任意 OpenAI Chat Completions 兼容接口，主控 / 协作 / 单个 Bot / 审批可分别选择模型，并统计 Token 用量。另有可选的第三方账号登录（**风险自负**，见下文）。
- **服务器探针**：只用 Python 标准库的只读采集脚本，展示 CPU、内存、磁盘、网络、延迟与丢包。
- **资产与订阅**：记录 VPS、域名等的续费信息，计算剩余价值与到期提醒。

## 一行命令安装

在一台 Linux 服务器（x86_64 或 arm64）上以 root 执行：

```bash
curl -fsSL https://raw.githubusercontent.com/llc370219/onefrog/main/install.sh | sudo bash
```

安装完成后终端会打印访问链接、用户名 `admin` 和随机初始密码；第一次登录必须同时修改用户名和密码。脚本会在需要时安装 Docker（先提示，可取消），然后拉取预构建镜像 `ghcr.io/llc370219/onefrog`，目标服务器不需要 Go 或 Node。

默认监听 `0.0.0.0:8080`、明文 HTTP，安装脚本会为此打印警告。**生产使用请放在 HTTPS 反向代理后面，并只绑定本机：**

```bash
curl -fsSL https://raw.githubusercontent.com/llc370219/onefrog/main/install.sh \
  | sudo OF_BIND_ADDR=127.0.0.1 OF_PUBLIC_URL=https://ops.example.com bash
```

安装选项、反向代理示例、升级、备份、卸载和排错见 **[docs/安装.md](docs/安装.md)**。

## 安全须知

- **使用 HTTPS。** 面板保存 SSH 凭据并能远程执行命令。Docker 发布的端口会绕过 ufw/firewalld，所以放在反向代理后面时务必设置 `OF_BIND_ADDR=127.0.0.1`。
- **使用权限受限的 SSH 用户**，不要把 root 交给 OneFrog。AI 生成的命令可能出错或被日志、网页、附件中的内容误导。
- **建议开启「智能审批」。** 它用服务端规则加 AI 自评判断风险，有风险的命令要你确认；能减少但不能消除风险，规则可能漏判少见的危险写法。选择「关闭审批」时 AI 会直接执行命令。服务器输出、文件和附件里的内容被当作不可信数据，由它们引出的命令会要求确认。详见 [docs/设置与模型配置.md](docs/设置与模型配置.md#命令审批)。
- **核对 SSH 主机指纹**（`ssh-keygen -lf /etc/ssh/ssh_host_ed25519_key.pub`）。
- **探针上报请使用 HTTPS 主控地址**，否则上报凭据以明文传输。
- **`master.key` 与加密数据在同一个数据卷中**，拿到整个数据卷或备份即可解密；需要分开保管时用 `OF_MASTER_KEY` 从外部提供。
- 未指定 `OF_PUBLIC_URL` 时，安装脚本会请求 `https://ifconfig.me` 获取公网 IP，仅用于打印链接。
- 反向代理的地址默认按本机/内网信任 `X-Forwarded-For`，可用 `OF_TRUSTED_PROXIES` 调整（用于登录限流）。
- 容器内服务以非 root 用户运行。浏览器端启用了 CSP、CSRF 请求头校验、WebSocket 来源校验和登录失败限流。
- 发现安全问题请通过 GitHub 私下报告（Security → Report a vulnerability），不要公开提 issue，见 [SECURITY.md](SECURITY.md)。

### 第三方账号登录（风险自负）

「ChatGPT / Codex 登录」和「Google Antigravity 登录」使用**其他产品的官方客户端身份**调用**非公开内部接口**，可能违反提供方服务条款，账号可能被**限制或永久封禁**。每次登录前必须在弹窗中勾选确认。Google 登录不内置客户端凭据，需自行设置 `OF_GOOGLE_OAUTH_CLIENT_ID` / `OF_GOOGLE_OAUTH_CLIENT_SECRET`，否则按钮显示「需要配置」。详见 [docs/设置与模型配置.md](docs/设置与模型配置.md#第三方账号登录oauth风险自负) 和 [DISCLAIMER.md](DISCLAIMER.md)。不想承担这些风险时，只使用官方 API Key 的兼容渠道即可。

## 文档

| 文档 | 内容 |
| --- | --- |
| [docs/安装.md](docs/安装.md) | 安装选项、HTTPS 反向代理、安全建议、数据持久化与备份、排错 |
| [docs/使用说明.md](docs/使用说明.md) | 接入服务器、AI 助理、Bot、密钥、探针、资产模块与主要接口 |
| [docs/设置与模型配置.md](docs/设置与模型配置.md) | 模型渠道、OAuth 登录、模型路由、审批模式、提示词 |
| [docs/UI设计规范.md](docs/UI设计规范.md) | 前端贡献者的界面规范 |

## 本地开发

需要 Go（版本见 `go.mod`）和 Node.js 22。

```bash
# 后端（memory 驱动 + 演示数据，仅本机）
OF_HTTP_ADDR=127.0.0.1:8080 OF_DRIVER=memory OF_SEED=1 OF_ADMIN_PASSWORD=admin go run ./cmd/server

# 前端（另开终端，Vite 把 /api 和 /ws 代理到 127.0.0.1:8080）
cd web && npm ci && npm run dev

# 测试与构建
go vet ./... && go test ./...
cd web && npm run build
docker build -t onefrog:dev .
```

`docker-compose.yml` 和 `configs/config.example.env` 是**仅供本地开发**的配置（PostgreSQL、演示账号 `admin`/`admin`、只绑定 127.0.0.1），不要用于生产。

欢迎提交 issue 和 PR。提交前请确保 `go vet`、`go test`、`npm run build` 通过；界面改动请遵循 [docs/UI设计规范.md](docs/UI设计规范.md)。

## English

OneFrog is a self-hosted AI ops panel: connect your Linux servers over SSH and let a team of LLM bots inspect and operate them through chat, with per-command approval, SSH host-key pinning, a secret vault, lightweight host probes and an asset/subscription ledger.

```bash
curl -fsSL https://raw.githubusercontent.com/llc370219/onefrog/main/install.sh | sudo bash
```

The installer installs Docker if missing (after a notice; `OF_INSTALL_DOCKER=0` aborts instead), pulls the prebuilt image and prints the URL, username and a random initial password; both must be changed on first login. It listens on plain HTTP on all interfaces by default and warns about it. For real use, put it behind an HTTPS reverse proxy and bind to localhost with `OF_BIND_ADDR=127.0.0.1 OF_PUBLIC_URL=https://your.domain`, because it stores SSH credentials and Docker-published ports bypass host firewalls.

- On first login you choose how commands are approved. **Smart approval** (recommended) runs allowlisted read-only commands directly and asks you before anything the server-side rules or the AI consider risky; it reduces risk but cannot eliminate it. **Off** runs AI commands without asking. Commands derived from server output, files or attachments always ask in smart mode. Give OneFrog a dedicated, least-privilege SSH user, not root.
- Report security issues privately via GitHub (Security → Report a vulnerability), see [SECURITY.md](SECURITY.md).
- The optional ChatGPT/Codex and Google Antigravity logins reuse another product's official client identity and unofficial internal endpoints. They may violate the provider's terms and can get your account restricted or permanently banned. You must acknowledge this in a dialog before logging in. Google login needs your own `OF_GOOGLE_OAUTH_CLIENT_ID` / `OF_GOOGLE_OAUTH_CLIENT_SECRET`.
- `master.key` lives next to the encrypted data. Use `OF_MASTER_KEY` to keep them apart.
- Without `OF_PUBLIC_URL`, the installer queries `https://ifconfig.me` to print a link.

See [DISCLAIMER.md](DISCLAIMER.md). OneFrog is not affiliated with OpenAI, Google or any other provider.

## 许可证 / License

[GNU Affero General Public License v3.0](LICENSE)（AGPL-3.0）。如果你修改 OneFrog 并通过网络向他人提供服务，需要按 AGPL-3.0 公开相应源码。第三方组件的许可见 [THIRD_PARTY_NOTICES](THIRD_PARTY_NOTICES)。
