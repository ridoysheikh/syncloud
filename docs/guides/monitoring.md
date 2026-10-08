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

### 1. Add a notification channel

Alerts can go to webhooks, Slack, Discord, Telegram or email. Add a channel under **Monitoring → Alerts → Channels**, or:

```sh
synctl alerts channels add ops --type slack --url https://hooks.slack.com/services/…
synctl alerts channels add oncall --type telegram --bot-token 123:ABC --chat-id -100200300
synctl alerts channels test ops
```

### 2. Create a rule

Choose what to watch:

| Type | Fires when |
| --- | --- |
| **Service metric** | error rate, latency, request rate, CPU or memory crosses a threshold |
| **Log pattern** | too many lines contain a text, or have a level |
| **Service health** | a service turns degraded or down |
| **Failed deployment**, **build** or **job** | it fails or is rolled back |
| **Node down** | a node stops reporting for a minute |
| **PromQL** | any query crosses a threshold |

```sh
echo '{"name":"web 5xx","type":"metric","metric":"error_rate","op":">","threshold":5,
       "project":"shop","service":"web","forSeconds":120,"channels":["ops"]}' | synctl alerts rules apply -f -
synctl alerts active
```

Rules are checked every 30 seconds. They notify when they fire and again when they resolve. History is under **Alerts → History** (`synctl alerts events`).
