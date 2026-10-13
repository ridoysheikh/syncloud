# Add nodes

A node is any Linux server running the SynCloud agent. Tasks spread across nodes, and a node that goes down has its tasks replaced elsewhere.

## Join a server

1. In the dashboard, open **Compute → Nodes → Add node**, and choose **Create join token**. Or run `synctl nodes join-tokens create`.
2. Run the printed command on the new server, as root:

   ```sh
   curl -fsSL https://<dashboard>/join.sh | sudo bash -s -- --token SYN-JOIN-…
   ```

The script works in six steps and prints each one with its time:

1. **Checking the controller:** it stops at once if the server can't reach your dashboard.
2. **Docker Engine:** installed if missing, which usually takes 1–3 minutes. Its output is shown.
3. **Host firewall tools:** nftables, and a check that UDP 51820 (WireGuard) is free.
4. **Downloading the agent** from your controller, with a progress bar and a checksum check.
5. **Joining the cluster:** it then tries every address the agent can reach the controller at and shows which answer.

Agents connect to the controller through Traefik on port 443 (`agent.<your domain>`, then the controller's IP with that name) and use port 7443 only as a fallback. With 443 open for the dashboard, nothing else needs opening for agents. Without a base domain, agents use 7443.
6. **Starting the agent:** it streams the agent's log until the agent connects, for up to 90 seconds.

The **Add node** dialog follows along: joined, connected, then **Ready**, with a timer. Add `--quiet` to show only the steps, with command output only when something fails.

- **Single use:** the token is single-use and expires after an hour.
- **Self-signed certificates:** on a controller with a self-signed certificate, for example on a private network, the command pins the controller's public key instead of skipping the check.
- **Different address:** if other nodes must reach the new one on a different address, add `--advertise-address IP`.

## If it takes long or stops

- **Stopped at step 1:** the server can't open HTTPS to the dashboard. Check DNS, firewalls, and `--pin` for a self-signed certificate.
- **Slow at step 2:** Docker's installer downloads packages; on a slow mirror it can take several minutes.
- **"did not connect within 90 seconds":** run `syncloud-agent check-gateways` on the new server to see which addresses answer. Open TCP 443 (or 7443) on the controller to the new server, and UDP 51820 between nodes. The agent keeps retrying, and the node turns Ready once it gets through. Follow it with `journalctl -u syncloud-agent -f`.
- **"the controller refused the join":** the token was used or expired. Create a new one.

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
