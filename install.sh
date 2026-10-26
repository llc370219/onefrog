#!/usr/bin/env bash
# OneFrog one-line installer.
#
#   curl -fsSL https://raw.githubusercontent.com/llc370219/onefrog/main/install.sh | sudo bash
#   wget -qO- https://raw.githubusercontent.com/llc370219/onefrog/main/install.sh | sudo bash   # without curl
#
# Supported: Debian 12/13, Ubuntu 22.04/24.04 and newer, RHEL-compatible 9/10
# (Rocky, AlmaLinux, CentOS Stream), Fedora; x86_64 and arm64.
#
# Options (environment variables, pass them after sudo):
#   OF_VERSION=latest        image tag to install, e.g. v0.1.1
#   OF_PUBLISH_PORT=8080     host port
#   OF_BIND_ADDR=0.0.0.0     host address to bind; use 127.0.0.1 behind a reverse proxy
#   OF_PUBLIC_URL=           link printed at the end, e.g. https://ops.example.com
#                            (if unset, the public IPv4 is looked up via https://ifconfig.me;
#                             set OF_PUBLIC_URL to skip that external request)
#   OF_ADMIN_USER=admin      initial username (must be changed on first login)
#   OF_APPROVAL_MODE=        preset the command approval mode for headless installs:
#                            off | smart | manual | plan. Applies only while no mode has
#                            been chosen; otherwise the first login asks you to choose.
#   OF_UPDATE_CHECK=1        set to 0 to stop the panel from checking GitHub for new releases
#   OF_INSTALL_DIR=/opt/onefrog
#   OF_PROJECT=              docker compose project name. Defaults to "onefrog" for
#                            /opt/onefrog and "onefrog-<dir>" for other install dirs, so
#                            several instances can run side by side. Saved on first install.
#   OF_ALLOW_UNSUPPORTED_OS=0  set to 1 to try anyway on an end-of-life / unsupported OS
#   OF_INSTALL_DOCKER=1      set to 0 to abort instead of installing Docker
#   OF_BUILD_FROM_SOURCE=0   set to 1 to build the image on this server instead of pulling it
#   OF_REPO=https://github.com/llc370219/onefrog.git   source repo used with OF_BUILD_FROM_SOURCE=1
#   OF_IMAGE=ghcr.io/llc370219/onefrog                 image name
#
# Re-running the same command upgrades in place: the generated account, settings
# and the data volume are kept; only the image and docker-compose.yml are replaced.
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

  # ---- operating system ----
  # End-of-life releases usually cannot install Docker any more (their package
  # mirrors are archived), so stop early with a clear message.
  local os_id="" os_ver="" os_name="" eol=""
  if [ -r /etc/os-release ]; then
    # shellcheck disable=SC1091
    os_id=$(. /etc/os-release && printf '%s' "${ID:-}")
    # shellcheck disable=SC1091
    os_ver=$(. /etc/os-release && printf '%s' "${VERSION_ID:-}")
    # shellcheck disable=SC1091
    os_name=$(. /etc/os-release && printf '%s' "${PRETTY_NAME:-${ID:-}}")
  fi
  case "${os_id}:${os_ver%%.*}" in
    debian:[0-9]|debian:10|debian:11) eol=1 ;;
    ubuntu:1[0-9]|ubuntu:20|ubuntu:21) eol=1 ;;
    centos:[0-8]|rhel:[0-7]|rocky:[0-7]|almalinux:[0-7]|ol:[0-7]) eol=1 ;;
  esac
  if [ -n "$eol" ]; then
    say "检测到系统: ${os_name}。这个版本已经停止维护（EOL），软件源可能已下线，安装 Docker 很可能失败。"
    say "支持的系统: Debian 12/13、Ubuntu 22.04/24.04 及更新版本、Rocky / AlmaLinux / CentOS Stream 9/10、Fedora。"
    if [ "${OF_ALLOW_UNSUPPORTED_OS:-0}" != "1" ] && ! command -v docker >/dev/null 2>&1; then
      die "请升级系统或换用受支持的系统后再安装。已经装好 Docker 的话可以直接重试；确实要继续尝试，带上 OF_ALLOW_UNSUPPORTED_OS=1 重新运行。"
    fi
    say "继续安装（已有 Docker 或设置了 OF_ALLOW_UNSUPPORTED_OS=1）。"
  fi

  if ! command -v curl >/dev/null 2>&1; then
    say "缺少 curl，正在用系统包管理器安装..."
    if command -v apt-get >/dev/null 2>&1; then
      { apt-get update -qq && DEBIAN_FRONTEND=noninteractive apt-get install -y -qq curl ca-certificates; } || true
    elif command -v dnf >/dev/null 2>&1; then
      dnf install -y -q curl || true
    elif command -v yum >/dev/null 2>&1; then
      yum install -y -q curl || true
    fi
    command -v curl >/dev/null 2>&1 || die "缺少 curl，自动安装失败。请先修复软件源并安装 curl（apt install curl / dnf install curl）后重试。系统已停止维护时软件源可能已下线，见 https://github.com/llc370219/onefrog/blob/main/docs/安装.md#支持的系统"
  fi

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
    if ! curl -fsSL https://get.docker.com | sh; then
      die "Docker 安装失败（${os_name:-未知系统}）。常见原因：系统已停止维护、软件源（apt/yum）不可用或网络无法访问 download.docker.com。
请先修复软件源（例如 apt-get update 能正常完成），或参考 https://docs.docker.com/engine/install/ 手动安装 Docker 和 compose 插件，再重新运行本命令。"
    fi
    command -v docker >/dev/null 2>&1 || die "Docker 安装脚本已结束，但仍找不到 docker 命令。请手动安装 Docker 后重试。"
  fi
  docker compose version >/dev/null 2>&1 || die "Docker 已安装，但没有 compose 插件（docker compose）。请安装 docker-compose-plugin。"

  case "$install_dir" in /*) ;; *) die "OF_INSTALL_DIR 必须是绝对路径。" ;; esac
  install_dir="${install_dir%/}"
  [ -n "$install_dir" ] || die "OF_INSTALL_DIR 不能是 /。"
  env_file="${install_dir}/.install.env"
  compose_file="${install_dir}/docker-compose.yml"
  mkdir -p "$install_dir"
  chmod 700 "$install_dir"

  read_env() {
    local key=$1 line
    while IFS= read -r line || [ -n "$line" ]; do
      case $line in "$key"=*) printf '%s\n' "${line#"$key"=}"; return ;; esac
    done < "$env_file"
  }
  # Settings saved by an earlier run, or empty on a fresh install.
  saved() { if [ -f "$env_file" ]; then read_env "$1"; fi; }
  # ---- compose project (one per install dir) ----
  local project
  project=$(saved OF_PROJECT)
  local save_project=0
  if [ -z "$project" ]; then
    save_project=1
    if [ -n "${OF_PROJECT:-}" ]; then
      project="$OF_PROJECT"
    elif [ -f "$env_file" ] || [ "$install_dir" = "/opt/onefrog" ]; then
      # v0.1.1 and earlier always used "onefrog"; keep it so the data volume is reused.
      project=onefrog
    else
      project="onefrog-$(basename "$install_dir" | tr '[:upper:]' '[:lower:]' | tr -c 'a-z0-9_-' '-' | sed 's/^-*//; s/-*$//')"
      [ "$project" != "onefrog-" ] || project="onefrog-$(printf '%s' "$install_dir" | cksum | cut -d ' ' -f 1)"
    fi
    printf '%s' "$project" | grep -Eq '^[a-z0-9][a-z0-9_-]*$' || die "OF_PROJECT 只能包含小写字母、数字、- 和 _，并以字母或数字开头。"
  elif [ -n "${OF_PROJECT:-}" ] && [ "$OF_PROJECT" != "$project" ]; then
    die "这个安装目录已经使用项目名 ${project}，不能改成 ${OF_PROJECT}（会换成一个新的空数据卷）。"
  fi
  # Refuse to take over containers that belong to another install directory.
  local other_dir
  other_dir=$(docker ps -a --filter "label=com.docker.compose.project=${project}" --format '{{ .Label "com.docker.compose.project.working_dir" }}' 2>/dev/null | head -n 1 || true)
  if [ -n "$other_dir" ] && [ "${other_dir%/}" != "$install_dir" ]; then
    die "项目名 ${project} 已经被另一个安装目录 ${other_dir} 使用。继续会接管并重建那边的容器。
要在这台机器上再装一个实例，请换一个安装目录并指定新的项目名和端口，例如:
  OF_INSTALL_DIR=/opt/onefrog-2 OF_PROJECT=onefrog-2 OF_PUBLISH_PORT=8081"
  fi

  # ---- port conflict ----
  local want_port own_port="" users=""
  want_port="${OF_PUBLISH_PORT:-$(saved OF_PUBLISH_PORT)}"; want_port="${want_port:-8080}"
  own_port=$(docker ps --filter "label=com.docker.compose.project=${project}" --filter "publish=${want_port}" -q 2>/dev/null || true)
  if [ -z "$own_port" ]; then
    users=$(docker ps --filter "publish=${want_port}" --format '{{.Names}}' 2>/dev/null | tr '\n' ' ' || true)
    if [ -z "$users" ] && command -v ss >/dev/null 2>&1; then
      users=$(ss -Hltn "sport = :${want_port}" 2>/dev/null | head -n 1 || true)
    fi
    if [ -n "$users" ]; then
      die "端口 ${want_port} 已被占用（${users% }）。请换一个端口，例如带上 OF_PUBLISH_PORT=8081 重新运行。"
    fi
  fi

  # Version currently running, to tell "upgraded" from "already up to date".
  local previous="" cid
  cid=$(docker ps -a --filter "label=com.docker.compose.project=${project}" --filter "label=com.docker.compose.service=app" -q 2>/dev/null | head -n 1 || true)
  if [ -n "$cid" ]; then
    previous=$(docker inspect --format '{{ index .Config.Labels "org.opencontainers.image.version" }}' "$cid" 2>/dev/null || true)
    [ "$previous" != "<no value>" ] || previous=""
  fi

  case "${OF_APPROVAL_MODE:-}" in
    ""|off|smart|manual|plan) ;;
    *) die "OF_APPROVAL_MODE 只能是 off、smart、manual 或 plan。" ;;
  esac
  case "${OF_UPDATE_CHECK:-}" in
    ""|0|1) ;;
    *) die "OF_UPDATE_CHECK 只能是 0 或 1。" ;;
  esac

  # ---- credentials (reused on re-run) ----
  local fresh=0
  if [ ! -f "$env_file" ]; then
    fresh=1
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
      if [ -n "${OF_APPROVAL_MODE:-}" ]; then echo "OF_APPROVAL_MODE=${OF_APPROVAL_MODE}" >> "$env_file"; fi
      if [ -n "${OF_UPDATE_CHECK:-}" ]; then echo "OF_UPDATE_CHECK=${OF_UPDATE_CHECK}" >> "$env_file"; fi
    )
    chmod 600 "$env_file"
    say "已生成初始账号，保存在 ${env_file}（权限 600）。"
  else
    say "检测到已有安装，将原地升级：沿用 ${env_file} 和数据卷，不会重置账号、设置或数据。"
    # Allow changing port / bind address on re-run without touching credentials.
    local k
    for k in OF_PUBLISH_PORT OF_BIND_ADDR OF_PUBLIC_URL OF_APPROVAL_MODE OF_UPDATE_CHECK; do
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

  if [ "$save_project" = "1" ]; then
    printf 'OF_PROJECT=%s\n' "$project" >> "$env_file"
  fi

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
      OF_HOST_INSTALL_DIR: "${install_dir}"
      OF_PROJECT: "${project}"
    ports:
      - "${bind_addr}:${pub_port}:8080"
    volumes:
      - onefrog-data:/app/data
volumes:
  onefrog-data:
YAML

  say "正在启动..."
  docker compose --project-directory "$install_dir" -f "$compose_file" -p "$project" up -d --remove-orphans

  local ready=0 i=0
  while [ "$i" -lt 60 ]; do
    if curl -fsS --max-time 3 "http://127.0.0.1:${pub_port}/api/health" >/dev/null 2>&1; then
      ready=1; break
    fi
    i=$((i + 1)); sleep 2
  done

  local installed
  installed=$(docker image inspect --format '{{ index .Config.Labels "org.opencontainers.image.version" }}' "$ref" 2>/dev/null || true)
  [ -n "$installed" ] && [ "$installed" != "<no value>" ] || installed="$ref"

  say ""
  say "========================================"
  if [ "$fresh" = "1" ]; then
    say "OneFrog 已安装（${installed}）"
    say "链接: ${public}"
    say "用户名: ${admin_user}"
    say "初始密码: ${pass}"
    say "========================================"
    say "第一次登录必须修改用户名和密码，改完之前不能使用面板里的其他功能。"
    say "初始密码也保存在 ${env_file}（仅 root 可读）。"
  else
    if [ -n "$previous" ] && [ "$previous" = "$installed" ]; then
      say "OneFrog 已是最新版本（${installed}）"
    elif [ -n "$previous" ]; then
      say "OneFrog 已从 ${previous} 升级到 ${installed}"
    else
      say "OneFrog 已更新到 ${installed}"
    fi
    say "链接: ${public}"
    say "========================================"
    say "账号、设置和数据都已保留，请用你自己设置的用户名和密码登录。"
  fi
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
  say "常用命令（先 cd ${install_dir}）:"
  say "  docker compose -p ${project} ps        # 查看状态"
  say "  docker compose -p ${project} logs -f   # 查看日志"
  say "  docker compose -p ${project} restart   # 重启"
  if [ "$ready" -ne 1 ]; then
    printf '服务还没有通过健康检查。查看日志: cd %s && docker compose -p %s logs\n' "$install_dir" "$project" >&2
    exit 1
  fi
}

main "$@"
