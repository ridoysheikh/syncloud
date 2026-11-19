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

> Register your domains under **Domains** to choose a domain and subdomain from a list, use wildcards (`*.example.com`), and manage their DNS records and certificates in one place: see [Domains, DNS and certificates](domains-dns-certificates.md). Typing any hostname here still works as before.

Add a domain on the **Networking** tab, or with the CLI. The page shows the DNS record to create and checks it live. The certificate is issued (Let's Encrypt) as soon as DNS points at the cluster.

```sh
synctl services domains add web shop.example.com -p shop
synctl services domains check shop.example.com
```

Point the record at the controller's public IP, or at your [edge nodes](../operations/nodes-and-pools.md#edge-nodes). If the project has its [own load balancers](../operations/nodes-and-pools.md#project-load-balancers), create one A record for each of them instead. The check shows which addresses are still missing.

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

Websites and APIs need no port of their own: they share 80 and 443 through Traefik. TCP and UDP ports (mail, MQTT, game servers, SIP, databases you run yourself…) are private by default. Make one **public**, and clients reach it on the project's load balancer: the controller and the edge nodes, or the project's [own load balancers](../operations/nodes-and-pools.md#project-load-balancers).

In the dashboard, create the service as **Public TCP/UDP**. Pick a preset (mail server, MQTT, Minecraft) or add the ports yourself. On an existing service, use its **Networking** tab.

```sh
synctl services expose game udp -p arcade --public                    # an automatic port (20000–20999)
synctl services expose mail smtp -p post --public-port 25              # the port clients expect
synctl services expose mqtt tcp -p iot --public --allow 203.0.113.0/24
synctl services expose mail smtp -p post --public-port 0               # back to an automatic port
synctl services expose mqtt tcp -p iot --private                       # close it again
```

- **Automatic or chosen:**
  - Without `--public-port`, the port comes from the cluster's range (20000–20999 by default), and it stays with the service until you close it.
  - With `--public-port`, the service gets that number, ports below 1024 included, as long as it is free.
- **Allow list:** `--allow` limits clients to these addresses or CIDRs. It's enforced by Traefik and by the host firewall.
- **Range:** set it with the controller's `--public-ports LOW-HIGH` flag ([Configuration](../reference/configuration.md)).

Opening, closing or changing a public port restarts the Traefik replicas of the project's load balancer, which takes a few seconds. The dashboard warns before it does.

### Which ports can be chosen

The **port table** (**Network → Ports**, `synctl ports list`) records every public port on every load-balancer node. A port has one holder per node:

- Two projects that each have their own load balancer can both use 25.
- On the shared load balancer (the controller and the edge nodes), the second one is refused, and the message names the holder when it's in the same project.

These can never be chosen (`synctl ports list --reserved`):

| Port | Used by |
| --- | --- |
| 80/tcp, 443/tcp, 443/udp | Traefik (HTTP, HTTPS, HTTP/3) |
| 22/tcp, and any port sshd listens on | SSH |
| 51820/udp | WireGuard |
| 53/tcp+udp | the agent's DNS |
| 7443, 7444/tcp | the agent gateway |
| 8082/tcp | Traefik's admin port |
| 5432, 6379/tcp | databases by name |
| 20000–20999, 21000–21999 | automatic ports only |

A port that another program already listens on, on one of the load balancer's nodes, is refused too. Check a port before you use it:

```sh
synctl ports check 25 -p post
synctl ports check 19132/udp -p arcade
```

If a port is taken on the shared load balancer, give the project its [own load balancer](../operations/nodes-and-pools.md#project-load-balancers). Changing a project's load balancer is refused if one of its ports is taken on a new node.

### Real client IP (PROXY protocol)

By default the app sees the load balancer as the client. Turn on **PROXY protocol** for a TCP port, and Traefik sends the client's address first (PROXY protocol v2). Mail servers need it for their spam checks.

```sh
synctl services expose mail smtp -p post --proxy-protocol on
```

**The app must support it, or every connection fails.** For example:

- Postfix: `postscreen_upstream_proxy_protocol = haproxy`, or `smtpd_upstream_proxy_protocol = haproxy`
- Dovecot: `haproxy_trusted_networks`
- nginx: `listen … proxy_protocol`

UDP ports can't use it.

### TLS at the load balancer

For ports that start with TLS (SMTPS 465, IMAPS 993, MQTTS 8883), Traefik can handle TLS with the platform's certificate and send plain TCP to the app, so the app needs no certificate:

```sh
synctl services expose mail smtps -p post --public-port 465 --tls terminate
```

It needs the platform's base domain (Settings → Domain), for the certificate. Only TLS connections are accepted on that port then. Leave it off for ports that upgrade to TLS themselves (SMTP 25 and 587 with STARTTLS, IMAP 143).

### Mail servers

A mail server needs a few things outside SynCloud:

- an **MX** record for your domain pointing at the server's name;
- **SPF** and **DKIM** records in your DNS;
- **reverse DNS (PTR)** for the load balancer's IP, set at your provider.

Many providers (Hetzner, DigitalOcean) block **outgoing** port 25 by default. Sending needs a relay or an unblock request.

## Private access

Inside the cluster, every service is reachable at `<service>.<environment>.<project>.syncloud.internal` (or just `<service>` within its environment), on every port, whether public or not. [Security groups](network-security.md) decide who may connect.

---

Next: [Scaling and placement](scaling.md)
