# Nodes and pools

Joining a new server is covered in [Add nodes](../getting-started/add-nodes.md). This page is about running them.

## Joining and removing nodes

| Task | Command |
| --- | --- |
| Join a node | `synctl nodes join-tokens create`, then run the printed `join.sh` command on the new machine |
| List nodes | `synctl nodes list` (status, resources, agent version) |
| Stop placing new tasks on a node | `synctl nodes cordon NODE` |
| Move a node's tasks away | `synctl nodes drain NODE` (replacements start before the old tasks stop) |
| Remove a node | `synctl nodes delete NODE` (revokes its certificate; drain it first) |
| Reinstall a node with the same identity | `synctl nodes rejoin-command NODE` |

Node status:

- **Suspect** after 20 seconds without a heartbeat.
- **Not ready** after 60 seconds. Its tasks are then replaced on other nodes.
- **Back:** when it reconnects, its stale containers are removed.

A re-joined node keeps its ID, pool and tasks and gets a new certificate. The old certificate stops working. Use this when a machine is reinstalled.

## A node's page

Click a node under **Compute → Nodes** (the controller, `ctl-0`, too). Its page has three tabs:

- **Overview:** live CPU, memory, disk, load, running tasks and uptime; the node's details (OS, kernel, Docker, agent version); and history charts for 15 minutes to 7 days. The charts cover CPU, memory and disk against their totals, load, network and private-network traffic, running tasks, and its tasks' CPU and memory by service.
- **Tasks:** every task on the node, with links to their services.
- **Shell:** a login shell on the node itself, as the user the agent runs as. That is **root** on installed nodes.

The same from the CLI:

```sh
synctl nodes metrics w1 --range 6h     # latest, average and peak of each chart
synctl nodes shell w1                  # interactive shell
synctl nodes shell ctl-0 -- df -h      # one command
```

Node shells need the `node:NodeShell` permission, which only administrators have by default. Every session is recorded in the audit log as `node:Shell`. To turn node shells off on a machine, start its agent with `--no-host-shell` (or set `SYNCLOUD_AGENT_NO_HOST_SHELL=1`). Task exec still works there.

## Which nodes a project may use

By default, a project's services and jobs run on any schedulable node. To keep a project on certain machines, open the project's **Settings** tab, choose **Allowed nodes → Only these nodes**, and tick the nodes. Or use the CLI:

```sh
synctl projects nodes shop w1 w2     # only w1 and w2
synctl projects nodes shop --any     # any node again
synctl projects nodes shop           # show the list
```

Each service can narrow its project's list on its **Placement** tab, for example to keep one service on a single worker. The same tab sets spreading: spread over the nodes (the default) or pack onto as few as possible. In a spec this is `"placement": {"nodes": ["w1"], "strategy": "binpack"}`.

The rules:

- **A service can't widen its project's list.** Naming a node the project doesn't allow is rejected. Likewise, a project can't drop a node that one of its services is limited to; change the service first.
- **Changing the lists moves tasks.** Tasks on a node that is no longer allowed are replaced on an allowed node. As with a drain, the new task starts before the old one stops. If no allowed node has room, the old task keeps running and the service says why it can't place.
- **The controller node.** Naming `ctl-0` in a project's or service's list runs those tasks on the controller, even when the controller takes no general workloads. A draining controller still takes nothing.
- **Jobs follow their project's list.** Builds don't: they are platform work.

## Node pools

A pool groups nodes with the same purpose. It has a role:

- **worker:** runs tasks.
- **edge:** runs a Traefik replica and no tasks.

Nodes outside any pool form the `default` pool.

| Task | Command |
| --- | --- |
| Join a node to a manual pool | `synctl pools join-command POOL` |
| Move a node into a pool | `synctl pools move NODE POOL` (`default` for none) |
| Restrict a service to pools | set `"placement": {"pools": ["gpu"]}` in its spec |

In the dashboard, pools are under **Compute → Node pools**.

### Provider-backed pools and autoscaling

Add a cloud provider with **Compute → Node pools → Cloud providers**, or with `synctl pools providers add`. The supported types are:

- **Hetzner Cloud**
- **DigitalOcean**
- **webhook:** SynCloud POSTs `create`, `delete` and `list` as JSON to your URL, for any other cloud.

API tokens are encrypted with the master key and never shown again.

A pool backed by a provider has:

- a region, server type, image and SSH keys;
- `min` and `max` nodes.

New servers get cloud-init user data that installs the agent and joins with a single-use token bound to the pool and the server's name.

With **autoscaling** on, the controller checks each pool every 15 seconds.

**Scale out** happens when either:

- tasks that may run in the pool have waited unplaced for `pendingAfter` seconds (default 60), or
- the pool's nodes pass `headroom`% (default 80) full, counting reserved or really used CPU and memory, whichever is more.

Scale-out adds at most `maxStep` servers at a time (default 2) and never goes above `max`. A server that has not joined within `joinTimeout` (default 600 s) is deleted.

**Scale in** removes a node that has stayed below `scaleInBelow`% (default 40) for `scaleInAfter` seconds (default 600). A node is removed only when all of these hold:

- its tasks fit on the pool's other nodes,
- the pool stays at or above `min`,
- the node is not marked **scale-in protected**.

The node is drained first, then removed, and then its server is deleted. Every decision appears in the pool's scaling history (`synctl pools history POOL`).

## Edge nodes

Nodes in an `edge` pool run a Traefik replica. It has the same routes and certificates as the controller's Traefik and fetches them over the private network.

Traefik keeps its last configuration, so **public traffic keeps flowing while the controller is down**. The edge's host firewall opens 80 and 443.

Point your domain's A records at the edge nodes' public addresses. sslip.io names always point at the controller.

To check edge health, see **Network → Load balancers** or run `synctl pools edges`.

Edge nodes serve every project that uses the default load balancer, the same as the controller.

## Project load balancers

A project can have its own load-balancer nodes. Its public traffic (HTTP, HTTPS and its public TCP/UDP ports) then goes straight to those nodes and never passes through the controller. Set it under **Project → Load balancer**, or:

```sh
synctl projects ingress shop --nodes lb1,lb2   # its own nodes, in order
synctl projects ingress shop                   # show who serves it and where DNS must point
synctl projects ingress shop --controller      # back to the controller
```

- **Any node can be chosen**, workers included. They keep running tasks, and several projects can share the same nodes. Each one runs a Traefik replica with only the routes of the projects that chose it.
- **The controller and edge pools stop serving the project.** Its host firewall opens 80/443 on the chosen nodes, and the project's public ports open there only.
- **Generated addresses** become `<name>.<first node's IP>.sslip.io`, which works without DNS setup but reaches the first node only. The first node in the list gives these addresses.
- **Custom domains** need an A record for every chosen node. The domain's DNS check lists any that are missing.
- **Switching has no downtime.** The previous load balancer keeps serving each custom domain until that domain's DNS points only at the new nodes (checked every minute), then drops it. **Cut over now** (`--cut-over`) ends this at once. Old generated addresses keep answering on the previous load balancer during the move.
- **If every chosen node is down, the project is unreachable**, because DNS points at them and there is no fallback to the controller. The "system component" alert fires (`load balancers of <project>`). Keep at least two nodes for production projects.
- Removing a node that is a project's only load balancer is refused unless you pass `--force` (`synctl nodes delete ID --force`). That project then goes back to the controller.
- Public database endpoints stay on the controller and edge pools.

### Protection

**Project → Load balancer → Protection** puts Traefik-managed protection in front of every route of the project, on whichever load balancer serves it (`synctl projects protection PROJECT`):

- **Rate limit** per client (requests per second, minute or hour, with a burst). Over it, clients get 429.
- **Concurrent requests** per client and a **request body limit** (413 when exceeded).
- **Who is a client:** the connection's address, `X-Forwarded-For` at a depth (behind a proxy), or a header such as `CF-Connecting-IP` (behind Cloudflare).
- **Block list:** these clients get 403.
- **Bypass:** requests from listed IPs, under listed paths (health checks, webhooks), or carrying the secret header (`X-Syncloud-Bypass` by default; generate the secret once) skip the rate and concurrency limits. **Off** turns all protection off and keeps the settings.
- **Shared counters:** without Redis each load balancer counts on its own. **Create Valkey** makes a small managed Valkey in the project (listed under Databases) or you pick an existing one, and all of the project's load balancers share their counters. If it goes down, each node counts on its own until it is back, and an alert fires (Traefik would otherwise answer 500).
- **Network guard** (on the project's own nodes): the host firewall drops floods before they reach Traefik: new connections per second and concurrent connections per source on 80/443, with an optional auto-ban. Bypass IPs are exempt. A node shared by several projects uses the most permissive guard among them.

```sh
synctl projects protection shop --on --rate 50 --per 1s --burst 100 --concurrent 20 --body-mb 10
synctl projects protection shop --client-ip header:CF-Connecting-IP --block 203.0.113.0/24
synctl projects protection shop --bypass-ips 198.51.100.7 --bypass-paths /healthz --generate-secret
synctl projects protection shop --create-valkey production
synctl projects protection shop --guard-rate 100 --guard-conns 200 --guard-ban 30
```

The tab shows the last hour's requests, and how many were limited or blocked.

Every replica, with the projects it serves, is listed on **Network → Load balancers**.
