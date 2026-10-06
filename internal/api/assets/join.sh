#!/usr/bin/env bash
# SynCloud worker join (§6.1). Served by the controller at /join.sh:
#
#   curl -fsSL {{URL}}/join.sh | sudo bash -s -- --token SYN-JOIN-…
#
# Options: --token T (required), --name N (default: hostname),
#          --advertise-address IP (address other nodes use for WireGuard),
#          --pin sha256//… (the controller's key, when its certificate is
#          self-signed, e.g. on a private network), --skip-docker
set -euo pipefail
CONTROLLER="{{URL}}"
TOKEN="" NAME="" ADVERTISE="" PIN="" SKIP_DOCKER=0
while [ $# -gt 0 ]; do
  case "$1" in
    --token) TOKEN="$2"; shift 2 ;;
    --name) NAME="$2"; shift 2 ;;
    --advertise-address) ADVERTISE="$2"; shift 2 ;;
    --pin) PIN="$2"; shift 2 ;;
    --skip-docker) SKIP_DOCKER=1; shift ;;
    *) echo "unknown option: $1" >&2; exit 2 ;;
  esac
done
die() { printf '\033[31merror:\033[0m %s\n' "$*" >&2; exit 1; }
ok()  { printf '  \033[32m✓\033[0m %s\n' "$*"; }
[ -n "$TOKEN" ] || die "--token is required"
[ "$(id -u)" -eq 0 ] || die "run as root (sudo)"
command -v systemctl >/dev/null || die "systemd is required"
case "$(uname -m)" in x86_64) ARCH=amd64 ;; aarch64|arm64) ARCH=arm64 ;; *) die "unsupported architecture $(uname -m)" ;; esac

echo "Joining $CONTROLLER"
if ! command -v docker >/dev/null; then
  [ "$SKIP_DOCKER" -eq 1 ] && die "Docker is not installed"
  echo "  installing Docker Engine…"
  curl -fsSL https://get.docker.com | sh >/dev/null
fi
systemctl enable --now docker >/dev/null 2>&1 || true
ok "Docker $(docker version --format '{{.Server.Version}}' 2>/dev/null || echo '?')"
if ! command -v nft >/dev/null; then
  (apt-get -qq update && apt-get -qq install -y nftables) >/dev/null 2>&1 || die "install the nftables package"
fi
ok "nftables"
if ss -Hlun "sport = :51820" 2>/dev/null | grep -q .; then die "UDP port 51820 (WireGuard) is in use"; fi

# A self-signed controller certificate is accepted only with its pinned key.
CURL=(curl -fsSL)
[ -n "$PIN" ] && CURL+=(-k --pinnedpubkey "$PIN")
tmp=$(mktemp -d); trap 'rm -rf "$tmp"' EXIT
"${CURL[@]}" -o "$tmp/syncloud-agent-linux-$ARCH" "$CONTROLLER/downloads/syncloud-agent-linux-$ARCH" || die "agent download failed"
"${CURL[@]}" -o "$tmp/SHA256SUMS" "$CONTROLLER/downloads/SHA256SUMS" || die "checksum download failed"
(cd "$tmp" && grep " syncloud-agent-linux-$ARCH\$" SHA256SUMS | sha256sum -c --quiet) || die "checksum mismatch"
install -m 0755 "$tmp/syncloud-agent-linux-$ARCH" /usr/local/bin/syncloud-agent
ok "agent $(/usr/local/bin/syncloud-agent version)"

mkdir -p /etc/syncloud
echo "SYNCLOUD_ADVERTISE_ADDRESS=$ADVERTISE" > /etc/syncloud/agent.env
cat > /etc/systemd/system/syncloud-agent.service <<'UNIT'
[Unit]
Description=SynCloud agent
After=network-online.target docker.service
Wants=network-online.target
Requires=docker.service

[Service]
EnvironmentFile=-/etc/syncloud/agent.env
ExecStart=/usr/local/bin/syncloud-agent run --data-dir /var/lib/syncloud-agent
Restart=always
RestartSec=3
LimitNOFILE=65536

[Install]
WantedBy=multi-user.target
UNIT
systemctl daemon-reload

if [ ! -f /var/lib/syncloud-agent/agent.json ]; then
  args=(join --controller "$CONTROLLER" --token "$TOKEN" --data-dir /var/lib/syncloud-agent)
  [ -n "$NAME" ] && args+=(--name "$NAME")
  [ -n "$PIN" ] && args+=(--pin "$PIN")
  /usr/local/bin/syncloud-agent "${args[@]}"
else
  ok "already joined; restarting the agent"
fi
systemctl enable --now syncloud-agent >/dev/null 2>&1
systemctl restart syncloud-agent
ok "agent running. The node appears as Ready in the dashboard within seconds."
