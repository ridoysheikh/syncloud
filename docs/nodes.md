# Nodes and pools

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
- reservations pass `headroom`% (default 80).

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

To check edge health, see **Network → Edge nodes** or run `synctl pools edges`.
