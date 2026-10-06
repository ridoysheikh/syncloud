# Install

## Requirements

- **Controller host:** Linux (x86-64 or arm64) with systemd. At least 2 vCPU, 2 GB RAM and 20 GB of disk. A cluster of 50 nodes and 2,000 tasks fits on 4 vCPU and 8 GB.
- **Ports:** 80 and 443 must be open to the internet (Traefik). 7443/tcp must be reachable from nodes (agent gateway). 51820/udp must be reachable between all nodes (WireGuard).
- **Nodes:** Linux with systemd. The installer adds Docker if it is missing. WireGuard runs in the kernel when the module is available, otherwise in userspace.
- **Clock:** time must be in sync (NTP). Certificates and signed requests depend on it.

## Install the controller

```sh
curl -fsSL https://get.syncloud.dev/install.sh | sudo bash
```

The installer checks the host, installs Docker if needed, verifies the binaries against `SHA256SUMS`, and writes the systemd units `syncloud-controller` and `syncloud-agent`. It joins the local agent as node `ctl-0` and waits for the system tasks (Traefik, the registry, BuildKit, VictoriaMetrics and VictoriaLogs).

At the end it prints:

- the dashboard URL,
- a one-time **setup token**,
- the **recovery key** (`SYNRK-…`).

Store the recovery key somewhere safe and offline. Every backup is encrypted with the master key, and the master key is wrapped by the recovery key, so a backup cannot be restored without it.

Useful options:

| Option | Effect |
| --- | --- |
| `--base-domain example.com` | Use your own domain instead of `<ip>.sslip.io`. Point `*.example.com` at the controller. |
| `--public-ip IP` | Skip public IP detection. |
| `--acme-email you@example.com` | Contact address for Let's Encrypt. |
| `--version V` | Install a specific release. |
| `--from-dir DIR` | Install binaries you built yourself (`make release`). |

## First sign-in

Open the dashboard URL. The setup page asks for:

- the setup token,
- the last 6 characters of the recovery key,
- the root account's email and password.

The root account bypasses IAM checks. For daily work, create users with policies under **IAM**.

Then:

1. **Settings → Backups:** add an S3 destination. A warning stays in the header until backups run.
2. **Compute → Nodes:** create a join token and run the printed command on each worker.

## Join a node

```sh
curl -fsSL https://<dashboard>/join.sh | sudo bash -s -- --token SYN-JOIN-…
```

Or use `synctl nodes join-tokens create` to get the same command. See [Nodes and pools](nodes.md).

## synctl

Download synctl from **API & CLI** in the dashboard, or from `https://<dashboard>/downloads/synctl-linux-amd64`.

You can sign in two ways:

- **Device flow:** run `synctl login` and approve the code in the dashboard.
- **Access key:** create an access key under **IAM → My security** and run `synctl configure`.
