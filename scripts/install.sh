#!/usr/bin/env bash
# SynCloud controller installer (§5.0).
#
#   curl -fsSL https://get.syncloud.dev/install.sh | sudo bash
#   sudo ./install.sh --from-dir ./bin            # use locally built binaries
#   sudo ./install.sh --uninstall [--purge]
#
# Options:
#   --version V         release to install (default: latest)
#   --from-dir DIR      take syncloud-controller and syncloud-agent from DIR
#   --base-domain D     use your own domain instead of <ip>.sslip.io
#   --public-ip IP      skip public IP detection
#   --acme-email E      contact address for Let's Encrypt
#   --skip-docker       do not install Docker if it is missing
#   --force             continue when preflight checks fail
set -euo pipefail

RELEASE_URL="${SYNCLOUD_RELEASE_URL:-https://get.syncloud.dev/releases}"
# Release signing key (minisign). Empty until release signing is set up; then
# downloads without a valid signature are refused.
MINISIGN_PUBKEY="${SYNCLOUD_MINISIGN_PUBKEY:-}"
BIN_DIR=/usr/local/bin
DATA_DIR=/var/lib/syncloud
AGENT_DIR=/var/lib/syncloud-agent
ENV_FILE=/etc/syncloud/controller.env
API=http://127.0.0.1:7070

VERSION=latest FROM_DIR="" BASE_DOMAIN="" PUBLIC_IP="" ACME_EMAIL="" SKIP_DOCKER=0 FORCE=0 UNINSTALL=0 PURGE=0
while [ $# -gt 0 ]; do
  case "$1" in
    --version) VERSION="$2"; shift 2 ;;
    --from-dir) FROM_DIR="$2"; shift 2 ;;
    --base-domain) BASE_DOMAIN="$2"; shift 2 ;;
    --public-ip) PUBLIC_IP="$2"; shift 2 ;;
    --acme-email) ACME_EMAIL="$2"; shift 2 ;;
    --skip-docker) SKIP_DOCKER=1; shift ;;
    --force) FORCE=1; shift ;;
    --uninstall) UNINSTALL=1; shift ;;
    --purge) PURGE=1; shift ;;
    -h|--help) sed -n '2,17p' "$0"; exit 0 ;;
    *) echo "unknown option: $1" >&2; exit 2 ;;
  esac
done

bold() { printf '\033[1m%s\033[0m\n' "$*"; }
ok()   { printf '  \033[32m✓\033[0m %s\n' "$*"; }
bad()  { printf '  \033[31m✗\033[0m %s\n' "$*"; PREFLIGHT_FAILED=1; }
warn() { printf '  \033[33m!\033[0m %s\n' "$*"; }
die()  { printf '\033[31merror:\033[0m %s\n' "$*" >&2; exit 1; }

[ "$(id -u)" -eq 0 ] || die "run as root (sudo)"
command -v systemctl >/dev/null || die "systemd is required"

uninstall() {
  bold "Uninstalling SynCloud"
  systemctl disable --now syncloud-agent syncloud-controller 2>/dev/null || true
  if command -v docker >/dev/null; then
    ids=$(docker ps -aq --filter label=syncloud.managed=true || true)
    [ -n "$ids" ] && docker rm -f $ids >/dev/null && ok "removed SynCloud containers"
    docker network rm syncloud-system >/dev/null 2>&1 || true
  fi
  rm -f /etc/systemd/system/syncloud-controller.service /etc/systemd/system/syncloud-agent.service
  systemctl daemon-reload
  rm -f "$BIN_DIR/syncloud-controller" "$BIN_DIR/syncloud-agent"
  if [ "$PURGE" -eq 1 ]; then
    rm -rf "$DATA_DIR" "$AGENT_DIR" /etc/syncloud
    docker volume rm syncloud-registry syncloud-victoriametrics syncloud-victorialogs >/dev/null 2>&1 || true
    ok "removed all data"
  else
    warn "kept $DATA_DIR and $AGENT_DIR (use --purge to delete them)"
  fi
  exit 0
}
[ "$UNINSTALL" -eq 1 ] && uninstall

# ── 0. Preflight ────────────────────────────────────────────────────────────
PREFLIGHT_FAILED=0
bold "Preflight checks"
. /etc/os-release 2>/dev/null || true
case "${ID:-}:${VERSION_ID:-}" in
  ubuntu:22.04|ubuntu:24.04|ubuntu:26.04|debian:12|debian:13) ok "OS: $PRETTY_NAME" ;;
  *) warn "OS ${PRETTY_NAME:-unknown} is not tested (Ubuntu 22.04/24.04, Debian 12)" ;;
esac
case "$(uname -m)" in
  x86_64) ARCH=amd64 ;; aarch64|arm64) ARCH=arm64 ;;
  *) die "unsupported CPU architecture $(uname -m)" ;;
esac
ok "architecture: $ARCH"

cpus=$(nproc); mem_mb=$(awk '/MemTotal/ {print int($2/1024)}' /proc/meminfo)
disk_gb=$(df -BG --output=avail /var/lib 2>/dev/null | tail -1 | tr -dc 0-9)
[ "$cpus" -ge 2 ] && ok "CPUs: $cpus" || bad "CPUs: $cpus (minimum 2)"
[ "$mem_mb" -ge 3500 ] && ok "memory: ${mem_mb} MB" || bad "memory: ${mem_mb} MB (minimum 4 GB)"
[ "${disk_gb:-0}" -ge 20 ] && ok "free disk in /var/lib: ${disk_gb} GB" || bad "free disk in /var/lib: ${disk_gb:-?} GB (minimum 20 GB, 40 GB recommended)"

for port in 80 443; do
  holder=$(ss -Hltnp "sport = :$port" 2>/dev/null | grep -o 'users:(("[^"]*' | cut -d'"' -f2 | head -1 || true)
  if [ -z "$holder" ]; then
    ok "port $port/tcp is free"
  elif [ "$holder" = traefik ] && docker ps --format '{{.Names}}' 2>/dev/null | grep -qx syncloud-traefik; then
    ok "port $port/tcp is used by SynCloud's Traefik (reinstall)"
  else
    bad "port $port/tcp is used by $holder (stop it: Traefik must own 80 and 443)"
  fi
done

for mod in wireguard ip_vs nf_tables; do
  if modprobe -n "$mod" 2>/dev/null || [ -d "/sys/module/$mod" ]; then ok "kernel module $mod"; else warn "kernel module $mod not found (needed for private networking, Phase 1)"; fi
done
if [ "$(timedatectl show -p NTPSynchronized --value 2>/dev/null || echo no)" = "yes" ]; then ok "clock synchronized"; else warn "clock is not NTP-synchronized (certificates and request signing need correct time)"; fi

if [ "$PREFLIGHT_FAILED" -eq 1 ] && [ "$FORCE" -eq 0 ]; then
  die "preflight checks failed; fix them or re-run with --force"
fi

# ── 1. Dependencies ─────────────────────────────────────────────────────────
bold "Dependencies"
if ! command -v docker >/dev/null; then
  [ "$SKIP_DOCKER" -eq 1 ] && die "Docker is not installed (--skip-docker given)"
  echo "  installing Docker Engine…"
  curl -fsSL https://get.docker.com | sh >/dev/null
fi
systemctl enable --now docker >/dev/null 2>&1 || true
ok "Docker $(docker version --format '{{.Server.Version}}' 2>/dev/null || echo '?')"
command -v curl >/dev/null || die "curl is required"
if ! command -v nft >/dev/null; then
  echo "  installing nftables…"
  (apt-get -qq update && apt-get -qq install -y nftables) >/dev/null 2>&1 || die "install the nftables package"
fi
ok "nftables $(nft --version 2>/dev/null | awk '{print $2}')"

# ── 2. Binaries ─────────────────────────────────────────────────────────────
bold "Binaries"
tmp=$(mktemp -d); trap 'rm -rf "$tmp"' EXIT
if [ -n "$FROM_DIR" ]; then
  for b in syncloud-controller syncloud-agent; do
    [ -x "$FROM_DIR/$b" ] || die "$FROM_DIR/$b not found"
    cp "$FROM_DIR/$b" "$tmp/$b"
  done
  warn "using local binaries from $FROM_DIR (not signature-checked)"
else
  base="$RELEASE_URL/$VERSION"
  for f in "syncloud-controller-linux-$ARCH" "syncloud-agent-linux-$ARCH" SHA256SUMS; do
    curl -fsSL -o "$tmp/$f" "$base/$f" || die "download $base/$f failed"
  done
  if [ -n "$MINISIGN_PUBKEY" ]; then
    command -v minisign >/dev/null || die "minisign is required to verify the release signature"
    curl -fsSL -o "$tmp/SHA256SUMS.minisig" "$base/SHA256SUMS.minisig" || die "signature download failed"
    minisign -Vm "$tmp/SHA256SUMS" -P "$MINISIGN_PUBKEY" >/dev/null || die "release signature is invalid"
    ok "release signature verified"
  else
    warn "release signing is not configured; checking SHA-256 checksums only"
  fi
  (cd "$tmp" && grep -E " (syncloud-controller|syncloud-agent)-linux-$ARCH\$" SHA256SUMS | sha256sum -c --quiet) || die "checksum mismatch"
  mv "$tmp/syncloud-controller-linux-$ARCH" "$tmp/syncloud-controller"
  mv "$tmp/syncloud-agent-linux-$ARCH" "$tmp/syncloud-agent"
  ok "checksums verified"
fi
install -m 0755 "$tmp/syncloud-controller" "$BIN_DIR/syncloud-controller"
install -m 0755 "$tmp/syncloud-agent" "$BIN_DIR/syncloud-agent"
ok "installed $("$BIN_DIR/syncloud-controller" version) to $BIN_DIR"

# ── 3. Configuration and services ───────────────────────────────────────────
bold "Services"
if [ -z "$PUBLIC_IP" ]; then
  PUBLIC_IP=$(curl -fsS --max-time 5 https://api.ipify.org || curl -fsS --max-time 5 https://ipv4.icanhazip.com || true)
  PUBLIC_IP=$(echo "$PUBLIC_IP" | tr -d '[:space:]')
fi
[ -n "$PUBLIC_IP" ] || die "could not detect the public IP; pass --public-ip"
mkdir -p /etc/syncloud "$DATA_DIR" && chmod 700 "$DATA_DIR"
if [ ! -f "$ENV_FILE" ]; then
  cat > "$ENV_FILE" <<CONF
# SynCloud controller settings (flags: syncloud-controller --help)
SYNCLOUD_DATA_DIR=$DATA_DIR
SYNCLOUD_LISTEN=127.0.0.1:7070
SYNCLOUD_AGENT_LISTEN=0.0.0.0:7443
SYNCLOUD_AGENT_ADVERTISE=$PUBLIC_IP:7443
SYNCLOUD_PUBLIC_IP=$PUBLIC_IP
SYNCLOUD_BASE_DOMAIN=$BASE_DOMAIN
SYNCLOUD_ACME_EMAIL=$ACME_EMAIL
CONF
  chmod 600 "$ENV_FILE"
  ok "wrote $ENV_FILE"
else
  ok "kept existing $ENV_FILE"
fi

cat > /etc/systemd/system/syncloud-controller.service <<'UNIT'
[Unit]
Description=SynCloud controller
Documentation=https://syncloud.dev
After=network-online.target docker.service
Wants=network-online.target

[Service]
EnvironmentFile=/etc/syncloud/controller.env
ExecStart=/usr/local/bin/syncloud-controller serve
Restart=always
RestartSec=3
LimitNOFILE=65536
# Hardening: the controller only needs its data directory.
ProtectSystem=strict
ReadWritePaths=/var/lib/syncloud
ProtectHome=true
PrivateTmp=true
NoNewPrivileges=true

[Install]
WantedBy=multi-user.target
UNIT

cat > /etc/systemd/system/syncloud-agent.service <<'UNIT'
[Unit]
Description=SynCloud agent
After=network-online.target docker.service
Wants=network-online.target
Requires=docker.service

[Service]
ExecStart=/usr/local/bin/syncloud-agent run --data-dir /var/lib/syncloud-agent
Restart=always
RestartSec=3
LimitNOFILE=65536

[Install]
WantedBy=multi-user.target
UNIT
systemctl daemon-reload
systemctl enable --now syncloud-controller >/dev/null 2>&1
systemctl restart syncloud-controller

for _ in $(seq 1 30); do curl -fsS "$API/api/v1/system/status" >/dev/null 2>&1 && break; sleep 1; done
curl -fsS "$API/api/v1/system/status" >/dev/null || die "controller did not start: journalctl -u syncloud-controller"
ok "controller running"

if [ ! -f "$AGENT_DIR/agent.json" ]; then
  [ -f "$DATA_DIR/local-join.token" ] || die "missing $DATA_DIR/local-join.token"
  "$BIN_DIR/syncloud-agent" join --controller "$API" --token-file "$DATA_DIR/local-join.token" --name ctl-0 --data-dir "$AGENT_DIR" >/dev/null
  ok "local agent joined as ctl-0"
fi
systemctl enable --now syncloud-agent >/dev/null 2>&1
systemctl restart syncloud-agent
ok "agent running; starting system tasks (Traefik, registry, metrics, logs)…"

for _ in $(seq 1 120); do
  [ "$(docker ps --filter label=syncloud.system=true --filter status=running -q | wc -l)" -ge 4 ] && break; sleep 2
done
running=$(docker ps --filter label=syncloud.system=true --filter status=running -q | wc -l)
[ "$running" -ge 4 ] && ok "system tasks running ($running)" || warn "only $running system tasks running yet; check: syncloud-controller doctor"

# ── 4. Output ───────────────────────────────────────────────────────────────
domain=$(curl -fsS "$API/api/v1/system/status" | sed -n 's/.*"baseDomain":"\([^"]*\)".*/\1/p')
echo
bold "SynCloud is installed"
echo "  Dashboard:     https://${domain:-$PUBLIC_IP}"
if [ -f "$DATA_DIR/setup-token" ]; then
  echo "  Setup token:   $(cat "$DATA_DIR/setup-token")   (valid 1 hour)"
fi
if [ -f "$DATA_DIR/recovery-key" ]; then
  echo "  Recovery key:  $(cat "$DATA_DIR/recovery-key")"
  echo
  echo "  Save the recovery key now. It is shown only once and is needed to"
  echo "  restore backups. Setup asks for its last 6 characters."
fi
echo
echo "  Certificates are being requested; the first visit may show a"
echo "  self-signed certificate for a minute. Check: syncloud-controller doctor"
