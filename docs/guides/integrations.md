# Integrations

**Integrations** in the dashboard lists every connection to an outside service: Git hosts, notification channels, S3 endpoints, registries, cloud providers, backups and domains. This page covers Git.

## Connect a Git host

Once a host is connected, services pick repositories by name, and SynCloud handles the rest:

- **Clone credentials:** short-lived tokens for a GitHub App.
- **Push webhook:** created on the repository, so pushes build in seconds. It's removed when the service disconnects.
- **Commit statuses:** `syncloud/<project>/<env>/<service>` goes from pending, to success (built and deployed) or failure.

Polling continues as a safety net, so a missed webhook is still built.

### GitHub, in one click

**Integrations → GitHub → Create GitHub App on GitHub**, or `synctl integrations git github-app`.

1. GitHub asks you to confirm a private app under your account (or an organization). It needs contents (read), commit statuses (write), and push and pull request events.
2. Choose which repositories the app may see. You can add more later with **Install on more repositories**.

The app's key is sealed. Each clone or status update uses a token that lasts an hour. For GitHub Enterprise, set its URL in the same form.

### GitLab, Gitea, Forgejo, or GitHub with a token

Choose the provider, give the server URL (leave it blank for gitlab.com or github.com), and paste a token:

| Host | Token scopes |
| --- | --- |
| GitLab | `api` |
| Gitea / Forgejo | repository (read and write), user (read) |
| GitHub | classic `repo` + `admin:repo_hook`, or fine-grained Contents (read), Commit statuses (write), Webhooks (write) |

```sh
echo glpat-… | synctl integrations git add gitlab --kind gitlab
SYNCLOUD_GIT_TOKEN=… synctl integrations git add forge --kind gitea --url https://git.example.com
synctl integrations git repos forge api
```

> **Webhooks need a reachable dashboard.** On a private address, builds start on the next poll (every minute) instead of instantly.

## Built-in Git server

No Git host? Turn on the built-in **Forgejo** under **Settings → Platform**, or with:

```sh
synctl integrations git-server enable
synctl integrations git-server credentials     # its administrator
```

It runs at `https://git.<base-domain>` and is connected as **`git`**, with webhooks and statuses like any other host. Sign-up is closed: add accounts in Forgejo's site administration. It uses about 100 MB of memory.

Turning it off keeps the repositories, which come back when you turn it on again.

## Container registries

**Registry → Upstreams** stores credentials for pulling private images from Docker Hub, GHCR, Quay, GitLab or any registry:

```sh
SYNCLOUD_UPSTREAM_PASSWORD=ghp_… synctl registry upstreams set ghcr.io -u acme-bot
```

The built-in registry (`registry.<base-domain>`) holds images built from Git and anything you push:

```sh
synctl registry info          # how to log in and push
```

---

Related: [Git builds](git-builds.md)
