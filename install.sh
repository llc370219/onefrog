#!/usr/bin/env bash
# OneFrog one-line installer.
#
#   curl -fsSL https://raw.githubusercontent.com/llc370219/onefrog/main/install.sh | sudo bash
#
# Options (environment variables, pass them after sudo):
#   OF_VERSION=latest        image tag to install, e.g. v0.1.0
#   OF_PUBLISH_PORT=8080     host port
#   OF_BIND_ADDR=0.0.0.0     host address to bind; use 127.0.0.1 behind a reverse proxy
#   OF_PUBLIC_URL=           link printed at the end, e.g. https://ops.example.com
#                            (if unset, the public IPv4 is looked up via https://ifconfig.me;
#                             set OF_PUBLIC_URL to skip that external request)
#   OF_ADMIN_USER=admin      initial username (must be changed on first login)
#   OF_INSTALL_DIR=/opt/onefrog
#   OF_INSTALL_DOCKER=1      set to 0 to abort instead of installing Docker
#   OF_BUILD_FROM_SOURCE=0   set to 1 to build the image on this server instead of pulling it
#   OF_REPO=https://github.com/llc370219/onefrog.git   source repo used with OF_BUILD_FROM_SOURCE=1
#   OF_IMAGE=ghcr.io/llc370219/onefrog                 image name
#
# The whole script is wrapped in main() so a partially downloaded file never runs.
set -euo pipefail

main() {
  local install_dir="${OF_INSTALL_DIR:-/opt/onefrog}"
  local image="${OF_IMAGE:-ghcr.io/llc370219/onefrog}"
  local version="${OF_VERSION:-latest}"
  local repo="${OF_REPO:-https://github.com/llc370219/onefrog.git}"
  local build="${OF_BUILD_FROM_SOURCE:-0}"
  local env_file="${install_dir}/.install.env"
  local compose_file="${install_dir}/docker-compose.yml"

  say() { printf '%s\n' "$*"; }
  die() { printf '错误: %s\n' "$*" >&2; exit 1; }

  [ "$(uname -s)" = "Linux" ] || die "只支持 Linux 服务器。"
  [ "$(id -u)" -eq 0 ] || die "请用 root 运行，例如: curl -fsSL https://raw.githubusercontent.com/llc370219/onefrog/main/install.sh | sudo bash"
  command -v curl >/dev/null 2>&1 || die "缺少 curl，请先安装 curl（apt install curl / yum install curl）。"

  # ---- Docker ----
  if ! command -v docker >/dev/null 2>&1; then
    if [ "${OF_INSTALL_DOCKER:-1}" = "0" ]; then
      die "没有检测到 Docker。你设置了 OF_INSTALL_DOCKER=0，请先自行安装 Docker 和 compose 插件后再运行。"
    fi
    say "没有检测到 Docker。"
    say "接下来会用 Docker 官方脚本 https://get.docker.com 安装 Docker。"
    say "注意: Docker 会自行添加 iptables/nftables 规则，发布的端口可能绕过你现有的防火墙规则。"
    say "如不希望自动安装，请按 Ctrl+C 取消，或带上 OF_INSTALL_DOCKER=0 重新运行。10 秒后开始..."
    sleep 10
    curl -fsSL https://get.docker.com | sh
  fi
  docker compose version >/dev/null 2>&1 || die "Docker 已安装，但没有 compose 插件（docker compose）。请安装 docker-compose-plugin。"

  mkdir -p "$install_dir"
  chmod 700 "$install_dir"

  # ---- credentials (reused on re-run) ----
  if [ ! -f "$env_file" ]; then
    local port="${OF_PUBLISH_PORT:-8080}"
    local bind="${OF_BIND_ADDR:-0.0.0.0}"
    local admin_user="${OF_ADMIN_USER:-admin}"
    local public ip secure=0 pass
    if [ -n "${OF_PUBLIC_URL:-}" ]; then
      public="${OF_PUBLIC_URL%/}"
    else
      # External lookup of this server's public IPv4, only to print a working link.
      ip=$(curl -4 -fsS --max-time 5 https://ifconfig.me 2>/dev/null || true)
      [ -n "$ip" ] || ip=$(hostname -I 2>/dev/null | cut -d ' ' -f 1 || true)
      [ -n "$ip" ] || ip=127.0.0.1
      if [ "$bind" = "127.0.0.1" ]; then ip=127.0.0.1; fi
      if [ "$port" = "80" ]; then public="http://${ip}"; else public="http://${ip}:${port}"; fi
    fi
    case "$public" in https:*) secure=1 ;; esac
    pass=$(head -c 64 /dev/urandom | base64 | tr -dc 'A-Za-z0-9' | cut -c1-20)
    [ "${#pass}" -eq 20 ] || die "无法生成初始密码。"
    (
      umask 077
      cat > "$env_file" <<ENV
OF_DRIVER=memory
OF_ADMIN_USER=${admin_user}
OF_ADMIN_PASSWORD=${pass}
OF_FORCE_CREDENTIAL_SETUP=1
OF_SEED=0
OF_HTTP_ADDR=:8080
OF_PUBLIC_URL=${public}
OF_COOKIE_SECURE=${secure}
OF_PUBLISH_PORT=${port}
OF_BIND_ADDR=${bind}
ENV
    )
    chmod 600 "$env_file"
    say "已生成初始账号，保存在 ${env_file}（权限 600）。"
  else
    say "沿用已有的 ${env_file}，不会重置已经创建的账号。"
    # Allow changing port / bind address on re-run without touching credentials.
    local k
    for k in OF_PUBLISH_PORT OF_BIND_ADDR OF_PUBLIC_URL; do
      if [ -n "${!k:-}" ]; then
        local v="${!k}"
        [ "$k" = "OF_PUBLIC_URL" ] && v="${v%/}"
        grep -v "^${k}=" "$env_file" > "${env_file}.tmp" || true
        printf '%s=%s\n' "$k" "$v" >> "${env_file}.tmp"
        if [ "$k" = "OF_PUBLIC_URL" ]; then
          grep -v '^OF_COOKIE_SECURE=' "${env_file}.tmp" > "${env_file}.tmp2" || true
          case "$v" in https:*) echo 'OF_COOKIE_SECURE=1' ;; *) echo 'OF_COOKIE_SECURE=0' ;; esac >> "${env_file}.tmp2"
          mv "${env_file}.tmp2" "${env_file}.tmp"
        fi
        chmod 600 "${env_file}.tmp"
        mv "${env_file}.tmp" "$env_file"
        say "已更新 ${k}。"
      fi
    done
  fi

  read_env() {
    local key=$1 line
    while IFS= read -r line || [ -n "$line" ]; do
      case $line in "$key"=*) printf '%s\n' "${line#"$key"=}"; return ;; esac
    done < "$env_file"
  }
  local pub_port bind_addr public admin_user pass
  pub_port=$(read_env OF_PUBLISH_PORT); pub_port=${pub_port:-8080}
  bind_addr=$(read_env OF_BIND_ADDR); bind_addr=${bind_addr:-0.0.0.0}
  public=$(read_env OF_PUBLIC_URL)
  admin_user=$(read_env OF_ADMIN_USER)
  pass=$(read_env OF_ADMIN_PASSWORD)

  # ---- image: pull prebuilt, or build from source ----
  local ref="${image}:${version}"
  if [ "$build" = "1" ]; then
    command -v git >/dev/null 2>&1 || die "OF_BUILD_FROM_SOURCE=1 需要 git，请先安装 git。"
    local src="${install_dir}/src"
    if [ -d "${src}/.git" ]; then
      say "更新源码 ${src} ..."
      git -C "$src" fetch --depth 1 origin && git -C "$src" reset --hard FETCH_HEAD
    else
      say "克隆源码 ${repo} 到 ${src} ..."
      rm -rf "$src"
      git clone --depth 1 "$repo" "$src"
    fi
    ref="onefrog:local"
    say "正在本机构建镜像（需要较多内存和磁盘，首次可能要 5–10 分钟）..."
    docker build -t "$ref" "$src"
  else
    say "拉取镜像 ${ref} ..."
    if ! docker pull "$ref"; then
      die "拉取镜像失败: ${ref}
如果还没有发布镜像，可以改为在本机构建:
  curl -fsSL https://raw.githubusercontent.com/llc370219/onefrog/main/install.sh | sudo OF_BUILD_FROM_SOURCE=1 bash"
    fi
  fi

  cat > "$compose_file" <<YAML
# Generated by the OneFrog installer. Re-running the installer overwrites this file.
services:
  app:
    image: ${ref}
    restart: unless-stopped
    env_file:
      - .install.env
    environment:
      OF_DATA_DIR: /app/data
      OF_HTTP_ADDR: ":8080"
    ports:
      - "${bind_addr}:${pub_port}:8080"
    volumes:
      - onefrog-data:/app/data
volumes:
  onefrog-data:
YAML

  say "正在启动..."
  docker compose --project-directory "$install_dir" -f "$compose_file" -p onefrog up -d --remove-orphans

  local ready=0 i=0
  while [ "$i" -lt 60 ]; do
    if curl -fsS --max-time 3 "http://127.0.0.1:${pub_port}/api/health" >/dev/null 2>&1; then
      ready=1; break
    fi
    i=$((i + 1)); sleep 2
  done

  say ""
  say "========================================"
  say "OneFrog 已安装"
  say "链接: ${public}"
  say "用户名: ${admin_user}"
  say "初始密码: ${pass}"
  say "========================================"
  say "第一次登录必须修改用户名和密码，改完之前不能使用面板里的其他功能。"
  say "初始密码也保存在 ${env_file}（仅 root 可读）。"
  say ""
  case "${bind_addr}|${public}" in
    127.0.0.1\|*) ;;
    *\|http:*)
      say "!!! 警告: 面板正以明文 HTTP 对所有网络地址开放（${bind_addr}:${pub_port}）。"
      say "!!! 登录密码和会话 Cookie 可能被窃听。建议配置 HTTPS 反向代理后用"
      say "!!!   OF_BIND_ADDR=127.0.0.1 OF_PUBLIC_URL=https://你的域名 重新运行本安装命令。"
      say "" ;;
  esac
  say "安全提示:"
  say "- 面板会保存你服务器的 SSH 凭据，请尽快放到 HTTPS 反向代理（Caddy/Nginx）后面使用，不要长期用明文 http 登录。"
  say "- Docker 发布的端口会绕过 ufw/firewalld 等主机防火墙。只想通过反向代理访问时，用 OF_BIND_ADDR=127.0.0.1 重新安装。"
  say "- 云服务器的安全组需要放行端口 ${pub_port}（如果你直接访问它）。"
  say ""
  say "常用命令: cd ${install_dir} && docker compose -p onefrog ps | logs -f | restart"
  if [ "$ready" -ne 1 ]; then
    printf '服务还没有通过健康检查。查看日志: cd %s && docker compose -p onefrog logs\n' "$install_dir" >&2
    exit 1
  fi
}

main "$@"
