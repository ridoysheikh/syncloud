<h1 align="center">SynCloud</h1>

<p align="center">
  <b>Your own cloud on your own servers.</b><br>
  Deploy containers, databases and Git apps across a fleet of Linux machines, from one dashboard.
</p>

<p align="center">
  <a href="https://github.com/ridoysheikh/syncloud/releases/latest"><img alt="Release" src="https://img.shields.io/github/v/release/ridoysheikh/syncloud?sort=semver"></a>
  <a href="https://github.com/ridoysheikh/syncloud/actions/workflows/ci.yml"><img alt="CI" src="https://github.com/ridoysheikh/syncloud/actions/workflows/ci.yml/badge.svg"></a>
  <a href="LICENSE"><img alt="License: Apache-2.0" src="https://img.shields.io/badge/license-Apache--2.0-blue"></a>
</p>

---

SynCloud turns a handful of Linux servers into a small, self-hosted platform, somewhere between Heroku and AWS. You install one controller, join as many nodes as you like, and get:

- **Apps from an image or a Git repository**, with HTTPS addresses, custom domains, rolling deploys, rollbacks and migrations that run before each release.
- **Managed PostgreSQL and Valkey (Redis)** with replicas, automatic failover, autoscaling, backups and point-in-time restore.
- **A private network** between every node (WireGuard), with security groups between containers and a managed host firewall.
- **Metrics, logs, alerts and traffic insights** for every service, built in.
- **Teams and access control** with IAM users, groups, roles and policies, audited.
- **One binary per role.** The controller keeps its state in SQLite and backs itself up, encrypted, to S3. There is no etcd or Kubernetes to run.

## Install

On a fresh Ubuntu 22.04+ or Debian 12+ server with ports 80 and 443 open:

```sh
curl -fsSL https://github.com/ridoysheikh/syncloud/releases/latest/download/install.sh | sudo bash
```

When it finishes, it prints the dashboard URL, a one-time setup token and your recovery key. Open the URL, create the admin account, and you're ready to deploy.

The [installation guide](docs/getting-started/install.md) covers the options, such as your own domain, a specific version or an air-gapped host.

## Deploy something

In the dashboard, open **Projects → New project**, then **New service**. Alternatively, use the CLI:

```sh
synctl projects create shop
synctl services run web -p shop --image nginx:1.27 --port 80 --replicas 2
```

A minute later the service answers on its own HTTPS address. [Your first app](docs/getting-started/first-app.md) walks through images, Git builds, variables and domains.

## How it works

```
               ┌──────────────────────────── controller ───────────────────────────┐
  browser ───▶ │  dashboard + API · scheduler · SQLite · Traefik · registry ·      │
  synctl  ───▶ │  BuildKit · VictoriaMetrics · VictoriaLogs                        │
               └──────────────┬────────────────────────────────────────────────────┘
                              │ mTLS gRPC (agents dial out)
            ┌─────────────────┼─────────────────┐
         ┌──▼───┐          ┌──▼───┐          ┌──▼───┐
         │agent │──────────│agent │──────────│agent │   WireGuard mesh between nodes,
         │Docker│          │Docker│          │Docker│   host firewall on each node
         └──────┘          └──────┘          └──────┘
```

- **Controller:** holds the desired state, schedules tasks, and serves the dashboard and the API.
- **Agents:** run the containers in Docker on each node and report back.
- **Traefik:** routes public traffic to healthy tasks over the private network, with certificates from Let's Encrypt.

[Architecture](docs/reference/architecture.md) explains the design in more depth.

## Documentation

Start at the **[documentation map](docs/README.md)**: it routes you to the page you need, by task.

| | |
| --- | --- |
| **Get started** | [Requirements](docs/getting-started/requirements.md) · [Install](docs/getting-started/install.md) · [First app](docs/getting-started/first-app.md) · [Add nodes](docs/getting-started/add-nodes.md) · [The CLI](docs/getting-started/synctl.md) |
| **Build and run apps** | [Services](docs/guides/services.md) · [Deployments and rollbacks](docs/guides/deployments.md) · [Release commands and jobs](docs/guides/release-commands.md) · [Git builds](docs/guides/git-builds.md) · [Domains and ports](docs/guides/domains-and-ports.md) · [Scaling](docs/guides/scaling.md) |
| **Data** | [PostgreSQL](docs/guides/postgres.md) · [Valkey](docs/guides/valkey.md) · [S3 storage](docs/guides/storage.md) |
| **Observe and secure** | [Monitoring and alerts](docs/guides/monitoring.md) · [Access control](docs/guides/access-control.md) · [Network security](docs/guides/network-security.md) |
| **Operate** | [Upgrades](docs/operations/upgrades.md) · [Backup and restore](docs/operations/backup-restore.md) · [Nodes and pools](docs/operations/nodes-and-pools.md) · [Troubleshooting](docs/operations/troubleshooting.md) |
| **Reference** | [synctl](docs/reference/synctl.md) · [API](docs/reference/api.md) · [Configuration](docs/reference/configuration.md) |

## Status

SynCloud is young: **v0.x**. It runs real clusters in our tests, from three small VMs up to a 50-node load test, but expect rough edges and breaking changes between minor versions until 1.0. Back up the controller ([how](docs/operations/backup-restore.md)), and read the [changelog](CHANGELOG.md) before upgrading.

## Contributing

Bug reports, ideas and pull requests are welcome. [CONTRIBUTING.md](CONTRIBUTING.md) explains how to build it, run the tests and send a change. Please report security issues privately, as described in [SECURITY.md](SECURITY.md).

## License

[Apache License 2.0](LICENSE).
