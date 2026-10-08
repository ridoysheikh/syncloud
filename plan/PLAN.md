# SynCloud: Master Plan

> A web-UI-first, Docker-only control plane for a fleet of VPS servers.
> It aims for ECS-style scheduling and autoscaling, Coolify/Dokploy-style app deployment,
> and kubectl-style service management, while staying simpler to run than Kubernetes.

Status: **Draft v0.12** (2026-10-05)

### Decisions log
| # | Decision | Date |
|---|---|---|
| D1 | The backend (controller, agent, CLI) is written in **Go**. The UI is **React + Vite** (TypeScript). | 2026-10-05 |
| D2 | **No shared or replicated volumes.** Persistent object storage is **S3** (any external S3-compatible provider). Self-hosted S3 such as MinIO or Garage is deployed as an ordinary service, like any other app. Volumes are node-local only. | 2026-10-05 |
| D3 | The controller can **optionally run apps**. To do so it runs the same agent as a worker, so it is also a normal (schedulable) worker node. This is off by default and can be toggled. | 2026-10-05 |
| D4 | **Managed PostgreSQL is deferred** (§17). **Managed Redis started 2026-10-07 as managed Valkey** (Phase 12): Valkey 8 (BSD, Redis-compatible), primary + replicas with Sentinel failover, memory and read-replica autoscaling. | 2026-10-05, updated 2026-10-07 |
| D5 | **SynCloud's own state is stored in SQLite 3**, not PostgreSQL. The platform's data is small (config, IAM, desired and observed state, audit), so an embedded database inside the controller binary is enough. There is no separate DB server. Metrics stay in VictoriaMetrics. | 2026-10-05 |
| D6 | **The controller owns service health and continuous app monitoring.** Agents run fast local checks, but the controller is the single authority for health state, uptime history, incidents and alerts (§5.6). | 2026-10-05 |
| D7 | **The platform is ready on first start.** Installing or starting the controller brings up the self-hosted Docker registry, the Git connector and polling engine, and Traefik with its dynamic dashboard, all pre-configured. A fresh install can deploy from Git immediately (§5.0). | 2026-10-05 |
| D8 | **The registry gets its own ECR-style UI and dashboard**: repositories, images, push commands, lifecycle policies, permissions, scanning and usage, kept as simple as AWS ECR (§5.10). | 2026-10-05 |
| D9 | **One single dashboard.** Every UI (cluster, nodes, services, Traefik/traffic, registry, Git/builds, health, monitoring, storage, IAM, settings) is a page in **one** React app at `https://<base-domain>`. There are no separate UIs to open or log in to: no stock Traefik dashboard, no registry UI, no Grafana (§10). | 2026-10-05 |
| D10 | **Everything is controllable through the API and a shell, using IAM access keys and tokens.** The dashboard is just one client of the public API. Anything doable in the UI is doable via the REST API and `synctl` (local CLI or the in-dashboard Cloud Shell), authorized by the same IAM policies (§7.1). | 2026-10-05 |
| D11 | **Dashboard look and feel**: a modern dashboard with a **modular side nav and a header**, compact spacing (`p-1.5 sm:p-2 md:p-3 lg:p-4`), small border radius, **Tailwind CSS**, **Apache ECharts** with minimal styling, and **dark theme only** (no theme switcher) (§10.1). | 2026-10-05 |
| D12 | **Centralized logging is in v1.** Every container's logs are shipped by the agents to **VictoriaLogs** on the controller, so each service has one merged, searchable log view across all nodes (§9.2). | 2026-10-05 |
| D13 | **The controller manages the firewall for the whole cluster**: host firewalls on every node, AWS-style **security groups** between services, and egress rules. All of it is defined in the dashboard and API and enforced by the agents with nftables (§8.3). | 2026-10-05 |
| D14 | **Complete network visibility**: topology, mesh links, IPAM, internal DNS, per-node and per-container traffic, firewall hits and drops are all shown in the dashboard (§8.4). | 2026-10-05 |
| D15 | **Full infrastructure scaling**: besides service autoscaling (§5.5), **node pools** can add and remove servers automatically through cloud-provider APIs (cluster autoscaling, §6.5). | 2026-10-05 |
| D16 | **Jobs and quotas**: one-off tasks, scheduled (cron) jobs and pre/post-deploy hooks (§5.11), plus per-project **resource quotas and usage metering** (§7.2). | 2026-10-05 |
| D17 | **The design is edge-ready from day one.** TLS certificates are issued by the controller (not Traefik's own ACME), so any number of **edge nodes** can run Traefik replicas and keep public traffic flowing even if the controller is down (§8.5). | 2026-10-05 |
| D18 | **Default addresses use sslip.io.** The base domain defaults to the controller's public IP as an sslip.io name (e.g. `203-0-113-10.sslip.io`). The dashboard and API use that main name, the registry uses `registry.<base-domain>`, and every service gets its own subdomain. Your own domain can replace it later (§5.0.2). | 2026-10-05 |
| D19 | **The registry is private only.** There is no pull-through cache or mirror. Public images (Docker Hub, GHCR, …) are pulled directly by each node from their upstream registries (§5.9). | 2026-10-05 |
| D20 | **The controller runs directly on the host as a systemd service, not as a container.** Traefik, the registry, BuildKit, VictoriaMetrics and VictoriaLogs run as **system tasks** managed by the controller through its own local agent. There is no docker-compose and no second orchestrator (§5.0). | 2026-10-05 |
| D21 | **Service-to-service load balancing uses a stable virtual IP (VIP) per service**, load-balanced in the kernel on every node (IPVS), with no central hop. DNS returns the VIP. Public traffic keeps using Traefik, with retry on by default (§8.6). | 2026-10-06 |

---

## 1. Goals and Non-Goals

### Goals
1. **One controller, many workers.** A single controller node holds all state, the UI, the routing and the monitoring. Workers are thin: they only run containers.
2. **Joining a worker takes one command.** One line on a fresh VPS turns it into a worker that is connected, secured and monitored.
3. **Docker only.** Everything that runs is a Docker image. There is no Pods/CRD/Helm layer.
4. **Private networking by default.** All traffic between the controller and workers, and between containers on different workers, goes over an encrypted mesh. Nothing is ever pointed at a worker's public IP.
5. **Built-in Traefik, fully controlled from the UI.** Domains, TLS, middlewares and routing can all be managed from the UI, with live load metrics per service and in total.
6. **ECS-style services.** Services have a desired count, rolling deploys, health checks, circuit-breaker rollback, autoscaling and placement strategies.
7. **Git to running app.** Connect GitHub, GitLab or Gitea, or use a generic Git URL. Updates arrive by webhook or by polling. Images are built and pushed to a self-hosted registry and then deployed.
8. **AWS-like IAM.** Users, groups, roles, JSON policies, API tokens, MFA and an audit log, all scoped by project.
9. **Simple monitoring.** Charts for nodes, services and containers with sensible defaults, all inside the single dashboard, with no Grafana to wire up.
10. **One dashboard, full API and shell.** A single web dashboard for everything (D9), and full control through the API and shell using IAM access keys and tokens (D10).
11. **Run a full cloud infrastructure.** Centralized logs, a cluster-wide firewall, complete network visibility, node autoscaling, jobs and quotas: everything needed to run production workloads, not only to deploy apps (D12–D17).

### Non-Goals (v1)
- Multiple controllers or a replicated control plane. Instead: backups and a documented restore path (see §13).
- Non-Docker runtimes (VMs, Firecracker, raw processes).
- Multi-region federation.
- Full Kubernetes API compatibility.
- **Managed databases (PostgreSQL, Redis)**: deferred to a future track (D4, §17). Until then, users can still run any database image as an ordinary service with a node-local volume.
- **Shared, distributed or replicated volumes** (no NFS, Longhorn, Ceph and similar). Apps that need shared persistent data use **S3**. Data that must stay on one node uses a node-local volume, with the task pinned to that node.

---

## 2. High-Level Architecture

```
                         Internet
                            │
                ┌───────────▼────────────────────────────────────┐
                │                CONTROLLER NODE                  │
                │                                                 │
                │  ┌──────────┐   ┌────────────────────────────┐  │
                │  │ Traefik  │◄──┤ syncloud-controller (Go)   │  │
                │  │ (edge)   │   │  • REST/WS API + Web UI    │  │
                │  └────┬─────┘   │  • Scheduler / Reconciler  │  │
                │       │         │  • Autoscaler              │  │
                │       │         │  • IAM / Audit             │  │
                │       │         │  • Git poller + Builder    │  │
                │       │         │  • Agent gateway (gRPC)    │  │
                │       │         │  • Traefik config provider │  │
                │       │         └──────┬─────────────────────┘  │
                │       │                │                        │
                │  ┌────▼─────┐  ┌───────▼──────┐  ┌───────────┐  │
                │  │ Registry │  │ SQLite (WAL) │  │ Victoria- │  │
                │  │ (distrib)│  │ (embedded)   │  │ Metrics   │  │
                │  └──────────┘  └──────────────┘  │ + Logs    │  │
                │                                  └───────────┘  │
                │        wg0: 10.90.0.1  (WireGuard hub)          │
                └───────────────┬─────────────────────────────────┘
                                │  encrypted mesh (WireGuard)
          ┌─────────────────────┼─────────────────────┐
          │                     │                     │
  ┌───────▼───────┐     ┌───────▼───────┐     ┌───────▼───────┐
  │ WORKER  w-01  │     │ WORKER  w-02  │     │ WORKER  w-03  │
  │ syncloud-agent│     │ syncloud-agent│     │ syncloud-agent│
  │ Docker Engine │     │ Docker Engine │     │ Docker Engine │
  │ wg0 10.90.0.11│     │ wg0 10.90.0.12│     │ wg0 10.90.0.13│
  │ ctr 10.91.11/24│    │ ctr 10.91.12/24│    │ ctr 10.91.13/24│
  └───────────────┘     └───────────────┘     └───────────────┘
```
Logs are stored in VictoriaLogs (§9.2). Optional **edge nodes** run extra Traefik replicas for public traffic (§8.5).

### Components

| Component | Runs on | Purpose |
|---|---|---|
| `syncloud-controller` | Controller (host, systemd, D20) | A single Go binary: API, UI (embedded), scheduler, reconciler, autoscaler, IAM, builder orchestration, agent gateway and Traefik dynamic-config provider |
| Traefik v3 | Controller, plus optional edge nodes (§8.5) | Edge proxy. Gets routing **and TLS certificates** from the controller through the **HTTP provider** (certificates are issued by the controller, D17) |
| SQLite 3 (embedded) | Controller (inside the controller process) | Source of truth for all desired and observed state, IAM and audit. Single file at `/var/lib/syncloud/syncloud.db` |
| VictoriaMetrics (single-node) | Controller | Time-series store. Prometheus-compatible, low RAM, long retention |
| VictoriaLogs (single-node) | Controller | Central log store for every container, build, Traefik access log and system component (§9.2) |
| Docker Registry (`distribution`) | Controller (system task) | Private image registry at `registry.<base-domain>`, with token auth issued by the controller (D19) |
| BuildKit | Controller, or a dedicated "builder" worker | Builds images from Git |
| `syncloud-agent` | Every worker, and **always on the controller** (self-monitoring, plus running apps when the controller is schedulable, see §6.4) | A single Go binary: Docker executor, metrics collector, log shipper, health prober, WireGuard peer manager |

---

## 3. Tech Stack (decided, D1)

| Layer | Choice | Why |
|---|---|---|
| Controller and agent | **Go** | Single static binaries, first-class Docker SDK, Traefik and WireGuard libraries (`wgctrl`) are native Go, low memory |
| Agent ↔ controller | **gRPC bidirectional stream over mTLS** (inside WireGuard) | One long-lived outbound connection from the agent; commands and telemetry share the same stream |
| Frontend | **React + TypeScript + Vite**, **Tailwind CSS**, TanStack Query/Router, headless primitives (Radix) styled with our own compact Tailwind components, **Apache ECharts** (minimal theme) for charts | Rich, dense dashboards; ECharts handles real-time streaming charts well. Dark theme only (D11) |
| Realtime UI | WebSocket (or SSE) from the controller | Live metrics, logs, events and deploy progress |
| State DB | **SQLite 3** in WAL mode, using the pure-Go driver `modernc.org/sqlite` (no CGO, so the binary stays static). Migrations use `goose` and queries use `sqlc` | Small dataset and a single controller, so there is no DB server to run or back up separately. Internal events go through an **in-process event bus** (Go channels) instead of `LISTEN/NOTIFY` |
| Metrics | VictoriaMetrics | PromQL-compatible; Traefik and cAdvisor-style metrics fit directly |
| Logs | **VictoriaLogs** (single-node, on the controller), with LogsQL queries proxied through the controller API | Low resource use, fast full-text search, label-based streams that map directly to project/service/task (D12) |
| Firewall | **nftables**, managed by the agent in its own table | Atomic rule updates, sets for fast IP membership, per-rule counters (D13) |
| TLS | **golang.org/x/crypto/acme** inside the controller (no lego: small dependency tree) | Certificates are issued centrally and work across multiple Traefik replicas (D17) |
| Networking | WireGuard (kernel module) | Fast, simple, built into modern kernels |
| Builds | BuildKit + Dockerfile, with **Nixpacks** as a fallback (as in Coolify and Dokploy) | Proven approach |
| CLI / shell | `synctl` (Go, shares the generated API client), plus the in-dashboard Cloud Shell | kubectl-like UX, 100% API coverage, IAM access keys and tokens (§7.1) |

---

### SQLite usage rules
- There is **one writer connection** (writes are serialized through it), plus a pool of read connections. Settings: `journal_mode=WAL`, `busy_timeout=5000`, `foreign_keys=ON`, `synchronous=NORMAL`.
- **High-frequency data stays out of SQLite.** Metrics go to VictoriaMetrics, logs go to VictoriaLogs, and heartbeats live **in memory**. Only state transitions (for example a node going `Ready` → `NotReady`, or a task changing state) are written to SQLite.
- Audit events are stored in SQLite with a retention or pruning job, plus optional export to S3.
- The store layer sits behind Go interfaces, so moving to Postgres later is possible but not planned.

---

## 4. Core Resource Model

The model borrows from AWS ECS and adds projects.

```
Organization
 └── Project              (e.g. "shop")            ← the IAM boundary
      ├── Quota           (project limits; can be overridden per environment, §7.2)
      └── Environment     (production / staging / preview-*)
           ├── Service    (desired state: image, count, scaling, routing)
           │    ├── TaskDefinition (immutable revisions: image, env, resources, health, volumes)
           │    ├── Deployment     (a rollout from rev N → rev M)
           │    └── Task           (one running container instance on a node)
           ├── Secret / ConfigVar
           ├── Volume              (node-local only, D2)
           ├── S3Binding           (credentials/bucket for an external S3 or a self-hosted S3 service)
           ├── Route               (domain → service:port, middlewares)
           ├── Job                 (one-off, scheduled/cron, or deploy hook; runs a TaskDefinition, §5.11)
           │    └── JobRun         (one execution: status, exit code, duration, logs)
           └── SecurityGroup       (east-west firewall rules attached to services, §8.3)
Cluster-wide:
 ├── Node (worker)    labels, capacity, status, WireGuard IP, container subnet
 ├── NodePool         a group of nodes by label (e.g. "gpu", "eu-fra"); manual or provider-backed with autoscaling (§6.5)
 ├── CloudProvider    credentials and settings for Hetzner / DigitalOcean / Vultr / AWS EC2 … (§6.5)
 ├── HostFirewallPolicy  public-interface rules applied to node pools or single nodes (§8.3)
 ├── Certificate      issued and renewed by the controller (ACME), served to all Traefik replicas (§8.5)
 ├── GitSource        (GitHub App / GitLab / Gitea / generic + deploy key)
 ├── Registry         (built-in plus external credentials)
 │    ├── Repository   (project-scoped; immutability, scan-on-push, lifecycle and permission policies)
 │    └── Image        (digest, tags, size, push/pull history, scan results, in-use-by)
 ├── S3Endpoint       (external S3 providers: AWS, R2, Wasabi, B2, … used for backups and app buckets)
 ├── AlertRule / NotificationChannel
 └── IAM: User, Group, Role, ServiceAccount, Policy, AccessKey, ApiToken, AuditEvent
```

### Key entities (summary)

**TaskDefinition** (immutable and versioned)
```yaml
family: shop-api
revision: 14
containers:
  - name: api
    image: "@registry/shop/api:sha-3f2a1c"   # alias for the built-in registry, resolved at deploy time (§5.0.2)
    command: []
    ports: [{ container: 8080, name: http }]
    env: { NODE_ENV: production }
    secrets: [{ name: DB_URL, from: secret:shop/prod/db-url }]
    resources: { cpu: 0.5, memory: 512Mi, memoryLimit: 768Mi }
    healthCheck:
      type: http          # http | tcp | cmd
      path: /healthz
      interval: 10s
      timeout: 3s
      retries: 3
      startPeriod: 20s
    volumes: [{ name: uploads, mountPath: /data }]
    logging: { maxSize: 10m }
```

**Service** (mutable desired state)
```yaml
name: api
taskDefinition: shop-api:14
desiredCount: 3
deployment:
  strategy: rolling              # rolling | blue-green (v2)
  minimumHealthyPercent: 100
  maximumPercent: 200
  circuitBreaker: { enabled: true, rollback: true }
placement:
  strategy: spread(node)         # spread(node|label:zone) | binpack(memory|cpu) | random
  constraints: ["node.labels.tier == app"]
autoscaling:
  min: 2
  max: 10
  policies:
    - type: targetTracking
      metric: cpu                # cpu | memory | rps_per_task | p95_latency | custom PromQL
      target: 60
      scaleOutCooldown: 60s
      scaleInCooldown: 300s
routing:
  - host: api.shop.com
    port: http
    pathPrefix: /
    middlewares: [rate-limit-100, gzip]
```

---

## 5. Controller Subsystems

### 5.0 Controller installation and bootstrap (D7, D20)

**How the controller runs (D20)**
- `syncloud-controller` is a **host binary run by systemd**, like the agent. It needs direct access to the host's WireGuard, nftables and Docker, which is simpler and safer on the host than from a container.
- Every other platform component runs as a **system task**: a normal SynCloud task, defined inside the controller binary, started through the controller's own local agent (`ctl-0`).
  - System tasks: Traefik, registry, BuildKit, VictoriaMetrics, VictoriaLogs.
  - They are pinned to `ctl-0`, labeled `syncloud.system=true`, and can never be evicted, scaled in or edited by users. They are visible in the dashboard (read-only) with their health, metrics and logs.
  - Their versions are pinned per SynCloud release (a **release manifest**), so upgrades are tested combinations.
- So there is **one orchestrator for everything**, with no docker-compose. The same health checks, logs, metrics and rolling upgrades apply to the platform's own components.

**Install command**
```bash
curl -fsSL https://get.syncloud.dev/install.sh | sudo bash
```

**Install steps**
0. **Preflight checks** (abort with a clear fix for each failure):
   - OS: Ubuntu 22.04/24.04 or Debian 12 (more later), amd64 or arm64, run as root.
   - Kernel WireGuard support, systemd, nftables, IPVS modules (D21).
   - Resources: minimum 2 vCPU / 4 GB RAM / 40 GB disk (recommended 4 vCPU / 8 GB).
   - Ports free: 80/tcp, 443/tcp, 51820/udp (e.g. an existing nginx is reported).
   - A public IPv4 address can be detected, and port 80 is reachable from the internet (checked via an external probe), because certificates need it.
   - Clock in sync (NTP).
1. **Dependencies**: install Docker Engine and WireGuard tools if missing.
2. **Binaries**: download `syncloud-controller` and `syncloud-agent`, and **verify checksum and signature** before installing them. Install systemd units.
3. **`syncloud-controller init`**:
   1. **Base**: create `/var/lib/syncloud`, the SQLite DB and migrations, the master key (wrapped by a recovery key, see below), the internal CA and the controller WireGuard key, and bring up `wg0` (`10.90.0.1`).
   2. **Base domain**: detect the public IP and set the base domain to `<ip-with-dashes>.sslip.io` (§5.0.2).
   3. **Local agent**: start the agent and join it as node `ctl-0` (non-schedulable by default, D3). Apply the default **host firewall** for the controller: 80/443, WireGuard, and SSH (open by default, see below) (§8.3).
   4. **System tasks**, in order, through the local agent:
      - **Traefik** on `:80` and `:443` (public and on `10.90.0.1`), with the HTTP provider pointed at the controller for routes and certificates (D17), and metrics and access logs on.
      - **Registry** (private, D19) with controller token auth, local disk storage (or the S3 driver if S3 is configured), routed at `registry.<base-domain>`.
      - **BuildKit** with a cache volume (on the controller by default; it can move to a "builder" node later).
      - **VictoriaMetrics** and **VictoriaLogs**, with scrape targets registered.
   5. **Certificates**: the controller requests Let's Encrypt certificates (HTTP-01) for `<base-domain>` and `registry.<base-domain>`. If that fails (port 80 blocked, rate limit), it falls back to a self-signed certificate and the dashboard shows a banner with the reason and a "Retry" button.
   6. **Engines**: start the Git webhook receiver (`https://<base-domain>/hooks/git/*`), the polling scheduler (§5.8) and the health monitor (§5.6). They stay idle until used.
4. **Output** (printed once in the terminal):
   - Dashboard URL: `https://203-0-113-10.sslip.io`
   - **One-time setup token** (valid 1 hour), needed to open the setup wizard. This stops anyone else from claiming the new install first.
   - **Recovery key**, shown only once. The user must store it safely. It is needed to restore from backups (see §13).

**First-run wizard** (in the dashboard)
1. Enter the setup token.
2. Create the admin (root) account with MFA.
3. Confirm the recovery key has been saved (type its last 6 characters).
4. Optionally add an S3 endpoint for backups. Without it, a permanent "Backups are off" warning stays in the header until S3 is configured.
5. Optionally restrict SSH to specific IPs (recommended; applied with commit-confirm, §8.3).
6. Optionally connect a Git provider.
7. Show the one-line worker join command.

**Idempotent and repairable**: each step can be re-run. `syncloud-controller init` repairs a broken component, and `syncloud-controller doctor` checks every component (system tasks, certificates, DNS resolution of the base domain, ports, WireGuard, disk) and prints fixes.

### 5.0.1 Upgrade, recovery and uninstall

**Upgrade (controller and platform)**
- Started from the dashboard (Settings → Updates) or with `syncloud-controller upgrade [--version X]`. Release channels are `stable` and `beta`.
- Steps:
  1. Preflight: disk space, health of all system tasks, connected agents.
  2. **Snapshot**: an SQLite snapshot (`VACUUM INTO`) plus a forced Litestream sync if S3 is configured.
  3. Download and verify the new binaries.
  4. Swap the controller binary and restart it. Database migrations run on start.
  5. Upgrade system tasks to the versions in the new release manifest, one at a time, with health checks (the normal deployment engine, §5.4).
  6. Roll out the new agent version node by node (§6.2).
- **Automatic rollback**: if the controller or a system task is unhealthy within a few minutes of the upgrade, the previous binary and the DB snapshot are restored.
- **Version skew**: a controller supports agents one minor version older, so workers can be upgraded gradually.

**Master key and recovery key**
- Secrets are encrypted with the **master key**. The master key itself is stored on disk **wrapped (encrypted) by the recovery key**, and unwrapped into memory when the controller starts (the unwrapped copy is protected by file permissions; a TPM or KMS option comes later).
- Backups contain only the **wrapped** master key, so a stolen backup is useless without the recovery key.
- Restoring on a new host needs the backup location plus the recovery key (§13).

**Uninstall**
- `syncloud-controller uninstall` stops all system tasks, removes the WireGuard interface, the nftables table and the systemd units, and keeps `/var/lib/syncloud` unless `--purge` is given. Agents have the same command.

### 5.0.2 Domains: sslip.io by default (D18)

[sslip.io](https://sslip.io) is a free public DNS service where any name containing an IP address resolves to that IP. For example, `203-0-113-10.sslip.io` and `anything.203-0-113-10.sslip.io` both resolve to `203.0.113.10`. This gives every install working HTTPS addresses with no DNS setup.

**Dev mode too (user request, 2026-10-08):** a development or test controller also starts with an sslip.io base domain. It is built from the address the machine is reached at, not the public IP, which in dev is usually behind NAT:
1. `--public-ip`, when given;
2. otherwise the `--agent-advertise` IP;
3. otherwise the host's address on its default route;
4. otherwise `127.0.0.1`.

sslip.io resolves private addresses too, so `192-168-1-20.sslip.io` works from the same network. ACME is off in dev, so these names get self-signed certificates. An sslip.io/nip.io base domain follows a changed address in dev as well.

**Default addresses** (controller public IP `203.0.113.10`, base domain `203-0-113-10.sslip.io`):
| What | Address |
|---|---|
| Dashboard and API | `https://203-0-113-10.sslip.io` |
| Registry | `https://registry.203-0-113-10.sslip.io` |
| Each service | `https://<service>-<env>-<project>.203-0-113-10.sslip.io`, e.g. `https://api-production-shop.203-0-113-10.sslip.io` |
| Git webhooks | `https://203-0-113-10.sslip.io/hooks/git/<provider>` |
| Worker join script, CLI download | `https://203-0-113-10.sslip.io/join.sh`, `/cli.sh` |

**Certificates**
- Wildcard certificates are not possible on sslip.io (they need DNS-01, and nobody can create DNS records there). So the controller requests **one certificate per hostname** with HTTP-01, when a route is first created.
- **Rate limits (verified 2026-10-06)**: sslip.io is **not** on the Public Suffix List. Instead, Let's Encrypt gives sslip.io/nip.io (run by the same operators) a raised **shared** limit of 250,000 certificates per week across all users, so rate limiting is possible at busy times. The operators recommend falling back to the other domain or to an IP-address certificate.
- **Certificate fallback** (revised 2026-10-06 while building; a per-hostname nip.io certificate does not help users who browse the sslip.io name):
  1. A **self-signed placeholder** is created at once, so HTTPS works from the first second.
  2. Let's Encrypt HTTP-01 for the name. Failures retry with backoff (1m, 5m, 15m, 1h, 3h, 6h, then every 12h); "Request now" skips the wait.
  3. If **rate-limited**, the dashboard offers a one-click switch of the whole base domain to **nip.io**, which has its own limit.
  4. Phase 9: a second ACME CA (ZeroSSL, with external account binding) and an IP-address certificate for the dashboard.
- Certificates are reused and renewed early, and issued only when a route is created, to keep requests low.

**Things to know (shown in the dashboard Settings → Domains)**
- **Third-party dependency**: if sslip.io is unavailable, new DNS lookups of these names fail.
- **IP change**: the base domain contains the controller IP. If the IP changes, the dashboard offers to switch the base domain, then regenerates all default routes and certificates.
- **Edge nodes**: an sslip.io name points at one IP only, so it cannot spread traffic over several edge nodes. High-availability ingress (§8.5) needs your own domain.

**Your own domain (optional, any time)**
- **Custom domains per service**: as before (§5.7). Add a domain, create the DNS record, and the controller issues the certificate.
- **Replace the base domain**: Settings → Domains → set e.g. `cloud.example.com` (with wildcard DNS `*.cloud.example.com` and, optionally, DNS-provider credentials for a wildcard certificate). The dashboard, registry and every default service address move to the new base domain. The old sslip.io addresses keep working (redirecting) for a transition period.

**Stable image references**
- Image references inside SynCloud use the alias `@registry/<project>/<repo>:<tag>`, which is resolved to `registry.<base-domain>` at deploy time. Changing the base domain never breaks existing TaskDefinitions.

### 5.1 API layer
- **API-first**: the REST (JSON) API under `/api/v1/...` plus WebSocket `/api/v1/stream` is the **only** way into the controller. The dashboard, `synctl`, the Cloud Shell and CI all use the same public API, with no private UI-only endpoints (D10).
- The API is defined by an OpenAPI spec. The TS client (dashboard) and Go client (`synctl`, SDK) are generated from it. A **CI parity check** fails the build if an API operation has no `synctl` command.
- Every request passes through **IAM authorization** (§7), and every mutation writes an **audit event**.

### 5.2 Reconciler (the core loop)
- Desired state (SQLite) is compared with observed state (agent reports).
- One goroutine per service, using a work queue as Kubernetes controllers do. It is triggered by events (spec change, task died, node lost, scaling decision) and also runs as a periodic resync every 30s.
- It produces **actions** (`StartTask`, `StopTask`, `PullImage`, `UpdateRoutes`) that are sent to agents over gRPC.
- It is idempotent: every action carries a desired-generation number, and agents ACK with that generation.

### 5.3 Scheduler (task placement)
1. **Filter** nodes that are `Ready`, match the constraints, have enough free CPU and memory (based on reservations, not usage), have the volume if it is node-local, and have the port free if a host port is requested.
2. **Score**:
   - `spread`: prefer nodes (or label values) with the fewest tasks of this service.
   - `binpack`: prefer nodes with the least remaining capacity.
   - Tie-breakers: image already cached on the node (fast start) and lower current load.
3. **Bind**: create a Task row (`PENDING`) and send `StartTask` to the chosen agent.

### 5.4 Deployment engine (ECS-like rolling update)
- Rollout from rev N to rev M, bounded by `minimumHealthyPercent` and `maximumPercent`:
  1. Start new tasks up to the max-percent budget.
  2. Wait for them to become **healthy**: the container health check passes **and** the Traefik target is reachable.
  3. Drain old tasks: remove them from Traefik, wait for `deregistrationDelay` (default 15s), then stop them.
  4. Repeat until every task runs rev M.
- **Circuit breaker**: if more than X tasks fail to become healthy (default `max(3, desired/2)`), mark the deployment `FAILED` and **automatically roll back** to the last stable revision.
- Every deployment step is streamed to the UI as an event timeline.

### 5.5 Autoscaler (ECS Application Auto Scaling equivalent)
- Evaluates every 15s against VictoriaMetrics.
- **Target tracking**: `desired = ceil(current * metric / target)`, clamped to min/max, respecting cooldowns. Scale-in is conservative and needs N consecutive evaluations.
- **Step scaling** (v1.1): threshold bands map to ±N tasks.
- **Scheduled scaling** (v1.1): cron expressions set min/max/desired.
- Metrics available out of the box: CPU %, memory %, RPS per task (from Traefik), p95 latency (from Traefik) and any custom PromQL.
- Every decision is logged with the reason, e.g. "cpu avg 82% > 60% target → 3→5 tasks", and shown in the UI.
- Scaling out is always capped by the project's **quota** (§7.2). If there is no room on any node, the new tasks stay `Pending (insufficient capacity)`, which is the signal the **cluster autoscaler** uses to add nodes (§6.5).

### 5.6 Service health and continuous app monitoring (controller-owned, D6)

The controller is the **single source of truth for health**. There are two layers of checks:

| Layer | Who | What | Why |
|---|---|---|---|
| **Local checks** | Agent, on the task's node | Docker health status, HTTP/TCP/cmd probes from the TaskDefinition, every 5–10s | Fast reaction: an unhealthy container is reported within seconds |
| **Central checks** | Controller health monitor | Probes every task **over the WireGuard network** (container IP:port), plus **end-to-end checks through Traefik** on the public route (DNS → TLS → router → app) | Independent view that catches network, routing, TLS and DNS problems the node itself can't see |

**Health model**
- **Task health**: `starting` → `healthy` / `unhealthy` / `stopped`. It combines both layers: a task is only `healthy` if the local check passes **and** the controller can reach it.
- **Service health**: `healthy` (all desired tasks healthy), `degraded` (some unhealthy, or fewer running than desired), `down` (zero healthy) or `deploying`.
- **Node health**: heartbeat every 5s. `Ready` → `Suspect` after 20s without a heartbeat → `NotReady` after 60s, after which all of its tasks are **rescheduled** to other nodes.

**Automatic actions**
- An unhealthy task is removed from Traefik immediately, then replaced after N failures (ECS behavior).
- Restart loops are detected (crash-loop backoff) and the service is flagged with the last exit code and log tail.
- Healthy-target gating: Traefik only receives tasks that are `healthy`, so there is no traffic to starting or failing containers.
- Nodes can be cordoned or drained from the UI (kubectl `cordon`/`drain` equivalent).

**Continuous app monitoring (uptime)**
- Every service with a route gets an **uptime monitor** automatically, with no setup. Additional HTTP/TCP/keyword/SSL checks can be added per service or for external URLs.
- The controller records every check result: **uptime %** (24h/7d/30d), response-time history, status-code history and an **incident timeline** (opened when a service goes `degraded`/`down`, closed on recovery, with the cause attached: crash, OOM, failed health check, node lost, cert error).
- Health state is kept **in memory** for speed and **only transitions** are written to SQLite (D5). Check timings go to VictoriaMetrics.
- Alerts fire on state transitions (§9), with flap protection (N consecutive failures before alerting).
- Optional **public status page** per project (v1.1).

### 5.7 Traefik integration: dynamic and simple

Traefik is **fully managed by the controller**. Users never write Traefik labels or files.

**How config flows**
- Traefik runs with **only** the HTTP provider, pointed at `http://controller:7070/traefik/config` and polled every 2s. The controller regenerates the config instantly on any change (deploy, scale, health change, route edit), so routing is always live.
- Load-balancer servers are the **healthy task container IPs on the WireGuard network** (§5.6). Unhealthy or draining tasks are removed automatically.

**Connecting a service is simple**
- When a service exposes an HTTP port, it **automatically gets a default address**: `<service>-<env>-<project>.<base-domain>`, which is an sslip.io name by default (§5.0.2), with its own certificate issued on creation. It works as soon as the first task is healthy.
- **Add a custom domain** in one step: type the domain, and the UI shows the DNS record to create, checks it live, then the **controller** issues the certificate (HTTP-01, or DNS-01 for wildcards) and serves it to every Traefik replica (§8.5).
- **Connect** on any service: pick the domain, path and port, toggle HTTPS redirect, and you're done. The same dialog is available from the domain's side ("point this domain at…").
- **Retry is on by default** for every HTTP route (e.g. 2 attempts, idempotent methods only), so a request that hits a task in the moment it fails is retried on another task.
- **Middleware presets** as toggles: rate-limit, basic-auth, IP allow-list, redirect www, security headers, compress, retry, circuit breaker, CORS. They can be reused and attached to any route.
- Sticky sessions, weighted routing (canary/blue-green in v2), TCP/UDP routers (e.g. exposing a database port with an allow-list).
- An "Advanced" raw YAML editor that validates before applying, for anything the UI doesn't cover.

**Dynamic traffic dashboard** (SynCloud's own UI, not the stock Traefik dashboard)
- **Live traffic map**: domain → router → middlewares → service → tasks. Each task is colored by health, and the edges show live RPS. Clicking any element opens its details and actions.
- **Live load metrics**: Traefik's Prometheus metrics are scraped into VictoriaMetrics, giving RPS, error rate (4xx/5xx), p50/p95/p99 latency, open connections and bytes in/out, **per service, per router and in total**, streamed at 2–5s resolution.
- **Live request tail** (sampled access log) with filters by route, status and client IP.
- Certificate list with expiry, renewal status and one-click renewal.
- A **"Raw config" debug view** (read-only) shows the exact dynamic config currently served to Traefik, inside the same dashboard. The stock Traefik dashboard is not exposed (D9).

### 5.8 Git and build pipeline

**Sources**
- GitHub App (preferred: webhooks and status checks), GitLab and Gitea/Forgejo (OAuth plus webhooks), or a **generic Git URL** (HTTPS token or SSH deploy key, which the UI generates).

**Triggers**
- **Webhook** push or tag (instant). Signatures are verified.
- **Polling** (always available, and the only option for hosts without webhooks; see below).
- Manual "Deploy now", API or `synctl deploy`.

**Polling mechanism**
- Every app linked to Git has a **watch rule**: repo, branch or tag pattern (e.g. `main`, `release/*`, `v*`), optional **path filters** for monorepos (e.g. `services/api/**`), and an interval (default 60s, minimum 15s).
- A **polling scheduler** in the controller keeps a single priority queue of watch rules. Checks are spread out with jitter so they don't all fire at once.
- Each check is cheap: `git ls-remote` (or a provider API call with ETag / `If-None-Match`) compares the remote HEAD SHA with the last-seen SHA stored in SQLite. Nothing is cloned unless something changed.
- When the SHA changed and path filters match, a **build job** is enqueued. Webhooks and polling use the same queue and are **deduplicated by commit SHA**, so the same commit never builds twice.
- Errors (auth, rate limit, network) cause exponential backoff per rule and show up in the UI with the last error. Provider rate limits are respected globally.
- If webhooks are configured, polling automatically slows to a safety-net interval (e.g. 10 min) to catch missed webhooks.
- Per-app toggles: **auto-deploy on/off** (build only, deploy manually), and target environment per branch (e.g. `main` → production, `develop` → staging).

**Build**
- Clone (shallow, with a cached local copy per repo), detect the build type (Dockerfile → Nixpacks → static), run BuildKit with a layer cache, then push to the **built-in registry** as `@registry/<project>/<app>:<git-sha>` (that is, `registry.<base-domain>/…`, plus the `latest-<branch>` tag).
- Build concurrency limits are global and per project. Build logs are streamed live.

**Deploy**
- A new TaskDefinition revision is created with the new image, and the service is updated, which triggers the rolling deployment (§5.4). Workers pull the image from the private registry over WireGuard.
- Commit status (building / deployed / failed) is reported back to GitHub, GitLab or Gitea.
- **Preview environments** (v1.1): one per PR, at `pr-123.app.example.com`, torn down automatically when the PR closes.

**UI**: Git sources, per-app watch rules, last-seen SHA and last-check time, poll and webhook history, build queue and history, and a "Redeploy this commit" action.

### 5.9 Self-hosted registry (private, D19)
- It is **pre-provisioned at install** as a system task (§5.0), so it is ready before the first app exists.
- It is a **private registry only**. There is no pull-through cache or mirror.
- `distribution/distribution` runs behind Traefik at **`registry.<base-domain>`** (e.g. `registry.203-0-113-10.sslip.io`) with a normal public certificate, so `docker login/push/pull` work from anywhere with **no insecure-registry settings and no custom CA** on any machine.
- **Token auth** is issued by the controller, so registry access is controlled by IAM (`registry:Push` and `registry:Pull` on a project's repositories).
- **Workers pull over the private mesh**: the agent adds a hosts entry on each node mapping `registry.<base-domain>` to `10.90.0.1`. Docker connects to the same hostname, so the certificate is still valid, but the traffic goes over WireGuard instead of the public internet. Pulls use short-lived pull tokens provided by the controller.
- **Public images** (Docker Hub, GHCR, Quay, …) are pulled **directly by each node** from the upstream registry. Credentials can be stored per upstream (e.g. a Docker Hub account to avoid anonymous pull rate limits) and are passed to the agent for pulls.
- Full UI and dashboard: see §5.10.
- **Event tracking**: the registry's notification webhooks send every push, pull and delete to the controller, which records them in SQLite. This provides last-pulled times, counts and "in use by" data, none of which `distribution` tracks itself.
- **Pre-pull**: before a rolling deploy, the scheduler tells the target nodes to pull the new image first, which makes the cut-over faster.

### 5.10 Registry UI and dashboard (ECR-style, D8)

The registry should feel like AWS ECR: a repository list, a repository detail page with its images, and simple settings, with no registry knowledge required.

**Registry dashboard (landing page)**
- Tiles: total repositories, total images, storage used (and trend), pushes and pulls today.
- Charts: storage over time, pushes and pulls over time, bandwidth served to workers.
- Top repositories by size and by pulls; recent pushes feed; vulnerability summary (when scanning is on).
- GC status: last run, space reclaimed, next scheduled run.

**Repositories list** (ECR "Private repositories")
| Column | Notes |
|---|---|
| Name | `<project>/<repo>`; repos are scoped to projects |
| URI | `registry.<base-domain>/<project>/<repo>`, with a copy button |
| Images | count of tagged and untagged images |
| Size | total unique size |
| Last push / last pull | from registry events |
| Tag immutability | Enabled / Disabled |
| Scan on push | Enabled / Disabled |
| Lifecycle policy | Active / None |

- **Create repository**: name, project, tag immutability, scan on push. Repositories are also **auto-created** when an app is first built from Git or when an authorized user pushes.
- Search and filter by project.

**Repository detail**
- **Images tab**: a table of image tags, digest (short and copyable), pushed at, size, last pulled, scan status and findings count, and **"In use by"** (the services and tasks running this digest, linking to them). Untagged images are shown separately.
  - Actions: copy URI / `docker pull` command, **deploy this image** to a service, re-tag, delete (blocked if in use unless forced), and scan now.
  - Image detail: manifest, platforms (multi-arch), layers with sizes, config (env, entrypoint, labels), the Git commit and build it came from (for built images), and scan findings.
- **"View push commands"** button: a modal showing ready-to-copy steps, as ECR does:
  ```bash
  synctl registry login            # or: docker login registry.<base-domain> -u <user> -p <token>
  docker build -t <project>/<repo> .
  docker tag <project>/<repo>:latest registry.<base-domain>/<project>/<repo>:latest
  docker push registry.<base-domain>/<project>/<repo>:latest
  ```
- **Lifecycle policy tab**: rules with priorities, such as:
  - expire untagged images older than N days,
  - keep only the last N images matching a tag prefix (e.g. `sha-*`),
  - expire images older than N days with tag prefix X.
  - A **"Preview" (dry run)** shows exactly which images would be deleted before the policy is saved. Images currently in use by a service are **never** deleted.
  - Policies run on a schedule and are followed by registry GC. Each run is logged.
- **Permissions tab**: a repository policy (the same JSON format as IAM, §7) granting pull or push to users, roles, groups or other projects, with a simple form view and a JSON view.
- **Settings tab**: tag immutability, scan on push, description, and delete repository.

**Upstream credentials page**
- Stored credentials for public registries (Docker Hub, GHCR, Quay, ECR, …) used by nodes when pulling public or third-party private images.

**Image scanning** (v1.1)
- **Trivy** runs on the controller (or the builder node) on push or on demand, and the vulnerability DB is updated daily.
- Findings by severity are shown per image. An optional deploy gate blocks deploys of images with Critical findings.

**Access tokens**
- The UI generates registry-only tokens for CI (push/pull scoped to specific repositories, with an expiry). `docker login` works with an IAM API token or a registry token.

**IAM actions (registry)**
- `registry:ListRepositories`, `registry:CreateRepository`, `registry:DeleteRepository`, `registry:Push`, `registry:Pull`, `registry:DeleteImage`, `registry:PutLifecyclePolicy`, `registry:SetRepositoryPolicy`, `registry:ScanImage`, `registry:ManageUpstreamCredentials`.


### 5.11 Jobs: one-off, scheduled and deploy hooks (D16)

Not everything is a long-running service. Jobs reuse the same TaskDefinitions, scheduler, agents, logs and metrics, but run **to completion**.

| Kind | Example | Trigger |
|---|---|---|
| **One-off task** | `rails db:migrate`, a data fix, a backfill | UI "Run task", `synctl run service/api -- rails db:migrate`, API |
| **Scheduled job** | nightly report, cleanup, cache warm-up | Cron expression with a timezone |
| **Pre-deploy hook** | database migration before the new version goes live | Automatically, before a rolling deployment starts (§5.4) |
| **Post-deploy hook** | cache purge, smoke test, notify | Automatically, after a deployment succeeds |

**Job spec**
```yaml
name: nightly-report
kind: scheduled
taskDefinition: shop-api:14        # or "service: api" to always use the service's current revision
command: ["node", "scripts/report.js"]
schedule: "0 2 * * *"
timezone: Asia/Dhaka
concurrencyPolicy: forbid          # allow | forbid | replace
timeout: 30m
retries: 2                         # with exponential backoff
startingDeadline: 10m              # skip a run if it could not start within this window
historyLimit: { succeeded: 10, failed: 20 }
placement: { constraints: ["node.labels.tier == batch"] }
```

**Behavior**
- A pre-deploy hook must succeed, otherwise the deployment is **aborted** (the old version keeps running) and an incident is opened.
- If the controller was down when a scheduled run was due, the run is started on recovery only if it is still within `startingDeadline`. Missed runs are listed in the UI.
- Every JobRun records status, exit code, duration, node and a link to its logs (§9.2). Failures can trigger alerts.
- **UI**: a Jobs page per project (list, next run, last result, run history), a "Run now" button, and a job-run detail page with logs.
- IAM actions: `job:Create`, `job:Run`, `job:Delete`, `job:View`.
---

## 6. Worker Agent

### 6.1 One-line join
```bash
curl -fsSL https://203-0-113-10.sslip.io/join.sh | sudo bash -s -- --token SYN-JOIN-xxxx
```
`join.sh`:
1. Runs the same preflight checks as the controller (OS, kernel WireGuard, nftables, resources, time sync; 51820/udp free), installs Docker if it is missing, and installs WireGuard tools.
2. Downloads the `syncloud-agent` binary from the controller (with checksum and signature verification) and installs a systemd unit.
3. The agent calls `POST /api/v1/nodes/join` over the **public HTTPS** endpoint (this is the only time public traffic is used), presenting the join token and its WireGuard public key.
4. The controller returns the node ID, the WireGuard IP (`10.90.0.x`), the container subnet (`10.91.x.0/24`), the peer list, an **mTLS client certificate**, and the CA.
5. The agent brings up `wg0`, creates the Docker network `syncloud` with the assigned subnet, adds the hosts entry `registry.<base-domain> → 10.90.0.1` (§5.9), applies its host firewall, and opens the gRPC stream to `10.90.0.1:7443` over WireGuard and mTLS.
6. The node appears in the UI as `Ready` with its specs, OS, Docker version and labels.

Join tokens can be single-use or reusable with a TTL, and can pre-assign labels and a node pool.

### 6.2 Agent responsibilities
| Function | Detail |
|---|---|
| Executor | Handles `StartTask`, `StopTask`, `PullImage`, `Exec`, `Logs` and `Restart` through the Docker SDK. Labels every container with `syncloud.task_id` etc. |
| Observer | Watches Docker events and reports task state changes immediately |
| Metrics | Collects node metrics (CPU, memory, disk, net, load, file descriptors) and per-container metrics (CPU, memory, net, block IO, restarts) every 5s, batched and **pushed** via the gRPC stream. Also exposes `/metrics` on the WireGuard IP so the controller can **pull** with a Prometheus scrape as an alternative mode |
| Health prober | Runs HTTP/TCP/cmd probes defined in the TaskDefinition |
| Logs | Tails every container's stdout/stderr, enriches each line with labels (project, env, service, task, node, revision) and ships batches to the controller for VictoriaLogs (§9.2). Has a local disk buffer for outages |
| Network | Manages WireGuard peers (full mesh, §8) and the routes for container subnets. Measures mesh link latency, packet loss and throughput to every peer |
| Firewall | Applies the host firewall and security-group rules as nftables rules, reports rule counters and sampled drops, and corrects drift (§8.3) |
| Self-update | The controller can push a new agent version, which is rolled out node by node |
| GC | Removes stopped containers and dangling images on a schedule and under disk pressure |

**Push vs pull:** push over the gRPC stream is the default (simplest, works behind NAT). A pull/scrape mode is available as a per-node setting. Both write into the same VictoriaMetrics.

### 6.3 Resilience
- If the controller is unreachable, the agent **keeps existing containers running** (it uses Docker restart policies), buffers metrics (up to N minutes) and reconnects with backoff.
- On reconnect, the agent sends a full state snapshot and the reconciler resolves any differences.

### 6.4 Controller as a worker (optional, D3)
- `install.sh` on the controller **always** installs the same `syncloud-agent`. It joins locally and automatically (no token, using a loopback/WireGuard `10.90.0.1`), so the controller is simply node `ctl-0` in the node list. This agent also runs the platform's **system tasks** (D20).
- Node flag `schedulable: false` is the default. The agent still reports metrics, so the controller host is monitored like any other node.
- Toggle in the UI: **Settings → Controller node → "Allow workloads on controller"**, or `synctl node uncordon ctl-0`. This is the same cordon mechanism as any worker, with no special code path.
- Protection when it is enabled:
  - **Reserved resources** for the control plane (default 1 vCPU and 1.5 GB RAM, configurable) are subtracted from schedulable capacity.
  - System tasks (Traefik, registry, BuildKit, VictoriaMetrics, VictoriaLogs) carry a `syncloud.system=true` label and can never be evicted or rescheduled by the reconciler. The controller itself is a host process (systemd), not a container.
  - User tasks on the controller get a lower OOM priority (`oom_score_adj`) than system containers.
- This makes a **single-server setup** possible: one VPS runs everything, and real workers are added later with the one-line join.


### 6.5 Node pools and cluster autoscaling (D15)

Service autoscaling (§5.5) adds tasks; **cluster autoscaling** adds and removes **servers**.

**Node pools**
- A node pool is a group of nodes with the same labels and role (`worker`, `edge`, `builder`).
- **Manual pool**: nodes are added with the one-line join (§6.1). When capacity runs out, the dashboard shows an alert and an "Add node" wizard with the join command.
- **Provider-backed pool**: has a **CloudProvider** (Hetzner Cloud, DigitalOcean, Vultr, Linode, AWS EC2, …), region, instance type, OS image, SSH key, labels, and `min` / `max` / `desired` node counts.

**How a node is created**
1. The controller calls the provider API to create the server, with **cloud-init user data** that runs the normal `join.sh` using a short-lived, pool-scoped join token.
2. The node joins, gets its WireGuard IP and firewall policy, and becomes `Ready`. This is the same path as a manual join.
3. If the node doesn't become `Ready` within a timeout (e.g. 10 min), it is deleted and the attempt is recorded as failed.

**When it scales**
- **Out**: tasks have been `Pending (insufficient capacity)` for more than 60s, or reserved capacity in the pool is above a headroom target (e.g. 80%). The autoscaler simulates the scheduler to pick how many nodes of the pool's type are needed.
- **In**: a node's reserved capacity has been below a threshold (e.g. 40%) for 10 min **and** all of its tasks fit on other nodes. The node is then cordoned, drained (with deployment-safe rescheduling), and the server is deleted through the provider API.
- Protections: nodes running tasks with node-local volumes are never scaled in automatically; a per-node "scale-in protection" flag; max nodes added or removed per step; cooldowns; and the pool's min/max.
- Every decision is logged with its reason, and node add/remove events appear in the cluster timeline.

**Provider plugins**
- A small Go interface (`CreateServer`, `DeleteServer`, `ListServers`, `InstanceTypes`, `Regions`) per provider. Credentials are stored encrypted (like secrets).
- The first providers are an open question (§18).

**UI**: Node Pools page (pools, nodes per pool, capacity and reservation charts, scaling history, provider settings), plus an "Add node" wizard for manual pools.
IAM actions: `nodepool:*`, `cloudprovider:*`.
---

## 7. IAM (AWS-like)

### Model
- **User**: email and password (argon2id), TOTP MFA, WebAuthn (v1.1), OIDC SSO (v1.1).
- **Group**: a set of users, with policies attached.
- **Role**: assumable by users or by machine identities (CI tokens, the Git builder).
- **Policy**: a JSON document modeled on AWS:
```json
{
  "Version": "2026-01",
  "Statement": [
    {
      "Effect": "Allow",
      "Action": ["service:Get*", "service:List*", "service:Scale", "service:Deploy"],
      "Resource": ["srn:syncloud:project/shop/env/production/service/*"]
    },
    {
      "Effect": "Deny",
      "Action": ["secret:Read"],
      "Resource": ["srn:syncloud:project/shop/env/production/*"]
    }
  ]
}
```
- **Evaluation**: explicit Deny beats Allow, which beats the implicit deny (the AWS rule).
- **Managed policies** are built in: `AdministratorAccess`, `ProjectOwner`, `Developer`, `Deployer`, `ReadOnly` and `BillingViewer` (future).
- **Credentials**: access keys, personal access tokens and temporary role sessions (see §7.1), all governed by the same policies.
- **Audit log**: who, what, resource, before/after diff, IP, user agent and time. It can be searched and exported in the UI.
- **Policy simulator** in the UI answers "Can user X do action Y on resource Z?" (as AWS does).

### 7.1 Programmatic access: API and shell (D10)

The API keys and tokens can control **everything**: every resource and action in the platform, limited only by the IAM policies attached to their user or role.

**Credential types**
| Type | Format | Use | Auth method |
|---|---|---|---|
| **Access key** | Key ID `SYNAK…` plus a secret (shown once) | `synctl`, SDKs, automation, CI | **HMAC-SHA256 request signing** (like AWS SigV4: method, path, body hash, timestamp), so the secret never travels on the wire and replays are rejected after 5 min |
| **Personal access token** | `syn_pat_…` | Quick scripts, `curl`, `docker login`, webhooks | `Authorization: Bearer` |
| **Role session** (STS-like) | Temporary key and secret plus a session token, 15 min–12 h | CI pipelines, Cloud Shell, cross-project access | Obtained via `synctl sts assume-role` / `POST /api/v1/sts/assume-role`. Can require MFA |
| **Registry token** | Scoped to repositories | `docker push/pull` from CI | Bearer (registry token service, §5.10) |

- Each user can have **at most 2 active access keys** (for rotation). Every credential has an optional expiry, an optional IP allow-list, and last-used time, IP and action.
- **Service accounts**: non-human users (no password or console login) that only have keys, for CI and automation.
- Policies can add conditions such as `syn:MFAPresent`, `syn:SourceIp` and `syn:CredentialType` (e.g. deny deletes from long-lived keys).
- **Bootstrap**: `install.sh` prints a one-time **setup token**. In the first-run wizard it is exchanged for the **root (admin) account** with MFA (§5.0). Access keys for the root account are discouraged (as with the AWS root account); admins create IAM users and service accounts instead.
- Every API call, from any credential, appears in the **audit log** with the credential ID.

**`synctl`: the shell interface**
- A single Go binary for Linux, macOS and Windows, installed with `curl -fsSL https://<base-domain>/cli.sh | sh` (or downloaded from the dashboard).
- `synctl configure` writes **profiles** to `~/.syncloud/credentials` (endpoint, access key, default project/env), as `aws configure` does. Env vars `SYNCLOUD_ACCESS_KEY_ID`, `SYNCLOUD_SECRET_ACCESS_KEY` and `SYNCLOUD_ENDPOINT` are also supported for CI.
- `synctl login` uses the browser (device-code flow) to create a short-lived session for humans, so no long-lived keys are needed on laptops.
- Covers **100% of the API** (enforced by the parity check, §5.1): kubectl-style verbs (§10) plus resource commands, e.g. `synctl registry repos list`, `synctl traefik routes create`, `synctl iam users create`, `synctl git watch add`, `synctl health incidents`.
- Output as `-o table|json|yaml|wide`, with `--watch` for live views, and shell completion for bash, zsh, fish and PowerShell.
- `synctl api <METHOD> <path>` is a raw signed API call, as an escape hatch.

**Cloud Shell (in the dashboard)**
- A terminal panel in the single dashboard (xterm.js over WebSocket), available from every page.
- It runs in a sandboxed, short-lived container on the controller with `synctl` (plus `curl`, `jq`, `git` and `docker` CLI) pre-installed and **pre-authenticated with a temporary role session for the logged-in user**. It can only do what that user's IAM policies allow.
- An optional small persistent home directory per user, with an idle timeout. Sessions are audited.

**API reference and SDKs**
- Interactive API docs (generated from OpenAPI) live inside the dashboard, with a "copy as `curl`" / "copy as `synctl`" button on every action, so anything done in the UI can be scripted.
- A Go SDK (the same client `synctl` uses) and a TypeScript SDK (the same client the dashboard uses) are published.


### 7.2 Quotas and usage metering (D16)

**Quotas** (per project, optionally overridden per environment):
| Quota | Example |
|---|---|
| Reserved vCPU / memory | 16 vCPU, 32 GB |
| Tasks / services / jobs | 200 / 30 / 50 |
| Routes and custom domains | 50 |
| Registry storage | 50 GB |
| Log ingestion and retention | 5 GB/day, 14 days |
| Concurrent builds | 2 |
| Allowed node pools | `default`, `eu-fra` (projects can be restricted to specific pools) |

- Enforced **at admission**: the API rejects changes that would exceed a quota, with a clear error. The autoscaler and the scheduler respect quotas too.
- The dashboard shows usage against quota per project, with warnings at 80% and 100%.

**Usage metering**
- The controller records per project and environment: vCPU-hours and GB-hours reserved and used, network egress, registry storage, log volume and build minutes.
- These appear as daily usage charts and a monthly usage report (exportable as CSV). They are the basis for a future cost or billing view.

IAM actions: `quota:View`, `quota:Set`, `usage:View`.
---

## 8. Private Networking

**Design: WireGuard full mesh plus routed per-node container subnets.** This is similar in spirit to flannel over WireGuard, but managed by the controller.

- Address plan (configurable):
  - `10.90.0.0/16`: WireGuard node IPs (controller `10.90.0.1`).
  - `10.91.0.0/16`: container space, with each node getting a `/24` (up to 254 containers per node by default; the size is configurable).
  - `10.92.0.0/16`: **service virtual IPs** (one stable VIP per service, D21, §8.6). These are never assigned to an interface; they exist only as load-balancer addresses.
- Each agent configures:
  - WireGuard peers to **all** other nodes (full mesh, so traffic between workers never hairpins through the controller). The controller distributes peer lists whenever a node joins or leaves.
  - `AllowedIPs` per peer = the peer's node IP plus its container `/24`, which gives routing for free.
  - A Docker bridge network `syncloud` using the node's `/24`, with `iptables` rules that allow forwarding between `wg0` and the bridge **without NAT**, so container IPs are routable across the mesh.
- **Traefik on the controller** reaches task containers directly at `10.91.x.y:port` over the mesh. Worker public IPs are never used.
- **Service discovery** inside the cluster:
  - Internal DNS (§8.1) resolves `api.production.shop.syncloud.internal` to the service's **stable VIP**, which every node load-balances locally (§8.6).
  - `tasks.api.production.shop.syncloud.internal` returns the individual healthy task IPs, for apps that balance on the client side.
- Container ports are **never** published on public interfaces. Public traffic only enters through Traefik (controller or edge nodes, §8.5).
- All firewalling is described in §8.3.

### 8.1 Internal DNS
- Records are maintained by the controller and served on every node over the mesh (the agent runs a small DNS forwarder on the node's WireGuard IP, so lookups stay local and survive a controller outage, using the last known records).
- Names: `<service>.<env>.<project>.syncloud.internal` (the service VIP, §8.6), `tasks.<service>.<env>.<project>.syncloud.internal` (healthy task IPs, TTL 5s), `<task-id>.task.syncloud.internal`, and `<node>.node.syncloud.internal`.
- Every other name is forwarded to upstream resolvers (configurable).

### 8.2 IPAM
- The controller allocates node IPs from `10.90.0.0/16` and a container `/24` per node from `10.91.0.0/16`, and records every task IP in SQLite (which task had which IP, and when).
- Released subnets and IPs go back to the pool after a cool-down, so stale connections never reach a new owner.

### 8.3 Firewall: managed from the controller (D13)

There are three layers, all defined centrally in the dashboard or API and enforced by each node's agent:

| Layer | Protects | Default |
|---|---|---|
| **Host firewall** | The node's **public** interface | Deny all inbound, except WireGuard UDP from cluster nodes, SSH from an allow-list, and 80/443 on the controller and edge nodes. Outbound: allow |
| **Security groups** | Traffic **between containers** inside the private network (east-west) | Services in the same project + environment can reach each other; everything else is denied. Traefik (controller or edge) can always reach the ports of routed services |
| **Egress rules** | Traffic **from containers to the internet** | Allow all (can be restricted per security group) |

**Host firewall policies**
- Attached to a node pool or a single node. Rules: protocol, port or range, source CIDR (or "cluster nodes"), and a description.
- Typical use: allow SSH only from an office IP, open a custom TCP port on edge nodes, block everything else.

**Security groups (AWS-style)**
- A security group is attached to one or more services (and jobs). Each task of those services becomes a member.
- Inbound rules: protocol + port + **source**, where the source is another security group, a service, a whole project/environment, `edge` (Traefik), or a CIDR. Outbound rules work the same way with a **destination**. Rules are stateful (replies are always allowed).
- Each project gets a default security group that implements the default isolation above. Cross-project access (e.g. `billing` calling `shop/api`) is an explicit rule.

**How it is enforced**
- Each agent owns a dedicated nftables table (`inet syncloud`), separate from Docker's own rules, so neither overwrites the other.
- Security-group membership is kept in **nftables sets** of container IPs. When a task starts or stops anywhere, the controller pushes set updates to every agent, so rules never need to be rebuilt.
- Changes are applied **atomically** (one nftables transaction) and versioned with a generation number. Every agent reports the generation it has applied.
- **Drift detection**: every 60s the agent hashes its live ruleset, and if anything differs from what the controller sent (e.g. someone ran `nft` by hand) it restores the managed rules and raises an event.
- **Lock-out protection**: the WireGuard path to the controller is always allowed and cannot be removed. Host firewall changes are applied as "commit-confirm": if the agent loses its controller connection within 60s after applying, it rolls back automatically.
- The controller node runs the same agent, so it is protected by the same system.

**Visibility**
- Per-rule **hit counters** (packets and bytes) and sampled **dropped-packet logs** (via nflog) are shipped as metrics and logs, so you can see which rules are used and what is being blocked.
- **Effective rules viewer**: for any node or task, it shows the exact rules that apply after all policies are combined.
- **Reachability check**: "Can `shop/api` reach `billing/worker` on TCP 8080?" The controller evaluates the rules and answers with the rule that allows or denies it (like the AWS Reachability Analyzer).
- **Preview before apply**: every change shows a diff of the effective rules and which nodes and tasks are affected.

IAM actions: `firewall:View`, `firewall:EditHostPolicy`, `firewall:EditSecurityGroup`, `firewall:AttachSecurityGroup`.

### 8.4 Network visibility (D14)

The **Network** section of the dashboard shows:
- **Topology map** (ECharts graph): nodes and the WireGuard links between them, colored by link health, with latency, packet loss, last handshake and throughput per link. Containers can be shown grouped under each node.
- **Traffic**: inbound and outbound bytes and packets per node, per service and per container; public ingress (Traefik) vs. internal (mesh) vs. internet egress; top talkers.
- **IPAM**: subnets, allocated IPs, which task holds each IP, and IP history.
- **Internal DNS**: all records and their current targets, plus a lookup tool.
- **Firewall**: host policies, security groups, rule hit counters, drop log, effective rules and the reachability check (§8.3).
- **Connections** (v1.1): active connections per container from conntrack sampling, to see which services actually talk to each other.

### 8.5 Ingress and edge nodes (D17)

**v1 default**: public traffic enters through Traefik on the controller.

**Edge-ready by design**
- **Certificates are issued by the controller** (ACME), stored encrypted in SQLite and backed up, and delivered to Traefik through the HTTP provider's TLS section. Traefik never runs its own ACME. This is what makes multiple replicas possible.
- HTTP-01 challenges: every Traefik replica forwards `/.well-known/acme-challenge/*` to the controller. DNS-01 is done directly by the controller with provider credentials.

**Edge nodes**
- A node with the `edge` role (in an `edge` node pool) runs a Traefik replica that gets the same routing and certificates from the controller over the mesh, and reaches task containers over the mesh. Its host firewall opens 80/443.
- Public DNS points at all edge nodes (multiple A records, a provider floating IP, or a provider load balancer). This **requires your own domain**: sslip.io names point at a single IP (§5.0.2).
- **If the controller goes down, edge nodes keep serving traffic** with the last config they received. Only changes pause.
- Edge Traefik metrics and access logs are collected like the controller's, so traffic dashboards show the total across all edges plus a per-edge breakdown.
- Edge health is checked by the controller. An unhealthy edge can be removed from a provider floating IP or load balancer automatically (where the provider supports it).


### 8.6 Service-to-service load balancing: virtual IPs (D21)

**Why not DNS round-robin**: apps cache DNS answers and keep long-lived connection pools, so traffic sticks to a few tasks, new tasks get nothing until callers reconnect, and dead task IPs stay cached. A central proxy on the controller would route all internal traffic through one host.

**Design** (the same idea as Docker Swarm's IPVS mode and Kubernetes' kube-proxy):
- Every service with ports gets a **stable VIP** from `10.92.0.0/16` when it is created. The VIP never changes for the life of the service, even across deploys and scaling.
- Internal DNS returns the VIP, so DNS caching no longer matters.
- **Every agent** programs a local kernel load balancer (**IPVS**, with nftables for marking and firewall integration) so that connections from containers on that node to `VIP:port` go **directly** to a healthy task IP on any node, over the mesh. There is no central hop.
- **Backend updates**: the controller pushes the healthy task list per service to every agent whenever a task starts, stops, becomes unhealthy or starts draining (the same delta mechanism as security-group sets, §8.3). Unhealthy and draining tasks are removed within seconds.
- **Algorithm**: least-connections by default (round-robin selectable per service). Optional source-IP affinity per service.
- **Firewall**: packets are checked after the VIP is translated to the real task IP, so security groups (§8.3) apply to the real source and destination.
- **Draining**: during a deploy, old tasks get weight 0 (no new connections) while existing connections finish, matching Traefik's deregistration delay (§5.4).

**Limits and options**
- This is **per-connection (L4)** balancing. Long-lived HTTP/2 or gRPC connections stay on one task. Those apps can use the `tasks.` DNS name with client-side balancing. A per-node L7 proxy may be added in v2 if needed.
- Requires the IPVS kernel modules (`ip_vs`, `ip_vs_rr`, `ip_vs_lc`), which the preflight checks verify (§5.0).

**UI**: the service page shows its VIP, its current backend list per node (as programmed by the agents), connection counts per backend, and the internal DNS names.
---

## 9. Monitoring and Observability

### 9.1 Metrics data flow
```
agent (node+container metrics) ──gRPC push──► controller ──remote-write──► VictoriaMetrics
Traefik /metrics ─────────────── scrape ────────────────────────────────► VictoriaMetrics
controller internal metrics ──── scrape ────────────────────────────────► VictoriaMetrics
agent firewall/mesh counters ──── gRPC push ────────────────────────────► VictoriaMetrics
UI ◄── WebSocket (live, 2–5s) ── controller ◄── PromQL queries ─────────── VictoriaMetrics

agent (container logs) ──gRPC log stream──► controller (auth, quota, labels) ──► VictoriaLogs
Traefik access logs / build logs / system logs ─────────────────────────────► VictoriaLogs
UI ◄── WebSocket (live tail) ─── controller ◄── LogsQL (IAM-filtered) ─────── VictoriaLogs
```

### Dashboards (built in, no Grafana needed)
1. **Cluster overview**: node count by state, total and used CPU/RAM/disk, running tasks, services health (healthy/degraded/failed), total RPS, error rate and p95, recent events and alerts.
2. **Nodes list and node detail**: per-node CPU, memory, disk, network and load charts; the tasks on the node; Docker info; WireGuard handshake status and latency to the controller.
3. **Service detail**: task count over time with autoscaling events overlaid, CPU and memory per task, RPS, latency and errors from Traefik, a deployment timeline, logs and events.
4. **Traefik / Edge**: live traffic map, global and per-router RPS, status codes, latency heatmap, top routes, request tail, and TLS certificate expiry, in total and per edge node (§5.7, §8.5).
5. **Container detail**: live stats, logs (`tail -f`), an exec terminal (xterm.js over WebSocket), and inspect output.
6. **Health and uptime**: every service with its current health, uptime % (24h/7d/30d), response-time sparkline, and an open incidents list with the incident timeline (§5.6).
7. **Network**: topology, mesh links, traffic, IPAM, DNS and firewall (§8.4).
8. **Logs**: the logs explorer (§9.2).
9. **Capacity**: reserved vs. used CPU/memory per node pool, pending tasks, cluster autoscaling history, quota usage per project (§6.5, §7.2).

### 9.2 Centralized logging (D12)

**Collection**
- The agent tails every container's stdout/stderr through the Docker API (Docker's local log files are size-capped and rotated).
- Each line gets labels: `project`, `env`, `service`, `task_id`, `revision`, `node`, `container`, `stream` (stdout/stderr), plus a detected `level` (from JSON fields or common patterns).
- JSON log lines are parsed into fields so they can be searched (e.g. `user_id:42`).
- Lines are batched, compressed and sent over a dedicated gRPC log stream to the controller. The controller checks the project's log quota (§7.2) and writes to VictoriaLogs.
- If the controller is unreachable, the agent buffers to disk (default 512 MB per node, oldest dropped first) and replays on reconnect.
- Also collected: Traefik access logs (every replica), build logs, job runs, deploy events, agent and controller logs, and firewall drop logs.

**Logs explorer (in the dashboard)**
- **Per-service merged view**: all lines from every task of the service on **every node**, ordered by time, each tagged with its task and node (color-coded). Tasks that have already stopped are included.
- **Live tail** across all nodes, with pause and resume.
- Search: full text plus filters for level, task, node, revision, stream, time range and JSON fields. Shown with a log-volume histogram (errors highlighted).
- **Context view**: jump to the lines before and after a match, even across tasks.
- Saved queries, shareable links, and download (filtered) as a file.
- The same view is available at project level (all services) and at cluster level (admins).

**Access and retention**
- Queries go through the controller API, which **adds the caller's IAM scope to every query**. Users only ever see logs of projects they're allowed to read (`logs:Read`).
- Retention per project (default 7 days, configurable within quota) and a global disk cap. Optional archive to S3 (v1.1).
- **Log-based alerts**: e.g. "more than 50 `level=error` lines in 5 min for `shop/api`", using the same alert channels.
- CLI: `synctl logs -f service/api --since 1h --grep timeout`.

### Alerts (v1)
- Rule types: service health transitions (degraded/down/recovered), uptime check failed, crash loop, node down, log pattern thresholds, firewall drop spikes, mesh link down, pending tasks (insufficient capacity), quota near limit, job failed, disk above 85%, a service below its desired count, a deployment failed, a cert expiring in under 14 days, error rate above X%, and custom PromQL.
- Channels: email, Slack, Discord, Telegram and generic webhook.

### Retention
- Metrics: raw data for 15 days, downsampled data for 90 days (configurable).
- Logs: per project, see §9.2.

---

## 10. Web UI: One Single Dashboard (D9)

**One app, one login, one place.** The whole platform is a single React + Vite SPA served by the controller at `https://<base-domain>`. Every dashboard in this plan is a page inside it: cluster overview, node monitoring, service health and uptime, the Traefik traffic map and routing, the ECR-style registry, Git and builds, metrics, S3, IAM and settings. **No component ships its own UI** (Traefik, distribution, VictoriaMetrics and BuildKit are all headless behind the controller).

**Global layout**
- **Header**: org/project/environment switcher, global search (any resource by name, ID, domain, image or commit), notifications (alerts, incidents, deploys), **Cloud Shell** button (§7.1) and the user menu (keys, MFA, profile).
- **Side nav** (modular, §10.1): the sections below.
- **Main area**: the page. Every resource page shares the same layout: header with status and actions, then tabs (Overview, Metrics, Logs, Events, Settings, YAML).
- **Bottom drawer** (toggle): Cloud Shell, live logs, or the event stream, which stays open while navigating.
- Cross-linking everywhere: an image links to the services using it, a service to its routes, tasks and nodes, an incident to the deploy that caused it, a route to the traffic map.


```
┌ Sidebar ────────────────┐
│ Dashboard               │  cluster overview
│ Projects ▸              │  project → envs → services/jobs/routes/secrets/volumes/quotas
│ Compute ▸               │
│   Services              │  all services (filterable)  ← "kubectl get svc"
│   Tasks                 │  all running containers      ← "kubectl get pods"
│   Jobs                  │  one-off, scheduled, deploy hooks
│   Deployments           │  rollout history / status
│   Nodes                 │  workers, join, cordon/drain
│   Node Pools            │  pools, providers, cluster autoscaling
│ Network ▸               │
│   Traffic               │  Traefik: live traffic map, domains, routes, middlewares, certs
│   Edge Nodes            │  Traefik replicas
│   Topology              │  mesh links, latency, throughput
│   Firewall              │  host policies, security groups, hits/drops, reachability
│   IPAM & DNS            │  subnets, IPs, internal DNS records
│ Logs                    │  logs explorer (merged across nodes), live tail, saved queries
│ Storage (S3)            │  S3 endpoints, bindings, bucket browser
│ Registry ▸              │  dashboard, repositories, images, lifecycle, upstream credentials, tokens (ECR-style)
│ Git & Builds ▸          │  sources, watch rules, webhooks/polling, build queue & logs
│ Health ▸                │  service health, uptime monitors, incidents
│ Monitoring ▸            │  metrics explorer (PromQL), capacity, alerts
│ API & CLI               │  API docs, synctl download, Cloud Shell
│ IAM ▸                   │  users, groups, roles, service accounts, policies, access keys, tokens, audit
│ Settings                │  domains (sslip.io / own), certificates, backups, updates, SMTP, usage
└─────────────────────────┘
```

UI principles:
- **Everything is live**, with no refresh button. WebSocket subscriptions are scoped to the current view.
- **Every form has a "View YAML" toggle**: you can edit the service as a form or as a manifest, and both stay in sync. This mirrors a kubectl/GitOps mental model.
- A **command palette** (⌘K) offers quick actions such as "scale api to 5", "restart service", "tail logs".
- An **event stream** panel appears on every resource page.
- **"Copy as API / CLI"** on every action, so the dashboard teaches the API (D10).
- Dark theme only (D11); usable on tablets; keyboard-first navigation.

### 10.1 Design system and layout (D11)

**Layout**
```
┌──────────────────────────────────────────────────────────────────────┐
│ ▣ SynCloud │ shop ▾ / production ▾ │ 🔍 Search…  ⌘K │ 🔔 │ >_ │ 👤   │  ← header (h-10/h-11, sticky)
├────────────┬─────────────────────────────────────────────────────────┤
│ ◧ Overview │ Services › api                      ● Healthy  [Deploy]│  ← page header (compact)
│ ▸ Compute  │ Overview  Metrics  Logs  Events  Settings  YAML         │  ← tabs
│   Services │ ┌───────┬───────┬───────┬───────┐                       │
│   Tasks    │ │ Tasks │  RPS  │  p95  │ Errors│  ← stat tiles         │
│   Nodes    │ └───────┴───────┴───────┴───────┘                       │
│ ▸ Network  │ ┌──────────────────────┬──────────────────────┐         │
│ ▸ Registry │ │  chart               │  chart               │         │
│ ▸ Git      │ └──────────────────────┴──────────────────────┘         │
│ ▸ Health   │ ┌─────────────────────────────────────────────┐         │
│ ▸ IAM      │ │ table (dense rows)                          │         │
│ ⚙ Settings │ └─────────────────────────────────────────────┘         │
│ « collapse │                                                         │
├────────────┴─────────────────────────────────────────────────────────┤
│ ▔ Cloud Shell │ Logs │ Events                         (bottom drawer)│
└──────────────────────────────────────────────────────────────────────┘
```

**Modular side nav**
- The nav is built from a **module registry**: each feature area (Compute, Network, Registry, Git & Builds, Health, Monitoring, Storage, IAM, API & CLI, Settings) is a self-contained frontend module that declares its nav group, routes, icon, required IAM permission and optional badge (e.g. open incidents count):
  ```ts
  export const registryModule: DashboardModule = {
    id: 'registry', label: 'Registry', icon: BoxIcon, order: 40,
    permission: 'registry:ListRepositories',
    nav: [
      { label: 'Dashboard',    to: '/registry' },
      { label: 'Repositories', to: '/registry/repos' },
      { label: 'Upstreams',    to: '/registry/upstreams' },
    ],
    routes: registryRoutes,
    badge: useRegistryBadge,   // optional live counter
  };
  ```
- Items the user has no IAM permission for are hidden. Groups are collapsible, the nav collapses to an icon rail (`w-56` ↔ `w-12`), and the state is remembered per user. On mobile it becomes a slide-over.
- New features (e.g. the future managed databases, §17) plug in as new modules without touching the shell.

**Header**
- Sticky, compact height. It holds the logo, project/environment switcher, global search with the ⌘K command palette, notifications, Cloud Shell toggle and user menu.
- Each page has its own compact page header below it: breadcrumb, title, status badge, primary actions, then the tabs.

**Spacing and shape (dense UI)**
- Standard container/card padding: **`p-1.5 sm:p-2 md:p-3 lg:p-4`**. Gaps follow the same scale (`gap-1.5 sm:gap-2 md:gap-3 lg:gap-4`). Margins stay minimal; layout spacing comes from `gap`, not margins.
- **Border radius is minor**: `rounded-sm` (2px) for badges, `rounded-input` (4px) for every form field (enforced in `index.css`), `rounded-btn` (5px) for buttons, and `rounded` (4px) at most for cards and panels. There are no pill shapes except status dots.
- **Borders blend in**: `line`/`line-strong` sit a few steps above the surfaces, selected and focused borders and focus outlines use the dim `line-accent`, and status borders are at most 20% opacity. No gradients anywhere.
- Dense tables: `text-xs`/`text-sm`, row height ~28–32px, sticky headers, monospace for IDs, digests and IPs.
- Thin 1px borders instead of heavy shadows to separate panels.
- These are defined once as Tailwind component classes or React primitives (`<Panel>`, `<StatTile>`, `<DataTable>`, `<PageHeader>`), so every module looks the same.

**Dark theme only**
- One dark palette, defined as Tailwind theme tokens (CSS variables): background, surface, surface-raised, border, text, text-muted, and accent. Status colors are healthy (green), degraded (amber), down (red), deploying (blue) and neutral (gray).
- There is no light mode or theme switcher, and no `dark:` variants. Dark is simply the design.
- **Text contrast is at least 8.5:1** against bg, surface, raised and hover, placeholders included; status colors double as text, so they are light pastels, and tints behind text stay at 10%. Disabled controls are exempt.

**Charts: Apache ECharts, minimal**
- One shared **SynCloud ECharts theme** registered once: transparent background, no chart borders, faint dashed split lines, muted axis labels (`text-xs`), no axis lines or ticks, thin 1.5px lines, light area fill with low opacity, no symbols except on hover, and compact grid margins.
- A single dark tooltip style; legends hidden by default (shown only for multi-series charts, small, at the top).
- Shared wrappers: `<TimeSeriesChart>`, `<Sparkline>`, `<Gauge>`, `<Heatmap>`, `<TrafficMap>` (ECharts graph series for the Traefik traffic map, §5.7).
- Real-time charts append points from the WebSocket stream (`appendData`/`setOption` with `notMerge: false`), with a rolling window and no re-render of the React tree.
- A consistent time-range picker (5m / 1h / 6h / 24h / 7d / custom) per page, with synced crosshairs between charts on the same page.

### kubectl-style management (UI and `synctl`)
| kubectl | synctl | UI |
|---|---|---|
| `get pods` | `synctl get tasks -p shop -e prod` | Tasks table |
| `describe` | `synctl describe service api` | Service page |
| `logs -f` | `synctl logs -f service/api` | Logs tab |
| `exec -it` | `synctl exec -it task/abc -- sh` | Terminal tab |
| `scale` | `synctl scale service/api --count 5` | Scale dialog |
| `rollout restart/undo/status` | `synctl rollout restart\|undo\|status service/api` | Deployments tab |
| `apply -f` | `synctl apply -f syncloud.yaml` | YAML editor |
| `cordon/drain` | `synctl node drain w-02` | Node actions |
| `top` | `synctl top nodes\|tasks` | Charts |

---

## 11. Repository Layout (proposed monorepo)

```
SynCloud/
├── plan/                     # this planning folder
├── cmd/
│   ├── controller/           # main for syncloud-controller
│   ├── agent/                # main for syncloud-agent
│   └── synctl/               # CLI
├── internal/
│   ├── api/                  # HTTP handlers, WS hub, OpenAPI
│   ├── iam/                  # policies, evaluation, access keys, signing, STS, tokens, audit
│   ├── cloudshell/           # sandboxed in-dashboard terminal sessions
│   ├── store/                # SQLite repos + migrations (sqlc/goose)
│   ├── reconciler/
│   ├── scheduler/
│   ├── deploy/               # rolling deploy engine, circuit breaker
│   ├── autoscaler/
│   ├── traefik/              # dynamic config generator + metrics
│   ├── registry/             # token auth, events, repos/images, lifecycle, GC, upstream creds, scanning
│   ├── system/               # system tasks (traefik, registry, buildkit, vm, vl), release manifest, upgrades
│   ├── domains/              # base domain (sslip.io default), custom domains, @registry alias
│   ├── git/                  # providers, webhooks, poller
│   ├── build/                # BuildKit / Nixpacks orchestration
│   ├── metrics/              # VM remote-write, PromQL client
│   ├── network/              # IPAM, WireGuard peer distribution, DNS, topology
│   ├── firewall/             # host policies, security groups, rule compiler, reachability
│   ├── logs/                 # log ingest, VictoriaLogs client, IAM-scoped queries, log alerts
│   ├── jobs/                 # one-off, cron scheduler, deploy hooks
│   ├── quota/                # quotas, admission checks, usage metering
│   ├── nodepool/             # node pools, cluster autoscaler
│   │   └── providers/        # hetzner, digitalocean, vultr, aws, …
│   ├── certs/                # ACME (x/crypto/acme), certificate store, renewal
│   ├── agentgw/              # gRPC server for agents
│   ├── storage/              # S3 endpoints, bindings, credential vending
│   └── agent/                # agent-side: docker, collector, prober, wg, nftables, log shipper, dns forwarder
├── proto/                    # agent.proto (gRPC contract)
├── web/                      # React + Vite + Tailwind UI (embedded into controller binary)
│   ├── src/shell/            # layout, header, modular side nav, command palette, drawer
│   ├── src/ui/               # compact primitives: Panel, StatTile, DataTable, PageHeader…
│   ├── src/charts/           # ECharts minimal theme + chart wrappers
│   └── src/modules/          # one folder per feature module (compute, network, registry, …)
├── deploy/
│   ├── install.sh            # controller installer (preflight, deps, verify, init)
│   ├── join.sh               # worker one-liner
│   └── systemd/              # unit files for controller and agent (no compose, D20)
└── docs/
```

---

## 12. Agent ↔ Controller Protocol (sketch)

```proto
service AgentGateway {
  rpc Connect(stream AgentMessage) returns (stream ControllerMessage);
}

message AgentMessage {
  oneof msg {
    Hello          hello      = 1;  // node id, version, capacity, full task snapshot
    Heartbeat      heartbeat  = 2;
    TaskStatus     task       = 3;  // state change: PENDING→PULLING→RUNNING→HEALTHY / EXITED
    MetricsBatch   metrics    = 4;  // node + container samples
    CommandResult  result     = 5;  // ack with generation / error
    LogChunk       log        = 6;  // interactive streams (exec-side tail, debugging)
    ExecIO         exec       = 7;
    FirewallStatus firewall   = 8;  // applied generation, ruleset hash, rule counters
    MeshStats      mesh       = 9;  // per-peer latency, loss, handshake, throughput
    JobRunStatus   job        = 10; // job run started / exit code / finished
  }
}

message ControllerMessage {
  oneof msg {
    StartTask      start      = 1;
    StopTask       stop       = 2;  // with drain timeout
    PullImage      pull       = 3;
    PeerUpdate     peers      = 4;  // WireGuard mesh changes
    StreamLogs     logs       = 5;
    ExecStart      exec       = 6;
    AgentUpgrade   upgrade    = 7;
    ConfigUpdate   config     = 8;  // intervals, probes, labels
    FirewallApply  firewall   = 9;  // full ruleset (generation N) or set-membership delta
    DnsUpdate      dns        = 10; // internal DNS records delta
    RunJob         job        = 11; // start a run-to-completion task
    EdgeConfig     edge       = 12; // edge nodes: Traefik config endpoint + credentials
    VipUpdate      vip        = 13; // service VIP backends delta (add/remove/weight)
  }
}

// Separate stream so high log volume never delays commands or heartbeats.
service LogIngest {
  rpc Ship(stream LogBatch) returns (stream LogAck);  // compressed batches, acked for disk-buffer cleanup
}
```

---

## 13. Controller Durability (single controller)

Because there is only one controller:
- **Workloads survive a controller outage.** Containers keep running and Traefik keeps its last config. Only routing changes, scaling and deploys pause.
  - ⚠️ Without edge nodes, Traefik runs only on the controller, so **public traffic depends on the controller host**. With **edge nodes** (§8.5), public traffic keeps flowing during a controller outage.
  - Internal DNS keeps answering (agents cache records), and firewall rules stay in place on every node.
- **Backups**: the SQLite database is replicated continuously to **S3 with Litestream** (embedded as a library or run as a sidecar), with scheduled `VACUUM INTO` snapshots as a fallback. These are backed up together with certificates and keys, registry storage (the registry can use the **S3 storage driver** directly) and certificates. The metrics snapshot is optional.
- **Restore**: `install.sh --restore s3://... --recovery-key …` on a new host restores the SQLite file from Litestream and unwraps the master key with the recovery key (§5.0.1). Agents reconnect automatically because they dial the controller's **public endpoint and WireGuard key** from the backup.
  - With **your own domain**, the only manual step is updating DNS to the new IP.
  - With **sslip.io**, the new host has a new IP, so the base domain changes: the controller regenerates default addresses and certificates, and agents are told the new controller endpoint through a one-time re-pointing command printed during restore (`syncloud-agent repoint …`). Using your own domain avoids this.

---

## 14. Security Checklist
- mTLS between agent and controller, with an internal CA on the controller and automatic certificate rotation.
- Everything goes over WireGuard; nothing is exposed on worker public interfaces. Host firewalls are deny-by-default and managed centrally (§8.3).
- Projects are isolated from each other at the network level by default security groups (§8.3).
- Log queries are always filtered by the caller's IAM scope (§9.2).
- Cloud-provider credentials are encrypted like secrets and usable only by the node-pool autoscaler.
- Secrets are encrypted at rest with AES-256-GCM using a master key that is wrapped by a recovery key (§5.0.1; KMS/TPM later), are only sent to the agent at task start, and are never logged.
- Install and join scripts verify checksums and signatures of every binary. The setup wizard is protected by a one-time setup token.
- Join tokens are single-use or time-limited and can be revoked.
- The Docker socket is accessed only by the agent and is never mounted into user containers unless explicitly allowed by policy.
- UI security: CSRF, secure cookies, rate limiting on auth, and MFA enforceable per org.
- Image provenance: optional image signing and verification (cosign) in v2.

---

## 15. Roadmap / Milestones

### Phase 0a: Controller core (2 wks) — ✅ done 2026-10-06
*Notes: the OpenAPI spec is hand-written (`internal/api/openapi.json`) and tests enforce route and synctl parity; the Go and TS clients are hand-written against it for now (code generation can replace them later). CI is in `.github/workflows/ci.yml`.*
- Monorepo scaffold, Go module, Vite UI shell, Makefile, CI.
- Controller server: config, SQLite (WAL, single writer) with goose migrations, the in-process event bus, structured logging.
- **API-first skeleton**: OpenAPI spec, setup-token bootstrap, root account with sessions, then access-key signing and bearer tokens; a `synctl` skeleton with `configure` and `api`. `synctl` then grows **alongside every phase** (parity check in CI).
- Single dashboard shell: Tailwind dark design tokens, compact primitives, module registry plus modular side nav, header, the ECharts minimal theme, and live WebSocket plumbing (§10.1).
- First-run wizard (setup token → admin account).

### Phase 0b: Install and system tasks (2 wks) — ✅ done 2026-10-06
*Depends on a minimal agent (Docker runner + gRPC link), which is pulled forward from Phases 1–2.*

**Progress**
- ✅ Slice 1 (2026-10-06), agent link: internal CA, join tokens, `syncloud-agent join` (the node key is generated on the node; the controller signs its CSR), an mTLS gRPC stream (`proto/syncloud/agent/v1`), heartbeats with node metrics, Ready/Suspect/NotReady tracking (only transitions written to SQLite), revocation on node delete, a local `ctl-0` join token, `synctl nodes …`, and the dashboard Nodes page and overview.
  - Follow-up: node certificates are valid for 90 days; automatic renewal over the stream is still to do (before Phase 1 ends).
- ✅ Slice 2a (2026-10-06), Docker runner and system tasks:
  - The agent's own minimal Docker Engine API client (API v1.44, Docker 25+), with an idempotent task runner (spec hash label, replace on change), Docker event watching, and a snapshot in Hello.
  - The controller pushes RunTask/StopTask over the stream. System tasks (Traefik v3.7.13, VictoriaMetrics v1.153.0, VictoriaLogs v1.53.0) run on `ctl-0` and are re-applied every minute.
  - The Traefik HTTP provider endpoint (token-protected) routes the dashboard. X-Forwarded-* is trusted from loopback only.
  - Dev mode uses `127.0.0.1:8080` for HTTP and `:443` for HTTPS (since 2026-10-08), so the dashboard is `https://<base domain>` with no port. Settings → Platform page; `synctl system tasks`.
- ✅ Slice 2b (2026-10-06), private registry: `registry:3.1.2` runs as a system task with Docker token auth.
  - The controller issues ES256 JWTs (x5c and libtrust-style kid) at `/api/v1/registry/token`.
  - `docker login` accepts an access key (key ID as username, secret as password) or any username with a personal access token.
  - Traefik routes `registry.<base-domain>` (`registry.localhost` in dev).
  - Verified with a real `docker login` / `push` / `pull`.
  - Until IAM (Phase 7), the root account gets all registry actions and other accounts get none.
  - **BuildKit moved to Phase 4**: it needs a privileged container and has no consumer before Git builds.
- ✅ Slice 3 (2026-10-06), base domain and certificates:
  - On first start the controller sets `<public-ip>.sslip.io` (or `--base-domain`); the IP comes from an interface or an echo service (`--public-ip` overrides).
  - The controller is the ACME client (HTTP-01). Traefik routes `/.well-known/acme-challenge/` to it, ahead of the HTTP→HTTPS redirect. Certificates and keys are sealed in SQLite and handed to Traefik inline through the HTTP provider's `tls` section.
  - Changing the base domain (Settings → Domains, `synctl domain set`) moves the routes, re-requests certificates, and re-renders the registry's token realm (the registry container is recreated).
  - Verified end to end against Pebble with a real Traefik: issuance, TLS chain, redirects, and a live domain change.
  - Dev mode: no base domain and ACME off by default.
- ✅ Slice 4 (2026-10-06), install, recovery and backups:
  - **Recovery key** (`SYNRK-…`, 200 bits) wraps the master key (HKDF + AES-GCM). It is printed once and written to `<data>/recovery-key` for install.sh; the setup wizard asks for its last 6 characters, then the file is deleted. Existing installs get one on their next start.
  - **Backups (changed from Litestream)**: encrypted bundles instead of WAL streaming. Each holds a `VACUUM INTO` snapshot plus the CA, registry and Traefik keys, sealed with the master key; the header carries the wrapped master key. They go to any S3 (minio-go) on a schedule (default hourly, keep 48), on demand, or as a download. Settings → Backups, `synctl backups …`, and a header warning while backups are off or failing. The DB is small, so a full snapshot is cheap; Litestream can come back in Phase 9 if a lower RPO is needed.
  - **Restore**: `syncloud-controller restore --file … | --s3-… --recovery-key …`. An sslip.io/nip.io base domain follows a changed public IP on start.
  - **`scripts/install.sh`**: preflight (OS, arch, CPU/RAM/disk, ports 80/443, kernel modules, NTP), Docker install, SHA-256 checksums (minisign signature check, active once a release key exists), systemd units, the local agent joined as `ctl-0`, waiting for system tasks, then the dashboard URL, setup token and recovery key. Also `--uninstall [--purge]`. `make release` builds the artifacts.
  - `syncloud-controller doctor` checks the keys, DB, API, disk, Docker, each system task, the base domain DNS, certificates and backups, and prints a fix for each failure.
  - Verified: backup to SeaweedFS S3, restore into an empty directory, sign-in on the restored controller; installer preflight in an Ubuntu 24.04 container. A full install on a fresh VM is still to be done.
  - Deferred to Phase 1: WireGuard (`wg0`) at install, and the host firewall.
- Minimal agent: gRPC stream to the controller, Docker runner for system tasks.
- Controller **installation** (§5.0): preflight checks, signed binaries, systemd units, `init` / `doctor`, the local agent, and **system tasks** (Traefik with the UI as the first route, private registry, BuildKit, VictoriaMetrics, VictoriaLogs).
- **sslip.io base domain**, controller-managed certificates (ACME) with the fallback (§5.0.2), served to Traefik; recovery key; Litestream backup wiring.

### Phase 1: Nodes, Networking and Host Firewall (3–4 wks) — ✅ done 2026-10-06 (DNS and VIPs moved to Phase 2)
**Progress**
- ✅ Slice 1a (2026-10-06), WireGuard mesh and IPAM:
  - The controller allocates a mesh index (`10.90.x.y`, `ctl-0` is always `10.90.0.1`) and a container `/24` per node, with a 1-hour cool-down before released addresses are reused. Every agent gets its full-mesh view (`NetworkConfig`) over the stream, and again whenever membership changes.
  - The agent creates the WireGuard interface `wg-syncloud` (not `wg0`, which users often have). It uses kernel WireGuard, or falls back to embedded **userspace WireGuard** when the module is missing (or with `SYNCLOUD_WIREGUARD_MODE=userspace`).
  - The Docker network `syncloud` (bridge `syncloud0`) uses `gateway_mode_ipv4=nat-unprotected` with Docker's masquerade turned off. The agent's own nftables table `ip syncloud` masquerades only traffic leaving the private ranges, and drops new connections to container IPs that do not arrive from the mesh or the local bridge.
  - Each agent measures RTT to its peers (a TCP probe on `<mesh-ip>:7444`) and reports handshakes and traffic. API `GET /network/mesh`, `synctl network mesh`, and the Network → Topology page (graph and members table).
  - Verified with `make e2e` (three Docker-in-Docker nodes, one on userspace WireGuard): cross-node container traffic keeps the source IP, the controller host reaches remote containers (Traefik's path), egress is masqueraded, and node removal updates the mesh.
  - **Deviation**: the agent ↔ controller gRPC stream stays on the controller's advertised address (mTLS) instead of moving into the mesh. Keeping the control plane independent of the data plane means a broken mesh can still be repaired from the controller.
  - The agent joins the mesh when it runs as root (`--network auto`); the non-root dev agent stays out.
- ✅ Slice 1b (2026-10-06), worker join and certificate renewal:
  - The controller serves `/join.sh`, filled in with the URL it was fetched from, and `/downloads/{agent,synctl}-linux-{amd64,arm64}` plus `SHA256SUMS` from `/usr/local/lib/syncloud/downloads` (populated by install.sh). join.sh installs Docker and nftables, verifies the agent checksum, joins and starts the systemd unit. The Nodes page shows the one-line command.
  - **Node certificate renewal**: with under 30 days left, the agent sends a CSR for a new key over the stream. The controller keeps the previous serial valid until the agent reconnects with the new certificate, and file writes recover from a crash mid-rename.
- ✅ Slice 1c (2026-10-06), host firewall:
  - Each node's `inet syncloud` table (IPv4 and IPv6, replacing the IPv4-only table) holds an input chain. Built-in rules that cannot be removed allow established traffic, loopback, the mesh and Docker bridges, essential ICMP/ICMPv6, and WireGuard from cluster nodes (an nftables set). The controller also keeps HTTP, HTTPS, the agent gateway and an intentionally exposed API port open. Everything else ends in a counted default deny.
  - **Policies** (`firewall_policies`) target all nodes or chosen nodes. A `default` policy is seeded with SSH open. Rules (tcp, udp, icmp or any, with ports and sources: IP, CIDR or `cluster`) are strictly validated, because they are rendered into nftables syntax. One renderer (`internal/firewall`) is shared by the agent and the controller, so the API shows each node's exact **effective rules** and nftables text.
  - **Commit-confirm**: a node that applies a changed firewall waits for the controller's confirmation (sent when its next heartbeat arrives) and rolls back to the last confirmed firewall after 60s without it.
  - **Drift**: before each 30s re-apply the agent compares the live table with what it applied. It counts and logs changes and restores the managed rules.
  - API `/firewall/policies`, `/firewall/nodes/{id}/effective`, `synctl firewall list|get|apply -f|delete|effective`, the Network → Firewall page (policy editor and effective rules), and `--firewall` (default on).
  - Verified in `make e2e`: default deny on public addresses, an allow-policy taking effect, mesh traffic unaffected, commit-confirm completing, and a hand-flushed chain being restored.
  - Phase 6 adds rule hit counters in the UI, the drop log, security groups and the reachability check.
- **Reordered**: internal DNS and service VIPs move into Phase 2 with services, which are their first users. The VIP data path uses **nftables DNAT load balancing** (as in kube-proxy's nftables mode) instead of IPVS: no extra kernel modules, and one atomic ruleset shared with the firewall. The trade-off is random or round-robin balancing, with no least-connections.
- Agent binary, `join.sh`, join flow, mTLS CA.
- WireGuard mesh with IPAM, per-node container subnets, and cross-node container connectivity tests.
- Internal DNS with the agent-side forwarder (§8.1).
- Service VIP allocation and per-node IPVS programming (§8.6), tested before Phase 2 uses it.
- **Host firewall** with nftables: default policies, commit-confirm, drift detection (§8.3).
- Heartbeats, node list/detail UI, node metrics, mesh link stats, first charts.

### Phase 2: Running Containers and Logs (3–4 wks) — ✅ done 2026-10-06 (middleware presets and the raw YAML editor move to Phase 5 with traffic insights)
**Progress**
- ✅ Slice 2a (2026-10-06), services, scheduler and reconciler:
  - Model: projects (each created with a first environment, default `production`), environments, services, **immutable task-definition revisions** (a spec change creates one, scaling does not), and tasks with history (the last 20 stopped tasks per service).
  - Spec v1: one container per task with image, command, env, ports (`http` ports get public routes in 2b), reservations and limits (default 0.1 CPU and 128 MiB, limit 2× memory), and placement `spread|binpack`.
  - **Scheduler** (§5.3): nodes must be Ready, connected, schedulable, have Docker and a ready private network, and have enough *reserved* CPU and memory free (90% of RAM and cores−0.1 allocatable). Then spread by the service's task count, or binpack by free memory, and by total tasks as the tie-breaker. Unplaceable tasks set a service status message that names the reasons.
  - **Reconciler** (§5.2): a per-service work queue triggered by API changes, task reports and node changes, with a 30s resync. It replaces exited or failed tasks (exponential backoff once 3 fail within 5 minutes), replaces tasks on NotReady or removed nodes, re-sends tasks that never reported, and does a basic rolling update capped at 200% (old revisions retire as new tasks run; health gating and the circuit breaker come in Phase 3). Deletion stops tasks, then removes the service. On agent reconnect it compares the container snapshot: missing tasks are re-sent or marked exited, and unknown containers are removed.
  - Tasks run with restart policy `no` (the reconciler owns restarts), on the `syncloud` network, with `SYNCLOUD_*` environment variables and labels. Agents report each task's IP and refuse tasks until the mesh network is applied.
  - Nodes get a `schedulable` flag (cordon/uncordon). `ctl-0` is off by default (D3) and on with `--controller-schedulable` (default in dev).
  - API under `/projects/{p}/environments/{e}/services/{s}` (PUT = create or update, kubectl-apply style), scale, rollback, tasks and revisions; `/services`, `/tasks`. synctl `projects`, `envs`, `services list|run|apply -f|scale|tasks|revisions|rollback|delete`, `tasks list|restart`, `nodes cordon|uncordon`. Pages: Compute → Services (list and a new-service dialog), the service page (scale, tasks, revisions with rollback, JSON spec editor) and Tasks.
  - Verified with `test/e2e/services.sh` on three Docker-in-Docker nodes: spread placement, every task reachable on its mesh IP from another node, scale-down, a rolling update, crash replacement, rollback as a new revision, and deletion with no containers left.
- ✅ Slice 2b (2026-10-06), routing:
  - Every `http` port gets a default hostname `<service>-<env>-<project>.<base-domain>`, or `<service>-<port>-<env>-<project>…` for further ports. Before a base domain exists it is `….localhost` on the dev HTTP port, which browsers resolve locally.
  - The Traefik config adds one router and load balancer per route. Servers are the running tasks' mesh IPs. A retry middleware (2 attempts) is on, and with a base domain routes are HTTPS with an HTTP→HTTPS redirect. Route hosts are added to the certificate manager, so each gets its own ACME certificate.
  - Routes are cached and invalidated by task and service changes. Stopping a task removes it from routing before the container stops.
  - Services report `endpoints`. Verified in e2e with a real Traefik on the controller node: 30 requests to the default hostname reached all three tasks on three nodes over the mesh.
- ✅ Slice 2c (2026-10-06), centralized logs:
  - The agent follows every managed container through the Docker logs API (demultiplexing stdout and stderr, with timestamps). It saves per-container positions in `logpos.json`, so a restarted agent resumes without gaps or duplicates. Lines queue in a 20k-line memory buffer that drops the oldest and counts drops; batches of up to 500 lines or 256 KiB go out as `LogBatch` messages on the agent stream. The disk buffer (§9.2) moves to Phase 9.
  - The controller labels lines from the task ID (project, environment, service, revision, node; system tasks show as project `syncloud`, env `system`). It detects the level from JSON fields or keywords, writes batches to VictoriaLogs (`/insert/jsonline`) and fans out to live tails.
  - Queries use LogsQL built only from exact-match labels and a **quoted** substring, so user input can never widen the scope (ready for IAM scoping in Phase 7).
  - API `GET /logs` (history, oldest first) and `GET /logs/tail` (Server-Sent Events). `synctl logs [-f] [-A] NAME --since --grep --task --node`. The Logs page has project, environment and service filters, history plus live tail with per-task colors and levels, and the service page gets a Logs tab.
  - Verified in e2e with a real VictoriaLogs: the tail streams lines, and history merges two tasks on two nodes.
- ✅ Slice 2d (2026-10-06), service discovery (§8.1, §8.6):
  - Every service with ports gets a **stable VIP** from `10.92.0.0/16` (lowest free index, with a 1-hour cool-down after deletion). The controller builds a **service directory** of VIPs with running backends (old revisions' ports are honoured during rollouts) and DNS records, and pushes it in full to every node when it changes (debounced to 300ms, with a resync every 30s). Agents keep the last copy, so DNS and VIPs survive a controller outage.
  - **DNS**: the agent serves `syncloud.internal` (`<svc>.<env>.<project>`, `tasks.<svc>.<env>.<project>`, `<node>.node`) on the `syncloud` bridge gateway, port 53, and forwards other names to the host's resolvers. Tasks get this server plus search domains (`<env>.<project>.syncloud.internal`, …), so `http://api:8080` works within an environment. (Development nodes without the mesh keep Docker's default DNS.)
  - **VIPs** use nftables DNAT (decision changed from IPVS). There is one chain per service port with `numgen random` over per-backend chains, a hairpin mark plus masquerade for a task reaching itself, and an immediate reject (TCP reset) for VIPs with no running backend. Rendering is shared with the firewall and validates every value.
  - The service view shows `vip` and `dnsName`.
  - Verified in e2e: the DNS name resolves to the VIP, `tasks.` returns all three IPs, external names resolve, and `http://web:8080` from inside a task is spread over all three tasks, including the caller itself (hairpin).
- ✅ Slice 2e (2026-10-06), exec:
  - `GET /tasks/{id}/exec` is a same-origin WebSocket (binary frames for terminal I/O; JSON text frames for `resize`, `eof`, `exit` and `error`). The controller relays sessions multiplexed on the agent stream (`ExecInput`/`ExecOutput`). The agent uses Docker's hijacked exec API and queues input that arrives before the exec is attached. The relay never blocks the agent stream: output for a client that is not reading is dropped.
  - `synctl exec TASK|service/NAME [-t] -- CMD…` uses raw terminal mode when interactive, propagates stdin EOF for pipes, and exits with the remote exit code. The dashboard has a shell button per running task (xterm.js, lazy-loaded). Every exec is audited (`task:Exec` with the command).
- ✅ Slice 2f (2026-10-06), custom domains:
  - `POST /…/services/{s}/domains` (`synctl services domains add|list|remove|check`) routes a host to one of the service's HTTP ports. The host is normalized and must be unique, and the platform's own hosts are refused. The response and list include a **DNS check**: the A record to create, current answers, and a ready flag. Domains join the route set, so they get Traefik routers and ACME certificates like default hostnames. The service page has a domains panel that polls DNS until it points here.
- TaskDefinition, Service, Task model; reconciler; scheduler (spread/binpack, constraints).
- Start, stop and restart; exec terminal.
- **Centralized logging** (§9.2): agent log shipper with disk buffer, log ingest, VictoriaLogs, logs explorer with the per-service merged view and live tail.
- Traefik HTTP provider: auto default domains, one-step custom domains plus DNS check, the Connect dialog, middleware presets (§5.7).
- Service UI with a YAML view.

### Phase 3: Deployments, Health and Jobs (3 wks) — ✅ done 2026-10-06
**Progress**
- ✅ Slice 3a (2026-10-06), health checks and deployments:
  - Spec `health` (`http` path, `tcp` or `cmd`, with interval, timeout, retries and start period) is **probed by the agent** against the task IP, or by exec for `cmd`. Probes live for the agent's lifetime and resume after a restart because the controller re-sends running tasks on reconnect. States: starting → healthy, or unhealthy after N consecutive failures.
  - Only **serving** tasks (running, and healthy when a check is defined) get Traefik routes, VIP backends and DNS `tasks.` records. Unhealthy tasks are replaced.
  - Rolling updates retire old tasks only as new ones *serve*. **Deployments** record each rollout (from→to revision, status, failed-task count, message). The **circuit breaker** (on by default) trips after max(3, ⌈desired/2⌉) failed or unhealthy tasks of the new revision and, if `rollback` is on, rolls back automatically to the previous revision as a new revision with the reason recorded. Old tasks keep serving throughout.
  - API `…/deployments`, `synctl services deployments`, a Deployments tab, health badges on tasks, and the latest deployment shown in the service view.
- ✅ Slice 3b (2026-10-06), drain: `POST /nodes/{id}/drain` (`synctl nodes drain`, Nodes page "drain") treats a node's tasks like an old revision, so replacements start elsewhere and the node's tasks stop once they serve. Uncordon ends a drain.
- Verified with `test/e2e/deploy.sh`: a health-gated first rollout, replacement of a task made unhealthy, a bad revision tripping the breaker and rolling back while the old tasks kept serving, and a drain.
- ✅ Slice 3c (2026-10-06), jobs (§5.11):
  - A job runs a service's task definition (current revision, or the one being deployed for hooks) with a command and extra environment, or a standalone `task` spec. Kinds are `oneoff`, `scheduled` (5-field cron plus macros, IANA timezone, `concurrencyPolicy` allow/forbid/replace, `startingDeadline`), `pre-deploy` and `post-deploy`, with timeout, retries (exponential backoff) and history limit.
  - Runs are tasks (`run_…`) placed by the same scheduler, with no health checks, ports or routes. Containers are removed 10s after exit so their last log lines are shipped. Reconnects resolve lost and pending runs. Missed cron slots beyond the deadline, and runs blocked by `forbid`, are recorded as **skipped**.
  - **Pre-deploy hooks gate deployments**: the new revision is stored but not made current (deployment status `waiting_hook`). All hooks must succeed for it to switch and roll out; one failure fails the deployment and the old revision keeps running. Post-deploy jobs start when a deployment succeeds.
  - API `…/jobs[/{job}[/runs]]`, `…/services/{s}/run`, `/runs/{id}[/cancel]`, `/jobs`. synctl `jobs list|apply -f|run [--wait]|runs|delete`, `run service/NAME -- CMD` (streams logs and exits with the run's code), `runs get|cancel`. The Compute → Jobs page shows next and last runs, Run now, history and per-run logs. Run logs are labelled `job-<name>` or `run-<service>`.
  - Verified in e2e: `synctl run` output and exit codes, a retry, a cron run, a failing pre-deploy hook aborting a deployment, and a passing one letting it switch.
  - Known gap: job runs are not yet counted in node reservations (they are short-lived); Phase 9.
- ✅ Slice 3d (2026-10-06), central health and incidents (§5.6):
  - The controller checks every routed service **end to end through the local Traefik** every 15s (HTTPS with SNI for the default hostname, or plain HTTP in development). A 5xx or connection error counts as failed; a 4xx means the app answered.
  - Service state: `healthy`, `degraded` (fewer serving than desired), `down` (none serving, or two failed checks in a row), `deploying` or `stopped`. The reason is derived from the probe, the service status, or the last failed task (exit code, 137/OOM hint, unhealthy, node lost).
  - **Incidents** open after a bad state holds for 2 evaluations (flap protection), keep the worst state, and close on recovery. Only transitions are stored. Uptime for 24h comes from in-memory samples, and 7d/30d from VictoriaMetrics (`syncloud_uptime_up`, `syncloud_uptime_latency_seconds`, pushed per check).
  - API `/health/services`, `/health/incidents`; `synctl health services|incidents`. Pages: Health → Services (uptime 24h/7d/30d and a 1h response-time sparkline) and Incidents.
  - Verified in e2e: a healthy service reports 100%, a service answering 503 opens a `down` incident with the cause, and fixing it closes the incident.
  - Deferred: central per-task probes over the mesh (the second half of layer 2) and alerts move to Phase 5.
- Two-layer health (agent plus controller), service and task health model, uptime monitors, incidents (§5.6).
- Rolling deploys, circuit breaker with rollback.
- Node failure, rescheduling, cordon and drain.
- **Jobs** (§5.11): one-off tasks, scheduled jobs, pre/post-deploy hooks.
- Deployment timeline UI and events.

### Phase 4: Git, Build and Registry (3 wks) — ✅ done 2026-10-06 (GitHub App, GitLab/Gitea OAuth, commit statuses, SSH deploy keys, per-branch target environments and Trivy move to "Later")
**Progress**
- ✅ Phase 4 close-out (2026-10-06):
  - **Git & Builds pages** replace their placeholders: Sources (every Git source with its service, watch rule, builder, trigger and last check or error; API `GET /git/sources`, synctl `builds sources`) and Builds (recent builds of every service with logs, Deploy and Cancel, and counts). Service links open the service's Builds tab (`?tab=builds`).
  - **Cancel** a running build from the dashboard (cancels its job run).
  - The side nav highlights only the most specific page (`/registry` no longer stays lit on `/registry/repos`).
- ✅ Slice 4a (2026-10-06), registry for deployments:
  - `@registry/<repo>:<tag>` in a task definition resolves to the registry host (`registry.<base-domain>`, or `--registry-pull-host`). The node gets a **pull-only bearer token for that repository** (30 min, `X-Registry-Auth` `registrytoken`), excluded from the spec hash so rotating tokens never recreate containers. Nodes need no `docker login`.
  - **Registry browser**: the controller reads the registry with tokens it issues to itself. It lists repositories (paginated catalog, empty ones hidden) and images (tag, digest, compressed size, platforms from the index or config, created time) and deletes tags (by manifest digest). API `/registry/info|repositories|images`; synctl `registry info|repos|images|delete`; the Registry dashboard (counts and copyable push commands) and Repositories pages.
  - Verified with `test/e2e/registry.sh`: `docker login` with an access key, push, browse, a service deployed from `@registry/…` pulled by the node with a minted token, and tag deletion.
  - Per-project registry permissions arrive with Phase 7 IAM.
- ✅ Slice 4c (2026-10-06), registry lifecycle policies and garbage collection:
  - **Lifecycle policies per repository** (SQLite), ECR-like:
    - Rules have a priority, a tag prefix ("" = every tag) and either `keepLast N` or `olderThanDays N`.
    - Each image belongs to the first rule whose prefix matches it, so a lower-priority rule never expires what a higher one kept.
    - A digest is only deleted when every tag pointing at it expires.
    - **In use = never deleted:** every service's current and previous revision (the rollback target) and both ends of in-flight deployments, whether written as `@registry/…` or with the registry host, by tag or digest.
    - **Preview (dry run)** shows each image's result, deciding rule and reason before saving.
  - **Cleanup runs**, every 24h or on demand (only when the registry system task runs):
    1. Apply the policies (delete manifests).
    2. Switch the registry to read-only maintenance mode (`REGISTRY_STORAGE_MAINTENANCE_READONLY`; pushes are refused, pulls keep working). The container is recreated.
    3. Run `registry garbage-collect --delete-untagged` inside it through the exec relay, measuring `du` before and after for the space reclaimed.
    4. Switch back. Every run is logged with what it expired; runs interrupted by a restart are marked failed.
  - API `/registry/lifecycle` (GET, PUT, DELETE, plus `/preview`) and `/registry/gc` (GET runs, POST start). The repository list flags repositories with a policy. synctl `registry lifecycle get|set|preview|delete` (with `--keep-last`/`--older-than`/`--prefix` shortcuts) and `registry gc run [--wait]|runs`.
  - **Dashboard:** a Lifecycle policy tab per repository (rules editor, preview table) and a Cleanup panel on the registry dashboard (runs, reclaimed space, "Clean up now").
  - Verified with `test/e2e/lifecycle.sh`, using the controller's real system tasks:
    - Pushes v1..v4 plus an overwritten tag; a service runs v1.
    - The preview keeps v1 as in use and expires v2; invalid rules are refused.
    - `synctl registry gc run --wait` expires v2 and reclaims space (untagged layers included).
    - Pushes work again afterwards, and the service kept running.
    - Screenshots checked.
- ✅ Slice 4d (2026-10-06), upstream credentials and pre-pull:
  - **Upstream credentials (§5.9)**:
    - One credential per registry host (Docker Hub aliases normalized to `docker.io`). The password is sealed with the master key and never returned.
    - Nodes resolve a non-`@registry` image's host the way Docker does (`nginx` → Docker Hub; the first part is a host only with a dot, a colon or `localhost`). A matching credential goes with the pull as `X-Registry-Auth`.
    - Git builds get every credential in their Docker config for private `FROM` images.
    - API `/registry/upstreams` (GET, PUT, DELETE `{id}`); synctl `registry upstreams set HOST -u USER` (password from stdin or `$SYNCLOUD_UPSTREAM_PASSWORD`) and `list|delete`; the Registry › Upstreams page.
  - **Pre-pull**: when a deployment changes the image, the controller sends `PullImage` to the nodes running the service and other eligible nodes up to the desired count. Agents pull in the background (de-duplicated, failures only logged).
  - **Fix:** removing a failed task's container no longer overwrites its failed state and error with "stopped", so a pull failure ("unauthorized") stays visible in the task list.
  - Verified with `test/e2e/registry.sh`:
    - A new revision's image is pre-pulled.
    - An image from the same registry by host name (no `@registry`, no docker login) is refused, then pulls once a credential is stored, and the API never returns the password.
    - `deploy.sh` and `services.sh` still pass.
- ✅ Slice 4f (2026-10-06), watch rules, path filters and builds without a Dockerfile (§5.8):
  - **Watch rules**: the branch can be a pattern (`release/*`, every matching branch is built) and an optional tag pattern (`v*`) builds new tags (annotated tags peeled to their commit) and pushes them as `:<tag>` too. The last seen commit of every matching ref is kept (`ref_shas`). A changed ref builds against its previous commit; a new branch or tag builds in full; the first check builds only a literal branch. Manual builds take a `ref` (required when the branch is a pattern).
  - **Path filters** for monorepos (`services/api/**`, `!docs/**`; `*` crosses directories). The build job fetches both commits (depth 1, by hash, falling back to the ref) and diffs them; when nothing matches it exits 78 and the build is **skipped** (status `skipped`, never deployed). Manual builds and new refs ignore filters.
  - **Webhooks slow polling** to a 10-minute safety net while they arrive (last webhook within 24h).
  - **Builders**: `auto` (default) uses the Dockerfile, else **Nixpacks** (v1.41.0, downloaded per build and checked against pinned SHA-256 digests, x86_64 and aarch64) generates one, else a folder with `index.html` (or only Nixpacks' staticfile provider) becomes a **static site** on `nginx:1.29-alpine`, port 80. `dockerfile`, `nixpacks` and `static` force one.
  - The build is now a script (`internal/builds/build.sh`, embedded) in the BuildKit image: it fetches the commit with git (token as an HTTP header) instead of BuildKit's Git context, then builds from local directories. **Layer cache** in the registry (`<repo>:buildcache`, `mode=max`), so repeat builds and Nix layers are fast on any node.
  - API, synctl (`--tags`, `--path` (repeatable), `--builder`, `builds run --ref`) and dashboard (Builder select, Tags, Watch paths, watched refs, a ref picker for Build now, skipped builds, the ref on each build; Builder and Watch paths in the wizard).
  - Verified with `test/e2e/builds.sh`: a docs-only commit is skipped and the next commit builds against it, a pushed tag `v1.0.0` on a branch that is not watched builds and is pushed as `:v1.0.0`, a repository with only `index.html` is served by nginx. With `WITH_NIXPACKS=1`, a Node app without a Dockerfile is built with Nixpacks and served. Unit tests for ref matching, the build decision and the build spec.
  - Not done: target environment per branch, GitHub App, commit statuses, SSH deploy keys, cancelling a build from the UI.
- ✅ Slice 4e (2026-10-06), registry event tracking (§5.9):
  - The registry system task posts its **notifications** to the controller (`/internal/registry/events`, authenticated with the internal token). To reach the controller's loopback API it now runs in host networking on `127.0.0.1:5000` (debug server on `127.0.0.1:5001`), like Traefik.
  - Only manifest events are kept (layers and configs are noise), including HEAD requests: Docker resolves a tag with HEAD, often the only manifest request on a warm node. The manifest requests of one pull (HEAD by tag, index, platform manifest) are merged per repository, actor and address within 30s. The dashboard reading manifests is not a pull. Redelivered events are ignored (unique event ID).
  - SQLite keeps events for 30 days and per-digest counters (pushes, pulls, last pushed, last pulled) until the image is deleted.
  - **In use by**: the services whose current revision runs each image (by tag or digest, `@registry/…` or host name).
  - API `GET /registry/events?repository=&limit=`; repositories gain `pulls`, `lastPushedAt`, `lastPulledAt`; images gain `pulls`, `lastPulledAt`, `inUseBy`. synctl `registry events [REPO]`, plus PULLED, PULLS and IN USE BY columns.
  - **Dashboard:** Pulls and Last push tiles and a Recent activity panel on the registry dashboard; an Activity tab per repository; Last pull and In use by (linked to the service) on images. Pages refresh live on `registry.event`.
  - Verified: `test/e2e/registry.sh` (push and node pull recorded, counters, in use by, browsing not counted, synctl) and `test/e2e/lifecycle.sh` (the real system task delivers pushes and the cleanup's delete).
- ✅ Slice 4b (2026-10-06), Git builds:
  - **Git source per service**: an https URL, branch, context directory, Dockerfile, an optional token (sealed with the master key), auto-deploy, and a poll interval of at least 15s. Connecting runs a smart-HTTP `ls-remote` (no git binary on the controller) to fail early on a wrong URL, branch or token.
  - **Polling with backoff**: errors double the interval, up to 1h. **Webhooks**: `POST /api/v1/hooks/git/{id}` accepts GitHub `X-Hub-Signature-256`, Gitea/Forgejo `X-Gitea-Signature` (HMAC-SHA256) and the GitLab `X-Gitlab-Token`. A webhook only triggers a check, so the payload is never trusted. Each commit is built once (unique on service plus SHA).
  - **Builds**: BuildKit (`moby/buildkit` via `buildctl-daemonless.sh`), run as a privileged, host-network **job run** (trigger `build`), so a build log is just that run's logs.
    - Builds can be pinned with `--build-node`. At most 2 run at once.
    - BuildKit fetches the commit itself (`context=<url>#<sha>:<dir>`, with the token as a `GIT_AUTH_TOKEN.<host>` secret).
    - It pushes `<repo>:<sha12>` and `latest-<branch>` with a 2h push token (Docker config `registrytoken`). `--registry-insecure` (dev default) pushes over HTTP.
  - **Deploy**: a successful build with auto-deploy rolls out a new revision with only the image changed (`@registry/<project>/<service>:<sha12>`). Any succeeded build can be redeployed by hand.
  - Task definitions gain `entrypoint` (the BuildKit image's entrypoint is buildkitd). TaskSpec gains `privileged` (platform builds only, never user-settable) and `placement.node`.
  - API `…/services/{s}/git`, `…/services/{s}/builds`, `/builds`, `/builds/{id}/deploy`, the webhook. synctl `builds connect|source|disconnect|list|run [--wait]|deploy`. A **Builds** tab on the service page: connect form, webhook URL and secret, Build now, build table with logs and Deploy.
  - Verified with `test/e2e/builds.sh`:
    - Smart-HTTP Git server → BuildKit on ctl-0 → private registry → auto-deploy serving the new content.
    - Signed webhook (a bad signature gets 401) → the next commit deployed.
    - A broken Dockerfile fails without deploying.
    - The same commit is refused twice; an older build is redeployed by hand.
    - Disconnect removes the webhook.
  - Still to do: GitHub App installation flow, watch rules and path filters, Nixpacks (builds without a Dockerfile), build cache volume, cancelling a build from the UI.
- ✅ Projects and shared variables (2026-10-06), a user-requested UI rework before the rest of Phase 4:
  - **Project pages**:
    - `/projects` shows projects as cards (environments, service count, task health).
    - `/projects/{p}/{env}` has an environment switcher, a **service card grid**, **Shared variables** and Settings (add or delete environments, delete the project).
    - The service page links back to its project.
  - **Full-page "New service" wizard** replaces the dialog: Source → Service → Variables → Review.
    - Source: a container image, or a Git repository with branch, context, Dockerfile, token and auto-deploy.
    - Service settings: project and environment, name, network (public HTTP, internal TCP or none), port, health path, tasks, CPU and memory.
    - Variables: a key/value editor with a `.env` paste box, masked values, and inherited shared variables marked when overridden.
    - Routes `/projects/{p}/{env}/new-service` and `/compute/services/new`. If connecting a Git repository fails, the half-created service is removed.
  - **Shared variables per environment** (`GET/PUT …/environments/{env}/variables`; synctl `envs vars|set|unset`):
    - Each revision snapshots them into a platform-set `sharedEnv`, so a rollback restores them too. A service's own `env` wins.
    - Changing them redeploys only the services whose variables change, as normal rolling revisions.
    - The service page has a Variables tab for its own variables.
  - **Services built from Git can exist before their first build**: image `@build` runs no tasks ("waiting for the first build", health "stopped"). The first deployed build starts the desired tasks. Task definitions also gained `entrypoint`.
  - Verified:
    - Unit test `TestSharedEnvRollsOutAsRevisions`.
    - `test/e2e/builds.sh` now creates the service as `@build` and checks that shared variables reach the container, with the service's own value winning.
    - The dashboard was checked in headless Chrome against a scratch controller (both wizard paths, and Git failure cleanup).
- ✅ Services only under projects, plus service metrics (2026-10-06), user-requested:
  - **No cluster-wide service list.**
    - "Projects" is its own top-level nav entry. Compute keeps Tasks, Jobs and Nodes.
    - Service pages live at `/projects/{p}/{env}/services/{name}`, and every link uses one `serviceUrl` helper.
    - The old `/compute/services` routes are gone. A service could never exist outside a project environment; now the UI reflects that too.
  - **Task metrics (§9.1)**:
    - **Sampling:** every 10s, each agent takes one-shot Docker stats for its running task containers. It computes CPU from consecutive samples (100% = one core), memory without page cache, and cumulative network and block I/O. Samples ride on the next heartbeat (`Heartbeat.tasks`), queued up to 5000 while the controller is unreachable.
    - **Storage:** the controller batches them into VictoriaMetrics as `syncloud_task_*`, labelled task, service_id, project, environment, service and node.
    - **API and CLI:** `GET …/services/{s}/metrics` (a series per task) and `GET …/environments/{env}/metrics` (a series per service), with ranges 15m, 1h, 6h, 24h and 7d. Each point averages CPU and takes peak memory over its step. The query ends on the next step boundary so the newest samples show at every range. synctl `metrics [SERVICE]` shows the latest values and peak CPU.
    - **Dashboard:** a **Metrics** tab (default) and Logs on every service; **Metrics** and **Logs** tabs on the project page for the selected environment. Charts cover CPU, memory (with the limit line), network in/out and disk read/write.
  - Verified with `test/e2e/metrics.sh`:
    - A CPU-burning service and an idle one, spread over 3 DinD nodes, give per-task and per-service series (burner ≈100% per task).
    - Every range shows the newest samples, and synctl `metrics` works.
    - Screenshots of both Metrics tabs were checked against that cluster.
- ECR-style registry UI (§5.10): dashboard, repositories, images, push commands, lifecycle policies with preview, permissions, upstream credentials, registry tokens; registry event tracking; pre-pull before deploys.
- (v1.1) Trivy scanning and the deploy gate.
- GitHub App, GitLab, Gitea and generic Git; webhooks plus the polling scheduler (watch rules, path filters, SHA dedup, backoff) (§5.8).
- BuildKit system task (privileged; moved from Phase 0b), BuildKit/Nixpacks builds, build logs, auto-deploy.

### Phase 5: Autoscaling and Traffic Insights (2–3 wks) — ✅ done 2026-10-06 (middleware presets, the raw YAML editor and central per-task probes move to Phase 6 with network visibility)
**Progress**
- ✅ Slice 5c (2026-10-06), alerts and notification channels (§9):
  - **Channels**: webhook (JSON), Slack, Discord, Telegram and email (SMTP: STARTTLS on 587, implicit TLS on 465). URLs, tokens and passwords are sealed with the master key; the API only shows a summary (host, chat, recipients). A test notification per channel; a channel a rule uses cannot be deleted.
  - **Rules**, evaluated every 30s, scoped to a project, environment and/or service:
    - `metric`: 5xx error rate, p95 latency, request rate (Traefik), CPU, memory % of limit (agents), per service over a window.
    - `promql`: any query, each series an instance.
    - `log`: lines containing a text (case-insensitive) and/or with a level, counted per service over a window.
    - `health`: a service degraded or down. `node`: a node not reporting.
    - `deployment`, `build`, `job`: each failure notifies once.
  - Instances go pending → firing after `forSeconds` → resolved, with a notification on firing and on resolving. States live in SQLite, so a restart does not notify again; an evaluation error leaves alerts as they are. Editing a rule starts its alerts over. Notifications carry the rule, severity, instance, message, value and a dashboard link. Delivery results are recorded per channel; history is kept 90 days.
  - API `/alerts/channels` (+ `/test`), `/alerts/rules`, `/alerts/active`, `/alerts/events`; synctl `alerts active|events`, `alerts rules list|apply -f|delete` (channels by name), `alerts channels add|list|test|delete`.
  - **Dashboard**: Monitoring › Alerts (Active, Rules, Channels with add and test, History with delivery results), a full-page rule editor, and the header bell shows the firing count (red when critical).
  - Verified with `test/e2e/alerts.sh` on the real system tasks, notifications to a webhook sink:
    - Channel secrets are not returned, and the test notification arrives.
    - A 5xx-rate rule fires to the webhook (JSON) and Slack (text), then resolves once the service answers 200.
    - A log rule and a health rule fire, and a failed deployment notifies once.
    - A node whose agent was killed fires, then resolves when the agent comes back.
    - synctl works. Unit tests cover the state machine, payload formats and validation.
  - Delivery errors never include the channel URL (Slack/Discord webhook paths and the Telegram bot token are secrets), only its host. Screenshots checked.
  - Not done: uptime and certificate-expiry rules, quota and firewall alerts (later phases), silences and routing by severity.
- ✅ Slice 5b (2026-10-06), target tracking autoscaling (§5.5):
  - One policy per service: min/max tasks, a metric and its target — **CPU** or **memory** (average per task, % of the reservation, from the agents' samples), **requests per task** or **p95 latency** (from Traefik) — plus a scale-out cooldown (default 60s), a scale-in cooldown (300s) and scale-in checks (4 × 15s).
  - Every 15s: `desired = ceil(current × value / target)`, clamped to min/max, nothing within ±10% of the target. Scale-out waits for starting tasks and the cooldown; scale-in needs N consecutive checks below target and then goes to the highest count any of them wanted. A count outside min/max is corrected at once. No data holds the count (an idle routed service counts as 0 req/s). Cooldowns survive restarts (last event).
  - Every change is recorded with its reason (`rps 11.9 > target 2 requests/s per task → 1→4 tasks`), kept 500 per service, published on `scaling.event`. The autoscaler writes `syncloud_service_tasks` (desired/running) and its measured value and target to VictoriaMetrics for charts.
  - API `GET/PUT/DELETE …/services/{s}/autoscaling`, `…/autoscaling/charts`, `…/scaling-events`; synctl `autoscale set|get|history|off`. Dashboard: an **Autoscaling** tab (policy form, pause/resume, current value vs target, tasks and metric-vs-target charts, scaling history).
  - API JSON no longer escapes `<`, `>` and `&`.
  - Verified with `test/e2e/autoscale.sh` on the real system tasks: ~10 req/s against 2 per task scales 1→4, idle scales back to 1 after the checks and cooldown, raising the minimum scales at once, a CPU-bound loop scales to its maximum, charts, synctl. Unit tests for the decision and validation. Screenshots checked.
  - Not done (v1.1 per plan): step and scheduled scaling, custom PromQL metrics, quota caps (Phase 7).
- ✅ Slice 5a (2026-10-06), traffic insights (§5.7, §9.1):
  - **Traefik metrics**: the controller scrapes Traefik's Prometheus endpoint (admin entrypoint, loopback) every 10s and imports the service, router and entrypoint series into VictoriaMetrics with SynCloud labels (`service_id`, `project`, `environment`, `app`, `edge`). Platform routes are labelled `syncloud/system/<component>`; series of deleted services are dropped. Traefik gets finer latency buckets (2ms…10s; the defaults read every fast request as ~50ms), and the system VictoriaMetrics shows data after 10s instead of 30s (`-search.latencyOffset`).
  - **Access log**: Traefik's JSON access lines (system task `sys-traefik`) become request lines of the service they reached, stream `access`, with method, host, path, status, duration, bytes, client, the answering task (`upstream`) and service ID as VictoriaLogs fields. They are left out of application logs unless asked for (`stream=access`, plus `status=5xx` and `client=` filters, history and live tail).
  - API: `GET /traffic` (everything), `…/environments/{env}/traffic`, `…/services/{s}/traffic` (charts: requests by status class, p50/p95/p99 latency, bandwidth, requests per service; plus a per-route table of the last 5 minutes), `GET /traffic/map` (hostname → service → task, each task's share of the request rate from the access log). synctl `traffic [SERVICE]`, `traffic map`, `requests [SERVICE] -f --status 5xx --client IP`.
  - **Dashboard**: Network › Traffic (live traffic map as a Sankey: hostnames → services → tasks colored by health and error rate, click through to the service; totals, charts, routes table, live request tail with status and client filters), a Traffic tab on every service and on the project page.
  - Verified with `test/e2e/traffic.sh` on the real system tasks: 2xx/4xx/5xx rates, p50 below 25ms, per-service/environment/total charts, request lines with status filtering and kept out of application logs, the map's task rates, synctl. `services.sh` and `metrics.sh` still pass; screenshots checked.
- Traefik metrics and access-log ingestion; dynamic traffic dashboard: live traffic map, RPS, latency and errors per service and in total, request tail (§5.7).
- Target-tracking autoscaler (CPU, memory, RPS/task, latency); scaling history UI.
- Alerts with notification channels, including log-based alerts.

### Phase 6: Security Groups and Network Visibility (3 wks) — ✅ done 2026-10-06
**Progress**
- ✅ Slice 6a (2026-10-06), security groups (§8.3):
  - Groups belong to a project, with inbound and outbound allow rules (protocol, ports, peers). Peers: `any`, `cluster`, an IPv4 address or CIDR, `group:[project/]name`, `service:[project/]env/name`, `environment:self|[project/]env`, `project:self|name`. Rules are validated, and peers that do not exist are refused.
  - A service uses the groups attached to it, or its project's **default group** (created with every project, and by the migration for existing ones): services in the same environment reach each other, all outbound traffic is allowed. The default group can be edited but not deleted, and a group other rules name cannot be deleted. Job runs use their service's groups; standalone runs the default group.
  - The controller compiles every group into **sets of task IPs** and rules, sent with the service directory to every node. Each agent enforces them for its own containers in its `inet syncloud` table: `sg_out` on the way out (allow = `return`) and `sg_in` on the way in, after `ct state established,related`, with a counted default deny. Containers may only send from their own subnet (anti-spoofing).
  - **Same-node isolation without extra kernel modules**: bridge ports are isolated and the host answers ARP for the subnet (`proxy_arp_pvlan`), so traffic between two containers on one node is routed through the forward chain like traffic between nodes (`br_netfilter` is not needed). New ports are isolated as they appear (netlink).
  - Agents also add their **local containers to sets from their labels** at once, so a new task is classified before the controller hears of it. Job run IPs are recorded so other nodes know them.
  - Cluster nodes (Traefik, health checks, operators on a node) can always reach every task.
  - **Hit counters** per rule survive the table being replaced (agents carry totals), and are exported to VictoriaMetrics. **Drop log**: denied flows (source, destination, protocol, port, packets) are recorded in dynamic nftables sets with per-element counters, again with no extra modules (no NFLOG), shipped in heartbeats, named (task, service, node) and written to VictoriaLogs as stream `firewall`. The host firewall's default deny logs drops too. Drift detection ignores the drop sets.
  - **Preview**: what saving a group changes, as a diff of each affected service's effective rules, with its tasks and nodes. **Reachability check**: "can A reach B on tcp/5432?" answered with the outbound and inbound rule that decide it, or why nothing allows it. **Effective rules** per service.
  - API `/security-groups`, `/projects/{p}/security-groups[/preview|/{g}]`, `…/services/{s}/security`, `/network/reachability`, `/firewall/drops`, `/firewall/counters`; synctl `sg list|get|apply -f [--dry-run]|delete|service|check`, `fw drops|counters`. `--security-groups` (default on).
  - Dashboard: Network › Security groups (groups with hit counts, members, nodes that cannot enforce them, the reachability check), a full-page editor with a live preview, a Security tab on every service, and the drop log and per-rule hits on the Firewall page (whose policy editor is now a full page too).
  - Verified with `test/e2e/secgroups.sh` on three nodes: isolation between environments and projects on the same node and across nodes, VIP and direct traffic within an environment, node hosts reaching tasks, isolated bridge ports, the drop log read back from VictoriaLogs, a custom group (preview, cross-project allow, still-blocked paths), the reachability check, rule hit counters, an outbound rule limiting a service to DNS, job runs as members, synctl, deletion falling back to the default group.
- ✅ Slice 6b (2026-10-06), network visibility (§8.2, §8.4):
  - **IPAM** API and page: each node's mesh address and subnet with the addresses in use and their owners, service VIPs with their backends and DNS names, and addresses in their cool-down. **IP history**: every assignment of a container address to a task or job run, kept 30 days.
  - **Internal DNS**: the zone as served on every node, and a lookup that answers like a task would (short names completed, other names from upstream resolvers).
  - **Throughput**: node traffic (all interfaces) and mesh traffic per node, now stored in VictoriaMetrics, plus the busiest services and tasks; on the Topology page.
  - synctl `network ipam|ip-history|dns|lookup|top`.
- ✅ Slice 6c (2026-10-06), routing extras (§5.6, §5.7):
  - **Middleware presets** per project, attached to services: ip-allowlist, rate-limit (per client IP), basic-auth (bcrypt hashes; passwords are write-only and kept when re-saving), redirect-www, cors, security-headers, circuit-breaker (expression checked), compress, retry (replaces the default 2 attempts). Every HTTP route of an attached service applies them in a fixed order.
  - **Custom configuration** (advanced): Traefik YAML merged into the generated config. Traefik rejects a whole configuration it cannot decode, so sections, fields and types are checked strictly, names cannot collide with generated ones (`svc-`, `dom-`, `mw-`, `syncloud-` are reserved), references must resolve, `@internal` services and `tls` are refused. Validate-only endpoint.
  - **Served configuration** view: exactly what Traefik polls, with certificates, keys and password hashes redacted.
  - **Central per-task probes**: the controller probes every running task over the private network (its HTTP health check, or a TCP connect to its first port) every 10s. After three failures the task leaves Traefik's routes until it answers again (VIPs keep it; other nodes may reach it), and the task shows "unreachable from controller". `--central-probes` (default on).
  - API `/middlewares`, `/projects/{p}/middlewares[/{m}]`, `/traefik/config`, `/traefik/custom[/validate]`; synctl `mw list|apply -f|delete`, `traefik config`, `traefik custom get|apply -f [--dry-run]`. Dashboard: Network › Routing (presets, custom YAML with validation, served config) and a full-page preset editor.
  - Verified with `test/e2e/routing.sh`: basic-auth (401/200, hashes never returned, re-save keeps passwords), ip-allowlist (403), security headers, rate-limit (429), refused bad presets; custom YAML served and bad YAML refused without replacing the good one; IPAM, IP history, DNS records and lookups; a task cut off from the controller leaves Traefik's routes while requests and its VIP keep working, then returns; node and mesh throughput; synctl.
- Fixed on the way: a restarted controller marked every node Not ready ~2s after start (the stored last-seen time is only written on transitions), which rescheduled all tasks. Timeouts now count from the controller's start at the earliest.
- Not done: per-container conntrack connections (v1.1 per plan), IPv6 for containers.
- **Security groups** and egress rules with nftables sets, default project isolation, preview and diff (§8.3).
- Rule hit counters, drop logs, effective rules viewer, reachability check.
- **Network section** of the dashboard: topology map, traffic per node/service/container, IPAM and DNS pages (§8.4).
- Carried over from Phase 2/3/5: Traefik middleware presets and the validated raw YAML editor (§5.7), central per-task probes over the mesh (§5.6).

### Phase 7: IAM, Multi-tenancy and Quotas (3 wks) — ✅ done 2026-10-06
**Progress**
- ✅ Slice 7a (2026-10-06), IAM (§7, §7.1):
  - **Users** (email, password, TOTP MFA) and **service accounts** (keys and tokens only, no console sign-in), **groups**, **roles**, JSON **policies** (`Version 2026-01`, Allow/Deny, wildcard actions and resources, conditions `syn:MFAPresent`, `syn:SourceIp`, `syn:CredentialType` with Bool, StringEquals/NotEquals/Like, IpAddress/NotIpAddress). Deny beats Allow beats the implicit deny.
  - **Every route is an action** named after its API operation (`service:ScaleService`, `network:CreateSecurityGroup`, …), derived from the OpenAPI spec, on a resource derived from its path (`srn:syncloud:project/shop/env/production/service/web`; tasks, runs and builds resolve to their service). Global listings are allowed when the action is granted somewhere and then **filtered per item** (services, tasks, jobs, builds, Git sources, health, incidents, security groups, middlewares, repositories, projects, quotas, usage); logs need a readable project; cluster-wide traffic needs the action everywhere; the live event stream is filtered too. A few self-service actions (own keys, tokens, MFA, password, sts, Cloud Shell) are always allowed. The root account bypasses checks; denials are audited.
  - **Managed policies**: AdministratorAccess, ReadOnly, BillingViewer, and per-project templates ProjectOwner, Developer, Deployer attached as `Developer:shop` (owners cannot change their own quotas). Customer policies, attachments to users, groups and roles, a **policy simulator**, and the action catalog.
  - **Credentials**: access keys (2 per user, expiry, IP allow-list), tokens (expiry, IP allow-list), administrators manage a service account's keys (`?userId=`). **STS**: `sts assume-role` gives temporary credentials (`SYNAS…` key, secret, session token sent as `X-Syncloud-Session-Token`), 15 min–12 h, for users and groups the role trusts, optionally only with MFA. **synctl login** (device flow): synctl shows a code, the person approves it in the dashboard (`/device`), synctl receives 12-hour credentials as them. Disabled users are signed out at once. MFA can be made mandatory for every person.
  - **Audit log**: every mutating call and every denial with actor, credential, resource, IP and detail; search by actor, action, resource, text and time, paging, CSV export.
  - Dashboard: IAM › Users (new user and service account, user page with disable, password and MFA reset, groups, policies, access keys), Groups, Roles, Policies (managed read-only, JSON editor, simulator), My security (MFA enrollment, password, account settings, what I can do), Audit log; the sign-in page asks for the authenticator code.
- ✅ Slice 7b (2026-10-06), quotas and usage (§7.2):
  - Per project and optionally per environment: CPU and memory reserved by desired tasks, tasks, services, jobs, custom domains, concurrent builds (extra builds wait in the queue), log volume per day (metered and warned, not enforced). **Admission**: applying or scaling a service (by hand or by the autoscaler), adding a job or a domain past a limit is refused with `quota_exceeded` and which limit. Usage against limits with warnings at 80% and 100%.
  - **Usage metering** every minute per project, environment and day: reserved and used CPU (core-hours) and memory (GiB-hours), network out, log bytes, build minutes; daily chart, month totals and a CSV report.
  - Not done: registry storage quotas (repository sizes are not cheap to compute yet), allowed node pools (Phase 8).
- ✅ Slice 7c (2026-10-06), API and shell:
  - **Cloud Shell**: a container per user on the controller node (configurable image, synctl mounted, persistent home volume, 30-minute idle stop), with temporary credentials (1 hour) carrying the user's own permissions, reaching the API over the private network (the controller also listens on its mesh address when its API is loopback-only). In the dashboard's bottom drawer and on the API & CLI page.
  - **API & CLI page** (`/developers`): the API reference from the OpenAPI spec with copy-as-curl and copy-as-synctl (from the CLI's own annotations), synctl download and login.
  - **SDKs**: Go (`sdk/go/syncloud`, the client synctl uses) and TypeScript (`sdk/typescript`, fetch and Web Crypto, signatures checked identical to Go's).
  - synctl `iam users|groups|policies|roles|attach|detach|attachments|mfa|password|settings|simulate|actions|permissions`, `sts assume-role [--env]`, `audit`, `login`, `quota list|set|unset`, `usage [--export]`.
- Verified with `test/e2e/iam.sh`: a Developer of shop sees and changes only shop (lists filtered; settings, users, other projects, cross-project logs and traffic denied); a service account with Deployer:shop scales with its access key and nothing more and cannot sign in; quotas refuse scaling past the limit, report usage and warnings, and project developers cannot raise them; MFA sign-in; an MFA-only read-only role via sts; synctl login approved in the dashboard; Cloud Shell runs synctl as the user; denials in the audit log and CSV; usage metered; synctl. Unit tests for the policy engine, TOTP (RFC 6238 vector) and the API flows. Screenshots checked.
- Users, groups, roles, service accounts, JSON policies with conditions, policy evaluation middleware, MFA.
- Access keys (rotation, max 2), personal access tokens, STS role sessions, `synctl login` device flow.
- Cloud Shell, in-dashboard API docs, published Go and TS SDKs.
- Audit log UI and policy simulator.
- **Quotas** with admission checks, and **usage metering** (§7.2).

### Phase 8: Node Pools, Cluster Autoscaling and Edge Nodes (3–4 wks) — ✅ done 2026-10-06
**Progress**
- ✅ Slice 8a (2026-10-06), node pools and cluster autoscaling (§6.5):
  - **Pools** with a role (`worker` or `edge`), manual (nodes join with a pool join command; nodes move between pools with `PUT /nodes/{id}/pool`) or **provider-backed** (region, server type, image, SSH keys, min/max). Nodes outside any pool form `default`. Services can be placed on pools (`placement.pools`); edge nodes take no tasks unless a service names their pool.
  - **Cloud providers**: Hetzner Cloud, DigitalOcean and a generic **webhook** (create/delete/list POSTed as JSON, for any other cloud). Tokens are sealed with the master key and never returned. Servers carry the label `syncloud-pool`; their **cloud-init** user data installs the agent and joins with a single-use token bound to the pool and the server's node name.
  - **Cluster autoscaler** (every 15 s per autoscaled pool): scale out when tasks restricted to the pool (or able to use it) have waited unplaced for `pendingAfter`, sized from their CPU and memory against the server size, or when reservations pass `headroom`; up to `maxStep` servers at once, never above max. A server that does not join within `joinTimeout` is deleted. **Safe scale-in**: a node below `scaleInBelow` for `scaleInAfter` is removed only if its tasks fit on the pool's other nodes, never below min, never a protected node: it is drained, removed, and its server deleted. Every decision is in the pool's scaling history.
  - Dashboard: Compute › Node pools (pools with their nodes, reservations, servers, scaling history, add-node join command, scale-in protection, cloud providers) and a full-page pool editor. synctl `pools list|apply|delete|history|join-command|move|providers|edges`.
- ✅ Slice 8b (2026-10-06), edge nodes (§8.5):
  - Nodes of an `edge` pool run a **Traefik replica** (system task `sys-edge-traefik`, host network, ports 80/443) fed by the controller over the private network (`/internal/traefik/config?edge=<node>`: the same routers, services and certificates, with task addresses on the mesh). Traefik keeps its last configuration, so **public traffic keeps flowing while the controller is down**.
  - The edge's host firewall opens 80/443 (built-in rules). The controller pings each replica every 10 s and imports its Prometheus metrics, so traffic charts include edge requests (labelled `edge=<node>`); access logs come in like the controller Traefik's.
  - Network › Edge nodes: health, public and mesh address, replica state. Leaving the edge pool removes the replica.
- Verified with `test/e2e/pools.sh`: an edge pool on w2 takes no tasks, routes a service over the mesh with its firewall open, and keeps serving while the controller is killed (tasks survive its restart); a provider pool backed by a fake cloud (`test/e2e/cloudsim`, which starts Docker-in-Docker nodes from the cloud-init join command) creates a server when a task restricted to the pool cannot be placed, the node joins and runs it, and once idle the node is drained, removed and its server deleted; synctl; leaving the edge pool stops the replica. Unit tests for the providers (against fake APIs), the pool spec and the scale-out/in decisions. Screenshots checked.
- Not done: provider-specific firewall/network setup (servers rely on the host firewall), spot/preemptible servers.
- Node pools (manual and provider-backed), provider plugins, cloud-init join (§6.5).
- Cluster autoscaler (scale out on pending tasks or headroom, safe scale in).
- **Edge nodes**: Traefik replicas fed by the controller, per-edge metrics, edge health (§8.5).

### Phase 9: Hardening (ongoing) — ✅ planned scope done 2026-10-06
**Progress**
- ✅ Slice 9a (2026-10-06), upgrades, uninstall and GC (§5.0.1):
  - **Controller upgrade** from Settings → Updates, `synctl system upgrade [--version V]` or `syncloud-controller upgrade` on the host. Releases come from `--release-url` (https://, or file:// for air-gapped hosts; same layout as install.sh plus `channels/<channel>`), checked against SHA256SUMS, and the new binary must report the expected version. The database is snapshotted (`VACUUM INTO`) and the current binary kept; a **guard** (a copy of the old binary, a transient systemd unit when under systemd, else its own session) stops the controller, swaps the binary and starts it the same way it was started (recorded in `controller-run.json`). The new controller must answer `GET /api/v1/system/health` (public: database and every system task running) with the new version within 5 minutes and **stay healthy** for `--upgrade-settle` (default 2 minutes); otherwise the **previous binary and database are restored** and restarted. On success the release's agent and synctl binaries replace the worker downloads. Progress steps in `<data>/upgrade/state.json`.
  - **Agent self-upgrade**: agents are upgraded **node by node** (`POST /nodes/agent-upgrade`, `synctl nodes upgrade-agents`, or per node on the Updates page) to the controller's version. The binary goes over the agent's stream in 1 MiB chunks with its SHA-256; the agent starts a guard, replaces itself and re-executes in place (same PID; containers keep running). The new agent confirms once the controller accepts it; if it exits or does not reconnect within 2 minutes the guard restores and restarts the old one, which reports why. A failure stops the rollout. Nodes show their agent version and whether it is outdated.
  - **Uninstall**: `syncloud-controller uninstall [--purge]` and `syncloud-agent uninstall [--purge]` stop and delete the systemd units, remove SynCloud containers and networks, the WireGuard interface and the nftables tables, and keep data unless `--purge` (which also deletes the platform volumes). install.sh `--uninstall` delegates to them.
  - **GC**: every 6 hours the controller deletes the audit log after 365 days, closed incidents, finished deployments (keeping the last 5 per service) and builds (keeping the deployed one) after 90 days, daily usage after 400 days, task definitions beyond the newest 50 per service (never the current, previous, in-flight or running ones), and all but the last 3 upgrade directories. Agents prune images no container uses (older than a day) when the disk is above 85%.
  - Verified with `test/e2e/upgrade.sh`: a release whose controller exits is rolled back by the host CLI (old binary and database back, tasks untouched); an agent binary that exits is rolled back by the agent's guard and the rollout reports why; a good release is installed through the API with tasks untouched and worker downloads updated; all three agents are upgraded one at a time with their containers left running and the mesh converging; synctl; uninstall removes containers, WireGuard and nftables and keeps data until `--purge`. Unit tests for release fetching and checksums, health settling, agent confirmation, store GC and upgrade directory pruning. Screenshots checked.
  - Not done: release signature checks (minisign, as in install.sh) for upgrades; system task image upgrades follow the new binary's manifest on its start rather than one by one with health gates; the rollout state is in memory (lost when the controller restarts).
- ✅ Slice 9b (2026-10-06), restore drills, chaos and load tests, docs:
  - **Re-join**: a join token bound to an existing node's name re-joins that node (same ID, pool, mesh address and tasks, new certificate; the old one stops working): `POST /nodes/{id}/rejoin-token`, `synctl nodes rejoin-command NODE`. `syncloud-controller restore` writes a single-use re-join token for `ctl-0` (`<data>/local-rejoin.token`) and prints the commands. `syncloud-agent set-controller --gateway HOST:PORT [--controller URL]` points a worker at a controller that moved; it keeps its identity and containers. An sslip.io base domain also follows an explicit `--public-ip` in dev mode.
  - **Fixed** (found by the chaos test): a node given up as NotReady that came back with a task's container still running had that task marked running again and the container kept, next to its replacement. The container is now removed and the task stays lost.
  - **Join rate limit** now counts only invalid tokens: many servers of a pool may join from one NAT address at once.
  - Verified with `test/e2e/restore.sh` (backup downloaded, the controller host removed, restored on a new host with a different IP: the sslip.io domain moved, sign-in with the old password, ctl-0 re-joined as the same node, workers pointed at the new address kept their containers, the restored controller schedules) and `test/e2e/chaos.sh` (a frozen worker goes NotReady and its tasks are replaced; on its return its stale containers are removed; tasks keep serving while the controller is killed and nothing is rescheduled after it restarts; a WireGuard partition between two workers cuts their tasks off from each other without changing node status or moving tasks, and healing restores reachability).
  - **Load test** (`test/load/run.sh`, `test/load/fakeagent`: real joins, mTLS streams, heartbeats and task reports without Docker), controller limited to 4 CPUs and 8 GB: 50 nodes joined in 1 s; 40 services × 50 tasks = 2,000 tasks running 3 s after the first apply, spread 40 per node; at steady state the controller used under 1% CPU and about 50 MiB; API p50/p95: GET /nodes 0/1 ms, /services 9/10 ms, /tasks (2,000 rows) 40/51 ms, one service 1/1 ms; a 50-task rolling update in about 1 s; no errors logged. The fake agents start tasks instantly, so this measures controller overhead, not image pulls.
  - **Docs** for operators in `docs/`: install, nodes and pools, S3 storage and metrics, operations (health, upgrades, agent rollout, backups, restore and moving the controller, uninstall, retention), testing.
- ✅ Slice 9c (2026-10-06), sections that were still placeholders:
  - **S3 storage** (§16): endpoints for any S3-compatible provider (credentials checked by listing buckets before saving; the secret sealed with the master key and never returned); a **bucket browser** (buckets with the services bound to each, create bucket, folders, upload up to 5 GiB from the dashboard and 256 MiB from synctl, downloads always as attachments, delete objects or folders, size of a prefix); **bindings** on a service's S3 tab: each binding (optional key prefix and variable prefix for more than one) reaches the tasks as `S3_ENDPOINT`, `S3_BUCKET`, `S3_PREFIX`, `S3_FORCE_PATH_STYLE`, `AWS_ACCESS_KEY_ID`, `AWS_SECRET_ACCESS_KEY`, `AWS_REGION`. Revisions snapshot the binding (endpoint name, bucket, prefix), never credentials, which are added when a task starts; changing bindings rolls out a new revision. Endpoints with bindings cannot be deleted. IAM actions under `s3:` on `srn:syncloud:s3/<endpoint>[/bucket/<bucket>]`. synctl `s3 endpoints|buckets|mb|ls|cp|rm|du|bindings`. Self-hosted S3 is MinIO or Garage deployed as an ordinary service and registered like any other endpoint.
  - **Metrics explorer** (§9.1): PromQL range queries over everything VictoriaMetrics stores, with a range picker, chart, series table with latest and max values, metric names and example queries; at most 200 series. It reads every project's series, so IAM checks `metrics:ExploreMetrics` on `srn:syncloud:metrics`. synctl `metrics query|names`.
  - Verified with `test/e2e/storage.sh`: MinIO deployed as a service and registered (wrong credentials refused, secret never returned); bucket, upload, folders, download as attachment, usage, folder delete; two bindings reach a new revision's task as `S3_*`/`AWS_*`/`BACKUP_*` variables, credentials stay out of revisions, a bound endpoint cannot be deleted; synctl; PromQL with labels, metric names, errors for bad queries, synctl metrics query. Screenshots checked.
  - Not done: per-binding scoped keys (provider-specific), bucket usage history.
- Controller **upgrade with automatic rollback**, uninstall (§5.0.1), backups and restore drills (including sslip.io IP change), agent self-upgrade, GC, docs, chaos tests (kill nodes, kill controller, partition the mesh), and load tests (target: 50 nodes and 2,000 tasks on a 4 vCPU / 8 GB controller).

### Phase 10: Integrations and UI polish (user request, 2026-10-06) — ✅ done 2026-10-06
**Progress**
- ✅ Slice 10a (2026-10-06), requested after Phase 9: "I can't see the Traefik configuring options", "full automatic systems, so I can easily connect to different things like GitHub", darker buttons with a small radius, and no stacked padding when cards nest:
  - **Git providers** (§5.8, from Phase 4's "Later" list): GitHub in **one click** through the GitHub App manifest flow. The dashboard posts a manifest to GitHub (personal account or organization, or GitHub Enterprise); GitHub's redirect returns a code, which is exchanged for the app's ID, private key and webhook secret. These are saved sealed under a connection ID chosen up front, so the app's webhook URL is final. The browser then continues to the installation page. The app signs RS256 JWTs and caches installation tokens (one hour); its webhook (`/api/v1/hooks/github-app/{id}`, HMAC-checked) checks every service building the pushed repository. **GitLab, Gitea/Forgejo and GitHub** connect with an access token, checked against the host before it is saved sealed. Connections list repositories and branches. A service picks `connection` + `repo` instead of a URL (wizard, Builds tab, `synctl builds connect --connection --repo`; the branch defaults to the repository's default). SynCloud then mints clone tokens, **creates the push webhook on the repository** (removed on disconnect or when the repo changes) and **reports commit statuses** (`syncloud/<project>/<env>/<service>`: pending → running → success/failure, linking to the Builds tab), in order through one background queue. Connections in use cannot be removed. API under `/integrations/git` (IAM `integration:` on `srn:syncloud:integration/git/<name>`); synctl `integrations git list|add|get|remove|repos|branches|github-app`; migration 00030.
  - **Integrations page**: Git provider cards and connections, plus links with counts to notification channels, S3 endpoints, container registries, cloud providers, backups and domains. Connection pages show the installations (GitHub App), services and repositories with the service building each.
  - **Traefik settings** (§5.7), **Network → Traefik**, `synctl traefik settings [set k=v…]`:
    - *Live* (dynamic config): HTTP→HTTPS redirect on/off, minimum TLS 1.2/1.3 and strict SNI (`tls.options.default`), default retry attempts (0 = off), HSTS, compression and a request body limit for every service route, ahead of per-service middlewares.
    - *Static* (flags on every replica, controller and edges): log level; entrypoint read, write and idle timeouts; trusted proxies for `X-Forwarded-*` (with a one-click Cloudflare list); PROXY protocol (only with trusted proxies); HTTP/3; upstream dial and response-header timeouts; idle connections per task. Flags are sorted so an unchanged setting never restarts Traefik; a change re-renders the system task and every edge replica.
    - The page shows the replicas and the exact flags added.
  - **UI**: buttons use darker fills (`btn`/`btn-primary` tokens) and a 5px radius (`rounded-btn`); a new `Toggle` switch. A Panel inside another Panel becomes a plain titled subsection without its own border or padding, and stat tiles inside panels become ruled figures, so padding never stacks.
  - Verified with `test/e2e/integrations.sh` against a real Gitea (rootless image): a wrong token is refused; the connection lists a private repository and its branches; Traefik settings are validated (no PROXY protocol without trusted proxies) and served (TLS options; body limit, HSTS, compression and 3 retries on the service route); a service from `forge` + `e2e/web` gets its webhook created on Gitea, builds with the connection's token, deploys, and Gitea shows pending then success; a push is delivered by Gitea's webhook through Traefik and deployed; a broken commit shows failure; synctl; a connection in use cannot be removed, disconnecting removes the webhook. Unit tests: a fake GitHub verifying the app JWT (installation token cached, uninstalled repos reported), GitHub/GitLab/Gitea hooks and statuses, the manifest flow (state single-use and per user), token checks and sealing, settings validation, stable flags and route chains. Screenshots checked.
  - Not done: GitLab/Gitea OAuth apps (tokens instead), pull request preview environments (v2 list), per-branch target environments, SSH deploy keys.
- ✅ Slice 10b (2026-10-07), built-in Git server (user request: "a self-hosted git platform option"):
  - **Forgejo** (`forgejo:13.0.5-rootless`) is an **optional system component** (`sys-git`), off by default; system components can now be optional (left out of the specs, the platform list and the health check while off; turning one off stops and removes its container).
  - It runs in host networking on `127.0.0.1:3002` with its data in the `syncloud-git` volume, served by Traefik at `git.<base-domain>` (with a certificate and the HTTP redirect; not on edges). Its configuration is rewritten from the environment on every start, so it follows domain changes. Sign-up is closed, SSH and Actions are off.
  - Turning it on (Settings → Platform or Integrations, `PUT /gitserver`, `synctl integrations git-server enable`) generates its secrets and an administrator password (sealed), waits for it to answer, creates the administrator inside the container through the exec relay, mints an API token and registers the Git connection **`git`**, so services build from it with webhooks and commit statuses like any other host. The administrator's sign-in is revealed on demand (audited). It cannot be turned off while services use it; off keeps the volume.
  - Settings → Platform and Integrations get a **Git** panel: the built-in server switch, its address and sign-in, and buttons to connect GitHub, GitLab (cloud or self-hosted) or a self-hosted Gitea/Forgejo.
  - Verified with `test/e2e/gitserver.sh` (real system tasks): off by default and unlisted; on → running, administrator created, connected as `git`; a service built from `syncloud/web`, a push delivered by Forgejo's webhook, statuses on both commits; synctl; refused off while in use; off removes the container and the connection and keeps the volume; on again brings the repository back. Unit tests for the optional component and the `git.<base-domain>` route.

### Phase 11: Real-cluster test (VM lab) — ✅ done 2026-10-07
**Progress**
- ✅ Slice 11a (2026-10-07), user request: "real test with low VMs, a hello-world API pushed to the registry, running in a project with Traefik, test the whole project":
  - `test/vmlab/vmlab.sh`: three rootless QEMU/KVM Ubuntu 24.04 VMs (2 GB controller, two 1 vCPU/1 GB workers) on a private multicast network without a public IP; `up`, `install` (real install.sh, admin, pinned worker join), `deploy`, `ssh`, `down`, `destroy`. `test/vmlab/hello`: a small Go API for tests.
  - Exercised for real: install and setup; joins; kernel WireGuard mesh; `docker push` to the registry; a service pulled from it on both workers; HTTPS through Traefik with HTTP redirect; load balancing; internal DNS and VIP between services; logs and traffic metrics; rolling updates under load; scaling; a hard-killed worker under load and its return; agent self-upgrade; rate-limit middleware and global Traefik settings live; the built-in Forgejo with a webhook-triggered BuildKit build deployed automatically; the dashboard.
  - **Fixed** (details in docs/testing.md): self-signed private-network installs (pinned join commands; registry certificate bundle for nodes, BuildKit and a trust command; Git polling, builds and Forgejo webhooks trust the cluster's certificates); task drain before stop (`deployment.drainSeconds`, default 5); spreading counts only the new revision; Traefik active health checks per route and a 2s connect timeout to tasks; builds reserve 0.25 CPU/512 MB and may run on the controller node, and say when they wait for a node; an older commit's build is not auto-deployed over a newer one; installer minimums (2 GB warns); Overview services tile.
  - Results: 0 failed of 1,250 requests over three rollouts; one hard-killed worker: 1 failed of 288 (the request in flight); ~1 ms p50 through Traefik; the platform uses ~650 MB on the controller.
  - Open: a later 15-minute run on the lab saw 24 failed of 4,245 requests while a v1 → v2 rollout and a 2 → 4 scale-up ran under load (0 of 2,000 in steady state); not yet narrowed down to one of the two.
- ✅ Slice 11b (2026-10-07), user request: "the Overview needs more detail, the three charts overlap and should forget old data, more charts, an overview of everything, responsive on every screen":
  - Node CPU, memory, disk and load and the controller's heap and goroutines are now stored in VictoriaMetrics; `GET /metrics/overview?range=` returns every Overview chart (exempt from synctl: `synctl metrics query` reads the same series).
  - Overview rebuilt: range picker (15m–7d, fixed windows replace the browser-side rolling buffer), 8 stat tiles, a "needs attention" list (nodes, alerts, degraded services, incidents, failed builds, certificates, backups), 10 history charts with one value axis each (heap and goroutines split), and panels for projects, builds, service health, activity, registry and certificates.
  - Charts use one validated categorical palette (dark surface, colourblind-checked); node colours are fixed per node across charts; time labels hide instead of overprinting.
  - Responsive: below md the side nav is an off-canvas menu; the header and tab rows (one shared `Tabs`) fit any width; an automated audit of every page at 360, 768, 1024, 1280 and 1920 px finds no horizontal overflow.
- ✅ Slice 11c (2026-10-07), user request: "full resource and usage monitors for individual nodes including the controller, their own dashboard, and a shell; and limit which nodes a project or service may use, set in the project section":
  - Node page (`/compute/nodes/<name>`): live tiles, details, history charts (`GET /nodes/{id}/metrics`: CPU, memory and disk against totals, load, network, mesh, tasks, tasks' CPU and memory by service), its tasks, cordon and drain, and a node shell.
  - Node shell (`GET /nodes/{id}/shell`, `synctl nodes shell`): `ExecStart.host` makes the agent run a login shell under a PTY on the host as its own user (root on installed nodes); audited as `node:Shell`; admins only by default; `--no-host-shell` turns it off per node.
  - Allowed nodes: `projects.nodes` (migration 00031, `PUT /projects/{p}/nodes`, `synctl projects nodes`) and `placement.nodes` per service, which must stay within the project's list. Applied at placement, not stored in revisions; tasks on nodes no longer allowed are retired like a drain (replacement first); naming `ctl-0` places on the controller even when it takes no general workloads; jobs follow the project, builds do not. UI: project Settings → Allowed nodes; service Placement tab (nodes and spread/binpack).
  - Tests: unit (placement rules, controller override, validation, host shell PTY/pipe/refusal); e2e `test/e2e/placement.sh`.

### Phase 12: Managed Valkey (Redis-compatible databases)
User decisions (2026-10-07): **Valkey 8** (BSD; same protocol and clients as Redis); **Sentinel failover** (primary and replicas on different nodes); **autoscaling = memory online + read-replica count** (no cluster mode, so clients keep two plain endpoints).

**Model.** A database belongs to a project environment, like a service, and shares its DNS namespace (a service and a database cannot have the same name). Each database is dedicated: its own containers, volumes and password. Stored in `databases` (sealed password, spec JSON, status) and `database_members` (one row per container).
- **Data members** `dbm_…`: `valkey-server` pinned to a node with a node-local volume (`syncloud-db-<id>-<n>`), AOF `everysec` plus RDB snapshots (or RDB only, or none). Members announce stable internal names (`m<n>.<db>.<env>.<project>.syncloud.internal`) so restarts and new IPs never confuse replication or Sentinel.
- **Sentinels** `dbs_…`: three small `valkey-sentinel` containers (quorum 2) on distinct nodes when the database has replicas; none for a single-member database.
- **Anti-affinity**: members never share a node; sentinels are spread. Placement respects the project's and the database's allowed nodes (§6.3). Reservations count in the scheduler like service tasks.
- **Endpoints**: `<db>.<env>.<project>.syncloud.internal:6379` (read-write, a VIP whose only backend is the current primary) and `<db>-ro.…:6379` (read-only, the replicas; the primary when there are none). Connection strings and `REDIS_URL`-style env vars are shown in the UI; a service can bind a database to get them injected.
- **Security**: `requirepass` + `masterauth` (32 random bytes, sealed); dangerous commands renamed off (`FLUSHALL`, `CONFIG`, `DEBUG`, `SHUTDOWN`, `MODULE`, `REPLICAOF`… are only reachable by the controller's admin user via ACL); the database joins its environment's default security group.

**Operator** (`internal/dbs`): reconciles members and sentinels like the workload manager, but stateful: a member is never moved while its node is merely slow. If a replica's or sentinel's node is not ready for 5 minutes it is replaced on another node (a replica resyncs from the primary); a lost single-member database waits for its node (or a restore). It polls each member every 5s (`ROLE`, `INFO`) over the private network: the primary is what Sentinel elected, and the read-write VIP follows it within seconds of a failover. Restarted old primaries rejoin as replicas.

**Autoscaling** (every 15s):
- **Memory**: `maxmemory` moves between min and max online (`CONFIG SET`, no restart). Up by 50% when used memory stays above 85% for 30s and the node has room; down by 25% after 30 minutes below 40% (never below 1.3× used). Container limits are set to max + overhead at creation; the scheduler reserves the current size.
- **Read replicas**: count between min and max on replica CPU (target, default 60%) or ops/s per replica; out after 1 minute above target, in after 10 minutes below half of it. Every change is a scaling event with its reason.

**Metrics**: per member from `INFO` (ops/s, memory used vs `maxmemory`, clients, keys, hit rate, evictions, expirations, replication offset lag, role) into VictoriaMetrics as `syncloud_db_*`, plus the container CPU/memory the agents already sample. Logs flow like task logs (service `db-<name>`).

**API / CLI / IAM** (tag `databases` → `database:*`, resource `…/env/<e>/database/<name>`): CRUD under `/projects/{p}/environments/{e}/databases`, `credentials`, `metrics`, `members`, `failover`, autoscaling settings and events; explorer: `keys` (SCAN by pattern and type, with TTL and size), `keys/{key}` (typed view: string, hash, list, set, sorted set, stream; edit, TTL, delete), `command` (console, deny-listed admin commands), `info`, `slowlog`. `synctl db …` for all of it.

**UI**: a project's **Databases** tab and a routed **New database** wizard (name, memory min/max, replicas min/max, persistence, eviction policy, nodes); a top-level **Databases** page listing all; a database page with Overview (endpoints, connection strings, members with role/node/lag), Metrics, Explorer (key tree, value editor, console), Autoscaling (settings and events), Logs and Settings.

**Slices**: 12a operator, endpoints, failover, API/CLI; 12b metrics and autoscaling; 12c dashboard and explorer; 12d backups (RDB snapshots to S3 on a schedule, restore into a new database).

**Progress**
- ✅ Slices 12a–12c (2026-10-07): `internal/dbs` (operator, Sentinel, probe every 5s, memory and replica autoscaler, explorer), migration 00032 (`databases`, `database_members`, `database_events`), VIPs shared with services, member DNS names and endpoints in discovery, databases in security groups and logs, `StopTask.remove_volumes` (only `syncloud-db-*`), scheduler `ExtraUsage`/`Reserved`/anti-affinity, 18 API operations, `synctl db …`, dashboard (Databases list, project tab, wizard, database page: overview with connection and members, metrics, explorer, console, autoscaling, logs, settings), docs/databases.md.
  - Health is `healthy` only when failover is possible (every sentinel knows every replica and its peers): the first e2e run showed a fresh replica Sentinel had not yet discovered.
  - e2e `test/e2e/databases.sh`: members on distinct nodes; writes and reads through both endpoints from a task; explorer and console; a frozen primary fails over and writes resume within ~15s, the old primary rejoins as a replica with data intact; memory autoscales 64 → 128 MiB at 98% use; read load adds a replica; deletion removes volumes.
- ⬜ Slice 12d: scheduled RDB snapshots to S3 and restore into a new database; binding a database to a service (inject `REDIS_URL`).

**12e: standalone databases, public endpoints, engine-neutral (user request, 2026-10-07)**

The user wants a database to work like an AWS-managed one: it gets a URL that is usable from outside the cluster, it does not have to belong to a project, and "database" must not mean only Redis (PostgreSQL comes later).

- **One namespace.** Database names are unique in the cluster, as in an AWS account, and every database is addressed as `/api/v1/databases/{name}`.
  - A database is either **standalone** (no project) or **in a project environment**, as before. A project database keeps its project's node limits and its environment DNS names.
  - Creating one: `POST /databases` for standalone; `POST /projects/{p}/environments/{e}/databases` for a project database, so IAM checks the project.
  - The IAM resource is `srn:syncloud:database/<name>` for standalone and `…/project/<p>/env/<e>/database/<name>` for a project database. The API resolves it from the name.
  - Names `engines` and anything ending in `-ro` are reserved.
- **Internal endpoints.**
  - Standalone: `<name>.db.syncloud.internal` and `<name>-ro.db.syncloud.internal`. Members are `m<n>.<name>.db.syncloud.internal`.
  - Project databases are unchanged: `<name>.<env>.<project>.syncloud.internal`. Existing Sentinel state refers to these names.
- **Access (internal).** Every database has its own access list, like an RDS security group, compiled as a synthesized security group attached only to it:
  - inbound from its own members (all ports);
  - inbound on the engine port from each listed peer: `project:P`, `environment:P/E`, `service:P/E/S`, a CIDR, or `cluster`.
  - A project database starts with `environment:<p>/<e>`, which is what the project default group gave it. A standalone database starts with none.
  - Platform addresses (Traefik on the controller and edges, health checks) are always allowed, as for services.
- **Public endpoint (external URL).** Per database, off by default.
  - Traefik terminates TLS on an engine entrypoint (Valkey `:6379`) and routes by SNI (`HostSNI`) to the current primary over the mesh:
    - `<name>.db.<base-domain>:6379` is read-write;
    - `<name>-ro.db.<base-domain>:6379` is read-only, served by the replicas (the primary when there are none).
  - The URL is `rediss://default:<password>@<name>.db.<base-domain>:6379`.
  - Certificates come from the controller like any route: HTTP-01 per host, and DNS-01 for wildcards later.
  - An IP allow-list (CIDRs, default `0.0.0.0/0`) becomes a Traefik TCP `ipAllowList` middleware.
  - The primary changes after a failover. The controller rewrites the TCP backends within one probe (≤ 5 s) and Traefik picks them up on its 2 s poll.
  - The host firewall opens the engine port on the controller and edge nodes only while at least one database is public.
  - A base domain is required. Without one, the endpoint shows why it is unavailable.
  - TLS is mandatory: SNI routing cannot work on plain TCP, and passwords must not cross the internet in clear text.
  - PostgreSQL will use Traefik's STARTTLS support for Postgres on its own `:5432` entrypoint, with the same model.
- **Engines.**
  - `GET /databases/engines` lists the engines with their versions, port, URL scheme and capabilities: Valkey is available; PostgreSQL is listed as planned.
  - `engine` is part of a database (default `valkey`).
  - Engine-specific API parts reject other engines with 400; the key explorer, console and Sentinel failover are Valkey's.
  - The wizard starts with the engine choice.
  - Code boundary: the operator core (members, placement, network, public routes, events) stays in `internal/dbs`. Engine behavior (task specs, probe, autoscale signals, explorer) is what PostgreSQL will add as its own files.
- **UI.**
  - **Databases** gets a sidebar entry.
  - The wizard has these steps:
    1. engine;
    2. name and owner, either standalone or a project environment;
    3. capacity;
    4. data;
    5. network: access list, public endpoint and allow-list;
    6. review.
  - The database page moves to `/databases/<name>` and gains a **Connectivity** tab: internal and public endpoints, connection strings, the access list and the public switch. The project tab lists that project's databases.
- **CLI.**
  - `synctl db create NAME [-p P -e E]` creates standalone without `-p`.
  - `synctl db network NAME --public on|off --allow CIDR… --access PEER…`.
  - `synctl db engines`.
  - Every item command takes only the name.

**Progress**
- ✅ 12e (2026-10-07):
  - **Store:** migration 00033 rebuilds the three database tables. The environment is nullable, names are globally unique, and a `network` JSON column is added. It was checked on a copy of the dev database: members and events were kept, and the access list defaults to the database's own environment.
  - **Engines and access:** `dbs.Engines` lists Valkey and PostgreSQL (planned). The access list compiles to a synthesized `dbsg_` security group with a new `self` peer kind.
  - **Public endpoints:** Traefik TCP routers use `HostSNI` with TLS termination and an `ipAllowList`. A `valkey` entrypoint is added on the controller and edges (`--public-valkey`, dev `127.0.0.1:16379`). The firewall opens the port only while a database is public, and public hosts get certificates.
  - **API, CLI and UI:**
    - the API moves to `/databases/{name}` plus `PUT …/network` and `GET /databases/engines`, with `database:CreateDatabase` checked on the exact resource;
    - database logs use `?database=`, authorized on the database;
    - `synctl db create|network|engines`;
    - the wizard gains Engine, Database (owner) and Network steps, and the database page a Connectivity tab;
    - a project tab lists the other databases it can reach.
  - **e2e** `test/e2e/databases.sh`:
    - a standalone database is refused, then reachable once its environment is on the access list;
    - `rediss://` through Traefik from another machine reads and writes, the read-only endpoint is read-only, and plain TCP is refused;
    - the allow-list is enforced, and the public endpoint follows a failover (writes resumed 13 s after the primary froze);
    - the existing steps all pass.

### Phase 13: Managed PostgreSQL (replicated, autoscaled, with WAL-G and analytics extensions)

User decisions (2026-10-07):
- **HA: Patroni + etcd.** Failover must keep working while the controller is down, as with Valkey's Sentinel.
- **Scale: read replicas + vertical resizing.** Write scale-out (Citus sharding) is out of scope.
- **Time series: TimescaleDB Apache edition.** The TSL edition forbids offering it as a database service.
- **Analytics: pg_duckdb with the cluster's S3 endpoints.**

PostgreSQL is the second engine of the Phase 12 core (§12e). It shares:
- global names and both kinds of owner (standalone or project);
- the access list and the public endpoint;
- placement with anti-affinity, member reservations and lost-member replacement;
- events, logs and the dashboard shell.

**Image** (`images/postgres/Dockerfile`, pinned as `ImagePostgres` and published by the release pipeline; dev and e2e build it locally with `make postgres-image`):
- based on `pgduckdb/pgduckdb:17-v1.x` (official Postgres 17 on Debian, plus pg_duckdb);
- from the PGDG and Timescale apt repositories:
  - **extensions:** `pgvector`, `pg_partman`, `pg_cron`, `timescaledb-2-oss` (Apache only), `postgis`, `hypopg`, plus the contrib modules (`pg_stat_statements`, `pgcrypto`, `hstore`, `pg_trgm`, `btree_gin`/`btree_gist`, `postgres_fdw`, `uuid-ossp`, `tablefunc`);
  - **tools:** `patroni` with `python3-etcd`, and `pgbouncer`;
- the `wal-g` binary (Apache 2).

One image runs every role, chosen by its command: Patroni-managed Postgres, PgBouncer, or the WAL-G helper.

**Coordination: the platform etcd.** One small etcd cluster serves every Postgres cluster:
- **Members:** 3 system members on distinct nodes (1 while the cluster has fewer than 3 nodes). Each has node-local data, a peer and client listener on the mesh, and auth with a sealed root password.
- **Patroni access:** each Postgres cluster gets its own etcd user and a key prefix `/syncloud/pg/<db-id>/`.
- **Membership changes:** a lost member's node gone for 10 minutes means the member is removed and added on another node. Changes go one at a time, and quorum is kept.
- **Controller outages:** etcd runs on nodes, so failover keeps working while the controller is down.

**Cluster model** (engine `postgres`, members `dbm_`):
- **Data members:** `m<n>`, each a Patroni plus Postgres container pinned to its node, with a node-local volume `syncloud-db-<id>-m<n>`. Names follow the database's internal zone, e.g. `m0.<db>.db.syncloud.internal`.
- **Replication:** Patroni bootstraps the primary, and replicas clone with `pg_basebackup`, or from the latest WAL-G backup when one exists.
- **Durability:** asynchronous by default. Optional `synchronous: on` sets `synchronous_mode` (on) with `synchronous_node_count` 1 and needs at least one replica.
- **Failover:** `ttl` 30 s, `loop_wait` 10 s. Patroni promotes the most up-to-date replica, and `maximum_lag_on_failover` is 1 MiB unless synchronous.
- **Switchover:** used for maintenance and rolling restarts, through Patroni's REST API on the mesh (`:8008`, with a password).
- **Endpoints** (the same model as Valkey; the probe asks every member's Patroni `/cluster` and VIPs follow its leader):
  - `<db>…:5432` is read-write on the leader;
  - `<db>-ro…:5432` reads from the replicas that are in sync and in the read pool.
- **Pooling:** PgBouncer is on by default. Two small pooler containers per cluster run on distinct nodes in transaction mode, with session mode on another port.
  - The pooled endpoints are `<db>-pool…:6432` and `<db>-pool-ro…:6432`.
  - Pool sizes follow `max_connections`.
  - App users are mirrored into `auth_query` through a `pgbouncer` lookup function, so no user list needs syncing.
- **Analytics replica (optional):**
  - one extra replica tagged `nofailover` and `noloadbalance`, kept out of the read pool;
  - higher `work_mem` and `max_parallel_workers_per_gather`, pg_duckdb on, `hot_standby_feedback` off;
  - endpoint `<db>-analytics…:5432`;
  - heavy warehouse queries never touch the primary or the app's read pool.

**Users, databases, extensions** (managed by the controller through the leader as the `syncloud_admin` superuser, which apps never get):
- **Initial setup:** an owner role `app` with a password and a database named after the cluster.
- **Management:** more roles (login, password, `CONNECTION LIMIT`, read-only or read-write grants per database) and more databases.
- **Extensions:** the allowed list above, enabled per database with `CREATE EXTENSION`. `shared_preload_libraries` holds `pg_stat_statements`, `timescaledb`, `pg_cron` and `pg_duckdb`; changing it restarts the members in a rolling way.
- **Parameters:** a validated allow-list (e.g. `work_mem`, `statement_timeout`, `log_min_duration_statement`) applied through Patroni's dynamic configuration. Settings that need a restart are marked as such.

**Explorer and administration (user request, 2026-10-07: "real exploration of the DB, like a web admin: list databases and users, walk table schemas, alter roles and privileges").** This is a pgAdmin-style page in the dashboard, served by the controller.
- **Connection:**
  - The controller connects to the leader over the mesh as `syncloud_admin`, with a short-lived connection per request and a pool of at most 4 per cluster, `statement_timeout` 30 s and `application_name=syncloud-explorer`. Browsing never goes through the client endpoints, and replicas are not used.
  - Every identifier passes through `pgx.Identifier`, and every value is a bind parameter. Privileges and role attributes are checked against fixed lists.
- **Protected objects:**
  - `syncloud_admin`, `replicator`, `pg_*` roles and the `postgres`/`template*` databases can be viewed but not changed or dropped.
  - `SUPERUSER`, `REPLICATION` and `BYPASSRLS` cannot be granted. As on RDS, the highest non-platform role is `CREATEROLE` + `CREATEDB`, and `app` has that.
- **Databases:** list (owner, size, encoding, collation, connections, `datallowconn`); create (name, owner, template `template1`); rename; change owner; drop (with a typed-name confirmation, which terminates the database's sessions first).
- **Roles:**
  - List: login, attributes, connection limit, `valid until`, member of, and the databases each role owns.
  - Create and alter: password (generated or typed; SCRAM; never returned), `LOGIN`, `CREATEDB`, `CREATEROLE`, `INHERIT`, connection limit, `valid until`, and membership in other roles (grant and revoke, with `ADMIN OPTION`).
  - Drop: chooses a role that takes over the dropped role's objects (`REASSIGN OWNED BY … TO …; DROP OWNED BY …` in each database), then drops it.
- **Schema browser** (per database):
  - The tree is schemas → tables, views, materialized views, sequences, functions and types. System schemas are hidden behind a toggle.
  - A table shows its columns (type, nullable, default, identity or generated, comment), constraints (PK, FK with target, unique, check), indexes (definition, size, scans), triggers, its size, row estimate, last vacuum/analyze, and the reconstructed `CREATE TABLE` DDL.
  - **Data tab:** pages of 100 rows (up to 1000), sorting by a column, simple column filters with bound values, and a row count on request. It is read-only; edits go through the console.
- **Privileges:**
  - For any database, schema, table, view, sequence or function, a role × privilege grid built from `aclexplode` (with grantor and `WITH GRANT OPTION`), plus default privileges (`pg_default_acl`) per schema.
  - **Editing** applies one change set in a transaction: GRANT or REVOKE of named privileges to a role, optionally `WITH GRANT OPTION`, on one object, on all tables, sequences or functions in a schema, or as `ALTER DEFAULT PRIVILEGES … IN SCHEMA`.
  - **Presets** (a role on a database): read-only (CONNECT, USAGE on schemas, SELECT on all tables and sequences, plus default privileges), read-write (adds INSERT, UPDATE, DELETE, TRUNCATE and sequence USAGE), and owner-like (membership in the owner).
  - **Effective view:** for a role, everything it can do on an object through membership, via `has_table_privilege` and its siblings.
- **Extensions** per database: available versus installed, with version. Install (`CREATE EXTENSION … CASCADE` into a chosen schema), update, and drop. Only the image's allow-list is offered.
- **SQL console:**
  - A database picker and a run-as role picker. The default role is `app`. Any role except the protected ones can be chosen, applied with `SET ROLE`, so the console never runs as the superuser.
  - **Read-only by default:** queries run in `BEGIN READ ONLY`. A write toggle runs in a normal transaction and is audited with the statement text.
  - Several statements per run; the result of each is shown with its column types, at most 1000 rows, the command tag and the duration. `EXPLAIN (ANALYZE, FORMAT JSON)` is shown as a plan tree. Errors include the SQLSTATE and position.
  - Recent queries are kept per viewer in the browser.
- **Sessions:** `pg_stat_activity` (user, database, client, state, wait event, query, duration, blocked by), with cancel and terminate. Platform sessions are hidden.
- **API** (Postgres only; other engines get 400): under `/api/v1/databases/{name}/pg/`:
  - `databases` (GET, POST), `databases/{db}` (PATCH, DELETE);
  - `roles` (GET, POST), `roles/{role}` (GET, PATCH, DELETE);
  - `schema?db=` (tree), `table?db=&schema=&name=` (detail and DDL), `rows?db=&schema=&table=` (data);
  - `privileges?db=&kind=&schema=&name=` (GET), `privileges` (POST: change set or preset);
  - `extensions?db=` (GET, POST, DELETE);
  - `query` (POST);
  - `sessions` (GET), `sessions/{pid}/cancel` and `/terminate` (POST).
- **IAM:** each operation is its own `database:*` action, so read access (browse, read-only query) can be granted without admin (roles, privileges, write queries). Every change is audited.
- **CLI:** `synctl db sql <name> [--db] [--as] [--write] "<sql>"`, `synctl db roles|role create|alter|drop`, `synctl db grant|revoke`, `synctl db pg-databases`.
- **Dashboard:**
  - The **Explorer** tab has a tree on the left and object tabs on the right (Columns, Data, Indexes, Constraints, Privileges, DDL).
  - The **Roles** tab has a list. Create and edit are full-page forms (`/databases/$name/roles/new`, `/roles/$role`), each with a membership and per-database privilege section.
  - The other tabs are **Console** and **Sessions**.

**Backups (WAL-G to an S3 endpoint, §16):**
- **Archiving:** `archive_command` with `wal-g wal-push` runs continuously, with the archive timeout at 60 s.
- **Base backups:** `wal-g backup-push` runs from a replica (the primary when there is none), on a schedule (default daily) and on demand.
- **Retention:** `retain N` full backups plus a time window, applied by `delete retain FULL`.
- **Storage:** the prefix `s3://<bucket>/<prefix>/syncloud-pg/<db-id>/`, with the S3 credentials sealed and given to members as secrets.
- **Encryption:** client-side libsodium with a sealed key per cluster.
- **Restore:** to any time within the window, or to a named backup, always into a new cluster. Patroni's `bootstrap.method: walg` uses `backup-fetch` plus `restore_command` and `recovery_target_time`. The source cluster is never touched.
- **Clones:** the same restore gives "clone from latest", e.g. for staging copies.
- **Dashboard:** a backup list (time, size, WAL range, duration), an RPO indicator showing the age of the last archived WAL, and an alert when archiving fails.

**13c implementation (user request 2026-10-07: "WAL-G with S3 configuration that enables point-in-time recovery"):**
- **Configuration:**
  - `spec.postgres.backup` = `{endpoint, bucket, prefix, everyHours (24), retainFull (7), retainDays (7)}`.
  - `endpoint` names a registered S3 endpoint (§16), so its credentials stay sealed there.
  - `prefix` defaults to `syncloud-pg/<db-id>`.
  - Backups can be turned on when a cluster is created or later. Changing them restarts members one at a time, switching over before the primary.
  - Turning backups off stops archiving. What is already in S3 stays there.
- **Members:**
  - WAL-G gets its settings from the member's environment: `WALG_S3_PREFIX`, the AWS variables from the endpoint, `WALG_COMPRESSION_METHOD=zstd` and `WALG_LIBSODIUM_KEY`. The key is a sealed 32-byte key per cluster, so the backups are encrypted.
  - `archive_mode` is always on, and `archive_command` is `wal-g wal-push %p` (`/bin/true` while backups are off). `archive_timeout` is 60 s, so no more than about a minute of writes is at risk.
  - Replicas set `restore_command` to `wal-g wal-fetch %f %p`, so they can catch up from the archive.
  - When a base backup exists, new replicas clone from it first (`create_replica_methods: [walg, basebackup]`, image script `walg-replica`). Without one they fall back to `pg_basebackup`.
- **Base backups:**
  - The controller runs a one-shot task (`dbk_…`, restart policy no) on a streaming replica's node, or on the primary's when there is no replica. The task mounts that member's volume read-only and connects to it as the superuser.
  - The task runs `wal-g backup-push`, then `wal-g delete retain FULL <retainFull> --after <now − retainDays> --confirm`.
  - A backup runs every `everyHours`, or on demand (`POST …/backups`). Its outcome becomes an event, and its logs are the database's logs.
  - Only one backup runs at a time per cluster.
- **Status:** the API reads the bucket directly through the S3 endpoint:
  - backups from `basebackups_005/*_backup_stop_sentinel.json` (name, start and finish times, LSNs, sizes);
  - the last WAL upload from `wal_005/`;
  - the restore window, from the oldest backup's start to the last WAL upload;
  - from the primary, `pg_stat_archiver` (last archived, failures).
  - An alert fires when nothing has been archived for 15 minutes while archiving is on (wired with 13d's alerts).
- **Restore:** `POST /databases` with `restore: {from, backup?, targetTime?}` creates a new cluster and never touches the source.
  - The new cluster gets the source's database name, passwords and S3 endpoint, and its own prefix and encryption key.
  - Patroni bootstraps it with `bootstrap.method: walg` (image script `walg-bootstrap`, which runs `backup-fetch` of the chosen or latest backup before the target).
  - It recovers with `restore_command` reading the source's archive (`walg-restore-wal`), `recovery_target_time` (or to the end of the archive) and `recovery_target_action: promote`.
  - Then it starts its own timeline and archive. A clone of the latest state is the same restore without a target time.
- **Image:** `17-r2` adds the three scripts.
- **e2e** (`test/e2e/postgres-backup.sh`, MinIO as a task):
  - write rows; take a backup; write more and note the time; write a row that should not survive;
  - restore to the noted time into a new cluster: its rows are exactly the ones before the time;
  - the backup list and window are shown, and a scheduled backup runs;
  - a new replica clones from WAL-G.

**Autoscaling** (every 15 s, with each change logged with its reason):
- **Read replicas** (min to max), on the read pool's average CPU or the active connections per replica: one more after 1 minute above the target, one fewer after 10 minutes below half of it. New replicas clone from WAL-G when a recent backup exists.
- **Vertical** (CPU and memory between min and max, as steps), on the primary's CPU (> 80 % for 10 minutes), memory pressure, or connections near `max_connections`:
  - the members restart one at a time with the new limits and tuned settings: `shared_buffers` 25 %, `effective_cache_size` 75 %, `work_mem` from connections, `max_connections` from memory;
  - replicas go first, then a switchover, then the old primary;
  - writes pause only for the switchover, about 2–5 s;
  - scaling down needs 6 hours under 30 %;
  - the scheduler must have room on the members' nodes; otherwise a member is moved by cloning a replica on another node and switching over to it.
- **Storage:** volume use is watched, with alerts at 80 % and 90 %. Node-local disks cannot grow online. A member short of room is replaced by a clone on a node with room, after a switchover if it is the primary.

**Metrics** (the controller queries every member, the same model as Valkey's probe, as `syncloud_pg_*`):
- TPS (commits and rollbacks), connections by state, cache hit ratio;
- replication lag in bytes and seconds, WAL rate, database sizes, deadlocks, temp bytes;
- the longest running transaction, checkpoint timing, and the age of the last WAL archive;
- top queries from `pg_stat_statements`.

**Dashboard:**
- **Wizard:** engine, owner, capacity (CPU and memory min/max, storage note, replicas min/max, analytics replica, synchronous), backups (S3 endpoint, schedule, retention), extensions, and network.
- **Database page:**
  - Overview: endpoints, the pooler, members with role, lag and timeline;
  - Connectivity: access list and public endpoint;
  - Metrics;
  - SQL console: read-only by default, with a write toggle that is audited and a row limit;
  - Schema browser: databases, schemas, tables with sizes and row estimates, indexes, a data preview;
  - Queries: top statements, running queries with cancel and terminate;
  - Users & databases; Extensions; Backups & restore; Autoscaling; Logs;
  - Settings: parameters, maintenance switchover, deletion.
- **Analytics:** pg_duckdb S3 secrets are created from a bound S3 endpoint, so `read_parquet('s3://…')` and `COPY … TO 's3://….parquet'` work from the SQL console and from apps.

**Public endpoint:**
- Traefik gets a `postgres` entrypoint on `:5432`, using its Postgres STARTTLS support and routing by `HostSNI` to the leader (or the pooler).
- The URL is `postgresql://app:…@<db>.db.<base>:5432/<db>?sslmode=require`.
- The read-only and analytics hosts work the same way.
- Inside the cluster, connections are plain over the private network, as for Valkey.

**API / CLI / IAM:**
- **Resource API:** under `/databases/{name}`, engine-specific parts return 400 for other engines:
  - `users`, `databases`, `extensions`, `parameters`, `backups` (list, create, `restore` → a new database), `switchover`;
  - `sql` (console), `schema`, `queries` (with cancel);
  - `metrics`, `events`.
- **CLI:** `synctl db …` gains `psql`-style `sql`, `users`, `backups`, `restore`, `switchover` and `extensions`.
- **IAM:** actions stay in the `database:*` namespace.

**PostgreSQL 18 (user request, 2026-10-08):**
- **Image:** one Dockerfile per major version (`PG_MAJOR` build argument), pinned as `ImagePostgres17` and `ImagePostgres18` in the manifest. Every component ships for 18: pg_duckdb 1.1.1, pgvector, partman, cron, hypopg, PostGIS 3.6, TimescaleDB 2.30 (Apache), Patroni 4.1.5 and WAL-G 3.0.9.
- **Versions:** the engine offers `18` (default) and `17`. Members use the image and `bin_dir` of the database's version.
- **Restores:** a restore always uses the source's major version.
- **Major upgrades (17 → 18):** not in this step. A later step will use `pg_upgrade` in a clone, or logical replication into a new cluster.
- **Nodes outside the mesh:** etcd and Patroni members address each other by discovery name, and a node outside the mesh (single-node dev) has no discovery DNS. `TaskSpec.network_aliases` gives each container its discovery name as a Docker network alias, so the names resolve on one node through Docker's own DNS. Fixes "waiting for the platform etcd".

**13c2. Configuration, optional extensions, replication (user request, 2026-10-08):**
- **Defaults:** a new cluster is plain PostgreSQL plus WAL-G. `pg_stat_statements` is the only preloaded library, because it ships with PostgreSQL.
- **Optional extensions:** `spec.postgres.extensions` lists the enabled add-ons: `timescaledb`, `pg_duckdb`, `pg_cron`, `vector`, `postgis`, `pg_partman` and `hypopg`.
  - Enabling one sets its preload library, if it has one. Only then can the explorer install it.
  - Disabling one is refused while it is installed in any database.
  - The contrib extensions that ship with PostgreSQL (`pg_trgm`, `pgcrypto`, …) stay installable at any time.
  - Clusters created before this slice have no `extensions` key. They keep every add-on enabled, so nothing changes under them.
- **Parameters:** `spec.postgres.parameters` is a map of curated settings. Each has a type (int, real, bool, enum, memory, time), a range, and a context (reload or restart). The controller validates them.
  - Memory-derived tuning is the default, and a user value wins.
  - Settings the platform owns are refused: archiving, `wal_level`, listening, hba, preload, `max_wal_senders`.
- **Replication:** `spec.postgres.replication` holds:
  - `mode`: `async`, `sync` or `strict`. `strict` means writes stop rather than run without a synchronous replica.
  - `syncReplicas`: `synchronous_node_count`.
  - `maxLagOnFailover` (MiB): a replica further behind than this is not promoted.
  - `failoverTtl` (s): the leader lease; `loop_wait` and `retry_timeout` follow from it.
  - `slots`, `hotStandbyFeedback`, `walKeepSize`, `maxSlotWalKeepSize`.
  - The old `synchronous: true` maps to `mode: sync`.
- **Applying to a running cluster:** settings are dynamic Patroni configuration, not container configuration, so changing them does not recreate members.
  - `bootstrap.dcs` is frozen at creation in `State.BootstrapDCS`, which keeps the spec hash stable.
  - The controller keeps the desired dynamic configuration (parameters, preload, replication) in sync through `PATCH /config` on the leader. Removed keys are sent as null. The applied hash is kept in `State.DCSHash`.
  - Reloadable settings apply at once.
  - For settings that need a restart, the controller restarts members flagged `pending_restart` one at a time: replicas first, then the leader. Each restart waits until the member streams again, and each is recorded as an event.
  - Vertical scaling also re-tunes memory this way. Before, the tuning was only applied at the first bootstrap.
- **UI:**
  - The wizard: replication mode, sync count and failover settings in Capacity; add-on toggles and a grouped parameter editor in Data.
  - A **Configuration** tab edits the same fields on a running cluster, plus replica count, memory, CPU and max connections, and shows which parameters have a restart pending.
  - A **Replication** tab shows `pg_stat_replication`: each replica's state, sync state, send/write/flush/replay lag and bytes behind, plus the replication slots and the timeline.
- **API / CLI:**
  - The spec fields go through the existing `PUT /databases/{name}`.
  - New endpoints: `GET /databases/{name}/pg/settings` (the catalog with live values and the add-ons) and `GET /databases/{name}/pg/replication`. The engine registry also carries the catalogs, so the wizard has them.
  - CLI: `synctl db settings [--changed]`, `db config set|unset NAME KEY[=VALUE]…`, `db addons NAME`, `db addon enable|disable NAME ADDON…`, `db replication NAME`, and `db replication set NAME --mode … --sync-replicas … --failover-ttl … --max-lag …`.
- **e2e:**
  - create a cluster with one extension and custom parameters, then check `SHOW`;
  - install a disabled extension (refused), enable it (rolling restart), then install it;
  - change a reload parameter (no restart) and a restart parameter (pending restart, then restarted, with events);
  - switch replication to sync and see `sync` in the Replication tab;
  - a single-node cluster outside the mesh runs etcd and Postgres (aliases).

**Slices:**
- **13a.** Image and `make postgres-image`; the platform etcd; the Patroni operator (create, replicas, failover and switchover, rw/ro endpoints, members on distinct nodes); `engine: postgres` available; credentials and URLs; e2e:
  - a 3-node cluster;
  - writes through rw, reads on ro;
  - a frozen primary fails over and writes resume;
  - the old primary rejoins;
  - failover still works with the controller stopped.
- **13b.** Explorer and administration (above): databases, roles, schema browser, privileges and presets, extensions, SQL console, sessions; e2e: create a role, grant read-only on a database, connect as it (SELECT works, INSERT refused), revoke, alter it, and drop it with reassign; browse schema, columns and DDL; run a console query as `app`; refuse protected roles and SUPERUSER.
- **13b2.** PgBouncer; public STARTTLS endpoint; parameters.
- **13c.** WAL-G archiving and scheduled backups to S3 (MinIO in e2e), the backups UI, PITR restore and clone into a new cluster. User request (2026-10-07): WAL-G with S3 configuration and point-in-time recovery is required, so 13c comes right after 13b, before 13b2.
- **13d.** Metrics and the dashboard charts; read-replica and vertical autoscaling; the analytics replica; storage alerts.
- **13e.** pg_duckdb S3 integration and warehouse UX (Parquet read/export, analytics endpoint); TimescaleDB, pgvector and partman checks in e2e; docs.

**Progress**
- ✅ 13a (2026-10-07):
  - **Image:** `ghcr.io/syncloud/postgres:17-r1` (`images/postgres`, `make postgres-image`, a CI job). It is built on `pgduckdb/pgduckdb:17-v1.1.1` and adds pgvector, TimescaleDB (Apache), PostGIS, pg_partman, pg_cron, hypopg, Patroni 4.1.5, PgBouncer, WAL-G v3.0.9 and etcd v3.7.2. The entrypoint roles are `patroni`, `etcd` and `pgbouncer`.
  - **Platform etcd:** `etcd_members` (migration 00034).
    - Bootstraps 3 members once 3 nodes are ready (otherwise 1), grows one at a time, and replaces a member lost for 10 minutes.
    - Root auth uses a sealed password. Each database gets an etcd user whose role covers `/syncloud/pg/<id>/`.
    - A synthesized security group allows only members and Postgres members to reach port 2379.
  - **Operator:**
    - Member configuration: Patroni config from the spec (memory-derived tuning, preload libraries, pg_hba, `use_pg_rewind`, slots).
    - The member start waits until the database's etcd user exists.
    - Probing: the controller probes `GET /patroni`, moves the VIPs to the leader and records failovers.
    - First boot creates the `app` role (granted `pg_monitor`) and database. App users are never superusers.
    - Switchover goes through Patroni's REST API. Rolling restarts switch over before restarting the leader.
  - **API, CLI and UI:**
    - Credentials include `database` and an `haUrl` (multi-host, `target_session_attrs=read-write`) that keeps working while the controller is down.
    - The wizard has PostgreSQL capacity steps (synchronous replication) and data steps (extension list).
    - The database page shows Leader, Replicas, Lag and Size tiles. Valkey-only tabs and columns are hidden.
  - **Verified:** `test/e2e/postgres.sh` (3 DinD nodes): writes on rw, reads on ro, ro refuses writes, `app` is not a superuser, extensions are preloaded, a frozen leader fails over and the old one rejoins, API switchover, and failover with the controller stopped through the HA URL; all rows are kept.
  - **Fixes found by the e2e** (both engines):
    - Removing a member on a node that cannot be told leaked its volume. Removals now record the volume in `database_orphans` (migration 00035), and the node removes it when it reports in.
    - After a controller restart, a member whose node had not reconnected yet looked changed, because its resolver was missing from the spec, and it was restarted. Spec-change restarts now wait for the member's node to connect.
  - **Docs:** a PostgreSQL section in docs/databases.md.
- ✅ 13b (2026-10-07):
  - **Explorer and administration:**
    - Backend: `internal/dbs/pgadmin.go`, `pgschema.go`, `pgprivs.go` and `pgquery.go`.
    - API: `internal/api/pgexplorer.go`, 21 operations under `/databases/{name}/pg/`.
    - CLI: `synctl db sql|databases|database|roles|role|grant|revoke|privileges|schema|describe|rows|extensions|extension|sessions|session`.
    - Dashboard: the Databases, Roles, Explorer, Console and Sessions tabs, plus full-page role forms.
  - **Safety:**
    - The console signs in as `app`. Read-only runs use one read-only transaction per statement over the extended protocol.
    - Passwords are sent as SCRAM verifiers.
    - Platform roles and databases are protected.
    - `app` gets CREATEDB, CREATEROLE and `pg_monitor`, plus SET (not inherit) on the roles created through the API.
  - **Verified:** `test/e2e/postgres-admin.sh`, plus UI flows in a browser.
  - **Fix:** roles with `VALID UNTIL 'infinity'` broke the role list. The query now maps infinity to NULL.
- ✅ 13c (2026-10-08):
  - **Configuration:** `spec.postgres.backup` names a registered S3 endpoint and bucket. Members get WAL-G's settings in their environment. `archive_command` is `wal-g wal-push %p` with a 60 s `archive_timeout`, and replicas set `restore_command` and clone from WAL-G with a fallback to `pg_basebackup`. Each cluster has its own libsodium key.
  - **Base backups:** one-shot `dbk_` tasks. They run as a sidecar that joins the member's network namespace (a new agent network mode, `task:<id>`) and reads its volume read-only over the Unix socket in `/data/run`. Runs are recorded in `database_backups` (migration 00036), and retention runs after each backup.
  - **Status:** read from S3 (the backup sentinels and the newest WAL) and from `pg_stat_archiver`.
  - **Restore:** `restore: {from, backup?, targetTime?}` on create. The new cluster bootstraps with Patroni's custom method (`walg-bootstrap`, `walg-restore-wal`, image `17-r2`) and keeps the source's database name and passwords.
  - **API, CLI and dashboard:**
    - API: `GET/POST /databases/{name}/backups`.
    - CLI: `synctl db backups`, `db backup now|config`, `db restore`.
    - Dashboard: a Backups tab, a full-page Restore form, and backups in the wizard.
  - **Verified:** `test/e2e/postgres-backup.sh`:
    - a scheduled backup; continuous archiving;
    - a restore to a noted moment holds exactly the earlier rows, and the source is untouched;
    - a manual backup, one at a time;
    - a WAL-G replica clone; a latest-state clone through the CLI.
  - **Found by the e2e:**
    - The backup sidecar could not reach S3 from its own address, which is why it joins the member's namespace.
    - A standalone restore cannot reach an in-cluster S3 service that only admits its own environment (documented).
  - **Note:** MinIO images are no longer freely pullable, so the backup e2e uses VersityGW. `test/e2e/storage.sh` still uses MinIO and needs the same change.
- ✅ PostgreSQL 18 and network aliases (2026-10-08):
  - **Images:** `images/postgres` builds per major version (`PG_MAJOR`): `ghcr.io/syncloud/postgres:18-r1` and `17-r2`. `make postgres-image` builds both; `postgres-image-18` builds one.
  - **Versions:** `system.PostgresImages` maps each version to its image. `--postgres-image 18=img,17=img` overrides them. The engine offers 18 (the default) and 17; `bin_dir` and the backup sidecar's image follow the database's version, and the platform etcd runs from the default version's image.
  - **Restores:** a restore keeps the source's version, and asking for another one is refused.
  - **Aliases:** `TaskSpec.network_aliases` (proto field 22). The agent sets them through `NetworkingConfig`. etcd and Patroni members carry their discovery names, which fixes "waiting for the platform etcd" on a node outside the mesh; checked in DinD with no discovery DNS.
  - **e2e:** `PG_VERSIONS` (default `18`) picks the images the nodes load. The backup e2e passes on 18 (server version check, restore version refusal). The CLI stage takes the controller URL from the environment, because `db backup config --endpoint` is the S3 endpoint.
- ✅ 13c2: configuration, optional extensions and replication (2026-10-08):
  - **Backend (`internal/dbs/pgconfig.go`):**
    - The add-on catalog, the parameter catalog (53 entries, with typed validation of units and ranges), and `PgReplicationSpec`.
    - `pgDynamicConfig`, applied with `syncPgConfig` (`PATCH /config`; removed keys sent as null). Restarts for pending settings go one at a time, replicas first, through `POST /restart` with `restart_pending`.
    - `bootstrap.dcs` is frozen in `State.BootstrapDCS`, so configuration changes never recreate members (checked in the e2e).
    - Explorer installs check that the add-on is enabled and its library loaded.
  - **API:** `GET …/pg/settings` and `GET …/pg/replication`. The engine registry carries the catalogs.
  - **CLI:** `db settings`, `db config set|unset`, `db addons`, `db addon enable|disable`, `db replication [set]`.
  - **Web:**
    - The wizard: replication fields in Capacity; add-ons and parameters in Data.
    - A Configuration tab: add-ons, size, replicas, replication, max connections, and grouped parameters with live values and restart-pending marks.
    - A Replication tab: replicas with lag, Patroni's members, and slots.
  - **etcd rollout fix:** a changed etcd member spec (image, aliases, DNS) now rolls out one member at a time, once the others have settled for 15 s. Before, existing members never got a new spec, so the alias fix never reached a dev node's etcd and it stayed in "waiting for the platform etcd". Verified on 3 nodes (members updated 20–25 s apart, the database stayed healthy).
  - **e2e:**
    - `postgres-config.sh` passes on 18:
      - a disabled add-on is refused;
      - enabling one restarts the replica, then the leader;
      - removing an installed add-on is refused;
      - a reload parameter applies without restarts and a restart parameter restarts the members;
      - sync mode gives a synchronous replica;
      - no container is recreated;
      - the CLI works.
    - `postgres-single.sh`: one node with `CTL_NETWORK=off` (lib.sh now takes `WORKERS` and `CTL_NETWORK`).
  - **Not in this step:** a leader with pending settings restarts in place (a few seconds without writes) rather than switching over first.
- ⬜ 13b2, 13d, 13e.

### Phase 14: UI/UX — dialogs, lazy loading, overflow (user request, 2026-10-08)

**Dialogs:** no native `confirm()`, `alert()` or `prompt()` anywhere.
- `web/src/ui/dialogs.tsx` provides promise-based `confirmDialog({title, message, confirmLabel, tone, typeToConfirm})` and `alertDialog({title, message, tone})`, rendered by one `<DialogHost/>` at the app root. It uses the existing `<dialog>` component: focus trap, Esc to cancel, Enter to confirm.
- Destructive actions use the danger tone. Deleting a database (or anything that names the data) requires typing its name.
- All 42 `confirm()` sites and the one `prompt()` site move to it. A lint check (a unit test that greps `src/`) keeps native dialogs out.

**Lazy loading:**
- **Client side:** `DataTable` renders 50 rows, then 50 more each time a sentinel row scrolls into view (IntersectionObserver), with a "Show more (N left)" button as a fallback. Every table in the app gets this.
- **Server side:** history endpoints take `limit` and `before` (the ID of the last item seen) and return `{items, next}`, where `next` is the cursor, or empty at the end.
  - The endpoints: database events, alert events, registry events, node-pool events, job runs, builds, deployments and audit (which already takes `before`).
  - The store compares `(created_at, rowid)` against the row the cursor names (integer IDs where the table has them), so pages are stable while new rows arrive.
  - The web uses `usePaged()` (`useInfiniteQuery`) and the table's footer loads the next page as it scrolls into view.
- **Logs:** `/logs` takes `before`. "Load older" fetches the previous window (the selected range) ending at the oldest line shown, so every query stays bounded. Live tail continues at the bottom, and the view keeps at most 5,000 lines.

**Overflow and responsiveness:**
- A Playwright sweep visits every route, both index and detail pages with seeded long names, at 390 px and 1440 px. It flags:
  - page-level horizontal scroll;
  - elements whose content overflows their box without being a scroll container.
- Fixes go into the shared components where possible:
  - `PageHeader` titles truncate;
  - detail rows wrap long values (`break-all` for IDs and URLs);
  - table cells cap width and truncate, with a title tooltip;
  - flex children get `min-w-0`;
  - code blocks scroll inside their box.
- The sweep runs again after the fixes.

**Progress:** ✅ 2026-10-08.
- **Dialogs:**
  - `ui/dialogs.tsx` provides `confirmDialog`, `alertDialog`, and `confirmAction`, which builds a dialog from one sentence: question, details, verb-based label and tone. It is rendered by `<DialogHost/>` in `main.tsx`.
  - All 42 `confirm()` sites and the `prompt()` site are converted.
  - Deleting a database requires typing its name.
  - `internal/web/nodialogs_test.go` fails on any native dialog in `web/src`.
- **Lazy loading:**
  - `ui/LoadMore` (IntersectionObserver plus a button).
  - `DataTable` renders 50 rows at a time and takes `hasMore`/`onLoadMore`.
  - `lib/paged.ts` provides `usePaged` (`useInfiniteQuery`; the refetch interval may depend on the loaded items).
  - `limit`/`before` and `{items, next}` on database, alert, registry and node-pool events, job runs, service and recent builds, and deployments. The cursors are integer IDs, or `(created_at, rowid)` comparisons against the row the cursor names (`TestCursorPages`). The OpenAPI spec documents them.
  - Logs: `before` on `/logs` (`Filter.Before` becomes a `_time:[start, end)` window). The log view's "Load older" button, and scrolling to the top, page back one range at a time, keep the reader's place, and report empty ranges. At most 5,000 lines are kept.
- **Overflow:**
  - The sweep (`scratchpad/pw/sweep.mjs`): crawl from the desktop nav (106 routes), then the same routes at 390 px. The page scroller (`main`) counts as the page.
  - It found the quotas header actions (page scroll on phones) and the service endpoint list (long URLs).
  - Fixed in shared components:
    - `PageHeader` actions and status wrap;
    - `Panel` header actions wrap (`min-h-8`);
    - detail lists use `minmax(0, 1fr)` with `break-all`;
    - tab rows fade at the right edge on narrow screens and keep the selected tab in view.
  - Also fixed the "1 tasks" plural.
  - The sweep afterwards: 0 issues at both widths.
  - Verified in the browser:
    - a confirmation dialog, with Cancel and Esc;
    - type-to-confirm (disabled until the exact name);
    - no native dialogs fired;
    - logs over a 5-minute range: 62 → 121 → 240 lines, with button and scroll loading.

- ✅ 14b (2026-10-08), requested after 14: borders close to the background, a small radius on inputs, 8.5:1 text contrast, no gradients:
  - New tokens: blended `line`/`line-strong`, `line-accent` for selected/focused borders and outlines, light text and status colors (`muted` 9.6:1, `faint` 8.7:1 on the hover surface), and `accent-fg` dark for text on light fills; primary buttons keep white text on a darker fill (9:1).
  - `index.css` gives every input, select and textarea the 4px radius, the dim focus border and the `faint` placeholder; raw selects share `border-line-strong`.
  - The tab fade mask and the Sankey link gradient are gone.
  - A Playwright contrast audit composites each text element's real background on all 104 routes at 1440 and 390 px: 0 below 8.5:1.

### Phase 15: Deployments, release commands, ports and domains, project settings (user request, 2026-10-08)

The request: full deployment history with rollback options; commands after the build and before the deployment (database migrations and similar); domain and port assignment for project services; better project settings.

**What exists, and the gaps** (reviewed 2026-10-08):
- **Deployments** (§5.4) record only `from_rev → to_rev`, status, failed tasks and a message.
  - They don't record who or what started them, the commit, what changed, the timeline of steps, or the hook runs.
  - History is per service only, and the table has no detail view.
- **Rollback** makes an old revision current as a new revision, and skips pre-deploy hooks, which is right for forward-only migrations.
  - Registry cleanup keeps the images of only the current and previous revisions, so rolling back further fails with a missing image.
  - There is no way to cancel a running deployment, and no redeploy of the same revision (a rolling restart).
- **Deploy hooks** (§5.11) work, but only as separate jobs on the Jobs page.
  - Several pre-deploy jobs run in parallel; migrations need a fixed order.
  - Post-deploy runs are not linked to their deployment.
  - There is no step after the build (tests or checks on the new image before it is deployed), and no build, install or start command overrides for Nixpacks.
- **Ports** are edited only in the raw task definition JSON.
  - Every HTTP port gets its generated address `<service>-<env>-<project>.<base>`, and it cannot be renamed or turned off.
  - Custom domains map a host to a port, with no path and no redirect.
  - TCP and UDP ports cannot be public; only databases have public TCP endpoints.

#### 15a: Deployment history and rollback

**Data:** migration `00037_deployment_details`.
- `deployments` gains:
  - `trigger`: `manual`, `git`, `rollback`, `auto-rollback`, `variables`, `redeploy`, `api`, `scale-spec`;
  - `actor`: a user ID, or `system`;
  - `build_id`, with the build's commit SHA, ref and message joined in views;
  - `image`;
  - `changes`: a JSON summary of the spec diff from `from_rev`. It covers the image, command, resources, ports, health check, placement, and the names of variables added, removed or changed. Values are never stored.
- New `deployment_events(id INTEGER PK, deployment_id, at, kind, message)` hold the timeline:
  - `started`, `hook-started`, `hook-succeeded`, `hook-failed`, `tasks-started` (n), `task-healthy`, `task-failed`, `drained` (n), `breaker-tripped`, `rolled-back`, `cancelled`, `succeeded`, `failed`, `superseded`.
  - The workload manager writes them where it already changes deployment state. They are kept as long as the deployment.
- `job_runs.deployment_id` is now set for post-deploy runs too.

**API:**
- `GET …/services/{name}/deployments` returns the enriched rows, cursor-paged as before.
- `GET …/services/{name}/deployments/{id}` returns the deployment with its events, hook runs, the change summary and the tasks of `to_rev`.
- `GET /projects/{p}/deployments?environment=&service=&status=&limit=&before=` returns every service's deployments in a project, newest first.
- `POST …/deployments/{id}/cancel` stops an in-progress deployment, or one waiting on its hooks. It returns to `from_rev` (status `cancelled`) and needs `service:RollbackService`.
- `POST …/rollback {revision, runHooks}`: `runHooks` (default false) runs pre-deploy hooks with the old revision, for down-migrations written as idempotent hooks. Rolling back to a revision whose private-registry image is gone fails with a clear message.
- `POST …/redeploy {runHooks}` performs a rolling restart.
  - It creates a revision identical to the current one apart from the platform-set `spec.redeployedAt`, which the spec editor hides, like `sharedEnv`.
  - Pre-deploy hooks run when `runHooks` is set (default true).

**Rollback window:**
- Registry cleanup keeps the images of each service's last **N revisions** (default 10).
- Each project sets N in its settings, between 1 and 50 (GC keeps 50 task definitions per service, so a larger window could not be rolled back to anyway).
- In the revisions list and the rollback dialog, an image cleanup has removed is marked "image removed", and that revision can't be selected.

**Web:**
- **Service › Deployments:**
  - Columns: status, revisions, trigger (an icon and a label), commit (short SHA and first line of the message, linked to the build), who, started and duration, and badges for pre- and post-deploy hooks.
  - A row opens the deployment page.
  - Row actions: "Roll back to this", for a succeeded deployment that isn't current, and "Cancel", while the deployment is in progress.
- **Deployment page** (`/projects/:p/:env/services/:name/deployments/:id`, full page):
  - a header with status and actions (Cancel, Roll back to before this, Redeploy);
  - the timeline;
  - "What changed", the diff of the two revisions with variables shown by name only;
  - hook runs with their logs, inline;
  - the tasks of the new revision;
  - logs from the deployment window.
- **Revisions** gains a "Compare with current" diff.
  - Its rollback dialog has a "Run pre-deploy hooks" checkbox, off by default, with a note on migrations.
  - It shows the rollback window.
- **Project › Deployments** (a new tab): the deployments of every service in the environment, with service and status filters, paged lazily.
- A "Redeploy" button in the service header.

**CLI:** `synctl deployments <svc>`, `synctl deployment <svc> <id>`, `synctl rollback <svc> [--revision N] [--run-hooks]`, `synctl redeploy <svc>`, `synctl deployment cancel <svc> <id>`.

**Progress 15a:** ✅ 2026-10-08.
- **Store:** migration `00037_deployment_details` adds `trigger`, `actor`, `build_id`, `image` and `changes` to deployments, a `deployment_events` table, and `projects.rollback_window`.
  - `ListProjectDeployments` filters by environment, service and status, cursor-paged.
  - Superseded deployments get a timeline event in the same transaction.
- **Workload:**
  - Apply, rollback and redeploy share one `rollout`.
  - The cause comes from the context (`WithCause`) or the actor (`build:<id>` is git, the circuit breaker is auto-rollback).
  - `Diff` summarizes the change; variables appear by name only, and health check, placement and rollout settings key by key.
  - The reconciler writes timeline events: tasks started, serving n/N, old tasks stopped, task failures, outcome.
  - `CancelDeployment`, `Redeploy` (the `redeployedAt` marker survives re-applies of the same spec), and `Rollback(…, runHooks)`, which refuses revisions whose image cleanup removed.
- **Jobs:** hook runs post their start and outcome on the timeline; post-deploy runs are linked to their deployment; hook retries keep their hook trigger; `CancelHooks` stops a cancelled deployment's runs.
- **Registry:** cleanup keeps the images of each service's latest `rollbackWindow` revisions; `ImageAvailable` checks with a manifest HEAD.
- **API, CLI and IAM:**
  - New endpoints: `GET …/deployments/{id}`, `POST …/deployments/{id}/cancel`, `POST …/redeploy`, `GET /projects/{p}/deployments` and `PUT /projects/{p}`; rollback takes `runHooks`.
  - Revisions carry `imageAvailable`.
  - synctl: `services deployment`, `cancel-deployment`, `redeploy [--skip-hooks]`, `rollback --run-hooks`, `projects deployments` and `projects update --rollback-window`.
  - The Deployer policy gains redeploy and cancel.
- **Web:**
  - `modules/compute/Deployments.tsx`: the history table (trigger, commit or user, change summary, hook badges, duration, cancel and roll-back actions) and the deployment page (timeline, what changed, hook runs with their logs, tasks of the revision).
  - The project's Deployments tab, with service and status filters.
  - A Redeploy button on the service.
  - Revisions with "image removed" and a "Compare" line diff.
  - Rollback and redeploy dialogs with a "Run pre-deploy jobs" checkbox (`confirmChoice`).
  - Project Settings › General holds the description and the rollback window.
- **Fixes found on the way:**
  - A spec change after a pre-deploy deployment that failed or was cancelled tried to reuse that unused revision number (UNIQUE error); revisions now follow the highest one.
  - A database whose backup S3 endpoint is unreachable hung in archive recovery: `restore_command` is now bounded (`timeout 30`), so members start from local WAL and streaming.
- **Tests:**
  - Unit: `TestDiff` and `TestDeploymentHistoryAndActions`.
  - e2e: the new `test/e2e/deployments.sh` and `deploy.sh` pass. `lib.sh` takes `E2E_PREFIX`, so they run beside a kept cluster, and `wait_mesh` counts the actual nodes.

#### 15b: Release commands and build settings

**Pre-deploy (release) commands:**
- **Sequential runs:** each service's pre-deploy jobs run one at a time, in an `order` field (default: creation order). The first failure stops the rest and aborts the deployment, and the old revision keeps running.
- **Revision and environment:** each run uses the new revision's image and variables (shared variables, S3, secrets), on the private network, so it can reach the environment's databases.
- **Timeline:** the deployment timeline shows each one.

**Post-deploy commands:**
- They run after the deployment succeeds, in order, and are linked to it.
- A failure raises an alert but changes nothing else.

**After-build checks:**
- A Git source has optional `postBuild` commands.
- After a build pushes its image, each one runs in that image as a job run (trigger `post-build`) with the target service's variables.
- The image is deployed only when all of them pass. Otherwise the build is `failed` with "after-build check failed: …", and the run's logs are linked.
- They are meant for tests and checks on the exact image. Migrations belong in pre-deploy, which runs once per deployment, also on rollbacks you opt into and on manual deploys.

**Build settings on the Git source:**
- `installCommand`, `buildCommand` and `startCommand`: Nixpacks' `--install-cmd`, `--build-cmd` and `--start-cmd`.
- `buildArgs`: Dockerfile `--build-arg`, a map whose values are stored encrypted.
- `buildEnv`: passed to Nixpacks with `--env`.

**Service › Deploy (a new tab):**
- **Release commands:** ordered lists of pre-deploy and post-deploy commands, each with timeout and retries. They are edited inline and stored as the service's hook jobs (`<service>-pre-1`, …), with no trip to the Jobs page. Jobs made elsewhere show up here too.
- **After-build checks and build commands**, when the service has a Git source.
- **Rollout:** circuit breaker, automatic rollback, drain seconds, and the rollback window.

**New service wizard:** the Deploy step gains an optional "Release command", for example `npm run migrate`.

**Progress 15b:** ✅ 2026-10-08.
- **Hooks:**
  - `advanceHooks` replaces the in-memory hook sets. From the store, it starts a deployment's first hook job without a run, waits on a running one, and ends the pre-deploy phase when all have passed or one has failed. A controller restart picks up where it stopped.
  - Jobs sort by the new `order` field, then by age.
  - Post-deploy jobs run the same way: a failure stops the rest and leaves the deployment as it is.
  - A hook retry that cannot start fails the hook.
  - A job's `entrypoint` replaces the image's ENTRYPOINT; release commands use `["sh","-c"]`.
- **First deployment:**
  - Creating a service with `releaseCommands` (`preDeploy` and `postDeploy` lists of `{command, timeout, retries}`) creates the jobs `<service>-pre-N` and `<service>-post-N`.
  - For an image service, the first deployment is held: `services.held`, migration `00038_release_commands`. The reconciler runs no task until the pre-deploy jobs pass.
  - A failure leaves the service stopped, with "the first pre-deploy jobs did not pass: fix them, then redeploy".
  - A Git service waits for its first build anyway, and that build's deployment runs the jobs.
- **Builds:**
  - Git sources have build settings: Nixpacks install, build and start overrides (passed as `NIXPACKS_*_CMD`), build variables (encrypted; passed to Nixpacks as `--env` and to the Dockerfile as `--opt build-arg:`; only their names are returned), and after-build checks.
  - API: `GET/PUT …/git/build-settings`. In the PUT, a variable set to null keeps its value.
  - synctl: `builds settings` and `builds configure` (`--install-cmd`, `--check`, `--var`, `--unset-var`, `--no-checks`).
  - A build with checks becomes `checking`. One `post-build` job run executes the checks in order (`set -e`) in the new image, with the service's variables and network. When they pass, the image is deployed; when one fails, the build fails with "after-build check failed (exit N)" and nothing is deployed.
- **Web:**
  - Service › Deploy holds:
    - release commands: ordered pre- and post-deploy lists with timeout and retries, reorder and remove, and each job's last run;
    - the build panel: checks, Nixpacks commands, and build variables whose values are write-only;
    - rollout settings: circuit breaker, automatic rollback and drain time.
  - The new-service wizard has a "Release command" field and shows it on the Review step.
  - Builds show the `checking` status and the check run's log.
- **Tests:**
  - Unit: `TestBuildSettings` (variables are never stored or returned in the clear; null keeps a value) and `TestHookJobName`.
  - e2e:
    - `deployments.sh` adds hooks running in order, one at a time; a failing hook stopping the rest; and release commands holding a new service, including a failing one that is fixed and redeployed.
    - `builds.sh` adds a build variable reaching a Dockerfile `ARG`; checks passing with the service's variables; a failing check blocking the deploy.
    - `deploy.sh` passes. `builds.sh` now takes `E2E_PREFIX` too.

#### 15c: Ports and domains

**Ports** are edited in a form on a new Service › Networking tab: name, container port and protocol, plus the health check. Saving creates a new revision.

**Routing settings** live on the service, not the revision, so changing them does not redeploy. New `service_routing(service_id, port_name, generated BOOL DEFAULT 1, label TEXT)`:
- `generated` turns the generated address off. A service with custom domains only is then reachable just at those.
- `label` replaces the generated name: `api` serves `api.<base>`.
  - Labels are unique across the cluster.
  - A label can't be one of the platform's reserved names (`registry`, `git`, `db`, …).

**Custom domains** gain:
- `path`: a path prefix, routed as `Host && PathPrefix`, with the longest prefix winning. Two services can then share a host, for example `/api` and `/`.
- `strip_prefix`;
- `redirect_to`: the domain answers with a 301 to another host, which covers www to apex.

**Public TCP and UDP ports:**
- **Port assignment:** a public port is assigned from a range on the controller and edge nodes (default 20000–20999, set in Settings › Network). It is shown as `<base>:<port>`, with an allow-list of CIDRs as for databases.
- **Traefik changes:** Traefik entrypoints are static, so assigning or releasing a port changes the Traefik system task's spec and re-sends the edge replicas. They restart together; the dashboard says so before a port is opened or closed. Changing an allow-list changes only firewall rules, so no replica restarts.
- **Port choice:** a port is chosen when it is made public and kept until it is released.

**Project › Domains** (a new tab) lists every address in the project for the environment: generated, custom and TCP. Each row shows the service and port, DNS status and certificate status, with "Add domain" pointing at any service and port.

**Progress 15c:** ✅ 2026-10-08.
- **Store:** migration `00039_routing` adds the `service_routing` table (`generated`, unique `label`, unique `public_port`, `allow`). It rebuilds `domains` with `path`, `strip_prefix`, `redirect_to` and `UNIQUE(host, path)`.
- **Workload:**
  - `Routing` and `SetRouting`. Labels follow DNS-label rules, cannot be reserved names (`registry`, `git`, `db`, `www`), and cannot collide with another service's generated address or a custom domain.
  - Public ports are the lowest free in `--public-ports` (default 20000–20999). They are released on close and when the service is deleted. `OnPublicPorts` fires when the set or an allow-list changes.
  - `Routes` honours labels and the generated-address switch.
  - Custom domains carry their path, prefix stripping and redirect. Removing by host removes every path of it; removing by ID removes one.
  - `PublicRoutes` sends traffic to serving tasks.
- **Traefik:**
  - HTTP routes get `Host && PathPrefix` (the longest rule wins), a `stripPrefix` middleware, and a permanent `redirectRegex` that keeps the path.
  - Plain TCP routers use `HostSNI(*)` without TLS, and a new UDP section carries UDP routes. Both are served without a base domain too.
  - Entrypoints are `tcp-N` and `udp-N` on the controller's system task and the edge replicas.
  - The host firewall opens public ports (TCP or UDP) on the controller and edge nodes, with the allow-list as sources (`mesh.PublicPort`).
- **API, CLI, web:**
  - API: `GET/PUT …/routing`, `GET /projects/{p}/addresses` (with DNS and certificate state), and domain `path`, `stripPrefix` and `redirectTo`. Domain IDs are accepted on delete.
  - CLI: `synctl services routing`, `services expose` (`--public`, `--private`, `--allow`, `--label`, `--no-generated`), `services domains add --path --strip-prefix --redirect-to`, and `projects addresses`.
  - Service › Networking holds the addresses (generated, label, public port, allowed clients), custom domains (route with a path and prefix stripping, or redirect), and the ports and health check form, which saves a revision. The service's endpoints include path domains and `tcp://` and `udp://` addresses.
  - Project › Domains lists every address with DNS and certificate state, and can add a domain for any service.
- **Fixes found on the way:** after a controller restart the system Traefik spec was rendered before the workload manager existed, so Traefik restarted without its public entrypoints. It is now rendered again before agents connect, and restarts no longer touch Traefik (verified on the test cluster).
- **Tests:**
  - Unit: `TestRoutingAndDomains` and `TestServiceRoutesPathsRedirectsAndPublicPorts`.
  - e2e: the new `test/e2e/ports.sh` (labels, the generated switch, shared host by path, redirects, a public TCP port carrying traffic, a UDP route, allow-lists, firewall rules, release on close and delete, synctl) and `services.sh` pass.
  - On the test cluster, the system Traefik restarted once with `--entrypoints.tcp-20000.address=:20000` and served the port.

#### 15d: Project settings

The Settings tab is split into sections:
- **General:** the description, and the project's ID and creation date.
- **Environments:**
  - Add one.
  - Clone one from another: services, with their latest specs, scaled to 0 unless "start services" is set; shared variables; jobs; and security groups. Domains and public ports are not cloned.
  - Delete one: you type its name, and its services must be deleted first, or "delete everything" is checked.
- **Deploy policy** for each environment:
  - **auto-deploy from Git:** when this is off, builds still run but deploying is manual;
  - **a deploy lock**, with a reason: it blocks deployments, except rollbacks and cancels, until it is lifted. API callers and `synctl` get `423 Locked` with the reason.
- **Rollback window** (15a).
- **Node limits**, which already exist.
- **Danger zone:** delete the project, after typing its name; only an empty project can be deleted, unless "delete everything" is checked.

**Progress 15d:** ✅ 2026-10-08.
- **Store:** migration `00040_environment_policy` adds `auto_deploy`, the lock (`lock_reason`, `locked_by`, `locked_at`) and `deleting` to environments, and `deleting` to projects.
- **Fix:**
  - Before: deleting an environment or project checked only for services. Their databases' rows cascaded away and left the members running.
  - Now both refuse while databases exist, and name them.
- **Deploy lock:**
  - `ErrLocked` → HTTP 423 `locked`, with the reason. It blocks:
    - creating or updating a service, and redeploying;
    - changing shared variables, checked before they are stored;
    - S3 bindings;
    - build deploys. A build is still recorded, with "not deployed: deploys to production are locked (…)".
  - Rollbacks, cancels and scaling still work.
  - The auto-deploy switch per environment: when it is off, builds run but are deployed by hand.
- **Clone:** `POST …/environments {cloneFrom, startServices}` copies:
  - the shared variables;
  - every service's current spec (at 0 tasks unless started);
  - the jobs;
  - security group memberships.

  Domains, public ports, Git sources, S3 bindings and databases are not copied, and the UI says so.
- **Delete everything:**
  - `DELETE …?force=true` on an environment or project answers 202. It marks the environment, or the project and its environments, `deleting`, removes their jobs, and deletes their services.
  - When the reconciler removes a service's last row, `finishDeletions` deletes the environment and then the project.
  - New services cannot be created in a deleting environment.
- **API and CLI:**
  - API: `PUT …/environments/{env}/policy`.
  - synctl: `envs lock --reason`, `envs unlock`, `envs auto-deploy on|off`, `envs create --from [--start]`, `envs delete --everything`, `projects delete --everything`; `envs list` shows the policy.
- **Web:**
  - **Settings:** General (description, rollback window, ID, created); Environments, each with lock and unlock (with a reason), the "builds deploy themselves" switch and delete (type the name, with an option to delete the services too); Add or copy an environment; Allowed nodes; Danger zone (delete the project the same way).
  - **Lock banner:** a locked environment shows it on its project and service pages, and Redeploy is disabled with the reason.
- **Tests:**
  - Unit: `TestDeployLockCloneAndDeleteEverything`.
  - e2e: the new `test/e2e/settings.sh`.

**Order:** 15a, then 15b, 15c and 15d. Each slice follows the usual workflow: unit tests, a DinD e2e (`test/e2e/deployments.sh`, extended per slice), screenshots, then this plan and a commit.

### Phase 16: Shared entity components (user request, 2026-10-08)

**Problem:** tasks, nodes and services are drawn by hand on each page, so they look and behave differently:
- three task tables (Tasks, the node's Tasks tab, and the service and deployment tables), each with different columns;
- a node is shown as plain text on most pages (database members, platform components, edges, IPAM, Traefik replicas, updates), a link on some, and with status badges copied in three places;
- service state badges and cards are written separately on the project, projects and overview pages.

**Design:** one module per entity under `web/src/entities/`, and every page uses it. A new `ui/HoverCard` primitive supports them:
- **HoverCard:** opens after 300 ms on hover or keyboard focus, in a portal, flipping to stay inside the viewport. On touch it does not open, and the link just navigates. The card's content mounts only while it is open, so a table of links costs nothing.
- **`entities/nodes.tsx`:**
  - `NodeStatus`: the status, plus draining or no new tasks.
  - `NodeUsage`: CPU, memory and disk meters.
  - `NodeLink` (by name or ID): a status dot and the name, linking to the node, with a hover card showing the status, usage, task count, versions and last seen. An unknown name renders as text.
  - `NodeCard`: a card for grid views.
  - `NodesTable`: moved here, with a task count column.
- **`entities/tasks.tsx`:**
  - `TaskState`: the state (or "stopping"), the health, and the error or unreachable note.
  - `TaskDots`: one dot per wanted task (running, starting, missing), for cards and headers.
  - `TasksTable`: the only task table. Columns can be hidden (service, node). It has filter chips with counts (all, running, starting, problems) and a search over ID, service, node and IP. Rows link the node and service and keep the shell and restart actions. It replaces the node page's own table.
- **`entities/services.tsx`:**
  - `ServiceState`: the badge.
  - `ServiceLink`: the state dot and `project/env/name`, with a hover card showing the state, image, tasks, revision and address.
  - `ServiceCard`: moved from the project page, with `TaskDots`.
- **Live data without extra subscriptions:** links read the cached `nodes` and `services` queries (`useNodeIndex` and `useServiceIndex`, without stream listeners). The pages that already list them keep them live.
- **Adoption:**
  - **Tasks:** the Tasks page, the service, deployment and node pages.
  - **Nodes:** the nodes page (a table and card toggle, remembered per browser), the overview, node limits, database members, platform components, edges, IPAM, Traefik replicas and updates.
  - **Services:** the project, projects and overview pages, the service header, and task tables.

**Tests:**
- `tsc` and the build.
- The overflow and contrast sweeps on the changed routes at 390 and 1440 px.
- Playwright: hovering a node link shows the card, the task filters narrow the rows, and the nodes card view renders.

**Progress:** ✅ 2026-10-08.
- **New:** `ui/HoverCard`, `entities/nodes.tsx`, `entities/tasks.tsx`, `entities/services.tsx` and `entities/TaskDots.tsx`. `useNodeIndex`, `useServiceIndex` and `useTaskIndex` share the caches without stream listeners. `CONTROLLER_NODE` replaces three copies of `"ctl-0"`.
- **Removed:** `compute/NodesTable.tsx`, the old `TasksTable`, the node page's own task table, `ServiceCard` from the project page, and the hand-written state badges on the platform and database member tables.
- **Nodes:**
  - The nodes page has a table and cards toggle, remembered per browser.
  - The node table has a Tasks column (running, plus starting).
  - Node links with hover cards: tasks, database members, platform components, edges, IPAM (nodes, addresses, top talkers), Traefik replicas and agent updates.
- **Tasks:**
  - The table shortens task IDs (the full ID is in the tooltip) and has a Health column.
  - Filter chips appear only when the tasks differ in state, and the search only above 8 tasks.
  - The restart button spins while it works.
- **Services:**
  - The cards and the service header show `TaskDots`, and project cards show them for all their tasks.
  - Service links in task tables open a hover card.
- **Checks:**
  - `tsc` and the build pass.
  - The overflow and contrast sweep over 13 changed routes at 390 and 1440 px found nothing.
  - Playwright: the node and service hover cards open, the cards view renders, and there are no console errors beyond the pre-login 401.

### Phase 16b: Rich selection controls (user request, 2026-10-08)

**Problem:** choices are still bare form controls:
- the node selectors are rows of checkboxes;
- options with an explanation are radio buttons;
- lists of services, groups, users or channels are checkbox runs;
- on/off settings are loose checkboxes.

**Design:** selection primitives in `ui/choice.tsx`. A selected item is shown by a blended accent border and a tinted background, with a small check mark (not a checkbox), and the title turns accent:
- **`ChoiceCard`:** a selectable tile with a title, description, optional icon and extra content. It is a `button` with `role="radio"` or `role="checkbox"` and `aria-checked`.
- **`ChoiceCards`:** a single choice among a few explained options, laid out as a grid of cards. It is a radiogroup that arrow keys move through.
- **`ChipSelect`:** a multi-select of short items as toggle chips, with "all/none" when the list is long.
- **`Segmented`:** two to four short options in one control, replacing small selects.
- **`NodePicker`** (`entities/nodes.tsx`): node cards showing the status dot, the controller tag, status, CPU and memory bars, and the task count. Names that are not joined stay listed so they can be removed.

**Adoption:**
- `NodePicker`: service and project node limits, the database wizards (Valkey and PostgreSQL node choice), and firewall policy targets (with an "All nodes" card).
- `ChoiceCards`:
  - any node or only these nodes;
  - spread or binpack;
  - standalone or replicated databases, and persistence;
  - restore to a moment or to the latest state;
  - the new service's exposure (public, internal or worker) and build type;
  - the node pool role (worker or edge).
- `ChipSelect`: services attached to security groups and middlewares, IAM group membership, group members and role trust, alert channels, and PostgreSQL privileges.
- `Segmented`: the node pool role, domain action (route or redirect), alert comparison and severity, grant or revoke, the drop log direction, S3 addressing, and replica counts.
- `Toggle` for every standalone boolean (auto-deploy, enabled, path-style, MFA, CORS credentials, security headers, pool autoscale, prefix strip, start copied services, scale-in protection, dialog options and role admin).
- `internal/web/nodialogs_test.go` also fails on `type="radio"` in `web/src`: radios are always cards or segmented.

**Tests:** `tsc` and the build, the overflow and contrast sweep, and Playwright screenshots of the node picker, wizard options and chips.

**Progress:** ✅ 2026-10-08.
- **Primitives:** `ui/choice.tsx` has `ChoiceCard`, `ChoiceCards` (arrow keys move the choice), `ChipSelect` (all/none above 6 items), `Segmented`, `ChoiceField` and `selectable()`. `ChoiceField` exists because a `<label>` around several buttons forwards clicks to the first one. The selected style is a `line-accent` border, a `btn-primary/15` fill, an accent title and a round accent check mark.
- **NodePicker:** the service and project node limits, the Valkey and PostgreSQL wizards, and firewall targets (after an All/Some nodes card).
- **Cards:**
  - any or only these nodes; spread or pack;
  - the database engine; standalone or project; persistence; replication mode;
  - restore to a moment or the latest state;
  - the new service's source, builder and network (public, internal, worker);
  - the alert type; the middleware type; the Git provider and repository connection;
  - person or service account; PostgreSQL role membership (with an admin toggle under each chosen card);
  - the confirm-dialog option.
- **Chips:** IAM groups, members and role trust; alert channels; services attached to security groups and middlewares; PostgreSQL privileges.
- **Segmented:**
  - the node pool role; domain route or redirect; above or below; alert severity;
  - grant or revoke; the drop log direction; S3 path-style or virtual-hosted;
  - replica counts (0–5) and sync replicas.
- **Toggles:** every remaining boolean.
- **Checks:**
  - `TestNoBareChoiceInputs` keeps `type="checkbox"`/`"radio"` out of `web/src`, and none remain.
  - The overflow and contrast sweep on 15 form routes at 390 and 1440 px found nothing.
  - Playwright: picking a node card sets `aria-checked`, arrow keys move a card choice, and there are no page errors.

### Phase 16c: Side nav location and width (user request, 2026-10-08) — ✅ done 2026-10-08

- **Active entry from the matched route:** the active entry comes from the router's matched route pattern (`activeNav`), not URL prefixes. A nav page matches itself; a detail page matches its longest whole-segment prefix, in its own module first, then anywhere; failing that, its module's first page.
  - "/projects/quotas" no longer also lights Projects.
  - `/projects/…/new-database` lights Projects.
  - `/device` lights API & CLI.
- **TanStack's own marking:** TanStack `Link` marks `aria-current` with a loose prefix test, so nav links pass `activeOptions={exact}`. Exactly one entry is current on every route.
- **Groups:** navigating into a closed group opens it (it can be closed again), and the current entry scrolls into view. The group of the current page shows an accent icon.
- **Width:** parents and children are full-width rows. The group buttons used to shrink to their content.
- **Checks:** Playwright over 24 routes found one current entry each, the expected one. All entries are 211 px wide. A closed group reopens on navigation and can be closed again.

### Phase 17: Public GitHub repository, releases and documentation (user request, 2026-10-08)

**Decisions (user):**
- Repository: `github.com/ridoysheikh/syncloud`, public, Apache-2.0.
- Releases are **built locally** and published as GitHub release files; GitHub builds nothing.
- A release carries tar.gz bundles **and** raw binaries.
- The managed PostgreSQL images **ship inside the release**: no public registry.
- The first release is `v0.1.0`.

**Packaging:**
- **Go module:** `github.com/ridoysheikh/syncloud`, so `go install …/cmd/synctl@latest` and the Go SDK work. The proto `go_package`, the ldflags (version and commit) and the scripts follow.
- **`make release VERSION=X`** builds into `dist/`:
  - raw binaries: controller and agent for linux amd64/arm64, and synctl for linux, macOS and windows;
  - `syncloud_<v>_linux_<arch>.tar.gz`: controller, agent, synctl, install.sh, LICENSE and README;
  - `synctl_<v>_<os>_<arch>.tar.gz`;
  - `syncloud-postgres-<tag>-linux-amd64.tar.gz`;
  - `install.sh` and `SHA256SUMS`.
- **`scripts/release.sh X`** checks the tree, tag and CHANGELOG section, runs vet and test, builds, verifies the checksums and the version, tags, pushes the tag, and runs `gh release create` with the CHANGELOG notes. A version with a suffix is a prerelease. It also has `--dry-run`, `--draft` and `--no-images`.
- **CI:** gofmt, vet, tests and the web build only. The release workflow and the image-push job were removed.
- **Release source** (`internal/upgrade`): a base of `https://github.com/<owner>/<repo>` uses the release-asset layout:
  - the `stable` channel is resolved through the `releases/latest` redirect;
  - `beta` through the REST API, including prereleases;
  - `v` prefixes are optional.

  Mirrors and `file://` keep the plain layout. `FetchFile` streams a large file and checks it against `SHA256SUMS`. The defaults of `--release-url` and install.sh are the repository, and install.sh writes `SYNCLOUD_RELEASE_URL` to the env file.
- **PostgreSQL images in the release:**
  - `tools/imagepack` turns `docker save` (an OCI layout) into a layout with gzip layers (`registry.CompressLayout`). It's 1.2 GB down to 433–488 MB per tag.
  - The pins are now tags (`PostgresTag17/18`), and the default images are `@registry/syncloud-system/postgres:<tag>`.
  - `internal/sysimage.Seeder.Ensure`, called by the database manager before members and etcd start, loads an archive into the built-in registry (`Browser.PushLayout`), in the background. It takes the archive from `<downloads>/images/` if one was placed by hand (air-gapped hosts), else downloads it from the controller's own version's release. Meanwhile the database shows "waiting for the PostgreSQL image: …".
  - Database task specs resolve `@registry/` with node pull tokens and the registry CA, after the spec hash, as services do.
  - `syncloud-system` is a reserved project name. Its repositories can't get lifecycle policies or be deleted by hand.
  - `--postgres-image` still overrides (development, e2e: `syncloud-postgres:<tag>` from `make postgres-image`).
  - The images are linux/amd64 only. This host has no arm64 emulation, and adding it would change the host kernel's binfmt setup.

**Documentation:**
- **`README.md`:** what it is, install, first deploy, how it works, a docs table, status, contributing and the license.
- **`docs/README.md`:** a map routed by goal ("I want to…").
- **Short pages:**
  - `getting-started/`: requirements, install (options, bundle, mirror and offline), first app, add nodes, synctl;
  - `guides/`: services, deployments, release commands and jobs, Git builds, domains and ports, scaling, PostgreSQL (overview, administration, backups), Valkey, S3 storage, monitoring, access control, network security, integrations;
  - `operations/`: upgrades, backup and restore, nodes and pools, Traefik, troubleshooting, data retention, uninstall;
  - `reference/`: synctl, API, configuration, architecture;
  - `development/`: building, testing, releasing.
- The old long pages were split into these. Every command was checked against `synctl --help`, and a link and anchor check passes.
- **Repository files:** `LICENSE` (Apache-2.0), `NOTICE`, `CHANGELOG.md`, `CONTRIBUTING.md`, `SECURITY.md`, issue forms and a PR template.

**Checks:**
- **Unit tests:**
  - `TestGitHubSource`: redirect, prerelease channel, `v` prefixes, layout detection;
  - `TestPackPushAndSeed`: CompressLayout, PushLayout against a fake registry, Ensure from a placed archive, and the development-build refusal.
- **Full suite:** the Go suite and vet pass.
- **Local build:** `make release VERSION=0.1.0` built every file, and `synctl version` prints `0.1.0 (<commit>)`.
- **Test cluster:** with the new controller and placed archives, the real registry received the 18-r1 image. The `orders` database member and the platform etcd were recreated from `registry.<base>/syncloud-system/postgres:18-r1` and are healthy.
- **Before publishing:** gitleaks over the whole history found only test fixtures, and there were no large blobs.

### Phase 18: Public database ports with TLS and plain connections (user request, 2026-10-09) — ✅ done 2026-10-09

**Problem (found on a real install):**
- The public endpoint was TLS-only and routed by SNI on a shared port (Valkey `:6379`, PostgreSQL `:5432`). Clients that don't send SNI (`redis-cli` without `--sni`, libpq before 14) got Traefik's self-signed default certificate (`certificate verify failed`).
- Plain clients (`redis://`, `sslmode=disable`) got Traefik's HTTP error text (`Protocol error, got "H"`). A plain connection carries no host name, so a shared port can't tell databases apart.
- The user wants both databases to take TLS **and** plain connections.

**Design:**
- **A port per endpoint.** Turning the public endpoint on assigns two ports from `--public-db-ports` (default `21000-21999`, kept apart from the service range `--public-ports`; the controller refuses overlapping ranges):
  - `port`: read-write, to the primary;
  - `readPort`: read-only, to the replicas (the primary when there are none).
  - The ports are kept while the endpoint is on and freed when it's turned off. Clients can't choose them. Existing public databases get theirs when the controller starts.
- **Both kinds of connection on each port.** Each port is its own Traefik entrypoint (`db-<port>`) with two catch-all routers:
  - TLS (`HostSNI(*)`, TLS terminated by Traefik). PostgreSQL's STARTTLS (SSLRequest) is handled by Traefik.
  - plain (`HostSNI(*)` without TLS), unless the database's **Require TLS** is on (`public.requireTls`, off by default).
  - Both carry the IP allow-list.
- **Certificates without SNI.** Traefik's default certificate (the `default` TLS store) is the platform's base-domain certificate instead of Traefik's self-signed one.
  - A client that sends SNI gets the database's own certificate, as before.
  - One that doesn't gets a valid Let's Encrypt chain for the base domain. `redis-cli` and libpq's `sslmode=require` check the chain only (or nothing), so they connect.
  - Clients that check the host name send SNI anyway.
- **The shared SNI ports stay** (`<name>.db.<base>:6379/5432`, TLS-only), so existing URLs keep working.
- **Opening a port** adds an entrypoint, so Traefik restarts (as for a public service port). The firewall opens the port on the controller and edges with the allow-list as sources.
- **URLs:**
  - `publicUrl` / `publicReadUrl`: TLS on the dedicated ports (`rediss://…:<port>`, `postgresql://…:<port>/db?sslmode=require`);
  - `publicPlainUrl` / `publicPlainReadUrl`: plain (`redis://…`, `?sslmode=disable`), absent with Require TLS.
- **Dashboard and CLI:**
  - The Connect panel lists TLS and plain URLs and a `redis-cli` / `psql` command.
  - Connectivity has a **Require TLS** toggle and says that plain connections send the password unencrypted.
  - `synctl db network NAME --require-tls on|off`; `synctl db get/network` print the ports.

**Checks:**
- Unit tests for port assignment, freeing, the backfill and the Traefik routers.
- DinD e2e:
  - Valkey: `redis-cli` with plain, `--tls` without `--sni`, and `--tls --sni`;
  - PostgreSQL: `psql` with `sslmode=disable`, `require` and `verify-full`;
  - Require TLS refuses plain connections;
  - after a controller restart, everything still works.

**Progress:**
- ✅ Done 2026-10-09:
  - `store.DatabasePublic` gained `port`, `readPort` and `requireTls`.
  - `dbs.assignPorts`, under a lock, assigns and frees ports at creation, on network changes and at start (the backfill). `PublicPorts` feeds the entrypoints (`db-<port>`) and the firewall. `--public-db-ports` is refused when it overlaps `--public-ports`.
  - Traefik: catch-all TLS and plain routers per port. `tls.stores.default.defaultCertificate` is the base-domain certificate, redacted in the dashboard view.
  - **Found while testing:** libpq 17+ offers only the ALPN `postgresql`, and Traefik's defaults refused it (`SSL error: no application protocol`), on the shared port 5432 too. The default TLS options now list Traefik's protocols plus `postgresql`.
  - synctl: `--require-tls`, the ports in `db get/create/network`, and plain URLs in `db credentials`.
  - Dashboard: the Require TLS toggle, the ports in Endpoints, and the TLS and plain URLs plus a `redis-cli`/`psql` line in Connect.
  - Docs: Valkey, PostgreSQL, troubleshooting, configuration, requirements. CHANGELOG 0.1.2.
- **Checks:**
  - `TestPublicPortsAssignedAndFreed`: backfill, kept ports, stale ports cleared, a full range, reuse, Require TLS, no SNI routes without a base domain.
  - `TestDedicatedDatabasePortsAndDefaultCertificate`.
  - Test cluster:
    - the existing `orders` got 21000/21001 at start; a new `kv` got 21002/21003, and a port sent by the client was ignored;
    - Valkey: plain, `--tls` without SNI, `--tls --sni` and the read-only port answer, and without SNI the base-domain certificate is served;
    - PostgreSQL (psql 18): `disable`, `prefer`, `require`, `require` with `sslsni=0`, direct TLS and the read-only port work;
    - Require TLS refuses plain connections and drops the plain URLs; turning it off brings them back;
    - all of it after a controller restart.
  - The full Go suite, vet and the doc link check pass.

### Phase 18b: Replicas on a one-server cluster (user report, 2026-10-09) — ✅ done 2026-10-09

- **Problem:** data members avoided each other's nodes with no fallback. On one server a replica could never be placed (`cannot place data 1: no node fits`), so the database stayed degraded, and the autoscaler could add more replicas that couldn't be placed either.
- **Fix:** every member kind falls back to sharing a node when no other node fits, as Sentinels did. `View.SameNode` (the primary and replicas on one node) is shown as a note on the database page and in `synctl db get`. Moving co-located replicas once a node is added is left for later; recreating the replica (replicas to 0 and back) moves it.
- **Checks (test cluster):** a Valkey limited to `w1` with a minimum of 1 replica became healthy, with failover ready and `sameNode`. Killing the primary promoted the replica in about 9 s, and the old primary rejoined as a replica. A crashed process is restarted by Docker (`unless-stopped`).

### Later (v2+)
Preview environments, blue/green and canary through weighted Traefik routing, log archive to S3, connection tracking view, domain-based egress rules, OIDC SSO, cosign verification, a one-click templates marketplace (as in Coolify), and a cost view. Managed databases are a separate future track (§17). (Replicated volumes are dropped per D2.)

---

## 16. Storage: S3 (D2)

- **No shared volumes.** There are two storage kinds:
  1. **Node-local volumes**: Docker volumes on one node. A task using one is **pinned** to that node by the scheduler (an automatic placement constraint). This suits caches, scratch space and single-instance stateful services.
  2. **S3 object storage**: everything that must be shared, durable or portable.
- **S3 endpoints** (cluster-level): register any S3-compatible provider (AWS S3, Cloudflare R2, Wasabi, Backblaze B2, DigitalOcean Spaces, …) with its endpoint, region, access keys (encrypted) and path-style flag.
- **Self-hosted S3** is **not** a special subsystem. MinIO or Garage is deployed as a normal service (from a template), and can then be registered as an S3 endpoint just like an external one.
- **S3 bindings** (per project/env): attach a bucket (with an optional prefix) to a service. The agent injects `S3_ENDPOINT`, `S3_BUCKET`, `AWS_ACCESS_KEY_ID`, `AWS_SECRET_ACCESS_KEY` and `AWS_REGION` as secrets. Optionally, per-binding scoped keys can be issued where the provider supports it.
- **Platform consumers of S3**: controller backups (§13), the registry storage driver (optional, §5.9) and build cache export (optional).
- **UI**: endpoints list with a connectivity test, bucket browser (list/upload/download/delete with IAM checks), usage per bucket, and which services use each binding.
- IAM actions: `s3:ListEndpoints`, `s3:Bind`, `s3:BrowseBucket`, `s3:ManageEndpoint`.

---

## 17. Future Track: Managed Databases (deferred, D4)

> **Not in scope for this plan.** This is a large, separate update that will get its own plan later. It is noted here only so the current design does not block it.

Ideas to carry forward (the user will provide the full design):
- **Managed PostgreSQL**: WAL-G backups to S3, custom replication and custom autoscaling, and a full administration UI.
- **Managed Redis**: now Phase 12 (managed Valkey).

Things the current phases should keep in mind, without building them:
- Keep the scheduler able to take **anti-affinity** constraints (primary and replicas on different nodes).
- Keep **S3 endpoints** (§16) generic so they can be reused for database backups later.
- Keep the IAM action namespace open for new resource types (e.g. `postgres:*`, `redis:*`).
- Keep the controller modular so database operators can be added as new modules.

---

## 18. Open Questions (for you to decide)
1. ~~**sslip.io and certificate limits**~~: resolved 2026-10-06. Not on the PSL, but a raised shared Let's Encrypt limit, plus a fallback chain (§5.0.2).
2. **Edge nodes timing**: the design is edge-ready from day one (D17), and edge nodes are in Phase 8. Should they move earlier because public traffic must survive a controller outage from the start?
3. **Cloud providers for node autoscaling**: which providers first? (Suggestion: Hetzner Cloud and DigitalOcean, then AWS EC2 and Vultr.)
4. **Scale target**: the expected number of workers and containers (this affects metrics and log retention defaults and controller sizing).
5. **Log defaults**: default retention (7 days suggested) and per-node disk buffer (512 MB suggested)?
6. **Licensing / distribution**: open source, private, or commercial?
7. **Other DB engines** (MySQL/MariaDB, MongoDB): out of scope, or served as plain service templates?

Resolved: language and frontend (D1), volumes and storage (D2), the controller as a worker (D3), managed databases deferred (D4), SQLite for the platform's own state (D5), centralized logging in v1 (D12), the controller-managed firewall (D13), sslip.io default domains (D18), service VIPs (D21), private registry only (D19), the controller on the host with system tasks (D20).


---

## 19. Requirements Coverage Checklist

Every capability the platform must have, and where the plan covers it.

| Requirement | Covered by | Phase |
|---|---|---|
| Manage the Docker registry (ECR-style UI) | §5.9, §5.10 | 0, 4 |
| Manage containers (start/stop/restart/exec/inspect) | §5.2–5.4, §6.2 | 2 |
| Manage every server node (join, monitor, cordon/drain, failure handling) | §5.6, §6 | 1, 3 |
| Add and remove servers automatically | §6.5 | 8 |
| Projects with multiple services and environments | §4 | 2 |
| Scale services on request count (RPS/task, latency) | §5.5 + Traefik metrics §5.7 | 5 |
| Scale services on resource usage (CPU/memory) | §5.5 | 5 |
| Rolling deploys, health checks, auto-rollback | §5.4, §5.6 | 3 |
| One-off, scheduled and deploy-hook jobs | §5.11 | 3 |
| Every resource, network and metric in the controller UI | §8.4, §9, §10 | 1–6 |
| Logs from all nodes merged per service, searchable | §9.2 | 2 |
| Firewall for the whole cluster, managed from the controller | §8.3 | 1, 6 |
| Private networking between all servers (no public worker IPs) | §8 | 1 |
| Load balancing: public (Traefik) and service-to-service (VIPs) | §5.7, §8.6 | 1, 2 |
| Internal DNS and service discovery | §8.1 | 1 |
| Traefik routing, domains and TLS, simple and dynamic | §5.7, §8.5 | 2, 5 |
| Public traffic that survives a controller outage | §8.5 | 8 |
| Git connections, webhooks and polling to update apps | §5.8 | 4 |
| Service health and continuous app monitoring | §5.6 | 3 |
| Alerts and notifications | §9 | 5 |
| IAM: users, roles, policies, keys, tokens, audit | §7, §7.1 | 7 |
| Full control through the API and shell | §5.1, §7.1 | 0 onward |
| Quotas and usage per project | §7.2 | 7 |
| S3 object storage | §16 | 4–7 |
| Controller installation, upgrade, uninstall | §5.0, §5.0.1 | 0, 9 |
| Working HTTPS addresses with no DNS setup (sslip.io) | §5.0.2 | 0 |
| Backups and restore of the controller | §13 | 0, 9 |
| One single dark dashboard | §10, §10.1 | 0 onward |
| Managed PostgreSQL and Redis | §17 (deferred track) | — |
