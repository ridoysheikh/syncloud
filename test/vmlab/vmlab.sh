#!/usr/bin/env bash
# A small real cluster on this machine: three Ubuntu 24.04 VMs under
# QEMU/KVM, rootless (no bridges, no sudo on the host).
#
#   ctl  10.77.0.11  2 vCPU, 2 GiB   controller + local agent (ctl-0)
#   w1   10.77.0.12  1 vCPU, 1 GiB   worker
#   w2   10.77.0.13  1 vCPU, 1 GiB   worker
#
# The VMs share a private L2 network (QEMU multicast socket) for the mesh,
# and reach the internet through QEMU user networking. The host reaches them
# over forwarded ports: SSH on 127.0.0.1:2221-2223, the controller's HTTP and
# HTTPS on 127.0.0.1:18080 and 18443.
#
#   test/vmlab/vmlab.sh up         # create (first run) and start the VMs
#   test/vmlab/vmlab.sh install    # build SynCloud, install it with install.sh,
#                                  # create the admin and join w1 and w2
#   test/vmlab/vmlab.sh deploy     # rebuild and replace the binaries in place
#   test/vmlab/vmlab.sh ssh ctl    # shell on a VM (or: ssh ctl -- CMD)
#   test/vmlab/vmlab.sh status
#   test/vmlab/vmlab.sh down       # stop the VMs (disks kept)
#   test/vmlab/vmlab.sh destroy    # stop and delete everything
set -euo pipefail
LAB=${SYNCLOUD_VMLAB:-$HOME/.cache/syncloud-vmlab}
IMG_URL=https://cloud-images.ubuntu.com/minimal/releases/noble/release/ubuntu-24.04-minimal-cloudimg-amd64.img
SEED_PORT=18099
QEMU_IMG=$(command -v qemu-img || true)
[ -n "$QEMU_IMG" ] || QEMU_IMG=$(ls "$HOME"/Android/Sdk/emulator/qemu-img 2>/dev/null || true)
[ -n "$QEMU_IMG" ] || { echo "qemu-img not found" >&2; exit 1; }

VMS=(ctl w1 w2)
declare -A IP=([ctl]=10.77.0.11 [w1]=10.77.0.12 [w2]=10.77.0.13)
declare -A MEM=([ctl]=2048 [w1]=1024 [w2]=1024)
declare -A CPUS=([ctl]=2 [w1]=1 [w2]=1)
declare -A SSHP=([ctl]=2221 [w1]=2222 [w2]=2223)
declare -A MAC=([ctl]=52:54:00:77:00:11 [w1]=52:54:00:77:00:12 [w2]=52:54:00:77:00:13)
KEY="$LAB/id_ed25519"

vm_ssh() {
  local vm=$1; shift
  ssh -q -i "$KEY" -p "${SSHP[$vm]}" -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null -o ConnectTimeout=5 -o LogLevel=ERROR root@127.0.0.1 "$@"
}

seed() {
  local vm=$1 d="$LAB/seed/$1"
  mkdir -p "$d"
  printf 'instance-id: syncloud-%s\nlocal-hostname: %s\n' "$vm" "$vm" > "$d/meta-data"
  cat > "$d/user-data" <<EOF
#cloud-config
hostname: $vm
disable_root: false
ssh_pwauth: false
users:
  - name: root
    ssh_authorized_keys: ["$(cat "$KEY.pub")"]
write_files:
  - path: /etc/netplan/60-lab.yaml
    permissions: "0600"
    content: |
      network:
        version: 2
        ethernets:
          lab:
            match: {macaddress: "${MAC[$vm]}"}
            set-name: lab0
            addresses: [${IP[$vm]}/24]
            mtu: 1500
runcmd:
  - netplan apply
  - sed -i 's/^#\?PermitRootLogin.*/PermitRootLogin prohibit-password/' /etc/ssh/sshd_config
  - systemctl restart ssh
EOF
  : > "$d/vendor-data"
}

up() {
  mkdir -p "$LAB"
  [ -f "$KEY" ] || ssh-keygen -q -t ed25519 -N "" -f "$KEY"
  if [ ! -f "$LAB/base.img" ]; then
    echo "== downloading Ubuntu 24.04 minimal cloud image"
    curl -fL --progress-bar -o "$LAB/base.img.part" "$IMG_URL" && mv "$LAB/base.img.part" "$LAB/base.img"
  fi
  # cloud-init fetches its seed over HTTP from the host (10.0.2.2 is the
  # host's loopback in QEMU user networking).
  if ! curl -fs "http://127.0.0.1:$SEED_PORT/" >/dev/null 2>&1; then
    (cd "$LAB/seed" 2>/dev/null || { mkdir -p "$LAB/seed"; cd "$LAB/seed"; }
     nohup python3 -m http.server "$SEED_PORT" --bind 127.0.0.1 > "$LAB/seed.log" 2>&1 & echo $! > "$LAB/seed.pid")
  fi
  for vm in "${VMS[@]}"; do
    seed "$vm"
    [ -f "$LAB/$vm.qcow2" ] || "$QEMU_IMG" create -q -f qcow2 -b "$LAB/base.img" -F qcow2 "$LAB/$vm.qcow2" 20G
    if [ -f "$LAB/$vm.pid" ] && kill -0 "$(cat "$LAB/$vm.pid")" 2>/dev/null; then
      echo "  $vm already running"; continue
    fi
    fwd="hostfwd=tcp:127.0.0.1:${SSHP[$vm]}-:22"
    [ "$vm" = ctl ] && fwd+=",hostfwd=tcp:127.0.0.1:18080-:80,hostfwd=tcp:127.0.0.1:18443-:443"
    qemu-system-x86_64 -name "syncloud-$vm" -enable-kvm -cpu host -smp "${CPUS[$vm]}" -m "${MEM[$vm]}" \
      -drive "file=$LAB/$vm.qcow2,if=virtio,cache=unsafe" \
      -netdev "user,id=n0,$fwd" -device virtio-net-pci,netdev=n0 \
      -netdev socket,id=n1,mcast=230.77.0.1:17700,localaddr=127.0.0.1 -device "virtio-net-pci,netdev=n1,mac=${MAC[$vm]}" \
      -smbios "type=1,serial=ds=nocloud;s=http://10.0.2.2:$SEED_PORT/$vm/" \
      -display none -serial "file:$LAB/$vm.console.log" -daemonize -pidfile "$LAB/$vm.pid"
    echo "  started $vm (${IP[$vm]}, ssh 127.0.0.1:${SSHP[$vm]})"
  done
  echo "== waiting for SSH and the private network"
  for vm in "${VMS[@]}"; do
    for _ in $(seq 1 120); do vm_ssh "$vm" true 2>/dev/null && break; sleep 2; done
    vm_ssh "$vm" "cloud-init status --wait >/dev/null 2>&1; ip -4 -br addr show lab0" || { echo "$vm did not come up: $LAB/$vm.console.log" >&2; exit 1; }
  done
  vm_ssh w1 "timeout 3 bash -c '</dev/tcp/${IP[ctl]}/22' && timeout 3 bash -c '</dev/tcp/${IP[w2]}/22'" && echo "  private network ok"
}

REPO=$(cd "$(dirname "$0")/../.." && pwd)
ADMIN_EMAIL=admin@lab.test
ADMIN_PASS=lab-password-123

build() {
  echo "== building SynCloud (version 0.0.0-lab)"
  mkdir -p "$LAB/bin"
  (cd "$REPO" && make web >/dev/null)
  for c in controller agent synctl; do
    n=syncloud-$c; [ $c = synctl ] && n=synctl
    (cd "$REPO" && CGO_ENABLED=0 GOOS=linux go build -ldflags "-X syncloud/internal/version.Version=0.0.0-lab" -o "$LAB/bin/$n" ./cmd/$c)
  done
  cp "$REPO/scripts/install.sh" "$LAB/bin/"
}

to_ctl() { scp -q -i "$KEY" -P "${SSHP[ctl]}" -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null "$@" root@127.0.0.1:/root/sc/; }

# lab_api PATH [curl args]: the API on the controller VM as the lab admin.
lab_api() {
  local p=$1; shift
  vm_ssh ctl curl -sS -b /root/jar -c /root/jar -H "'content-type: application/json'" -H "'Origin: http://127.0.0.1:7070'" "$@" "http://127.0.0.1:7070/api/v1$p"
}

install_lab() {
  build
  vm_ssh ctl mkdir -p /root/sc && to_ctl "$LAB"/bin/*
  echo "== install.sh on ctl"
  out=$(vm_ssh ctl "cd /root/sc && bash install.sh --from-dir /root/sc --public-ip ${IP[ctl]} --force 2>&1") || { echo "$out"; exit 1; }
  echo "$out" | grep -E '✓|✗|!' | sed 's/\x1b\[[0-9;]*m//g'
  token=$(echo "$out" | sed 's/\x1b\[[0-9;]*m//g' | sed -n 's/.*Setup token: *\([^ ]*\).*/\1/p')
  rk=$(echo "$out" | sed 's/\x1b\[[0-9;]*m//g' | sed -n 's/.*Recovery key: *\([^ ]*\).*/\1/p')
  suffix=$(printf '%s' "$rk" | tr -d -- '-\n' | tail -c 6)
  if [ -n "$token" ]; then
    lab_api /setup -X POST -d "'{\"setupToken\":\"$token\",\"email\":\"$ADMIN_EMAIL\",\"name\":\"Lab Admin\",\"password\":\"$ADMIN_PASS\",\"recoveryKeySuffix\":\"$suffix\"}'" >/dev/null
    echo "$rk" > "$LAB/recovery-key"
  fi
  lab_api /auth/login -X POST -d "'{\"email\":\"$ADMIN_EMAIL\",\"password\":\"$ADMIN_PASS\"}'" >/dev/null
  cmd=$(lab_api /nodes/join-tokens -X POST -d "'{\"description\":\"lab workers\",\"singleUse\":false,\"ttlMinutes\":60}'" | python3 -c 'import sys,json;print(json.load(sys.stdin)["command"])')
  echo "== joining workers: $cmd"
  for vm in w1 w2; do vm_ssh "$vm" "$cmd 2>&1 | grep -E 'Joined|already|error' " & done; wait
  echo
  echo "Dashboard: https://${IP[ctl]//./-}.sslip.io  (from this machine: add"
  echo "  --host-resolver-rules=\"MAP *.${IP[ctl]//./-}.sslip.io 127.0.0.1:18443, MAP ${IP[ctl]//./-}.sslip.io 127.0.0.1:18443\""
  echo "  to Chromium, or use: ssh -i $KEY -p ${SSHP[ctl]} -L 7070:127.0.0.1:7070 root@127.0.0.1  → http://127.0.0.1:7070)"
  echo "Sign in: $ADMIN_EMAIL / $ADMIN_PASS"
}

deploy() {
  build
  to_ctl "$LAB/bin/syncloud-controller" "$LAB/bin/syncloud-agent" "$LAB/bin/synctl"
  vm_ssh ctl 'set -e
    install -m755 /root/sc/syncloud-controller /root/sc/syncloud-agent /root/sc/synctl /usr/local/bin/
    D=/usr/local/lib/syncloud/downloads
    install -m644 /root/sc/syncloud-agent $D/syncloud-agent-linux-amd64; install -m644 /root/sc/synctl $D/synctl-linux-amd64
    (cd $D && sha256sum syncloud-agent-linux-* synctl-linux-* > SHA256SUMS)
    systemctl restart syncloud-controller syncloud-agent'
  echo "  controller restarted; upgrade the workers' agents from Settings → Updates (or POST /nodes/agent-upgrade with force)"
}

down() {
  for vm in "${VMS[@]}"; do
    if [ -f "$LAB/$vm.pid" ]; then
      vm_ssh "$vm" poweroff 2>/dev/null || true
      for _ in $(seq 1 30); do kill -0 "$(cat "$LAB/$vm.pid")" 2>/dev/null || break; sleep 1; done
      kill "$(cat "$LAB/$vm.pid")" 2>/dev/null || true
      rm -f "$LAB/$vm.pid"
      echo "  stopped $vm"
    fi
  done
  [ -f "$LAB/seed.pid" ] && { kill "$(cat "$LAB/seed.pid")" 2>/dev/null || true; rm -f "$LAB/seed.pid"; }
}

case "${1:-}" in
  up) up ;;
  install) install_lab ;;
  deploy) deploy ;;
  down) down ;;
  destroy) down; rm -rf "$LAB/"*.qcow2 "$LAB/seed" "$LAB/"*.log; echo "  disks deleted (the base image is kept in $LAB)" ;;
  ssh) vm=${2:?vm}; shift 2; [ "${1:-}" = "--" ] && shift; vm_ssh "$vm" "$@" ;;
  scp) shift; scp -q -i "$KEY" -P "${SSHP[$1]}" -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null "${@:2}" root@127.0.0.1:/root/ ;;
  status) for vm in "${VMS[@]}"; do
            s=stopped; [ -f "$LAB/$vm.pid" ] && kill -0 "$(cat "$LAB/$vm.pid")" 2>/dev/null && s=running
            echo "$vm ${IP[$vm]} $s"; done ;;
  *) sed -n '2,22p' "$0"; exit 1 ;;
esac
