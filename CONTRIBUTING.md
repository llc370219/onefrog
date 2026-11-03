# 参与 OneFrog 开发

[项目首页](README.md) · [文档导航](docs/README.md) · [开发约定](AGENTS.md)

## 从 dev 开始

`main` 对应正式版，`dev` 集成下一版改动。开发与本地预览请克隆 `dev`，避免在早期正式版上找不到开发脚本：

```bash
git clone --branch dev https://github.com/llc370219/onefrog.git
cd onefrog
./scripts/dev.sh
```

需要 Git、Docker Desktop（或可用的 Docker Engine）和 Node.js 22。后端在容器内编译，不必安装 Go。账号 `admin/admin` 仅用于本机演示。详情见 [dev 分支的本地预览指南](https://github.com/llc370219/onefrog/blob/dev/docs/本地预览.md)。

## 提交改动

1. 从最新 dev 创建 `feat/…`、`fix/…` 或 `docs/…` 等工作分支；外部贡献者先 fork。
2. 保持一次 PR 解决一个清楚的问题，补充必要的验证。
3. 将 PR 目标设为 **dev**，说明最终行为和检查结果。不要提交运行数据、密钥或私人交接文件。
4. CI 通过并完成审阅后合并；功能分支可删除。长期保留 main 和 dev。

仅修改文档或仓库组织时，不修改 `VERSION`、不创建发布标签。发布规则见 [版本与发布](https://github.com/llc370219/onefrog/blob/dev/docs/版本与发布.md)。面向 main 的文档同步应保持正式版行为描述准确，不能顺带带入未发布功能。

## 验证

代码改动遵循 [AGENTS.md](AGENTS.md) 的检查要求。文档改动至少检查本地链接、命令对应的分支与文件是否存在；配置重命名应验证引用和 Compose 配置。界面改动按 [UI 规范](https://github.com/llc370219/onefrog/blob/dev/docs/UI设计规范.md) 检查浅色、深色、桌面与手机布局。

## 可选：本地 PostgreSQL 开发栈

推荐先使用上面的预览脚本；需要测试 PostgreSQL 时，在仓库根目录执行：

```bash
docker compose -p onefrog -f compose.dev.yml up --build -d
docker compose -p onefrog -f compose.dev.yml down
```

`compose.dev.yml` 使用演示凭据，只绑定 `127.0.0.1:8080`。项目名显式设为 `onefrog`，与此前默认目录名为 onefrog 的开发安装保持一致；如果旧开发栈使用了其他项目名，沿用它，避免误以为原数据丢失。`down` 不带 `-v`，保留开发数据卷。

此文件原名 `docker-compose.yml`。**生产安装不使用仓库中的开发 Compose 文件**；安装脚本仍在服务器安装目录生成自己的 `docker-compose.yml`，原有生产升级命令不变。

## 问题反馈

使用问题和功能建议请提交 [Issue](https://github.com/llc370219/onefrog/issues)。安全漏洞按 [SECURITY.md](SECURITY.md) 私下报告。
