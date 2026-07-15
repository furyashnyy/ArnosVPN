#!/usr/bin/env bash
#
# ArnosVPN one-shot server installer (bare metal, no Docker).
#
# Builds the server from this repo, installs it as a systemd service, and
# configures it to terminate TLS itself on :443 with a Let's Encrypt certificate
# for your domain. This is the default, "install and configure in one go" path.
#
# Usage:
#   sudo ./install.sh                       # interactive (asks for domain/email)
#   sudo ./install.sh vpn.example.com you@example.com
#   sudo ARNOS_DOMAIN=vpn.example.com ARNOS_ACME_EMAIL=you@example.com ./install.sh
#
# Requirements: a public host where DNS for your domain points here and port 443
# is free and reachable. Debian/Ubuntu, RHEL/Fedora, Alpine and Arch are
# supported for dependency installation.
set -euo pipefail

# --- resolve inputs ---------------------------------------------------------
REPO_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
DOMAIN="${1:-${ARNOS_DOMAIN:-}}"
EMAIL="${2:-${ARNOS_ACME_EMAIL:-}}"
GO_MIN="1.24"
PREFIX="/usr/local/bin"
ETC_DIR="/etc/arnosvpn"
STATE_DIR="/var/lib/arnosvpn"
ENV_FILE="${ETC_DIR}/arnosvpn.env"
UNIT="/etc/systemd/system/arnosvpn.service"

log()  { printf '\033[1;34m[arnosvpn]\033[0m %s\n' "$*"; }
warn() { printf '\033[1;33m[arnosvpn]\033[0m %s\n' "$*" >&2; }
die()  { printf '\033[1;31m[arnosvpn]\033[0m %s\n' "$*" >&2; exit 1; }

[ "$(id -u)" = "0" ] || die "run as root (sudo ./install.sh)"

if [ -z "$DOMAIN" ]; then
  read -rp "Public domain clients will connect to (e.g. vpn.example.com): " DOMAIN
fi
[ -n "$DOMAIN" ] || die "a domain is required"
if [ -z "$EMAIL" ]; then
  read -rp "Contact e-mail for Let's Encrypt (optional, press Enter to skip): " EMAIL || true
fi

# --- dependencies -----------------------------------------------------------
install_deps() {
  local pkgs_common="iptables iproute2 ca-certificates curl"
  if   command -v apt-get >/dev/null 2>&1; then
    log "installing dependencies with apt"
    DEBIAN_FRONTEND=noninteractive apt-get update -qq
    DEBIAN_FRONTEND=noninteractive apt-get install -y -qq $pkgs_common wget >/dev/null
  elif command -v dnf >/dev/null 2>&1; then
    log "installing dependencies with dnf"; dnf install -y -q $pkgs_common wget >/dev/null
  elif command -v yum >/dev/null 2>&1; then
    log "installing dependencies with yum"; yum install -y -q $pkgs_common wget >/dev/null
  elif command -v apk >/dev/null 2>&1; then
    log "installing dependencies with apk"; apk add --no-cache iptables ip6tables iproute2 ca-certificates curl wget >/dev/null
  elif command -v pacman >/dev/null 2>&1; then
    log "installing dependencies with pacman"; pacman -Sy --noconfirm --needed iptables iproute2 ca-certificates curl wget >/dev/null
  else
    warn "unknown package manager — ensure iptables, iproute2 and ca-certificates are installed"
  fi
}
install_deps

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
# ArnosVPN server configuration (bare-metal / self-TLS on :443).
ARNOS_DOMAIN=${DOMAIN}
ARNOS_TLS_MODE=self
CERT_PROVIDER=letsencrypt
ARNOS_ACME_EMAIL=${EMAIL}
ARNOS_LISTEN=:443
ARNOS_PUBLIC_PORT=443
ARNOS_STATE_FILE=${STATE_DIR}/state.json
ARNOS_ACME_CACHE=${STATE_DIR}/acme
EOF
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

# Best-effort: open the firewall for 443 if a known firewall is active.
if command -v ufw >/dev/null 2>&1; then ufw allow 443/tcp >/dev/null 2>&1 || true; fi
if command -v firewall-cmd >/dev/null 2>&1; then
  firewall-cmd --permanent --add-port=443/tcp >/dev/null 2>&1 || true
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
log "first Let's Encrypt issuance can take ~30s; make sure ${DOMAIN} resolves here and 443 is open."
