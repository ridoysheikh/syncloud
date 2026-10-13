# Monitoring and alerts

Metrics (VictoriaMetrics) and logs (VictoriaLogs) are collected for every task, service and node. There's nothing to install.

## Where to look

| To see… | Dashboard | CLI |
| --- | --- | --- |
| the whole cluster | **Overview** | `synctl status` |
| a service's CPU, memory, network, disk | service → **Metrics** | `synctl metrics web -p shop` |
| requests, errors and latency per route | service → **Traffic**, **Network → Traffic** | `synctl traffic`, `synctl requests` |
| logs of every task, merged | service → **Logs**, **Logs** | `synctl logs -f web -p shop` |
| a node's usage history | **Compute → Nodes → (node)** | `synctl nodes metrics w1 --range 6h` |
| health and uptime | **Health** | `synctl health services` |
| any metric (PromQL) | **Monitoring → Metrics** | `synctl metrics query '…'` |

Charts cover 15 minutes to 7 days. Logs can be searched (`--grep`), followed live, and paged back in time.

```sh
synctl logs -A --since 15m --grep timeout          # every service
synctl metrics query 'sum by (service) (rate(traefik_service_requests_total[5m]))' --range 6h
```

The metrics explorer can read every project's series, so it needs `metrics:ExploreMetrics`. Per-service charts are visible to project members.

## Health and incidents

A service is **healthy** when all its tasks run and pass their health checks. The controller also probes each task over the private network and routes around unreachable ones. When a service turns degraded or down, an **incident** opens under **Health → Incidents**, and closes when the service recovers.

## Alerts

### Built-in System alerts

A new install starts with a set of **System** rules that email the root user, through the **Admins** channel:

| Rule | Fires when |
| --- | --- |
| Node offline | a node stops reporting for 2 minutes (the message says how many tasks moved to other nodes) |
| Node CPU / memory overload | above 90 % for 10 / 5 minutes |
| Node disk almost full | above 85 % |
| Database failover | a managed Valkey or PostgreSQL database gets a new primary |
| Database down or degraded | for 2 minutes |
| System component down | Traefik, the registry, metrics, logs or the Git server stop for 2 minutes |
| Backups failing | a backup fails, or none succeeds for 26 hours |
| Certificate expiring | within 14 days, or its renewal fails |
| Controller disk almost full | above 85 % |
| Service down or degraded | for 2 minutes |

They can be edited, turned off or deleted. **Restore defaults** on the Rules tab (or `synctl alerts rules restore-defaults`) brings back missing ones. While an alert stays active, a reminder goes out every 12 hours.

**Nothing is emailed until email is set up.** Do that next.

### 1. Set up email

**Settings → Email**: the SMTP server alert emails go through. Your mail provider, Amazon SES, Postmark, Mailgun or SendGrid all work.

```sh
SYNCLOUD_SMTP_PASSWORD=… synctl settings smtp set --host smtp.example.com --username alerts \
    --from alerts@example.com
synctl settings smtp test you@example.com
```

Security is **STARTTLS** (port 587, the default), **TLS** (465) or **none** (25, not recommended). Emails carry your workspace's logo and name ([branding](#workspace-branding)).

### 2. Add more notification channels

Alerts can also go to webhooks, Slack, Discord or Telegram. An email channel needs only recipients. Add a channel under **Monitoring → Alerts → Channels**, or:

```sh
synctl alerts channels add ops --type slack --url https://hooks.slack.com/services/…
synctl alerts channels add oncall --type telegram --bot-token 123:ABC --chat-id -100200300
synctl alerts channels add team --type email --to dev@example.com,ops@example.com
synctl alerts channels test ops
```

An email channel can still use its own SMTP server (`--smtp-host`, `--from`); channels created before 0.2.0 keep theirs.

### 3. Create a rule

Choose what to watch:

| Type | Fires when |
| --- | --- |
| **Service metric** | error rate, latency, request rate, CPU or memory crosses a threshold |
| **Log pattern** | too many lines contain a text, or have a level |
| **Service health** | a service turns degraded or down |
| **Failed deployment**, **build** or **job** | it fails or is rolled back |
| **Node down** | a node stops reporting for a minute |
| **PromQL** | any query crosses a threshold |
| **Node overload** | a node's CPU, memory or disk is above a percentage |
| **Database failover** / **health** | a managed database gets a new primary / is degraded or down |
| **System component** | a platform component stops |
| **Backups** | a backup fails, or none succeeds for N hours |
| **Certificates** | a certificate expires within N days, or can't be renewed |
| **Controller disk** | the controller's data disk is above a percentage |

```sh
echo '{"name":"web 5xx","type":"metric","metric":"error_rate","op":">","threshold":5,
       "project":"shop","service":"web","forSeconds":120,"channels":["ops"]}' | synctl alerts rules apply -f -
synctl alerts active
```

Rules are checked every 30 seconds. They notify when they fire, again every **Remind every** hours while they stay active (0 = never), and when they resolve. Failovers and failed deployments, builds and jobs notify once each. History is under **Alerts → History** (`synctl alerts events`), with each channel's delivery result.

## Workspace branding

**Settings → General**: the workspace name (header, sign-in page, alert emails), the browser tab title, and a square logo. A PNG, JPEG or WebP up to 512 KB works; the browser icon is made from it. Setup asks for them too, and every one is optional: the defaults are SynCloud's name and logo.

```sh
synctl settings branding set --name "Acme Cloud" --title "Acme Console" --logo acme.png
synctl settings branding set --remove-logo
```
