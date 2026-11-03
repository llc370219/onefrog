# OneFrog 开发约定

给参与开发的 AI 和协作者。OneFrog 是自托管的 AI 运维面板：Go 后端（`cmd/`、`internal/`）+ React/Vite 前端（`web/`），用 Docker 镜像发布。

## 工作方式

- 两个长期分支：`dev`（测试版）和 `main`（正式版）。改动走分支和 PR，**PR 的目标分支是 `dev`**，不直接推 `dev` 或 `main`。PR 描述里写清改了什么、怎么验证的。
- 发布 = 合并一个修改根目录 `VERSION` 的 PR，并同时新增 `docs/releases/<版本>.md` 发布说明（第一行 `# 标题`）。`dev` 上写 `vX.Y.Z-beta.N` 发测试版（镜像 `beta`，GitHub 预发布），`main` 上写 `vX.Y.Z` 发正式版（镜像 `latest`）。`.github/workflows/release.yml` 自动构建镜像、打标签、创建 Release。不要手动改 Release。
- 正式版只在维护者确认测试版没问题后发布：从 `dev` 开 PR 到 `main`，把 `VERSION` 改成正式版号。完整流程见 `docs/版本与发布.md`。
- 维护者用 `./scripts/dev.sh <PR 号>` 在本地预览 PR（见 `docs/本地预览.md`）。开完 PR 后告诉维护者 PR 编号和这条命令。

## 提交前检查

```bash
gofmt -l .            # 应无输出
go vet ./...
go test ./...
cd web && npm run build   # 包含 tsc --noEmit
shellcheck install.sh deploy/docker-entrypoint.sh scripts/dev.sh
```

CI（`.github/workflows/ci.yml`）会跑同样的检查，外加 Docker 构建和启动测试。

## 前端样式

- 只有一个样式文件 `web/src/index.css`。开头是主题变量：第一块是浅色（默认），`:root[data-theme=dark]` 和 `@media (prefers-color-scheme:dark)` 里是深色。两个深色块内容必须完全一致。
- **不要写死颜色**（`#xxx`、`rgb()`、`white` 等）。用变量：底色 `--canvas` / `--base`，卡片 `--s1` `--s2` `--s3`，输入框 `--field`，内嵌区域 `--inset`，弹层 `--popover`，文字 `--fg` 到 `--fg4`，边框 `--line` / `--line2`，强调色 `--accent` / `--accent-hover` / `--on-accent`，状态 `--ok` / `--warn` / `--danger`。半透明效果用 `color-mix(in srgb,var(--ink) 6%,transparent)` 这类写法，不要用 `#ffffff10`。
- 确实需要新颜色时，在浅色块和两个深色块里都加上同名变量。
- canvas 等需要在 JS 里取颜色的地方，用 `web/src/theme.ts` 的 `cssVar()`，并在依赖里带上 `useTheme().theme`，切换主题时才会重绘。
- 风格：暖色中性底、绿色强调色、页面和弹窗标题用衬线字体（`--font-serif`）、按钮 10px 圆角。
- 界面改动要在浅色和深色、桌面和 390px 手机宽度下都检查一遍。

## 安全

- 不要提交 `.env`、`.install.env`、`data/`、`master.key` 或任何密钥。
- 前端受 CSP 限制（`script-src 'self'`），不能用内联脚本；需要在首屏前执行的脚本放在 `web/public/assets/` 下作为外部文件引用。
