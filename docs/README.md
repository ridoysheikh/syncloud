# SynCloud documentation

SynCloud runs containers across your own servers. A **controller** (API, dashboard, scheduler, SQLite state) manages **nodes**. Each node runs an **agent** that starts containers in Docker and joins a WireGuard private network. Traefik serves public traffic.

These pages are for the people who install and run a SynCloud cluster:

| Page | What it covers |
| --- | --- |
| [Install](install.md) | Requirements, installing the controller, joining nodes, the first sign-in |
| [Nodes and pools](nodes.md) | Joining, draining and removing nodes; node pages and shells; which nodes a project may use; node pools, cloud providers, cluster autoscaling, edge nodes |
| [Integrations and Traefik settings](integrations.md) | Connecting GitHub (one-click app), GitLab and Gitea; automatic webhooks and commit statuses; global Traefik options |
| [Managed databases](databases.md) | Standalone or project databases (Valkey now): internal access lists, public TLS endpoints, Sentinel failover, memory and replica autoscaling, the explorer and console |
| [S3 storage and metrics](storage.md) | S3 endpoints, the bucket browser, binding buckets to services, the metrics explorer |
| [Operations](operations.md) | Health, upgrades and rollback, agent upgrades, backups, restore and moving the controller, uninstall, data retention |
| [Testing](testing.md) | Unit, end-to-end, chaos and load tests |

The design and the reasons behind it are in [plan/PLAN.md](../plan/PLAN.md). The API reference is in the dashboard under **API & CLI** (`/developers`), and as OpenAPI at `/api/v1/openapi.json`.
