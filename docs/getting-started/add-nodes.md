# Add nodes

A node is any Linux server running the SynCloud agent. Tasks spread across nodes, and a node that goes down has its tasks replaced elsewhere.

## Join a server

1. In the dashboard, open **Compute → Nodes → Add node**, and choose **Create join token**. Or run `synctl nodes join-tokens create`.
2. Run the printed command on the new server, as root:

   ```sh
   curl -fsSL https://<dashboard>/join.sh | sudo bash -s -- --token SYN-JOIN-…
   ```

The script installs Docker if needed, downloads the agent from your controller (checksum-checked), joins and starts it as a systemd service. The node appears as **Ready** within seconds.

- **Single use:** the token is single-use and expires after an hour.
- **Self-signed certificates:** on a controller with a self-signed certificate, for example on a private network, the command pins the controller's public key instead of skipping the check.
- **Different address:** if other nodes must reach the new one on a different address, add `--advertise-address IP`.

## What the node gets

- A certificate from the cluster's own CA. The agent talks to the controller over mutual TLS and dials out, so it needs no inbound port except WireGuard.
- An address on the private network (WireGuard). Every task can reach every other task and node over it, subject to [security groups](../guides/network-security.md).
- A managed host firewall.

## Check on it

```sh
synctl nodes list
```

Or click the node under **Compute → Nodes**: it shows live usage, history charts, its tasks, and a root shell.

## Next steps for a bigger cluster

| If you want to… | Read |
| --- | --- |
| drain a node for maintenance, or remove it | [Nodes and pools](../operations/nodes-and-pools.md) |
| keep a project on certain nodes | [Which nodes a project may use](../operations/nodes-and-pools.md#which-nodes-a-project-may-use) |
| create servers automatically on Hetzner or DigitalOcean | [Node pools and autoscaling](../operations/nodes-and-pools.md#node-pools) |
| keep public traffic up while the controller is down | [Edge nodes](../operations/nodes-and-pools.md#edge-nodes) |

---

Next: [The synctl CLI](synctl.md)
