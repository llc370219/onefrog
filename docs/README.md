# 文档导航

[项目首页](../README.md) · [正式版](https://github.com/llc370219/onefrog/releases/latest) · [测试版与开发文档](https://github.com/llc370219/onefrog/tree/dev/docs) · [English](README.en.md)

## 安装与使用

| 文档 | 什么时候看 |
| --- | --- |
| [安装](安装.md) | 部署、升级、多实例、HTTPS、备份恢复和排错 |
| [使用说明](使用说明.md) | 接入服务器、操作 Bot、使用探针和资产模块 |
| [设置与模型配置](设置与模型配置.md) | 接入模型、配置路由和命令审批 |
| [安全与隐私](安全与隐私.md) | 密钥存储、外部请求、账号登录风险与部署边界 |

## 参与开发

开发工具和未发布功能以 `dev` 分支为准。首次开发请从[贡献指南](../CONTRIBUTING.md)开始。

| 文档 | 内容 |
| --- | --- |
| [本地预览](https://github.com/llc370219/onefrog/blob/dev/docs/本地预览.md) | 克隆 dev、一条命令预览代码或 PR |
| [UI 设计规范](https://github.com/llc370219/onefrog/blob/dev/docs/UI设计规范.md) | 主题、组件与移动端规范 |
| [编码助手约定](../AGENTS.md) | 所有编码助手共用的开发规则 |

## 版本与发布

- [发布流程与测试版安装](https://github.com/llc370219/onefrog/blob/dev/docs/版本与发布.md)
- [版本发布说明源文件](https://github.com/llc370219/onefrog/tree/dev/docs/releases)
- [全部已发布版本与历史更新记录](https://github.com/llc370219/onefrog/releases)

历史版本的更新记录保留在 GitHub Releases；新的发布说明随版本保存在 `docs/releases/`，不重复补录旧版本。

## 仓库目录

| 路径 | 职责 |
| --- | --- |
| `cmd/server/` | Go 服务启动入口 |
| `internal/` | 后端业务、API、存储、SSH、Bot 与模块实现 |
| `web/` | React 前端、静态资源和前端依赖 |
| `configs/` | 可公开的配置示例 |
| `deploy/` | 容器运行辅助文件 |
| `scripts/` | 开发辅助脚本，当前从 dev 分支获取 |
| `.github/workflows/` | CI 与镜像发布工作流 |
| `docs/` | 使用、开发与发布文档 |
| `install.sh` | 用户安装与升级入口，保留根目录路径 |
| `compose.dev.yml` | 本地 PostgreSQL 开发栈，必须显式指定 `-f` |

测试与实现文件就近放置。运行数据、私人交接材料、密钥备份与本地审计报告不属于公开仓库内容。
