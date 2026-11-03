#!/usr/bin/env bash
# OneFrog 本地预览：一条命令启动后端和前端，改代码后自动生效。
#
#   ./scripts/dev.sh          用当前代码启动
#   ./scripts/dev.sh 12       切到 PR #12 的代码再启动
#   ./scripts/dev.sh main     切回 main 并拉取最新代码再启动
#   ./scripts/dev.sh stop     停掉后台的预览后端
#   ./scripts/dev.sh reset    清空本地演示数据（账号、服务器、对话等）
#
# 需要：git、Docker（Docker Desktop 需在运行）、Node.js 20+。不需要安装 Go：
# 后端在 Docker 里用当前目录的源码编译运行。
# 前端改动由 Vite 热更新；后端（Go）改动会被检测到并自动重启后端。
# 打开 http://localhost:5173 ，演示账号 admin / admin，仅监听本机。
set -euo pipefail

cd "$(dirname "$0")/.."
root=$(pwd)
container=onefrog-dev-api
api_port=${OF_DEV_API_PORT:-8080}
data_dir="$root/.dev/data"
go_image=${OF_DEV_GO_IMAGE:-golang:1.26-alpine}

say() { printf '%s\n' "$*"; }
die() { printf '错误：%s\n' "$*" >&2; exit 1; }

need() {
  command -v git >/dev/null 2>&1 || die "没有找到 git。macOS 可执行：xcode-select --install"
  command -v docker >/dev/null 2>&1 || die "没有找到 Docker。请安装 Docker Desktop：https://www.docker.com/products/docker-desktop/"
  docker info >/dev/null 2>&1 || die "Docker 没有在运行。请先打开 Docker Desktop，等它启动完成后再试。"
  command -v node >/dev/null 2>&1 || die "没有找到 Node.js。macOS 可执行：brew install node@22"
  local major
  major=$(node -p 'process.versions.node.split(".")[0]')
  [ "$major" -ge 20 ] || die "Node.js 版本太旧（当前 $(node -v)），需要 20 以上。"
}

clean_tree() {
  if ! git diff --quiet || ! git diff --cached --quiet; then
    die "当前目录有未提交的改动，切换前请先提交或执行 git stash 暂存。"
  fi
}

stop_api() { docker rm -f "$container" >/dev/null 2>&1 || true; }

checkout() {
  case "$1" in
    main)
      clean_tree
      say "→ 切换到 main 并拉取最新代码"
      git fetch --quiet origin main
      git checkout --quiet main
      git merge --quiet --ff-only origin/main
      ;;
    *)
      clean_tree
      say "→ 获取 PR #$1 的代码"
      git fetch --quiet origin "pull/$1/head" || die "找不到 PR #$1。"
      git checkout --quiet -B "pr-$1" FETCH_HEAD
      ;;
  esac
}

web_deps() {
  if [ ! -d web/node_modules ] || [ web/package-lock.json -nt web/node_modules/.package-lock.json ]; then
    say "→ 安装前端依赖（只在第一次或依赖变化时执行）"
    (cd web && npm ci --no-audit --no-fund)
  fi
}

start_api() {
  stop_api
  mkdir -p "$data_dir"
  say "→ 启动后端（第一次会下载 Go 依赖，可能需要几分钟）"
  docker run -d --name "$container" \
    -p "127.0.0.1:${api_port}:8080" \
    -v "$root:/src:ro" -w /src \
    -v onefrog-dev-gomod:/go/pkg/mod \
    -v onefrog-dev-gobuild:/root/.cache/go-build \
    -v "$data_dir:/data" \
    -e GOFLAGS=-buildvcs=false \
    -e OF_DRIVER=memory -e OF_SEED=1 \
    -e OF_ADMIN_USER=admin -e OF_ADMIN_PASSWORD=admin \
    -e OF_DATA_DIR=/data -e OF_HTTP_ADDR=:8080 -e OF_WEB_DIR= \
    -e OF_UPDATE_CHECK=0 \
    "$go_image" go run ./cmd/server >/dev/null
  wait_api
}

wait_api() {
  local i=0
  until curl -fsS "http://127.0.0.1:${api_port}/api/health" >/dev/null 2>&1; do
    if [ "$(docker inspect -f '{{.State.Running}}' "$container" 2>/dev/null)" != "true" ]; then
      docker logs --tail 40 "$container" >&2 || true
      die "后端没有启动成功，上面是最后的日志。"
    fi
    i=$((i + 1))
    [ "$i" -le 300 ] || die "后端 5 分钟内没有就绪。查看日志：docker logs $container"
    sleep 1
  done
}

# Restarts the backend whenever Go sources (or files they embed) change.
watch_api() {
  local stamp="$root/.dev/.api-stamp"
  touch "$stamp"
  while sleep 2; do
    if [ -n "$(find cmd internal -type f \( -name '*.go' -o -name '*.py' -o -name '*.sql' \) -newer "$stamp" -print -quit 2>/dev/null)" ]; then
      touch "$stamp"
      say "→ 检测到后端代码改动，正在重启后端…"
      docker restart "$container" >/dev/null && wait_api && say "→ 后端已重启"
    fi
  done
}

# Everything runs from main(), which bash reads completely before executing it:
# `dev.sh 12` checks out another version of this very file, and bash would
# otherwise keep reading the new file from the old byte offset.
main() {
  case "${1:-}" in
    -h|--help|help) sed -n '2,13p' "$0" | sed 's/^# \{0,1\}//'; exit 0 ;;
    stop) stop_api; say "预览后端已停止。"; exit 0 ;;
    reset) stop_api; rm -rf "$data_dir"; say "本地演示数据已清空，下次启动会重新生成。"; exit 0 ;;
    "") ;;
    main) need; checkout main ;;
    *[!0-9]*) die "不认识的参数：$1。用法见 ./scripts/dev.sh help" ;;
    *) need; checkout "$1" ;;
  esac
  [ -n "${1:-}" ] || need

  web_deps
  start_api
  watch_api &
  watcher=$!
  trap 'kill "$watcher" 2>/dev/null || true; stop_api' EXIT INT TERM

  branch=$(git rev-parse --abbrev-ref HEAD)
  say ""
  say "OneFrog 本地预览已启动（代码：$branch @ $(git rev-parse --short HEAD)）"
  say "  地址：http://localhost:5173"
  say "  账号：admin / admin"
  say "  改前端会自动刷新页面，改后端会自动重启。按 Ctrl+C 结束。"
  say ""
  cd web
  npm run dev -- --open
}

main "$@"; exit $?
