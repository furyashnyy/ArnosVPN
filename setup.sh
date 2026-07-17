#!/usr/bin/env bash
#
# ArnosVPN server setup (bare metal, no Docker).
#
# An interactive question-and-answer wizard: it asks a few questions (domain,
# e-mail, port…), then builds the server from this repo, installs it as a systemd
# service, and configures it to terminate TLS itself with a Let's Encrypt
# certificate for your domain. The default, "install and configure in one go" path.
#
# Usage:
#   sudo ./setup.sh                         # interactive wizard (recommended)
#   sudo ./setup.sh vpn.example.com you@example.com    # pre-fill the answers
#   sudo ARNOS_DOMAIN=vpn.example.com ./setup.sh        # non-interactive (no TTY)
#   sudo ./setup.sh uninstall               # completely remove ArnosVPN
#
# Requirements: a public host where DNS for your domain points here and the
# chosen port (443 by default) is free and reachable. Debian/Ubuntu, RHEL/Fedora,
# Alpine and Arch are supported for dependency installation.
set -euo pipefail

REPO_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
GO_MIN="1.24"
PREFIX="/usr/local/bin"
ETC_DIR="/etc/arnosvpn"
STATE_DIR="/var/lib/arnosvpn"
ENV_FILE="${ETC_DIR}/arnosvpn.env"
UNIT="/etc/systemd/system/arnosvpn.service"

log()  { printf '\033[1;34m[arnosvpn]\033[0m %s\n' "$*"; }
warn() { printf '\033[1;33m[arnosvpn]\033[0m %s\n' "$*" >&2; }
die()  { printf '\033[1;31m[arnosvpn]\033[0m %s\n' "$*" >&2; exit 1; }

[ "$(id -u)" = "0" ] || die "запустите от root (sudo ./setup.sh)"

# ask PROMPT [DEFAULT] -> prints the answer (or the default on empty input).
# The prompt goes to stderr so it stays visible under command substitution.
ask() {
  local ans
  if [ -n "${2:-}" ]; then read -rp "$1 [$2]: " ans; echo "${ans:-$2}"
  else read -rp "$1: " ans; echo "$ans"; fi
}
ask_yn() { # ask_yn PROMPT DEFAULT(y/n) -> returns 0 for yes
  local d="${2:-n}" ans hint="y/N"; [ "$d" = "y" ] && hint="Y/n"
  read -rp "$1 ($hint): " ans; ans="${ans:-$d}"; [[ "$ans" =~ ^[YyДд] ]]
}

# uninstall removes ArnosVPN completely. It is deliberately hard to trigger by
# accident: the operator must type "I UNDERSTAND" and then the project path
# exactly as shown (auto-detected — where this script lives).
uninstall() {
  local path="$REPO_DIR" port="443" c1 c2
  if [ -f "$ENV_FILE" ]; then
    port="$(grep -E '^ARNOS_PUBLIC_PORT=' "$ENV_FILE" 2>/dev/null | head -1 | cut -d= -f2 || true)"
    [ -n "$port" ] || port="443"
  fi
  echo
  warn "ПОЛНОЕ УДАЛЕНИЕ ArnosVPN"
  echo "Будут безвозвратно удалены:"
  echo "  • systemd-сервис   arnosvpn.service"
  echo "  • бинарники        ${PREFIX}/arnosvpn-server, ${PREFIX}/arnosvpnctl"
  echo "  • конфигурация     ${ETC_DIR}"
  echo "  • состояние        ${STATE_DIR}  (PSK и сертификаты!)"
  echo "  • каталог проекта  ${path}"
  echo
  [ -t 0 ] || die "удаление требует подтверждения — запустите в терминале"

  echo "Для подтверждения введите точно:  I UNDERSTAND"
  read -rp "> " c1
  [ "$c1" = "I UNDERSTAND" ] || die "не подтверждено — отменено"
  echo "Теперь введите путь к ArnosVPN точно как показано:  ${path}"
  read -rp "> " c2
  [ "$c2" = "$path" ] || die "путь не совпадает — отменено"

  log "останавливаю и удаляю сервис"
  systemctl disable --now arnosvpn.service >/dev/null 2>&1 || true
  rm -f "$UNIT"
  systemctl daemon-reload >/dev/null 2>&1 || true

  log "закрываю порт ${port} в фаерволе (если был открыт)"
  if command -v ufw >/dev/null 2>&1; then ufw delete allow "${port}/tcp" >/dev/null 2>&1 || true; fi
  if command -v firewall-cmd >/dev/null 2>&1; then
    firewall-cmd --permanent --remove-port="${port}/tcp" >/dev/null 2>&1 || true
    firewall-cmd --reload >/dev/null 2>&1 || true
  fi

  log "удаляю бинарники, конфигурацию и состояние"
  rm -f "$PREFIX/arnosvpn-server" "$PREFIX/arnosvpnctl"
  rm -rf "$ETC_DIR" "$STATE_DIR"
  rm -f /etc/sysctl.d/99-arnosvpn.conf /etc/modules-load.d/arnosvpn.conf

  log "ArnosVPN удалён. Удаляю каталог проекта ${path} …"
  cd / && rm -rf "$path"
  log "готово."
}

# CLI entry point for removal: `setup.sh uninstall`.
case "${1:-}" in
  uninstall|remove|--uninstall|-u) uninstall; exit 0 ;;
esac

# --- gather answers ---------------------------------------------------------
# Pre-fill from args/env; the wizard runs when attached to a terminal.
DOMAIN="${1:-${ARNOS_DOMAIN:-}}"
EMAIL="${2:-${ARNOS_ACME_EMAIL:-}}"
PORT="${ARNOS_PUBLIC_PORT:-443}"
DNS="${ARNOS_DNS:-1.1.1.1,1.0.0.1}"
PSK="${ARNOS_PSK:-}"

if [ -t 0 ]; then
  echo
  log "ArnosVPN — выберите действие:"
  echo "  1) Установить / обновить"
  echo "  2) Полностью удалить"
  if [ "$(ask "Действие" "1")" = "2" ]; then uninstall; exit 0; fi
  echo
  log "Установка — ответьте на несколько вопросов."
  echo
  DOMAIN="$(ask "Домен, на который подключаются клиенты (например vpn.example.com)" "$DOMAIN")"
  [ -n "$DOMAIN" ] || die "домен обязателен"
  EMAIL="$(ask "E-mail для Let's Encrypt (Enter — пропустить)" "$EMAIL")"
  PORT="$(ask "Порт HTTPS/WSS" "$PORT")"
  DNS="$(ask "DNS для клиентов (через запятую)" "$DNS")"
  if ask_yn "Задать свой пароль-ключ (PSK)? Иначе будет сгенерирован автоматически" "n"; then
    PSK="$(ask "PSK (base64, 32 байта)" "$PSK")"
  fi
  echo
  log "Проверьте настройки:"
  echo "    Домен:        $DOMAIN"
  echo "    E-mail:       ${EMAIL:-<нет>}"
  echo "    Порт:         $PORT"
  echo "    DNS:          $DNS"
  echo "    PSK:          $([ -n "$PSK" ] && echo 'задан вручную' || echo 'сгенерировать')"
  echo
  ask_yn "Продолжить установку?" "y" || die "отменено"
else
  # Non-interactive (piped / no TTY): use args/env and defaults.
  [ -n "$DOMAIN" ] || die "ARNOS_DOMAIN обязателен в неинтерактивном режиме"
fi

# --- dependencies -----------------------------------------------------------
install_deps() {
  local pkgs_common="iptables iproute2 ca-certificates curl git"
  if   command -v apt-get >/dev/null 2>&1; then
    log "installing dependencies with apt"
    DEBIAN_FRONTEND=noninteractive apt-get update -qq
    DEBIAN_FRONTEND=noninteractive apt-get install -y -qq $pkgs_common wget >/dev/null
  elif command -v dnf >/dev/null 2>&1; then
    log "installing dependencies with dnf"; dnf install -y -q $pkgs_common wget >/dev/null
  elif command -v yum >/dev/null 2>&1; then
    log "installing dependencies with yum"; yum install -y -q $pkgs_common wget >/dev/null
  elif command -v apk >/dev/null 2>&1; then
    log "installing dependencies with apk"; apk add --no-cache iptables ip6tables iproute2 ca-certificates curl git wget >/dev/null
  elif command -v pacman >/dev/null 2>&1; then
    log "installing dependencies with pacman"; pacman -Sy --noconfirm --needed iptables iproute2 ca-certificates curl git wget >/dev/null
  else
    warn "unknown package manager — ensure iptables, iproute2, ca-certificates and git are installed"
  fi
}
install_deps

# --- source tree ------------------------------------------------------------
# setup.sh builds the server from source, so it needs the repository next to it
# (go.mod + ./cmd/...). When it is run outside a full checkout — e.g. only the
# script was downloaded, or it was piped from curl — fetch the source into a temp
# dir and build from there. Override the source with ARNOS_REPO_URL (and, for a
# private repo, embed a token, or clone it yourself and run from inside it).
if [ ! -f "$REPO_DIR/go.mod" ]; then
  REPO_URL="${ARNOS_REPO_URL:-https://github.com/furyashnyy/ArnosVPN.git}"
  BRANCH="${ARNOS_REPO_BRANCH:-main}"
  log "no source tree next to setup.sh — cloning ${REPO_URL} (${BRANCH})"
  command -v git >/dev/null 2>&1 || die "git is required to fetch the source; install git and re-run"
  SRC_DIR="$(mktemp -d)"
  git clone --depth 1 --branch "$BRANCH" "$REPO_URL" "$SRC_DIR" >/dev/null 2>&1 \
    || git clone --depth 1 "$REPO_URL" "$SRC_DIR" >/dev/null 2>&1 \
    || die "failed to clone ${REPO_URL}; for a private repo set ARNOS_REPO_URL with a token, or clone it yourself and run ./setup.sh from inside"
  [ -f "$SRC_DIR/go.mod" ] || die "cloned ${REPO_URL} but it has no go.mod — wrong repository?"
  REPO_DIR="$SRC_DIR"
  CLEANUP_SRC="$SRC_DIR"
fi

# --- Go toolchain (only needed to build) ------------------------------------
have_go() {
  command -v go >/dev/null 2>&1 || return 1
  local v; v="$(go env GOVERSION 2>/dev/null | sed 's/^go//')" || return 1
  [ -n "$v" ] && [ "$(printf '%s\n%s\n' "$GO_MIN" "$v" | sort -V | head -1)" = "$GO_MIN" ]
}
GO=go
if ! have_go; then
  log "installing a temporary Go toolchain to build the server"
  arch="$(uname -m)"; case "$arch" in
    x86_64|amd64) garch=amd64;; aarch64|arm64) garch=arm64;;
    armv7l|armv6l) garch=armv6l;; *) die "unsupported CPU arch: $arch";;
  esac
  gover="$(curl -fsSL https://go.dev/VERSION?m=text | head -1)"
  [ -n "$gover" ] || die "could not determine latest Go version"
  tmp="$(mktemp -d)"
  log "downloading ${gover} (${garch})"
  curl -fsSL "https://go.dev/dl/${gover}.linux-${garch}.tar.gz" -o "$tmp/go.tgz"
  rm -rf "$tmp/go" && tar -C "$tmp" -xzf "$tmp/go.tgz"
  GO="$tmp/go/bin/go"
  CLEANUP_GO="$tmp"
fi

# --- build ------------------------------------------------------------------
# Build to a temp dir and move into place, so re-running to upgrade doesn't hit
# "text file busy" replacing the binary of a running service.
log "building arnosvpn-server and arnosvpnctl"
BUILD_DIR="$(mktemp -d)"
( cd "$REPO_DIR" && CGO_ENABLED=0 "$GO" build -trimpath -ldflags="-s -w" -o "$BUILD_DIR/arnosvpn-server" ./cmd/arnosvpn-server )
( cd "$REPO_DIR" && CGO_ENABLED=0 "$GO" build -trimpath -ldflags="-s -w" -o "$BUILD_DIR/arnosvpnctl" ./cmd/arnosvpnctl )
install -d "$PREFIX"
mv -f "$BUILD_DIR/arnosvpn-server" "$PREFIX/arnosvpn-server"
mv -f "$BUILD_DIR/arnosvpnctl" "$PREFIX/arnosvpnctl"
chmod 755 "$PREFIX/arnosvpn-server" "$PREFIX/arnosvpnctl"
rm -rf "$BUILD_DIR"
[ -n "${CLEANUP_GO:-}" ] && rm -rf "$CLEANUP_GO"
[ -n "${CLEANUP_SRC:-}" ] && rm -rf "$CLEANUP_SRC"

# --- system configuration ---------------------------------------------------
log "enabling IP forwarding and the TUN module"
modprobe tun 2>/dev/null || true
echo tun > /etc/modules-load.d/arnosvpn.conf
cat > /etc/sysctl.d/99-arnosvpn.conf <<'EOF'
net.ipv4.ip_forward=1
net.ipv6.conf.all.forwarding=1
EOF
sysctl --system >/dev/null 2>&1 || sysctl -w net.ipv4.ip_forward=1 >/dev/null 2>&1 || true

mkdir -p "$ETC_DIR" "$STATE_DIR"
chmod 700 "$STATE_DIR"

log "writing ${ENV_FILE}"
cat > "$ENV_FILE" <<EOF
# ArnosVPN server configuration (bare-metal / self-TLS). Generated by setup.sh.
ARNOS_DOMAIN=${DOMAIN}
ARNOS_TLS_MODE=self
CERT_PROVIDER=letsencrypt
ARNOS_ACME_EMAIL=${EMAIL}
ARNOS_LISTEN=:${PORT}
ARNOS_PUBLIC_PORT=${PORT}
ARNOS_DNS=${DNS}
ARNOS_STATE_FILE=${STATE_DIR}/state.json
ARNOS_ACME_CACHE=${STATE_DIR}/acme
EOF
[ -n "$PSK" ] && echo "ARNOS_PSK=${PSK}" >> "$ENV_FILE"
chmod 600 "$ENV_FILE"

log "installing systemd service"
cat > "$UNIT" <<EOF
[Unit]
Description=ArnosVPN server
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
EnvironmentFile=${ENV_FILE}
ExecStart=${PREFIX}/arnosvpn-server
Restart=always
RestartSec=3
AmbientCapabilities=CAP_NET_ADMIN CAP_NET_BIND_SERVICE
LimitNOFILE=1048576

[Install]
WantedBy=multi-user.target
EOF

# Best-effort: open the firewall for the chosen port if one is active.
if command -v ufw >/dev/null 2>&1; then ufw allow "${PORT}/tcp" >/dev/null 2>&1 || true; fi
if command -v firewall-cmd >/dev/null 2>&1; then
  firewall-cmd --permanent --add-port="${PORT}/tcp" >/dev/null 2>&1 || true
  firewall-cmd --reload >/dev/null 2>&1 || true
fi

log "starting the service"
systemctl daemon-reload
systemctl enable --now arnosvpn.service

# --- show the connect profile -----------------------------------------------
sleep 2
set -a; . "$ENV_FILE"; set +a
echo
log "installed. connect profile:"
echo
if "$PREFIX/arnosvpnctl" qr 2>/dev/null; then
  echo
  echo "  URI: $("$PREFIX/arnosvpnctl" uri 2>/dev/null || echo '(run: arnosvpnctl uri)')"
else
  warn "server still starting — view the profile with:"
  echo "  set -a; . ${ENV_FILE}; set +a; arnosvpnctl qr"
fi
echo
log "manage it with: systemctl status|restart|stop arnosvpn   ·   journalctl -u arnosvpn -f"
log "first Let's Encrypt issuance can take ~30s; make sure ${DOMAIN} resolves here and port ${PORT} is open."
