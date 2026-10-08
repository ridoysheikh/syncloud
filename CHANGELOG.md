# Changelog

All notable changes are listed here. Versions follow [semantic versioning](https://semver.org); until 1.0, minor versions may contain breaking changes, and each entry says how to upgrade.

## [0.1.2] - 2026-10-09

### Added

- **Public databases take TLS and plain connections.** A public Valkey or PostgreSQL database gets two ports of its own, from 21000–21999 (`--public-db-ports`): read-write and read-only. Each takes TLS (`rediss://`, `sslmode=require`) and plain (`redis://`, `sslmode=disable`) connections, so a plain connection now reaches the right database too.
- **Require TLS**, per database (**Connectivity**, or `synctl db network NAME --require-tls on`), refuses plain connections.
- The **Connect** panel and `synctl db credentials` show the TLS and plain URLs with the database's ports, and a ready `redis-cli` or `psql` command.

### Changed

- Clients that send no host name (SNI) now get the platform's Let's Encrypt certificate instead of Traefik's self-signed one. `redis-cli --tls` works without `--sni`, and so do older PostgreSQL clients.
- The shared ports 6379 and 5432 still work as before: TLS only, for clients that send SNI.

### Fixed

- After a controller restart, public database endpoints served Traefik's self-signed default certificate (`certificate verify failed`) until a database's network settings changed: the certificate list was built before the database manager started. Their firewall ports had the same start-up gap.
- PostgreSQL clients built on libpq 17 or later couldn't connect over TLS through Traefik (`SSL error: no application protocol`): Traefik didn't accept their `postgresql` ALPN protocol.

### Upgrading

- Public databases get their two ports when the upgraded controller starts. Their old addresses keep working.
- If a cloud firewall sits in front of your servers, allow **21000–21999/tcp** from your clients. SynCloud opens the ports in the servers' own firewall.
- Turning a public endpoint on or off restarts Traefik for a moment, because ports are added or removed (as for public service ports).

## [0.1.1] - 2026-10-08

### Fixed

- The Valkey guide's `redis-cli` command for public endpoints failed with `certificate verify failed`: redis-cli sends the host name (SNI) only with `--sni`. The docs and the dashboard now show a working command, and say that public endpoints need TLS with SNI.
- Setup's recovery key check didn't say that dashes aren't counted, so typing the last group with its dash never matched. The check now accepts the characters with or without dashes, or the whole key, and the installer prints the 6 characters to type.
- The installer ignored `--version` (and failed to find the latest release) because reading `/etc/os-release` replaced its `VERSION` setting. The `install.sh` of the 0.1.0 release has been replaced with the fixed one.

## [0.1.0] - 2026-10-08

The first public release.

### Platform

- One controller (Go, SQLite, embedded dashboard) and an agent per node. Nodes join with a one-line command, talk to the controller over mutual TLS, and share a WireGuard private network with a managed nftables firewall.
- Installer with preflight checks, Docker setup, checksum-verified downloads from GitHub releases, and an sslip.io domain with Let's Encrypt certificates out of the box.
- In-place controller and agent upgrades with automatic rollback; encrypted controller backups to S3; restore and move to a new host.

### Apps

- Projects, environments and services from an image or a Git repository (Dockerfile, Nixpacks or static), with the built-in registry and BuildKit.
- Rolling deploys with health checks, draining and a circuit breaker; a deployment history with changes, timelines and triggers; rollbacks (optionally with pre-deploy jobs), redeploy and cancel; a rollback window.
- Release commands before and after each deploy (migrations, smoke tests); after-build checks; build commands and encrypted build variables.
- Scheduled and one-off jobs, `synctl run` and `synctl exec`.
- Generated addresses with labels, custom domains (shared by path, prefix stripping, redirects), and public TCP/UDP ports with allow-lists.
- Target-tracking autoscaling (CPU, memory, requests, latency), placement, node pools with Hetzner, DigitalOcean or webhook autoscaling, and edge nodes.
- Environment deploy locks, auto-deploy switches, cloning, and deleting everything in an environment or project.

### Data

- Managed PostgreSQL 18 and 17: Patroni failover, replicas, replication modes, add-ons (pgvector, TimescaleDB, PostGIS, pg_duckdb, pg_cron…), curated parameters, WAL-G backups with point-in-time restore, and a pgAdmin-like explorer, console, roles and privileges editor. The images ship inside the release.
- Managed Valkey (Redis-compatible) with Sentinel failover and memory and replica autoscaling.
- S3 endpoints, a bucket browser, and bucket bindings for services.

### Observability and security

- Metrics, logs, traffic insights, health checks and incidents for every service and node; alert rules to webhooks, Slack, Discord, Telegram and email.
- IAM: users, service accounts, groups, roles, JSON policies, MFA, access keys and an audit log.
- Security groups between containers, Traefik middlewares, and host firewall policies.

### Install

```sh
curl -fsSL https://github.com/ridoysheikh/syncloud/releases/download/v0.1.0/install.sh | sudo bash -s -- --version 0.1.0
```
