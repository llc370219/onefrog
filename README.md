# OneFrog · 一只青蛙

<p align="center"><img src="web/public/assets/onefrog-logo-readme.png" width="96" alt="OneFrog logo"></p>

自托管的 AI 运维面板：通过对话管理服务器、Bot、密钥保险箱和自定义模块，执行过程、交互选项与审批结果都留在面板中。

[安装使用](#安装使用) · [文档导航](docs/README.md) · [参与开发](CONTRIBUTING.md) · [正式版](https://github.com/llc370219/onefrog/releases/latest) · [English](docs/README.en.md)

## 能做什么

- **AI 助理与多 Bot 协作**：分派任务、独立记忆、逐步显示执行进度；需要选择、密钥或审批时展示交互卡片。
- **服务器管理**：SSH 接入与指纹校验、远程命令、探针监控，以及 systemd 服务、Docker 容器和监听端口检测。
- **面板与模块管理**：通过助理管理 Bot 和模块，记录资产与订阅，创建带交互界面的自定义模块。
- **模型与密钥管理**：配置兼容渠道和模型路由，密钥加密保存并按名称调用，提供命令审批与变更记录。

详细功能和操作见[使用说明](docs/使用说明.md)。新功能先进入测试版，正式版以 [Releases](https://github.com/llc370219/onefrog/releases) 为准。

## 安装使用

在 Linux 服务器（x86_64 / arm64）上执行：

```bash
curl -fsSL https://raw.githubusercontent.com/llc370219/onefrog/main/install.sh | sudo bash
```

安装脚本拉取预构建镜像，目标服务器不需要 Go 或 Node.js。没有 Docker 时会提示安装。首次生成随机密码，首次登录必须修改用户名和密码；再次运行同一命令可原地升级，使用自定义安装目录时需继续指定该目录。

**默认入口为公网 HTTP `:8080`。生产部署应使用 HTTPS 反向代理，并将应用绑定到本机：**

```bash
curl -fsSL https://raw.githubusercontent.com/llc370219/onefrog/main/install.sh \
  | sudo OF_BIND_ADDR=127.0.0.1 OF_PUBLIC_URL=https://ops.example.com bash
```

反向代理需另行配置，`OF_PUBLIC_URL` 本身不会安装证书。完整安装选项、系统要求、多实例、升级、备份和排错见[安装文档](docs/安装.md)。

| 用途 | 入口 |
| --- | --- |
| 正式使用 | `main` 安装脚本、正式 Release、`latest` 镜像 |
| 提前试用 | [测试版安装与渠道说明](https://github.com/llc370219/onefrog/blob/dev/docs/版本与发布.md)，使用 `dev` 脚本和 `beta` 镜像 |
| 修改代码 / 预览 PR | [贡献指南](CONTRIBUTING.md)，从 `dev` 分支开始 |

仓库根目录的 `compose.dev.yml` **仅用于本地开发**，不是生产部署配置。服务器上的生产 `docker-compose.yml` 由安装脚本生成。

## 使用前了解

OneFrog 能在已接入的服务器执行命令。建议使用权限受限的 SSH 账号，开启智能或人工审批，并保持独立备份。默认主密钥与加密数据同卷保存，完整数据备份仍应作为敏感资料保管。

普通聊天内容、附件及参与分析的命令输出会按任务发送给配置的模型服务；密码请通过专用卡片或保险箱录入。可选的第三方账号登录使用非公开接口，存在账号受限或封禁风险。

请阅读[安全与隐私](docs/安全与隐私.md)和[免责声明](DISCLAIMER.md)。漏洞通过 [Security → Report a vulnerability](https://github.com/llc370219/onefrog/security/advisories/new) 私下报告。

## 文档与贡献

- [文档导航](docs/README.md)：安装、使用、设置、开发和版本发布入口。
- [参与开发](CONTRIBUTING.md)：本地预览、PR 目标、验证与开发 Compose。
- [开发约定](AGENTS.md)：所有编码助手和协作者共用的规则。
- [Issues](https://github.com/llc370219/onefrog/issues)：问题反馈与功能建议。

## English

OneFrog is a self-hosted AI operations panel for SSH server management, collaborative bots, approval workflows, monitoring, a secret vault, and custom interactive modules. Use `main` for stable installations and `dev` for development. Read the [English guide](docs/README.en.md) before deployment.

## 许可证

[GNU AGPL v3.0](LICENSE)。修改后通过网络提供服务时，需按许可证提供相应源码。第三方许可见 [THIRD_PARTY_NOTICES](THIRD_PARTY_NOTICES)。

`web/public/` 下的猫咪徽标为作者原创手绘作品，随项目按 AGPL-3.0 提供。OneFrog 与 OpenAI、Google 等服务商无隶属关系。
