#!/usr/bin/env bash
# SynCloud worker join (§6.1). Served by the controller at /join.sh:
#
#   curl -fsSL {{URL}}/join.sh | sudo bash -s -- --token SYN-JOIN-…
#
# Options: --token T (required), --name N (default: hostname),
#          --advertise-address IP (address other nodes use for WireGuard),
#          --pin sha256//… (the controller's key, when its certificate is
#          self-signed, e.g. on a private network), --skip-docker,
#          --quiet (no command output, steps only)
set -euo pipefail
CONTROLLER="{{URL}}"
TOKEN="" NAME="" ADVERTISE="" PIN="" SKIP_DOCKER=0 QUIET=0
while [ $# -gt 0 ]; do
  case "$1" in
    --token) TOKEN="$2"; shift 2 ;;
    --name) NAME="$2"; shift 2 ;;
    --advertise-address) ADVERTISE="$2"; shift 2 ;;
    --pin) PIN="$2"; shift 2 ;;
    --skip-docker) SKIP_DOCKER=1; shift ;;
    --quiet) QUIET=1; shift ;;
    *) echo "unknown option: $1" >&2; exit 2 ;;
  esac
done

# ── output ──────────────────────────────────────────────────────────────────
if [ -t 1 ]; then B=$'\033[1m' D=$'\033[2m' G=$'\033[32m' R=$'\033[31m' Y=$'\033[33m' N=$'\033[0m'; else B="" D="" G="" R="" Y="" N=""; fi
STEPS=6 STEP=0 T0=$SECONDS TS=$SECONDS
step() { STEP=$((STEP + 1)); TS=$SECONDS; printf '\n%s[%d/%d]%s %s\n' "$B" "$STEP" "$STEPS" "$N" "$*"; }
ok()   { printf '      %s✓%s %s %s(%ss)%s\n' "$G" "$N" "$*" "$D" "$((SECONDS - TS))" "$N"; }
info() { printf '      %s%s%s\n' "$D" "$*" "$N"; }
warn() { printf '      %s!%s %s\n' "$Y" "$N" "$*"; }
die()  { printf '\n%serror:%s %s\n' "$R" "$N" "$*" >&2; exit 1; }
# Runs a command, showing its output indented and dimmed (or only on failure with --quiet).
run() {
  local log; log=$(mktemp)
  if [ "$QUIET" -eq 1 ]; then
    if ! "$@" >"$log" 2>&1; then sed "s/^/      ${D}│${N} /" "$log" | tail -n 30; rm -f "$log"; return 1; fi
  else
    if ! "$@" 2>&1 | tee "$log" | sed -u "s/^/      ${D}│ /;s/\$/${N}/"; then rm -f "$log"; return 1; fi
  fi
  rm -f "$log"
}

[ -n "$TOKEN" ] || die "--token is required"
[ "$(id -u)" -eq 0 ] || die "run as root (sudo)"
command -v systemctl >/dev/null || die "systemd is required"
case "$(uname -m)" in x86_64) ARCH=amd64 ;; aarch64|arm64) ARCH=arm64 ;; *) die "unsupported architecture $(uname -m)" ;; esac

# A self-signed controller certificate is accepted only with its pinned key.
CURL=(curl -fsSL --connect-timeout 10 --retry 3 --retry-delay 2)
[ -n "$PIN" ] && CURL+=(-k --pinnedpubkey "$PIN")

printf '%sJoining %s%s as %s%s%s\n' "$B" "$CONTROLLER" "$N" "$B" "${NAME:-$(hostname)}" "$N"

# ── 1. reach the controller ─────────────────────────────────────────────────
step "Checking the controller"
if ! status=$("${CURL[@]}" --max-time 15 "$CONTROLLER/api/v1/system/status" 2>&1); then
  die "can't reach $CONTROLLER from this server: $status
       Check that this server can open HTTPS to it (curl -v $CONTROLLER/api/v1/system/status),
       and for a self-signed certificate, that --pin is in the command."
fi
version=$(printf '%s' "$status" | sed -n 's/.*"version":"\([^"]*\)".*/\1/p')
ok "controller ${version:+$version }answers"

# ── 2. Docker ───────────────────────────────────────────────────────────────
step "Docker Engine"
if ! command -v docker >/dev/null; then
  [ "$SKIP_DOCKER" -eq 1 ] && die "Docker is not installed (--skip-docker was given)"
  info "not installed: installing it with get.docker.com (usually 1–3 minutes)"
  run sh -c "$(printf '%q ' "${CURL[@]}") https://get.docker.com | sh" || die "the Docker install failed (output above)"
fi
systemctl enable --now docker >/dev/null 2>&1 || true
docker info >/dev/null 2>&1 || die "Docker is installed but not running: systemctl status docker"
ok "Docker $(docker version --format '{{.Server.Version}}' 2>/dev/null || echo '?')"

# ── 3. host tools ───────────────────────────────────────────────────────────
step "Host firewall tools"
if ! command -v nft >/dev/null; then
  info "installing nftables"
  if command -v apt-get >/dev/null; then
    run sh -c 'DEBIAN_FRONTEND=noninteractive apt-get -q update && DEBIAN_FRONTEND=noninteractive apt-get -q install -y nftables' || die "installing nftables failed (output above)"
  elif command -v dnf >/dev/null; then
    run dnf -y -q install nftables || die "installing nftables failed (output above)"
  else
    die "install the nftables package, then run this again"
  fi
fi
if ss -Hlun "sport = :51820" 2>/dev/null | grep -q .; then die "UDP port 51820 (WireGuard) is already in use on this server"; fi
ok "nftables ready, UDP 51820 free"

# ── 4. agent ────────────────────────────────────────────────────────────────
step "Downloading the agent"
tmp=$(mktemp -d); trap 'rm -rf "$tmp"' EXIT
DL=("${CURL[@]}" --max-time 600)
[ -t 1 ] && [ "$QUIET" -eq 0 ] && DL=("${DL[@]/-fsSL/-fSL}" --progress-bar)
"${DL[@]}" -o "$tmp/syncloud-agent-linux-$ARCH" "$CONTROLLER/downloads/syncloud-agent-linux-$ARCH" || die "the agent download failed"
"${CURL[@]}" -o "$tmp/SHA256SUMS" "$CONTROLLER/downloads/SHA256SUMS" || die "the checksum download failed"
(cd "$tmp" && grep " syncloud-agent-linux-$ARCH\$" SHA256SUMS | sha256sum -c --quiet) || die "checksum mismatch: the download was corrupted, run this again"
install -m 0755 "$tmp/syncloud-agent-linux-$ARCH" /usr/local/bin/syncloud-agent
ok "agent $(/usr/local/bin/syncloud-agent version) ($(du -h /usr/local/bin/syncloud-agent | cut -f1), checksum verified)"

# ── 5. join ─────────────────────────────────────────────────────────────────
step "Joining the cluster"
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
  out=$(/usr/local/bin/syncloud-agent "${args[@]}" 2>&1) || die "the controller refused the join: $out
       A join token is single-use and expires after an hour: create a new one under Compute → Nodes → Add node."
  ok "${out%%. Start*}"
else
  ok "already joined; restarting the agent"
fi
# The agent dials the controller's gateway (mutual TLS): Traefik on 443
# first, the direct port last. Check which addresses answer.
GATEWAY=$(sed -n 's/.*"gateway": *"\([^"]*\)".*/\1/p' /var/lib/syncloud-agent/agent.json)
if checks=$(/usr/local/bin/syncloud-agent check-gateways 2>&1); then
  printf '%s\n' "$checks" | sed "s/^/      /"
else
  printf '%s\n' "$checks" | sed "s/^/      /"
  warn "no gateway address answers: the agent won't connect until one does (firewall or security group on the controller?)"
fi

# ── 6. start and wait for the connection ────────────────────────────────────
step "Starting the agent"
since=$(date '+%Y-%m-%d %H:%M:%S')
systemctl enable syncloud-agent >/dev/null 2>&1
systemctl restart syncloud-agent
info "waiting for it to connect (agent log below)"
shown=0 connected=0
for _ in $(seq 1 45); do
  sleep 2
  lines=$(journalctl -u syncloud-agent --since "$since" -o cat --no-pager 2>/dev/null || true)
  total=$(printf '%s\n' "$lines" | grep -c . || true)
  if [ "$total" -gt "$shown" ]; then
    printf '%s\n' "$lines" | tail -n +"$((shown + 1))" | fold -s -w 150 | sed "s/^/      ${D}│ /;s/\$/${N}/"
    shown=$total
  fi
  if printf '%s' "$lines" | grep -q "connected to controller"; then connected=1; break; fi
  systemctl is-active --quiet syncloud-agent || { sleep 1; systemctl is-active --quiet syncloud-agent || break; }
done
if [ "$connected" -ne 1 ]; then
  printf '\n%sThe agent did not connect within 90 seconds.%s\n' "$Y" "$N" >&2
  # Name the likely cause from the agent's own error.
  if printf '%s' "$lines" | grep -q "connection refused"; then
    printf '  • %s refused the connection: nothing listens there. On the controller check\n    ss -ltnp | grep 7443   (SYNCLOUD_AGENT_LISTEN in /etc/syncloud/controller.env must be 0.0.0.0:7443)\n' "$GATEWAY" >&2
  elif printf '%s' "$lines" | grep -qE "i/o timeout|deadline exceeded|no route to host|while dialing"; then
    printf '  • packets to %s are dropped: a firewall between this server and the controller blocks TCP %s.\n' "$GATEWAY" "${GATEWAY##*:}" >&2
    printf '    On the controller: ufw allow %s/tcp  (or firewall-cmd --add-port=%s/tcp --permanent; firewall-cmd --reload),\n' "${GATEWAY##*:}" "${GATEWAY##*:}" >&2
    printf '    and open it in your cloud provider'"'"'s firewall / security group if it has one.\n' >&2
  elif printf '%s' "$lines" | grep -qE "certificate|x509|handshake"; then
    printf '  • the TLS handshake failed: the controller'"'"'s address may have changed since this server joined.\n' >&2
  fi
  printf '  • follow it:   journalctl -u syncloud-agent -f\n' >&2
  printf '  • open TCP 443 (or %s) on the controller to this server\n' "${GATEWAY:-7443}" >&2
  printf '  • open UDP 51820 between nodes (WireGuard)\n' >&2
  printf '  • it keeps retrying; the node turns Ready once it connects\n' >&2
  exit 1
fi
ok "connected"

printf '\n%s✓ %s joined in %ss.%s It shows as Ready under Compute → Nodes once its private network is up.\n' \
  "$G" "${NAME:-$(hostname)}" "$((SECONDS - T0))" "$N"
