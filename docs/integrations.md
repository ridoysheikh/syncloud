# Integrations and Traefik settings

**Integrations** in the dashboard lists every connection to an outside service:

- Git providers (described below);
- notification channels;
- S3 endpoints;
- container registries;
- cloud providers;
- backups;
- domains.

## Built-in Git server

You don't need GitHub at all: turn on the built-in **Forgejo** under **Settings → Platform** or **Integrations**, or with `synctl integrations git-server enable`. It's off by default.

When you turn it on, SynCloud:

- runs Forgejo as a system task on the controller node, at `https://git.<base-domain>` with its own certificate;
- creates its administrator (shown with **Administrator sign-in**, or `synctl integrations git-server credentials`);
- connects it as the Git connection **`git`**, so you can pick its repositories in the new-service wizard, with webhooks and commit statuses set up like any other host.

Sign-up is closed: add people's accounts from Forgejo's Site administration. It needs about 100 MB of memory.

You can't turn it off while services build from it. Turning it off removes the container and the `git` connection, but **keeps the repositories** in the `syncloud-git` volume, and they come back when you turn it on again.

## Git providers

Connect a Git host once. After that, services pick a repository by name instead of a URL, and SynCloud handles the rest:

- **Clone credentials:** it mints the credentials for cloning and polling (short-lived tokens for a GitHub App).
- **Push webhook:** it creates the webhook on the repository, so pushes build in seconds. It removes the webhook when the service disconnects.
- **Commit statuses:** it reports each build on its commit as `syncloud/<project>/<environment>/<service>`. A commit goes from *pending* (queued or building) to *success* (built and deployed) or *failure*.

Polling continues as a safety net (every 10 minutes while webhooks arrive), so a missed delivery is still built.

### GitHub (one click)

Open **Integrations → GitHub → Create GitHub App on GitHub**, or run `synctl integrations git github-app`.

1. GitHub asks you to confirm a new **private app** under your account, or under an organization if you give one. It requests these permissions:
   - contents and metadata: read;
   - commit statuses: write;
   - push and pull request events.
2. Back in SynCloud, the app is saved and GitHub opens its installation page. Choose the accounts and repositories the app may see. You can add more later with **Install on more repositories**.

SynCloud stores the app's private key sealed with the master key. It keeps no long-lived token: each clone, poll and status update uses an installation token that lasts one hour.

For **GitHub Enterprise**, set its URL under *GitHub Enterprise* in the same form.

### GitLab, Gitea / Forgejo, GitHub with a token

Choose the provider, give the server URL (blank means gitlab.com or github.com), and paste an access token. The form links to the host's token page, with the scopes filled in where the host allows it.

| Host | Token scopes |
| --- | --- |
| GitLab | `api` |
| Gitea / Forgejo | repository (read and write), user (read) |
| GitHub | classic token with `repo` and `admin:repo_hook`, or a fine-grained token with Contents (read), Commit statuses (write) and Webhooks (write) |

SynCloud checks the token against the host before saving it, then stores it sealed with the master key. The API never returns it.

```sh
echo glpat-… | synctl integrations git add gitlab --kind gitlab
SYNCLOUD_GIT_TOKEN=… synctl integrations git add forge --kind gitea --url https://git.example.com
synctl integrations git list
synctl integrations git repos forge api
```

### Building from a connected repository

To connect a repository, you can:

- pick it in the **New service** wizard (source: Git repository);
- pick it on a service's **Builds** tab;
- run:

  ```sh
  synctl builds connect api -p shop --connection github --repo acme/api --context services/api
  ```

The branch defaults to the repository's default branch. **Any Git URL** still works for other hosts, with an optional token. In that case you add the webhook by hand: its URL and secret are shown on the Builds tab.

**Webhooks need a reachable dashboard.** The Git host must reach the dashboard URL: your domain, or the default sslip.io name. On a private address, builds still happen, but they start on the next poll (every minute by default) instead of instantly.

**Removing a connection.** You can't remove a connection while services build from it. Disconnect them first.

## Traefik settings

**Network → Traefik** holds the options for every Traefik replica, on the controller and on edge nodes. The same options are available as `synctl traefik settings`.

### Applied live

These are part of the configuration Traefik polls, so they apply within about two seconds.

| Setting | Default | Notes |
| --- | --- | --- |
| Redirect HTTP to HTTPS | on | When off, services also answer on plain HTTP. |
| Minimum TLS version | 1.2 | Set 1.3 for modern clients only. |
| Strict SNI | off | Refuses TLS clients that don't name a known domain. |
| HSTS max-age | 0 (off) | |
| Retry attempts | 2 | On another task, idempotent requests only. 0 turns retries off. A service's retry middleware replaces it. |
| Compress responses | off | |
| Request body limit | unlimited | Larger requests get 413. |

These run on every service route before the service's own middlewares.

### Restart Traefik

These become Traefik command-line flags. Saving a change restarts every replica, which takes a few seconds each.

| Setting | Default | Notes |
| --- | --- | --- |
| Read, write and idle timeouts | Traefik's: 60s, none, 180s | Of the public entrypoints. |
| Trusted proxies | none | IPs or CIDRs whose `X-Forwarded-*` headers are kept. **Use Cloudflare ranges** fills in Cloudflare's published list. |
| PROXY protocol | off | From trusted proxies only. |
| HTTP/3 | off | Over UDP on the HTTPS port. |
| Connect timeout, response header timeout and idle connections per task | Traefik's | For connections to tasks. |
| Log level | INFO | |

The page shows the exact flags these settings add.

Example:

```sh
synctl traefik settings
synctl traefik settings set minTls=1.3 hstsSeconds=31536000 compress=true
synctl traefik settings set trustedIPs=173.245.48.0/20,103.21.244.0/22 readTimeout=120s
```

### Other routing pages

- **Network → Routing:** per-service middlewares (rate limits, passwords, allow-lists, CORS and more), a validated raw YAML editor for anything else, and the exact configuration served to Traefik.
- **Network → Traffic:** live load, the traffic map and the request tail.
