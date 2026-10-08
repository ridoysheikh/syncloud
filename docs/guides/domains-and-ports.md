# Domains and ports

How each port of a service is reached is set on its **Networking** tab. The project's **Domains** tab lists every address in an environment.

## The generated address

Every HTTP port gets an HTTPS address under the cluster's base domain:

```
https://<service>-<environment>-<project>.<base-domain>
```

- **Rename it with a label:** `shop` serves `https://shop.<base-domain>` instead. Labels are unique, and `registry`, `git`, `db` and `www` are reserved.
- **Switch it off** to serve the port only on custom domains.

```sh
synctl services expose web http -p shop --label shop
synctl services expose web http -p shop --no-generated
synctl services routing web -p shop        # every port and how it's reached
```

## Custom domains

Add a domain on the **Networking** tab, or with the CLI. The page shows the DNS record to create and checks it live. The certificate is issued (Let's Encrypt) as soon as DNS points at the cluster.

```sh
synctl services domains add web shop.example.com -p shop
synctl services domains check shop.example.com
```

Point the record at the controller's public IP, or at your [edge nodes](../operations/nodes-and-pools.md#edge-nodes).

### Share a domain by path

Several services can share a host, each owning a path prefix. The longest prefix wins:

```sh
synctl services domains add web shop.example.com -p shop                      # everything else
synctl services domains add api shop.example.com -p shop --path /api --strip-prefix
```

With `--strip-prefix`, `/api/orders` reaches the `api` service as `/orders`.

### Redirect a domain

Answer with a permanent redirect to another host, keeping the path and query:

```sh
synctl services domains add web www.shop.example.com -p shop --redirect-to shop.example.com
```

## Public TCP and UDP ports

TCP and UDP ports (databases you run yourself, game servers, MQTT, SIP…) are private by default. Mark one **public**, and it gets a port from the cluster's range (20000–20999 by default) on the controller and the edge nodes:

```sh
synctl services expose game udp -p arcade --public
synctl services expose mqtt tcp -p iot --public --allow 203.0.113.0/24
synctl services expose mqtt tcp -p iot --private           # close it again
```

- **Allow list:** `--allow` limits clients to these addresses or CIDRs. It's enforced by Traefik and by the host firewall.
- **Port assignment:** the assigned port stays with the service until you close it. Closed ports are reused.
- **Range:** set it with the controller's `--public-ports LOW-HIGH` flag ([Configuration](../reference/configuration.md)).

Opening or closing a public port restarts the Traefik replicas, which takes a few seconds. The dashboard warns before it does.

## Private access

Inside the cluster, every service is reachable at `<service>.<environment>.<project>.syncloud.internal` (or just `<service>` within its environment), on every port, whether public or not. [Security groups](network-security.md) decide who may connect.

---

Next: [Scaling and placement](scaling.md)
