# Architecture

## The pieces

```
                   ┌──────────────────────── controller host ─────────────────────────┐
  browser, synctl ─┼─▶ Traefik :443 ─▶ controller :7070  (API, dashboard, scheduler,  │
                   │                     SQLite, reconcilers, IAM, ACME, backups)     │
                   │   system tasks: registry · BuildKit · VictoriaMetrics ·          │
                   │                 VictoriaLogs · Forgejo (optional)                │
                   │   agent "ctl-0"                                                  │
                   └───────────────────────────▲──────────────────────────────────────┘
                                               │ gRPC over mutual TLS :7443 (agents dial out)
                ┌──────────────────────────────┼──────────────────────────────┐
           ┌────┴────┐                    ┌────┴────┐                    ┌────┴────┐
           │ agent   │◀── WireGuard ─────▶│ agent   │◀── WireGuard ─────▶│ agent   │
           │ Docker  │    mesh :51820     │ Docker  │                    │ Docker  │
           │ nftables│                    │ nftables│                    │ nftables│
           └─────────┘                    └─────────┘                    └─────────┘
```

| Part | Does |
| --- | --- |
| **Controller** (one Go binary) | Holds the desired state in SQLite, schedules tasks, runs the reconcilers (services, deployments, databases, routes, certificates, firewall), serves the API and the dashboard (embedded), issues node certificates, and backs itself up to S3. |
| **Agent** (one Go binary per node) | Runs containers in Docker, reports their state, keeps the WireGuard mesh and the nftables firewall, serves internal DNS, and relays logs, metrics, shells and exec. |
| **Traefik** | Routes public traffic to healthy tasks over the mesh, terminates TLS, and runs middlewares. It polls its configuration from the controller every 2 seconds. [Edge nodes](../operations/nodes-and-pools.md#edge-nodes) run more replicas. |
| **System tasks** | Platform components the controller runs on its own node: the registry, BuildKit, VictoriaMetrics and VictoriaLogs. |
| **synctl** | The CLI: a signed API client. |

## Design choices

- **SQLite, not etcd.** One controller with WAL-mode SQLite is simple to run, back up and restore. A cluster of 50 nodes and 2,000 tasks fits on a 4 vCPU / 8 GB controller.
- **Tasks keep running without the controller.** Agents keep their containers, and edge Traefik replicas keep routing. PostgreSQL fails over through its own etcd. A dead controller stops changes, not traffic.
- **Agents dial out.** Nodes need no inbound port besides WireGuard, so nodes behind NAT work.
- **No Kubernetes.** Docker on each node, and the scheduler, health checks, rolling deploys, service VIPs (nftables) and DNS are SynCloud's own.
- **Encrypted at rest where it matters.** Secrets (Git tokens, S3 keys, database passwords, build variables) are sealed with a master key, which is wrapped by your recovery key in backups.
- **Self-contained releases.** Binaries, the installer and the managed PostgreSQL images are all files of a GitHub release. The cluster needs no outside registry of ours.

## Inside a node

- Each node has a `/24` of the private network on the `syncloud` Docker bridge.
- Services get a **virtual IP**, load-balanced by nftables on every node, with no central hop, and DNS names under `syncloud.internal`.
- **Security groups** are nftables rules on the bridge, enforced on the node where the receiving container runs.

## More

- The full design, with the reasoning behind each decision, is in [plan/PLAN.md](../../plan/PLAN.md).
- How the agent and the controller talk: `proto/syncloud/agent/v1/agent.proto`.
