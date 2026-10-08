# Requirements

## Servers

| Role | Minimum | Recommended |
| --- | --- | --- |
| **Controller** (runs the dashboard, API and platform components; also a node) | 2 vCPU, 2 GB RAM, 10 GB disk | 4 GB RAM and 40 GB disk if it also builds images |
| **Worker node** | 1 vCPU, 1 GB RAM | as much as your apps need |

- **Operating system:** Linux with systemd, on x86-64 or arm64. Ubuntu 22.04, 24.04 and 26.04 and Debian 12 and 13 are tested.
- **Docker:** the installer adds it if it's missing.
- **WireGuard:** runs in the kernel when the module exists, otherwise in userspace.
- **Time:** the clock must be synchronized (NTP). Certificates and signed requests depend on it.

The platform itself uses about 650 MB on the controller and about 300 MB on a worker. A cluster of 50 nodes and 2,000 tasks fits on a 4 vCPU / 8 GB controller.

## Network

| Port | From | Why |
| --- | --- | --- |
| 80/tcp, 443/tcp | the internet | the dashboard and your apps (Traefik) |
| 7443/tcp | your nodes | nodes connect to the controller (mutual TLS) |
| 51820/udp | between all nodes | the private network (WireGuard) |
| 20000–20999 tcp/udp | the internet, optional | public TCP/UDP ports you open for services |
| 21000–21999 tcp | the internet, optional | public database endpoints you turn on |
| 5432, 6379 tcp | the internet, optional | the same, on shared ports (TLS clients that send SNI) |

Nodes always dial out to the controller, so workers behind NAT work as long as WireGuard traffic between them can pass.

SynCloud manages each node's firewall (nftables). It opens only what the cluster needs, plus your own [host firewall policies](../guides/network-security.md#host-firewall).

## A domain (optional)

Without a domain, everything works on `<your-ip>.sslip.io` names with real Let's Encrypt certificates. To use your own, point a wildcard record at the controller before installing:

```
*.cloud.example.com.   A   203.0.113.10
```

and install with `--base-domain cloud.example.com`. You can change it later under **Settings → Domains**. Apps can also have any number of [custom domains](../guides/domains-and-ports.md) of their own.

## S3 storage (recommended)

Controller backups and PostgreSQL backups go to an S3-compatible bucket: AWS S3, Cloudflare R2, Backblaze B2, Wasabi, or a self-hosted MinIO or Garage. You can add one after installing.

---

Next: [Install](install.md)
