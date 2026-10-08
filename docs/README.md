# SynCloud documentation

Each page answers one question and stays short. Find what you want to do below, and follow the link.

> **New here?** Read these four in order: [Requirements](getting-started/requirements.md) → [Install](getting-started/install.md) → [First app](getting-started/first-app.md) → [Add nodes](getting-started/add-nodes.md).

## I want to…

### Get started

| …do this | Read |
| --- | --- |
| check whether my servers are big enough | [Requirements](getting-started/requirements.md) |
| install the controller | [Install](getting-started/install.md) |
| deploy my first app | [First app](getting-started/first-app.md) |
| add more servers | [Add nodes](getting-started/add-nodes.md) |
| use the command line | [The synctl CLI](getting-started/synctl.md) |

### Build and run apps

| …do this | Read |
| --- | --- |
| understand projects, environments and services | [Services](guides/services.md) |
| set environment variables and secrets | [Services › Variables](guides/services.md#variables) |
| see what was deployed, roll back or lock deploys | [Deployments and rollbacks](guides/deployments.md) |
| run database migrations before each release | [Release commands and jobs](guides/release-commands.md) |
| run a cron job or a one-off command | [Release commands and jobs](guides/release-commands.md#jobs) |
| build and deploy from GitHub, GitLab or Gitea | [Git builds](guides/git-builds.md) |
| put my app on my own domain | [Domains and ports](guides/domains-and-ports.md) |
| expose a TCP or UDP port (game server, MQTT…) | [Domains and ports › Public ports](guides/domains-and-ports.md#public-tcp-and-udp-ports) |
| scale on CPU or traffic, or pin to some nodes | [Scaling and placement](guides/scaling.md) |

### Store data

| …do this | Read |
| --- | --- |
| create a PostgreSQL database | [PostgreSQL](guides/postgres.md) |
| back up and restore PostgreSQL to a point in time | [PostgreSQL backups](guides/postgres-backups.md) |
| manage roles, privileges and run SQL | [PostgreSQL administration](guides/postgres-admin.md) |
| create a Redis-compatible cache | [Valkey](guides/valkey.md) |
| give an app a bucket, or browse S3 | [S3 storage](guides/storage.md) |

### Observe and secure

| …do this | Read |
| --- | --- |
| see metrics and logs, and get alerted | [Monitoring and alerts](guides/monitoring.md) |
| give teammates or CI limited access | [Access control](guides/access-control.md) |
| control which containers may talk to each other | [Network security](guides/network-security.md) |
| add rate limits, passwords or CORS to a route | [Network security › Middlewares](guides/network-security.md#middlewares) |
| connect GitHub, GitLab, Gitea or the built-in Git server | [Integrations](guides/integrations.md) |

### Operate the cluster

| …do this | Read |
| --- | --- |
| upgrade SynCloud | [Upgrades](operations/upgrades.md) |
| back up the controller, or move it to a new server | [Backup and restore](operations/backup-restore.md) |
| drain, remove or group nodes; autoscale servers | [Nodes and pools](operations/nodes-and-pools.md) |
| tune Traefik (TLS, timeouts, proxies) | [Traefik settings](operations/traefik.md) |
| find out why something is broken | [Troubleshooting](operations/troubleshooting.md) |
| know how long history is kept | [Data retention](operations/data-retention.md) |
| remove SynCloud from a server | [Uninstall](operations/uninstall.md) |

### Look something up

| …find | Read |
| --- | --- |
| every synctl command | [synctl reference](reference/synctl.md) |
| the HTTP API and SDKs | [API](reference/api.md) |
| controller flags, environment variables, ports and files | [Configuration](reference/configuration.md) |
| how the pieces fit together | [Architecture](reference/architecture.md) |

### Work on SynCloud itself

| …do this | Read |
| --- | --- |
| build from source and run it locally | [Building](development/building.md) |
| run the unit, end-to-end and VM tests | [Testing](development/testing.md) |
| cut a release | [Releasing](development/releasing.md) |

---

The design notes and the reasons behind each decision are in [plan/PLAN.md](../plan/PLAN.md). Found something unclear or wrong? [Open an issue](https://github.com/ridoysheikh/syncloud/issues/new/choose).
