# Changelog

All notable changes are listed here. Versions follow [semantic versioning](https://semver.org); until 1.0, minor versions may contain breaking changes, and each entry says how to upgrade.

## [Unreleased]

### Changed

- **A new setup page.** First-run setup is now a wide, four-step wizard: verify the server, the workspace, the root account, and a review. A step rail shows progress, and a preview shows the header and browser tab. The password field shows its strength and what it still needs. The review sums up every step, with links back to edit it. An error from the server sends you back to the step it belongs to. On phones, the rail becomes a row of step numbers.

## [0.2.0]

### Added

- **Workspace branding.** Setup asks for a workspace name, and optionally a browser title and a logo. **Settings › General** changes them later. The name shows in the header, on the sign-in page and in alert emails. The logo is a square PNG, JPEG or WebP up to 512 KB, and the browser icon is made from it. Unset values keep SynCloud's name and logo. From the CLI: `synctl settings branding get|set`.
- **Email settings.** **Settings › Email** holds one SMTP server (STARTTLS, TLS or none) with a test button. Email channels now need only recipients. Alert emails come in HTML with your logo and name, plus plain text. From the CLI: `synctl settings smtp get|set|test|disable`.
- **System alerts, on by default.** New rule types: node overload (CPU, memory or disk), database failover, database health, system component down, backups failing, certificate expiring, and controller disk filling up. A new install, or an existing one on upgrade, gets a set of System rules that email the root user through an **Admins** channel once email is set up. They can be edited or disabled, and **Restore defaults** brings back deleted ones. Node-offline alerts say how many tasks moved to other nodes.
- **Reminders.** A firing alert is sent again every **Remind every** hours (the System rules default to 12, 0 = never).
- **The recovery key is kept on the controller.** The root user can open it at any time under **Settings › Backups › Recovery key**, or with `synctl backups recovery-key show`. Installs from before can't show their old key: **Create recovery key** (`synctl backups recovery-key create`) makes a new kept key and re-keys the backups in S3 so it opens them all. Nothing is re-encrypted, so services and data aren't affected.

### Changed

- Email channels created before this release keep their own SMTP server. New ones use **Settings › Email**.

### Upgrading

- On first start, the System alert rules and the **Admins** email channel (the root user's address) are created. Add an SMTP server under **Settings › Email** to receive them.

## [0.1.9]

### Added

- **Registry images in S3.** The built-in registry can keep its images in an S3 bucket instead of on the controller's disk. Switching copies every image through a temporary registry and checks the copy before the registry moves. Pushes pause during the copy; pulls keep working. After leaving the controller's disk, the images are deleted from it. Nodes download layers from the bucket directly. Switching back to disk works the same way, and an old S3 location is kept for you to delete. In the dashboard: **Registry › Storage**. From the CLI: `synctl registry storage get|set`.

### Changed

- Registry garbage collection now runs a dry run first. It refuses to delete anything if the dry run would keep no images while repositories still have tags, which protects against storage that lists objects wrongly.
- An S3 endpoint the registry uses can't be deleted. Editing its credentials restarts the registry with the new ones.

## [0.1.8]

### Added

- **Restore PostgreSQL from another server's WAL-G archive.** A new managed database can be built from an archive a server outside SynCloud wrote to S3: point it at the endpoint, bucket and prefix (plus the server's `WALG_LIBSODIUM_KEY` if the archive is encrypted) and restore to the archive's end or to a moment. The new database runs the archive's PostgreSQL version, gets SynCloud's own superuser, replication and app passwords, makes the app user the owner of its app database, and archives under its own prefix. The source archive is only read. The old server's configuration files are set aside, so libraries and paths only it had don't stop the restore. In the dashboard: **Databases › Restore from another server**. From the CLI: `synctl db archive` and `synctl db restore-archive`.

### Fixed

- Restoring a SynCloud database whose backups use a different S3 endpoint than the restored database's own no longer mixes up the two endpoints' credentials.

### Upgrading

- The managed PostgreSQL images are now `17-r3` and `18-r2` (new restore scripts). PostgreSQL members move to them one at a time, replicas first, with a switchover before the primary, as for any setting that needs a restart.

## [0.1.7]

### Added

- **Image updates for services that run a ready-made image** (**Deploy › Image updates**):
  - **Deploy new pushes automatically** watches the image's registry and deploys a new push of its tag: within seconds for the built-in registry, every 5 minutes (or 1 minute to 1 hour) for others. **Check now** checks at once.
  - **Deploy webhook:** a secret URL for CI to call after `docker push`, with any registry. It answers `202` when a deployment starts and `200` when the tag hasn't changed.
  - Deployments started this way show **New image push** as their trigger. A locked environment, or one that deploys by hand, isn't deployed to.
  - From the CLI: `synctl services auto-deploy on|off|check|webhook|get`.

### Fixed

- **Redeploy now deploys the newest push of the image's tag.** Before, nodes pulled an image only when they didn't have it yet, so after the first deploy a moved tag (`:latest`, `:main`, `:v2`) was never pulled again: a redeploy restarted the old image, and nodes could run different builds under the same tag. Each revision now records the exact image its tag pointed to (its digest), and every node runs exactly that. Changing a variable or setting keeps the image; a new image, a redeploy or an image update moves to the newest push. A rollback returns to the exact old image. The service page shows the digest next to the image.
- A new image that doesn't exist in its registry is refused when it's set, instead of failing on the nodes.

### Upgrading

- Running services keep their image until their next redeploy, image change or image update; from then on they're pinned to a digest. If a registry can't be reached, deploys go ahead with the image as it is on the nodes.
- Private registries need their credentials under **Registry › Upstreams** for checking tags, as for pulling.

## [0.1.6]

### Added

- **Override a service's command in the dashboard.** The service's **Deploy** tab and the New Service wizard have a **Command** setting: the image's own (default), a shell line (`node worker.js`), or exact arguments with an optional entrypoint. One image can now run several services — a web server, a worker, a scheduler — each with its own command. Saving rolls out a new revision.

## [0.1.5] - 2026-10-10

### Changed

- **CPU and memory are shared by default; reserving is a choice.** Before, every task's CPU and memory were set aside on its node, used or not. A node whose apps used 6% CPU could still count as full and refuse new tasks (`cannot place task: no node fits (1 not enough CPU)`), leaving services degraded and deploys stuck. Now each task's CPU and memory are either:
  - **Shared** (the default): what the task is expected to use. CPU never stops a task from being placed; when a node is busy, tasks get CPU in proportion to their `cpu`. Memory goes to a node with that much really free.
  - **Reserved**: set aside on the node as before, and kept for the task when the node is busy or memory runs low.

  Choose under the service's **Placement › Resources**, in the spec (`"cpuMode": "reserved"`, `"memoryMode": "reserved"`), or with `synctl services run … --reserve-cpu --reserve-memory`. Database members reserve their memory and share their CPU.
- A rollout no longer waits for room its old tasks are about to free.
- Node pool autoscaling and the pool view count a node's CPU and memory as reserved or really used, whichever is more.
- Tab rows stay on one row. When the tabs don't fit, arrows appear at the clipped end, a mouse wheel scrolls the row sideways, and the selected tab stays in view without the page jumping. Arrow keys move between tabs.

### Fixed

- Some tabs crashed with "Cannot read properties of null (reading 'map')" when a list was empty (for example a group with no members). The API now always sends empty lists as `[]`.
- A tab or page that fails to load now shows the error with **Try again** in its place. Before, the whole page went blank; now the tabs and the rest of the dashboard keep working, and switching tabs clears the error.

### Upgrading

- Existing services become **shared** with the CPU and memory they had. Services waiting with "not enough CPU" are placed once the controller is upgraded. To keep a service's old guarantee, set it to **Reserved** (this rolls out a new revision).
- Running containers are not restarted. Service tasks get their CPU weights in place when their agent reconnects after the upgrade; database members get theirs the next time they're updated.

## [0.1.4] - 2026-10-10

### Changed

- **Every dialog looks and works the same.** Each dialog has a title bar, a body that scrolls on long content, and its buttons in a footer that stays in place. An **Esc** button in the top-right corner closes it, as the Esc key does.
- **Themed dropdowns instead of the browser's own.** Every select in the dashboard now matches the rest of the UI and works from the keyboard (arrows, Enter, Esc, type to jump). Lists longer than ten get a search box. Fields that suggest values (IAM actions, branches, registry hosts, security group peers, database access) show a themed suggestion list that filters as you type.
- Traffic charts, the Overview, the traffic map and the request list show your services' traffic only. The platform's own requests, most of them the dashboard polling its API, are left out. To see them, choose **syncloud (platform)** in the request log, or use the Metrics Explorer.

### Fixed

- **Request counts were wrong.** The uptime monitor checks each service through Traefik every 15 seconds, and those checks were counted as the service's requests. An idle service showed about 0.07 req/s, its latency was the checks' latency, and an app without a `/` page showed nearly 100% 4xx. The checks now take a separate route to the app, so they're no longer counted in charts, alerts, autoscaling or the request log.
- **Requests through custom domains were missing** from the traffic charts, alerts and request-rate autoscaling. In the request log they were filed under the platform (`syncloud/system/dom-…`). They now count for the service the domain points to.
- Hover cards and dropdowns opened from a dialog appeared behind it.

### Upgrading

- Metrics already stored keep the old counts, so charts are accurate from the time the upgraded controller starts.
- Traefik's generated configuration (**Network › Traefik**) lists a `…_probe` router and service next to each service route. These carry the uptime checks; leave them alone.

## [0.1.3] - 2026-10-09

### Fixed

- The first PostgreSQL database never started on an installed server ("waiting for the PostgreSQL image: … read-only file system"): the controller downloaded the image into `/usr/local/lib/syncloud/downloads/images`, which its systemd sandbox makes read-only. It now downloads and unpacks under `/var/lib/syncloud/images`; archives placed by hand in either directory are used.
- On a single-server cluster, a Valkey or PostgreSQL database with replicas stayed **degraded** forever (`cannot place data 1: no node fits`): replicas had to go on another node, and there was none. They now share the node when no other may run them, as Sentinels already did, and the database page and `synctl db get` note that all members are on one node.

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
