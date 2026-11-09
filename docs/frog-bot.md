# Frog Bot 助理

OneFrog 的「AI 助理」由 [Frog Bot](https://github.com/llc370219/frog-bot/tree/314a84f14a811fcf92e15d91411a1458fc34c2b9) 驱动。聊天模型直接回答短问题，耗时操作交给后台 Agent；刷新或离开页面后，任务仍在服务器运行。任务卡支持暂停、恢复、取消和重试；定时计划由服务端调度。

## 模型和权限

- 登录仍使用 OneFrog 账号。在「设置 → 模型」配置默认模型和协作模型。聊天使用默认主控的模型路由，后台 Agent 使用主控的协作路由；单独为主控绑定模型时，两个角色都遵循该绑定。已有 API 密钥、OAuth 和用量记录继续生效。
- 助理的文件工具只能访问当前账号的 Frog Bot 工作区。Frog Bot 没有面板宿主机的 Shell 权限。
- 需要操作已登记的服务器时，后台 Agent 会发起具体命令的确认。批准仅适用于卡片上的服务器和命令；执行仍受 OneFrog 的审批策略、SSH 指纹校验、密钥规则和审计约束。
- 原有群聊、Bot 专属会话、代码工作区、面板任务和自动化仍通过「运维会话」及原页面使用。原会话可以继续访问，不会自动复制进 Frog Bot 对话。

## 数据与开发

- 聊天事件、任务检查点、计划和记忆按账号加密保存于 `OF_DATA_DIR/frog-bot/`。工作区附件和产物保存在同目录的账号工作区；备份与迁移会一并打包。Frog Bot 卡片保存的密钥使用单独的加密保险箱，SSH 命令继续使用 OneFrog 原有保险箱。
- 前端通过 `/api/frog-bot/*` 使用 OneFrog 的登录和跨站请求校验；事件流支持断线续传。Frog Bot 随 OneFrog 构建和启动，不需要运行第二个服务。
- 源码基于 Frog Bot 提交 `314a84f`，位于 `internal/frogbot/` 和 `web/src/frogbot/`；宿主适配位于 `internal/api/frogbot.go` 与 `internal/bot/frogbot.go`。更新上游代码时，保留宿主权限、加密、模型路由和生命周期适配。
