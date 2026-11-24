#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 yreidev 与贡献者（见 NOTICE）
# SPDX-License-Identifier: AGPL-3.0-only
# 复制或改写本文件须保留以上声明，并按 NOTICE 的署名条款注明出处：https://github.com/yreidev/workbuddy2api-panel

# Linux 一键安装 / 原地升级 / 卸载脚本（默认配置 systemd 服务）。
# 从 GitHub Releases 下载对应架构的二进制压缩包并强制校验 checksums.txt（SHA-256），
# 保留 LICENSE / NOTICE，创建 auths/、data/、data/qoder-umid/sdk 及 .config 软链。
set -euo pipefail

REPO="${WB2A_REPO:-yreidev/workbuddy2api-panel}"
VERSION="latest"
INSTALL_DIR="/opt/workbuddy2api"
RUN_USER="wb2api"
RUN_GROUP=""
SERVICE_NAME="workbuddy2api"
LISTEN_ADDR=""
PROXY_URL=""
NO_SYSTEMD=0
UNINSTALL=0
PURGE=0
DRY_RUN=0

TMPDIR_WORK=""
cleanup() {
  if [[ -n "$TMPDIR_WORK" && -d "$TMPDIR_WORK" ]]; then
    rm -rf "$TMPDIR_WORK"
  fi
}
trap cleanup EXIT

usage() {
  cat <<'EOF'
WorkBuddy2API Panel Linux 一键安装脚本

用法：
  sudo bash scripts/install-linux.sh [选项]

选项：
  --version <ver>     指定版本（默认 latest；可填如 1.14.0、v1.14.0、1.14.0-rc.1）
  --dir <path>        安装目录（默认 /opt/workbuddy2api，须为绝对路径）
  --user <name>       systemd 运行用户（默认 wb2api，不存在时自动创建系统用户）
  --service <name>    systemd 服务名（默认 workbuddy2api）
  --listen <addr>     监听地址（写入 systemd 的 WB2A_LISTEN，如 127.0.0.1:7863）
  --proxy <url>       下载与服务出站代理（如 http://127.0.0.1:7890 或 socks5h://127.0.0.1:1080）
  --no-systemd        仅下载校验并解包布置目录，不写 systemd 单元也不启动服务
  --uninstall         卸载服务与主程序（默认保留 auths/ 与 data/ 数据）
  --purge             与 --uninstall 联用，连同安装目录下的 auths/ 与 data/ 一并删除
  --dry-run           仅打印安装/卸载计划与 systemd 服务内容，不联网也不改本机文件
  -h, --help          显示本帮助
EOF
}

die() {
  echo "错误：$*" >&2
  exit 1
}

require_value() {
  local opt="$1"
  local val="${2:-}"
  if [[ -z "$val" || "$val" == -* ]]; then
    die "参数 ${opt} 需要一个值"
  fi
}

reject_unsafe_chars() {
  local name="$1"
  local val="$2"
  if [[ "$val" =~ [[:space:][:cntrl:]] ]]; then
    die "参数 ${name} 不能包含空白或控制字符"
  fi
}

parse_args() {
  while [[ $# -gt 0 ]]; do
    case "$1" in
      --version)
        require_value "$1" "${2:-}"
        VERSION="$2"
        shift 2
        ;;
      --version=*)
        VERSION="${1#*=}"
        require_value "--version" "$VERSION"
        shift
        ;;
      --dir)
        require_value "$1" "${2:-}"
        INSTALL_DIR="$2"
        shift 2
        ;;
      --dir=*)
        INSTALL_DIR="${1#*=}"
        require_value "--dir" "$INSTALL_DIR"
        shift
        ;;
      --user)
        require_value "$1" "${2:-}"
        RUN_USER="$2"
        shift 2
        ;;
      --user=*)
        RUN_USER="${1#*=}"
        require_value "--user" "$RUN_USER"
        shift
        ;;
      --service)
        require_value "$1" "${2:-}"
        SERVICE_NAME="$2"
        shift 2
        ;;
      --service=*)
        SERVICE_NAME="${1#*=}"
        require_value "--service" "$SERVICE_NAME"
        shift
        ;;
      --listen)
        require_value "$1" "${2:-}"
        LISTEN_ADDR="$2"
        shift 2
        ;;
      --listen=*)
        LISTEN_ADDR="${1#*=}"
        require_value "--listen" "$LISTEN_ADDR"
        shift
        ;;
      --proxy)
        require_value "$1" "${2:-}"
        PROXY_URL="$2"
        shift 2
        ;;
      --proxy=*)
        PROXY_URL="${1#*=}"
        require_value "--proxy" "$PROXY_URL"
        shift
        ;;
      --no-systemd)
        NO_SYSTEMD=1
        shift
        ;;
      --uninstall)
        UNINSTALL=1
        shift
        ;;
      --purge)
        PURGE=1
        shift
        ;;
      --dry-run)
        DRY_RUN=1
        shift
        ;;
      -h|--help)
        usage
        exit 0
        ;;
      *)
        die "未知参数：$1（用 --help 查看用法）"
        ;;
    esac
  done
}

validate_args() {
  if [[ "$PURGE" -eq 1 && "$UNINSTALL" -ne 1 ]]; then
    die "--purge 必须与 --uninstall 一起使用"
  fi

  reject_unsafe_chars "--version" "$VERSION"
  if [[ "$VERSION" != "latest" && ! "$VERSION" =~ ^v?[0-9]+\.[0-9]+\.[0-9]+([.-][0-9A-Za-z.-]+)?$ ]]; then
    die "--version 格式不合法：$VERSION（应为 latest 或语义化版本号，如 1.14.0）"
  fi

  reject_unsafe_chars "--dir" "$INSTALL_DIR"
  if [[ "$INSTALL_DIR" != /* ]]; then
    die "--dir 必须是绝对路径：$INSTALL_DIR"
  fi
  while [[ "$INSTALL_DIR" == *//* ]]; do
    INSTALL_DIR="${INSTALL_DIR//\/\//\/}"
  done
  while [[ "$INSTALL_DIR" == */ ]]; do
    INSTALL_DIR="${INSTALL_DIR%/}"
  done
  if [[ -z "$INSTALL_DIR" ]]; then
    die "--dir 不能为根目录 /"
  fi
  if [[ "/${INSTALL_DIR}/" == */../* || "/${INSTALL_DIR}/" == */./* ]]; then
    die "--dir 不能包含 . 或 .. 路径段：$INSTALL_DIR"
  fi

  reject_unsafe_chars "--user" "$RUN_USER"
  if [[ ! "$RUN_USER" =~ ^[a-z_][a-z0-9_-]*\$?$ ]]; then
    die "--user 用户名不合法：$RUN_USER"
  fi

  reject_unsafe_chars "--service" "$SERVICE_NAME"
  SERVICE_NAME="${SERVICE_NAME%.service}"
  if [[ -z "$SERVICE_NAME" || ! "$SERVICE_NAME" =~ ^[A-Za-z0-9._@-]+$ ]]; then
    die "--service 服务名不合法：$SERVICE_NAME"
  fi

  if [[ -n "$LISTEN_ADDR" ]]; then
    reject_unsafe_chars "--listen" "$LISTEN_ADDR"
  fi
  if [[ -n "$PROXY_URL" ]]; then
    reject_unsafe_chars "--proxy" "$PROXY_URL"
  fi
}

detect_arch() {
  local raw="${WB2A_ARCH:-$(uname -m)}"
  case "$raw" in
    x86_64|amd64) ARCH="amd64" ;;
    aarch64|arm64) ARCH="arm64" ;;
    *) die "暂不支持的 Linux 架构：${raw}（目前提供 linux/amd64 与 linux/arm64 预编译包）" ;;
  esac
}

resolve_release() {
  if [[ "$VERSION" == "latest" ]]; then
    RELEASE_TAG="latest"
    ARCHIVE_NAME="wb2api-panel-linux-${ARCH}.tar.gz"
  else
    local clean="${VERSION#v}"
    RELEASE_TAG="v${clean}"
    ARCHIVE_NAME="wb2api-panel-v${clean}-linux-${ARCH}.tar.gz"
  fi
  BASE_URL="https://github.com/${REPO}/releases/download/${RELEASE_TAG}"
  ARCHIVE_URL="${BASE_URL}/${ARCHIVE_NAME}"
  CHECKSUMS_URL="${BASE_URL}/checksums.txt"
}

escape_systemd() {
  local val="$1"
  printf '%s' "${val//%/%%}"
}

resolve_run_group() {
  RUN_GROUP="$(id -gn "$RUN_USER" 2>/dev/null || echo "$RUN_USER")"
}

render_service() {
  local protect_home="true"
  if [[ "$INSTALL_DIR" == "/home" || "$INSTALL_DIR" == /home/* || "$INSTALL_DIR" == "/root" || "$INSTALL_DIR" == /root/* ]]; then
    protect_home="false"
  fi
  local group="${RUN_GROUP:-$RUN_USER}"
  local dir_sd listen_sd proxy_sd
  dir_sd="$(escape_systemd "$INSTALL_DIR")"
  listen_sd="$(escape_systemd "$LISTEN_ADDR")"
  proxy_sd="$(escape_systemd "$PROXY_URL")"
  cat <<EOF
# SPDX-FileCopyrightText: 2026 yreidev 与贡献者（见 NOTICE）
# SPDX-License-Identifier: AGPL-3.0-only
# 复制或改写本文件须保留以上声明，并按 NOTICE 的署名条款注明出处：https://github.com/yreidev/workbuddy2api-panel

[Unit]
Description=WorkBuddy2API Panel 网关服务
Documentation=https://github.com/yreidev/workbuddy2api-panel
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
User=${RUN_USER}
Group=${group}
WorkingDirectory=${dir_sd}
Environment=HOME=${dir_sd}
Environment=TZ=Asia/Shanghai
EOF
  if [[ -n "$LISTEN_ADDR" ]]; then
    printf 'Environment=WB2A_LISTEN=%s\n' "$listen_sd"
  fi
  if [[ -n "$PROXY_URL" ]]; then
    printf 'Environment=HTTPS_PROXY=%s\n' "$proxy_sd"
    printf 'Environment=HTTP_PROXY=%s\n' "$proxy_sd"
    printf 'Environment=ALL_PROXY=%s\n' "$proxy_sd"
  fi
  cat <<EOF
ExecStart=${dir_sd}/wb2api -config ${dir_sd}/data/config.json
Restart=on-failure
RestartSec=5s
TimeoutStopSec=30s
KillMode=mixed
LimitNOFILE=65535

NoNewPrivileges=true
PrivateTmp=true
ProtectSystem=strict
ProtectHome=${protect_home}
ReadWritePaths=${dir_sd}/auths ${dir_sd}/data

[Install]
WantedBy=multi-user.target
EOF
}

require_cmd() {
  command -v "$1" >/dev/null 2>&1 || die "缺少依赖命令：$1"
}

compute_sha256() {
  local file="$1"
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum "$file" | awk '{print $1}'
  elif command -v shasum >/dev/null 2>&1; then
    shasum -a 256 "$file" | awk '{print $1}'
  else
    die "缺少 sha256sum 或 shasum，无法校验压缩包完整性"
  fi
}

download_to() {
  local url="$1"
  local dest="$2"
  local curl_args=(-fsSL --retry 2 --connect-timeout 20 -o "$dest")
  if [[ -n "$PROXY_URL" ]]; then
    curl_args+=(-x "$PROXY_URL")
  fi
  curl "${curl_args[@]}" "$url"
}

ensure_system_user() {
  if id -u "$RUN_USER" >/dev/null 2>&1; then
    return
  fi
  local nologin="/usr/sbin/nologin"
  if [[ ! -x "$nologin" ]]; then
    nologin="/sbin/nologin"
  fi
  if [[ ! -x "$nologin" ]]; then
    nologin="/bin/false"
  fi
  if command -v useradd >/dev/null 2>&1; then
    useradd --system --user-group --home-dir "$INSTALL_DIR" --no-create-home --shell "$nologin" "$RUN_USER"
  elif command -v adduser >/dev/null 2>&1; then
    adduser --system --group --home "$INSTALL_DIR" --no-create-home --shell "$nologin" "$RUN_USER"
  else
    die "系统缺少 useradd/adduser，无法自动创建用户 ${RUN_USER}；请先手动创建或用 --user 指定已有用户"
  fi
}

unit_path() {
  printf '/etc/systemd/system/%s.service' "$SERVICE_NAME"
}

print_dry_run_uninstall() {
  cat <<EOF
[dry-run] 计划执行卸载：
  安装目录：${INSTALL_DIR}
  systemd：$([[ "$NO_SYSTEMD" -eq 1 ]] && echo "跳过（--no-systemd）" || echo "停止、禁用并删除 $(unit_path)")
  删除文件：wb2api、LICENSE、NOTICE、README.md、config.example.json、internal/providers/**/NOTICE
  数据目录：$([[ "$PURGE" -eq 1 ]] && echo "彻底删除 ${INSTALL_DIR}（含 auths/ 与 data/）" || echo "保留 ${INSTALL_DIR}/auths 与 ${INSTALL_DIR}/data")
EOF
}

do_uninstall() {
  if [[ "$DRY_RUN" -eq 1 ]]; then
    print_dry_run_uninstall
    return
  fi
  if [[ "$NO_SYSTEMD" -eq 0 && "$(id -u)" -ne 0 ]]; then
    die "卸载 systemd 服务需要 root 权限（请使用 sudo，或仅卸载用户目录时加 --no-systemd）"
  fi
  if [[ "$NO_SYSTEMD" -eq 0 ]] && command -v systemctl >/dev/null 2>&1; then
    local up
    up="$(unit_path)"
    if systemctl list-unit-files "${SERVICE_NAME}.service" >/dev/null 2>&1; then
      systemctl stop "${SERVICE_NAME}.service" 2>/dev/null || true
      systemctl disable "${SERVICE_NAME}.service" 2>/dev/null || true
    fi
    if [[ -f "$up" ]]; then
      rm -f "$up"
      systemctl daemon-reload
    fi
  fi

  if [[ "$PURGE" -eq 1 ]]; then
    if [[ -d "$INSTALL_DIR" ]]; then
      rm -rf "$INSTALL_DIR"
    fi
    echo "已完成彻底卸载，已删除 ${INSTALL_DIR}（含 auths/ 与 data/）。"
    return
  fi

  rm -f \
    "${INSTALL_DIR}/wb2api" \
    "${INSTALL_DIR}/LICENSE" \
    "${INSTALL_DIR}/NOTICE" \
    "${INSTALL_DIR}/README.md" \
    "${INSTALL_DIR}/config.example.json" \
    "${INSTALL_DIR}/internal/providers/NOTICE" \
    "${INSTALL_DIR}/internal/providers/cosy/NOTICE"
  rmdir "${INSTALL_DIR}/internal/providers/cosy" 2>/dev/null || true
  rmdir "${INSTALL_DIR}/internal/providers" 2>/dev/null || true
  rmdir "${INSTALL_DIR}/internal" 2>/dev/null || true

  echo "已卸载主程序与服务；已保留数据目录："
  echo "  - ${INSTALL_DIR}/auths"
  echo "  - ${INSTALL_DIR}/data"
  echo "如需彻底清除全部数据，请加 --purge 重新执行。"
}

print_dry_run_install() {
  if [[ "$NO_SYSTEMD" -eq 0 ]]; then
    resolve_run_group
  fi
  cat <<EOF
[dry-run] 计划执行安装/升级：
  仓库：${REPO}
  架构：linux/${ARCH}
  版本标签：${RELEASE_TAG}
  压缩包地址：${ARCHIVE_URL}
  校验文件：${CHECKSUMS_URL}（强制 SHA-256 校验）
  安装目录：${INSTALL_DIR}
  目录布置：${INSTALL_DIR}/auths（0700）、${INSTALL_DIR}/data（0700）、${INSTALL_DIR}/data/qoder-umid/sdk（0700）
  SDK 软链：${INSTALL_DIR}/.config -> ${INSTALL_DIR}/data/qoder-umid/sdk
  随包保留：wb2api、LICENSE、NOTICE、README.md、config.example.json、internal/providers/**/NOTICE
  运行用户：$([[ "$NO_SYSTEMD" -eq 1 ]] && echo "当前用户（--no-systemd）" || echo "${RUN_USER}:${RUN_GROUP}")
  systemd：$([[ "$NO_SYSTEMD" -eq 1 ]] && echo "跳过（--no-systemd）" || echo "写入 $(unit_path) 并启用启动")
  查看密钥命令（不打印明文）：grep api_key ${INSTALL_DIR}/data/config.json
EOF
  if [[ "$NO_SYSTEMD" -eq 0 ]]; then
    echo ""
    echo "--- 预计写入的 $(unit_path) ---"
    render_service
  fi
}

do_install() {
  if [[ "$DRY_RUN" -eq 1 ]]; then
    print_dry_run_install
    return
  fi

  if [[ "$NO_SYSTEMD" -eq 0 && "$(id -u)" -ne 0 ]]; then
    die "安装 systemd 服务需要 root 权限，请使用 sudo 运行（或加 --no-systemd 安装到有写权限的目录）"
  fi
  require_cmd curl
  require_cmd tar
  if [[ "$NO_SYSTEMD" -eq 0 ]]; then
    require_cmd systemctl
  fi

  TMPDIR_WORK="$(mktemp -d)"
  local archive_file="${TMPDIR_WORK}/${ARCHIVE_NAME}"
  local checksums_file="${TMPDIR_WORK}/checksums.txt"

  echo "正在下载 ${ARCHIVE_URL} ..."
  download_to "$ARCHIVE_URL" "$archive_file"
  echo "正在下载校验文件 ${CHECKSUMS_URL} ..."
  download_to "$CHECKSUMS_URL" "$checksums_file"

  local expected actual
  expected="$(awk -v f="$ARCHIVE_NAME" '$2 == f || $2 == ("*" f) { print tolower($1); exit }' "$checksums_file")"
  if [[ -z "$expected" ]]; then
    die "checksums.txt 中找不到 ${ARCHIVE_NAME} 的 SHA-256 记录，已中止安装"
  fi
  actual="$(compute_sha256 "$archive_file" | tr 'A-F' 'a-f')"
  if [[ "$actual" != "$expected" ]]; then
    die "SHA-256 校验失败！预期 ${expected}，实际 ${actual}"
  fi
  echo "SHA-256 校验通过：${actual}"

  local unpack_dir="${TMPDIR_WORK}/unpack"
  mkdir -p "$unpack_dir"
  tar -xzf "$archive_file" -C "$unpack_dir"
  [[ -f "${unpack_dir}/wb2api" ]] || die "压缩包内缺少 wb2api 主程序"
  [[ -f "${unpack_dir}/LICENSE" && -f "${unpack_dir}/NOTICE" ]] || die "压缩包内缺少 LICENSE 或 NOTICE"

  if [[ "$NO_SYSTEMD" -eq 0 ]]; then
    ensure_system_user
    resolve_run_group
    if systemctl is-active --quiet "${SERVICE_NAME}.service" 2>/dev/null; then
      echo "检测到旧版服务正在运行，先停止以便安全原地升级..."
      systemctl stop "${SERVICE_NAME}.service"
    fi
  fi

  mkdir -p "${INSTALL_DIR}" "${INSTALL_DIR}/auths" "${INSTALL_DIR}/data" "${INSTALL_DIR}/data/qoder-umid/sdk"
  chmod 0755 "${INSTALL_DIR}"
  chmod 0700 "${INSTALL_DIR}/auths" "${INSTALL_DIR}/data" "${INSTALL_DIR}/data/qoder-umid/sdk"

  # 官方 Qoder UMID SDK 读取 $HOME/.config/.locale_cfg；软链到 data/ 便于备份与持久化
  if [[ -e "${INSTALL_DIR}/.config" && ! -L "${INSTALL_DIR}/.config" ]]; then
    echo "警告：${INSTALL_DIR}/.config 已存在且不是符号链接，保持原样不覆盖。" >&2
  else
    ln -sfn "${INSTALL_DIR}/data/qoder-umid/sdk" "${INSTALL_DIR}/.config"
  fi

  install -m 0755 "${unpack_dir}/wb2api" "${INSTALL_DIR}/wb2api.new"
  mv -f "${INSTALL_DIR}/wb2api.new" "${INSTALL_DIR}/wb2api"

  for f in LICENSE NOTICE README.md config.example.json internal/providers/NOTICE internal/providers/cosy/NOTICE; do
    if [[ -f "${unpack_dir}/${f}" ]]; then
      mkdir -p "$(dirname "${INSTALL_DIR}/${f}")"
      install -m 0644 "${unpack_dir}/${f}" "${INSTALL_DIR}/${f}"
    fi
  done

  if [[ "$NO_SYSTEMD" -eq 0 && "$(id -u)" -eq 0 ]]; then
    chown -R "${RUN_USER}:${RUN_GROUP}" "${INSTALL_DIR}/auths" "${INSTALL_DIR}/data"
    if [[ -L "${INSTALL_DIR}/.config" ]]; then
      chown -h "${RUN_USER}:${RUN_GROUP}" "${INSTALL_DIR}/.config" || true
    fi
    local up
    up="$(unit_path)"
    render_service > "$up"
    chmod 0644 "$up"
    systemctl daemon-reload
    systemctl enable --now "${SERVICE_NAME}.service"
  fi

  local listen_hint="${LISTEN_ADDR:-127.0.0.1:7863}"
  if [[ "$listen_hint" == :* ]]; then
    listen_hint="127.0.0.1${listen_hint}"
  fi

  echo ""
  echo "安装完成（版本：${RELEASE_TAG}，架构：linux/${ARCH}，目录：${INSTALL_DIR}）。"
  if [[ "$NO_SYSTEMD" -eq 0 ]]; then
    echo "  查看服务状态：sudo systemctl status ${SERVICE_NAME}"
    echo "  查看运行日志：sudo journalctl -u ${SERVICE_NAME} -f"
    echo "  查看管理员密钥：sudo grep api_key ${INSTALL_DIR}/data/config.json"
  else
    echo "  手动启动示例：HOME=${INSTALL_DIR} ${INSTALL_DIR}/wb2api -config ${INSTALL_DIR}/data/config.json"
    echo "  首次启动后查看管理员密钥：grep api_key ${INSTALL_DIR}/data/config.json"
  fi
  echo "  管理面板地址：http://${listen_hint}/panel/"
  echo "  说明：若使用 Qoder 自动设备初始化，宿主机需有 glibc 与 libstdc++（经代理初始化/刷新还需 proxychains4）；原生 ARM64 硬件尚未实测，遇不兼容可导入官方设备身份。"
}

main() {
  parse_args "$@"
  validate_args
  if [[ "$UNINSTALL" -eq 1 ]]; then
    do_uninstall
  else
    detect_arch
    resolve_release
    do_install
  fi
}

main "$@"
