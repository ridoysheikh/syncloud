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
| D4 | **Managed PostgreSQL and Redis are deferred.** They are a separate, large future track and are **not part of this plan or its phases**. They are only noted for awareness (§17). | 2026-10-05 |
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
| TLS | **lego** (ACME library) inside the controller | Certificates are issued centrally and work across multiple Traefik replicas (D17) |
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
 ├── Certificate      issued and renewed by the controller (lego), served to all Traefik replicas (§8.5)
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
- **Certificate fallback chain** (automatic, per hostname):
  1. Let's Encrypt HTTP-01 for the `sslip.io` name.
  2. If rate-limited: the same name on **nip.io** (`api-production-shop.203-0-113-10.nip.io`), which resolves the same way. The route serves both names.
  3. If still failing: a second ACME CA (ZeroSSL) for the sslip.io name.
  4. For the dashboard only: a Let's Encrypt **IP-address certificate** for `https://203.0.113.10`.
  5. Last resort: self-signed, with a dashboard banner and automatic retries with backoff.
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
- **Certificates are issued by the controller** (lego/ACME), stored encrypted in SQLite and backed up, and delivered to Traefik through the HTTP provider's TLS section. Traefik never runs its own ACME. This is what makes multiple replicas possible.
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
- **Border radius is minor**: `rounded-sm` (2px) for inputs, buttons and badges, and `rounded` (4px) at most for cards and panels. There are no pill shapes except status dots.
- Dense tables: `text-xs`/`text-sm`, row height ~28–32px, sticky headers, monospace for IDs, digests and IPs.
- Thin 1px borders instead of heavy shadows to separate panels.
- These are defined once as Tailwind component classes or React primitives (`<Panel>`, `<StatTile>`, `<DataTable>`, `<PageHeader>`), so every module looks the same.

**Dark theme only**
- One dark palette, defined as Tailwind theme tokens (CSS variables): background, surface, surface-raised, border, text, text-muted, and accent. Status colors are healthy (green), degraded (amber), down (red), deploying (blue) and neutral (gray).
- There is no light mode or theme switcher, and no `dark:` variants. Dark is simply the design.

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
│   ├── certs/                # ACME (lego), certificate store, renewal
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

### Phase 0b: Install and system tasks (2 wks)
*Depends on a minimal agent (Docker runner + gRPC link), which is pulled forward from Phases 1–2.*

**Progress**
- ✅ Slice 1 (2026-10-06), agent link: internal CA, join tokens, `syncloud-agent join` (the node key is generated on the node; the controller signs its CSR), an mTLS gRPC stream (`proto/syncloud/agent/v1`), heartbeats with node metrics, Ready/Suspect/NotReady tracking (only transitions written to SQLite), revocation on node delete, a local `ctl-0` join token, `synctl nodes …`, and the dashboard Nodes page and overview.
  - Follow-up: node certificates are valid for 90 days; automatic renewal over the stream is still to do (before Phase 1 ends).
- ⏳ Slice 2: Docker runner and system tasks.
- ⏳ Slice 3: sslip.io base domain and certificates.
- ⏳ Slice 4: install script, recovery key, Litestream.
- Minimal agent: gRPC stream to the controller, Docker runner for system tasks.
- Controller **installation** (§5.0): preflight checks, signed binaries, systemd units, `init` / `doctor`, the local agent, and **system tasks** (Traefik with the UI as the first route, private registry, BuildKit, VictoriaMetrics, VictoriaLogs).
- **sslip.io base domain**, controller-managed certificates (lego) with the fallback chain (§5.0.2), served to Traefik; recovery key; Litestream backup wiring.

### Phase 1: Nodes, Networking and Host Firewall (3–4 wks)
- Agent binary, `join.sh`, join flow, mTLS CA.
- WireGuard mesh with IPAM, per-node container subnets, and cross-node container connectivity tests.
- Internal DNS with the agent-side forwarder (§8.1).
- Service VIP allocation and per-node IPVS programming (§8.6), tested before Phase 2 uses it.
- **Host firewall** with nftables: default policies, commit-confirm, drift detection (§8.3).
- Heartbeats, node list/detail UI, node metrics, mesh link stats, first charts.

### Phase 2: Running Containers and Logs (3–4 wks)
- TaskDefinition, Service, Task model; reconciler; scheduler (spread/binpack, constraints).
- Start, stop and restart; exec terminal.
- **Centralized logging** (§9.2): agent log shipper with disk buffer, log ingest, VictoriaLogs, logs explorer with the per-service merged view and live tail.
- Traefik HTTP provider: auto default domains, one-step custom domains plus DNS check, the Connect dialog, middleware presets (§5.7).
- Service UI with a YAML view.

### Phase 3: Deployments, Health and Jobs (3 wks)
- Two-layer health (agent plus controller), service and task health model, uptime monitors, incidents (§5.6).
- Rolling deploys, circuit breaker with rollback.
- Node failure, rescheduling, cordon and drain.
- **Jobs** (§5.11): one-off tasks, scheduled jobs, pre/post-deploy hooks.
- Deployment timeline UI and events.

### Phase 4: Git, Build and Registry (3 wks)
- ECR-style registry UI (§5.10): dashboard, repositories, images, push commands, lifecycle policies with preview, permissions, upstream credentials, registry tokens; registry event tracking; pre-pull before deploys.
- (v1.1) Trivy scanning and the deploy gate.
- GitHub App, GitLab, Gitea and generic Git; webhooks plus the polling scheduler (watch rules, path filters, SHA dedup, backoff) (§5.8).
- BuildKit/Nixpacks builds, build logs, auto-deploy.

### Phase 5: Autoscaling and Traffic Insights (2–3 wks)
- Traefik metrics and access-log ingestion; dynamic traffic dashboard: live traffic map, RPS, latency and errors per service and in total, request tail (§5.7).
- Target-tracking autoscaler (CPU, memory, RPS/task, latency); scaling history UI.
- Alerts with notification channels, including log-based alerts.

### Phase 6: Security Groups and Network Visibility (3 wks)
- **Security groups** and egress rules with nftables sets, default project isolation, preview and diff (§8.3).
- Rule hit counters, drop logs, effective rules viewer, reachability check.
- **Network section** of the dashboard: topology map, traffic per node/service/container, IPAM and DNS pages (§8.4).

### Phase 7: IAM, Multi-tenancy and Quotas (3 wks)
- Users, groups, roles, service accounts, JSON policies with conditions, policy evaluation middleware, MFA.
- Access keys (rotation, max 2), personal access tokens, STS role sessions, `synctl login` device flow.
- Cloud Shell, in-dashboard API docs, published Go and TS SDKs.
- Audit log UI and policy simulator.
- **Quotas** with admission checks, and **usage metering** (§7.2).

### Phase 8: Node Pools, Cluster Autoscaling and Edge Nodes (3–4 wks)
- Node pools (manual and provider-backed), provider plugins, cloud-init join (§6.5).
- Cluster autoscaler (scale out on pending tasks or headroom, safe scale in).
- **Edge nodes**: Traefik replicas fed by the controller, per-edge metrics, edge health (§8.5).

### Phase 9: Hardening (ongoing)
- Controller **upgrade with automatic rollback**, uninstall (§5.0.1), backups and restore drills (including sslip.io IP change), agent self-upgrade, GC, docs, chaos tests (kill nodes, kill controller, partition the mesh), and load tests (target: 50 nodes and 2,000 tasks on a 4 vCPU / 8 GB controller).

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
- **Managed Redis OSS**: autoscaling, multiple hosted plans, and a full administration UI.

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
