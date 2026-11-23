# Domains, DNS and certificates

One place for every domain, its DNS records and every TLS certificate: **Domains** in the dashboard, `synctl domains`, `synctl dns` and `synctl certs`, and an API your own apps can call.

SynCloud **manages and instructs**; it is not a name server. You keep your DNS where it is. With **Cloudflare** connected, SynCloud writes the records itself; with any other DNS host, it shows exactly which records to create and checks them live.

## Register a domain

**Domains → Add domain.** Pick where its DNS lives:

| | What SynCloud does |
| --- | --- |
| **Cloudflare** (a connected provider) | Creates the records a service needs, removes duplicates, and writes and removes the TXT records for wildcard certificates. |
| **Manual** (any DNS host) | Shows the records to create, checks them live, and asks you to add a TXT record for a wildcard certificate (again at each renewal). |

```sh
synctl dns providers add cloudflare --token "$CLOUDFLARE_TOKEN"
synctl domains add example.com --provider dnsp_ab12      # managed
synctl domains add example.org                            # manual
```

A new domain can be used by **all projects**, wildcards included. Narrow it any time to chosen projects (and whether they may use wildcards): **Domain → Access**, or `synctl domains update example.com --project shop --project blog`. Names outside every registered domain stay free for everyone, as before, and a service that already uses a name keeps working: grants only decide who may *add* names. The platform's own names (the base domain, `registry.`, `git.`, `agent.`, `db.`) are never available.

### The Cloudflare token

Create it at **My Profile → API Tokens** with `Zone · Zone · Read` and `Zone · DNS · Edit` on the zones SynCloud should manage. Add `Zone · SSL and Certificates · Edit` to issue origin certificates. The token is checked when you connect it, sealed in the database and never shown again; **DNS providers → Replace token** swaps it. Disconnecting a provider leaves its domains as manual ones and keeps their certificates.

## DNS records

**Domain → Records** lists, adds, edits and deletes A, AAAA, CNAME, TXT, MX, SRV, CAA and NS records (with TTL and Cloudflare's proxy switch). A record SynCloud created is marked; deleting the address of a service domain asks first.

**Point at the cluster** creates the records a name needs to reach your services (one A record for each of the project's load balancers, or the controller):

```sh
synctl dns point example.com shop --plan            # what would change
synctl dns point example.com shop                   # do it
synctl dns point example.com shop --replace         # also remove records SynCloud did not create that are in the way
```

### Without duplicates

Pointing works from what the name needs, not from what you clicked:

1. A record that is already right is **kept**: no churn, no TTL reset.
2. A record that is missing is **created**.
3. **Exact duplicates** (same type, name and content) collapse to one.
4. A record **SynCloud created earlier** that is no longer needed (the old address after a project moved its load balancers) is **removed**.
5. A record **it did not create** that is in the way is only listed, with the reason. It is removed only when you confirm (`--replace`, or the checkbox in the dashboard). Nothing of yours is ever removed silently.
6. A CNAME is never left beside other records on the same name.
7. Running it again changes nothing.

**Clean up** (`synctl dns cleanup example.com --apply`) removes exact duplicates and the `_acme-challenge` records SynCloud left behind. Another tool's challenge records are listed and never touched.

### Look a name up

**Domains → DNS lookup**, or `synctl dns lookup shop.example.com A`, asks public resolvers and the zone's own servers. It is how you see whether a TXT record has spread.

## Wildcards on services

A service can serve **`*.example.com`** or **`*.sub.example.com`**. In the service's **Networking → Custom domains**, choose the domain, a subdomain, and switch **Wildcard** on; or:

```sh
synctl services domains add web '*.example.com' -p shop
synctl services domains add web '*.sub.example.com' -p shop
```

- **One label.** `*.example.com` matches `a.example.com`, not `example.com` and not `a.b.example.com` (a TLS wildcard covers one label, and so does the route). Add `example.com` as its own host, and `*.b.example.com` for the next level.
- **An exact host always wins.** `api.example.com` on one service beats `*.example.com` on another, whatever their lengths. The more specific wildcard wins among wildcards. Paths, prefix stripping and redirects work as on any host, and the app sees the real host, so multi-tenant apps can read the label.
- **Refused:** a bare `*`, a wildcard in the middle (`a.*.example.com`), `*.localhost`, a wildcard that would cover the platform's own names, and the same host and path on two services.
- **DNS.** A wildcard DNS record (`*.example.com A …`) answers every name below it, also `a.b.example.com`, which the certificate does not cover. The check resolves a random label to see whether the wildcard points here.
- **Certificate.** A wildcard host needs a wildcard certificate, which needs DNS validation (below). Until it exists, HTTPS uses a placeholder, and the page says so.

## Certificates

**Domains → SSL certificates** lists every certificate: the ones made for your routes (automatic, as before) and the ones you asked for. Each has its names, issuer, method, expiry, the hosts it serves and a history.

### Where a certificate comes from

| Source | |
| --- | --- |
| **Let's Encrypt** (default) | Free, trusted, renewed by itself. HTTP-01, or DNS-01 for wildcards. |
| **Another ACME authority** | ZeroSSL, Google Trust Services, SSL.com, Buypass or any CA with an ACME directory, usually with an external account binding (a key id and an HMAC key from the provider's dashboard). **Domains → Authorities → Add authority**. |
| **Cloudflare origin CA** | Long-lived certificates for hosts behind Cloudflare's proxy. **Not trusted by browsers**: works only with SSL mode "Full (strict)". |
| **Imported** | A certificate you bought or got from your host: paste or upload the chain and the key (PEM or PKCS#12), or make a **request (CSR)** here, give it to the provider and upload what they sign. The key never leaves SynCloud. It does not renew by itself: an alert warns before it expires, and **Replace** keeps its place. |
| **Self-signed** | For private or internal names, under *Advanced*. Not trusted by browsers, never chosen by itself. |

```sh
synctl certs request example.com '*.example.com'
synctl certs request '*.sub.example.com' --authority ca_ab12 --key-type rsa-2048
synctl certs import --chain fullchain.pem --key privkey.pem
synctl certs csr new mail.example.com                    # prints the CSR for your provider
synctl certs csr complete csr_ab12cd --chain signed.pem
synctl certs authorities add ZeroSSL --preset zerossl --eab-kid KID --eab-hmac HMAC
```

Let's Encrypt keeps working as before: its account is adopted as it is, existing certificates become "Let's Encrypt, automatic", and nothing is reissued. Choose another authority for a whole domain (**Access → Certificate authority**), or per certificate. Set a **fallback authority** and an order is retried there when the first one fails repeatedly or is rate limited.

### Wildcard certificates: the `_acme-challenge` record

A wildcard name can only be proved with DNS (DNS-01): the authority wants a TXT record at **`_acme-challenge.<name without the *.>`**.

| Name on the certificate | TXT record |
| --- | --- |
| `example.com` and `*.example.com` | `_acme-challenge.example.com` (two values) |
| `*.sub.example.com` | `_acme-challenge.sub.example.com` |

A name's challenge is answered in one of four ways. The **Challenge** tab of a domain shows which, and lets you change it.

| Way | When | What you do | Renewals |
| --- | --- | --- | --- |
| **SynCloud's challenge server** (*builtin*) | the server is on (one click); the default for a domain without a provider | create one CNAME, once | automatic |
| **Through the provider** (*direct*) | the domain has a DNS provider | nothing | automatic |
| **Another domain you own** (*delegated*), optional | a validation domain that has a provider | create one CNAME, once | automatic |
| **By hand** (*manual*) | none of the above | add each TXT record | a person, about every 60 days |

The first match wins: an external validation domain you chose, then SynCloud's server (chosen for the domain, or used because it has no provider), then the domain's provider, then by hand. So a provider keeps working with nothing to set up, a domain without one is answered by SynCloud once the server is on, and the external way is always a choice.

### SynCloud's own challenge server

Any domain, at any registrar, can renew wildcard certificates by itself if SynCloud answers the challenge. Turn it on once for the whole cluster with one click: **Domains → Challenge server → Turn on** (or **Turn on** on any domain's Challenge tab, or `synctl domains challenge-server on`). There is nothing to type and nothing to create at any DNS host.

It works because of sslip.io. sslip.io already hands every name under `_acme-challenge.<a-b-c-d>.sslip.io` to the address `a.b.c.d`. So when the controller at `203.0.113.9` listens on port 53, it is the server for `_acme-challenge.203-0-113-9.sslip.io` automatically.

Then each domain needs only one CNAME, once. The domain's Challenge tab and the certificate's Challenge page show it:

```text
_acme-challenge.example.org   CNAME   3f9a1c07d2be._acme-challenge.203-0-113-9.sslip.io
```

A certificate for `*.example.org` then goes like this:

1. Let's Encrypt asks for the TXT record at `_acme-challenge.example.org`.
2. Your DNS host answers with the CNAME. sslip.io sends the question on to `203.0.113.9`.
3. SynCloud answers with the value of the open order, and the certificate is issued.

At every renewal the value is new. SynCloud stores it itself and the CNAME stays the same, so renewals need nobody.

- `example.org` and `*.example.org` share one CNAME. A nested wildcard such as `*.shop.example.org` uses `_acme-challenge.shop.example.org`, which has its own CNAME.
- The controller's own `*.203-0-113-9.sslip.io` also gets a real wildcard certificate this way, with no CNAME at all. Its challenge name is the zone itself.
- **Port 53** is open by default in SynCloud's own host firewall (UDP and TCP on the controller, from anywhere). Open UDP and TCP 53 in your cloud provider's firewall or security group too, and make sure nothing else listens on that port; the installer warns when something does. The listen address is set with `--acme-dns-listen` (default `:53`). If the port is busy, the page shows it, and turning the server on again retries.
- **Check it works** sends a probe value through public resolvers. The probe can only come back if sslip.io's delegation reaches this controller and port 53 is open.
- **If the public address changes**, SynCloud moves the zone within 10 minutes. Each domain's Challenge tab then shows the new CNAME to set. Behind NAT, give the address by hand (**Behind NAT? Give the public address**, or `--ip`); an address given by hand is kept.
- The server keeps values in its own store, so an order survives a restart, and removes them afterwards. It answers TXT, and the zone's SOA and NS, for its zone only. It refuses every other question, so it is not a name server for your domains and not an open resolver.
- Turning it off sends domains back to adding the TXT record by hand. Certificates that already exist keep serving.
- It depends on sslip.io answering, as the default base domain already does. A renewal that fails keeps retrying for weeks before the certificate expires.

- **Through the provider:** SynCloud writes the TXT record, waits until DNS answers, finishes the order and removes it. If the name already is a CNAME that you made, the record is written where it points (when that is a domain SynCloud can write), because a TXT cannot sit beside a CNAME.
- **Another domain you own (optional):** the external way, for when you would rather not open port 53 or run the server: a domain at a registrar or DNS host SynCloud cannot write still renews by itself. Pick a registered domain with a provider as the domain's **validation domain** (`synctl domains update example.org --challenge-zone example.com`). The authority is sent there with one CNAME:

  ```text
  _acme-challenge.example.org   CNAME   3f9a1c07d2be.acme.example.com
  ```

  SynCloud then writes and removes the TXT record at `3f9a1c07d2be.acme.example.com` itself, at the first certificate and at every renewal. The CNAME is set **once**: `example.org` and `*.example.org` share it, while a nested wildcard such as `*.shop.example.org` has its own name and target (the **Check a name** box on the tab shows each). If the domain being delegated also has a provider, SynCloud creates the CNAME for you (**Create the CNAME for me**, or `synctl domains delegate`); on a manual domain you create that one record at your DNS host. Until it is visible the certificate's order waits, shows exactly this record, and finishes the moment resolvers answer. Any domain you own can be the validation domain; it need not be related. A name that is a CNAME cannot hold other records, so an existing record on that name is only replaced when you confirm.
- **By hand:** the certificate's **Challenge** tab shows each name and value to copy, what public resolvers and the zone's own servers answer, and **Verify now**. SynCloud also checks every minute and finishes the order as soon as every record is visible. The order stays open a week. Every renewal has a new value, so a certificate that cannot renew by itself raises the *Certificate expiring* alert 14 days (or whatever you set) before it expires. Connecting Cloudflare, or delegating with one CNAME, makes it automatic.
- sslip.io and nip.io names cannot use DNS-01 (you cannot add records there): they stay on per-host HTTP-01. The one exception is the controller's own `<ip>.sslip.io` while SynCloud's challenge server is on (see above).

Check any name from the CLI or API (the same answer for every way): `synctl domains challenge example.org --name '*.shop.example.org'` (`GET /api/v1/domains/{zone}/challenge?name=`) says which way it is answered, the name and target, and whether public resolvers show the CNAME.

A domain can **keep one wildcard certificate** (`example.com` and `*.example.com`): **Domain → Access**, or `--wildcard-cert`. For the platform's own generated addresses, **SSL certificates → Use one wildcard certificate** replaces the per-address certificates with `*.<base domain>`; those that serve traffic stay until the new one is valid.

### Which certificate serves a name

For each host the controller hands Traefik one certificate: the one **pinned** to the host, else a valid one with the exact name, else the narrowest valid wildcard, else the latest-expiring. An expired certificate is never chosen while a valid one covers the name. A host with none keeps its own automatic certificate, as before.

### Lifecycle

**Renew now**, **Reissue with other names** (the old certificate serves until the new one is valid), **Revoke** (at the authority for ACME; an imported one just stops being served), **Delete**, an **Auto-renew** switch, the **key type** (ECDSA P-256 by default; RSA 2048, 3072 or 4096) from the next issuance, and **alert days** (30, 14, 7 and 1 by default). The **History** tab keeps orders, challenges, renewals, failures with the authority's message, revocations, imports and exports for a year; the same actions are in the audit log.

## Using it from your apps

Everything above is in the API: `synctl api`, the Go SDK (`client.CreateDomainRecord`, `PointDomain`, `RequestCertificate`, …) and the TypeScript SDK (`sc.domains.createRecord(...)`, `sc.certificates.request(...)`).

Give an app its own key the usual way: create a **service account**, attach a policy, create an **access key** (IAM, or `synctl iam`). Actions are `domain:*`, `dns:*` and `certificate:*`; resources are `srn:syncloud:domain/<zone>/record/<name>` and `srn:syncloud:certificate/<zone>/<id>`; a condition `syn:DnsRecordType` limits a key to some record types.

Managed policies: **DomainAdministrator**, **DomainEditor:`example.com`** (one domain's records and certificates) and **AcmeChallengeWriter:`example.com`** (create and delete TXT records named `_acme-challenge.*`, for an app doing its own DNS-01). ReadOnly lists and looks up but cannot change anything.

```json
{ "Version": "2026-01", "Statement": [{
    "Effect": "Allow",
    "Action": ["domain:CreateDomainRecord", "domain:DeleteDomainRecord", "domain:ListDomainRecords"],
    "Resource": "srn:syncloud:domain/example.com/record/*.apps.example.com",
    "Condition": { "StringEquals": { "syn:DnsRecordType": ["A", "CNAME"] } }
}]}
```

That key can register its own subdomains under `apps.example.com`, and nothing else. An app that watches records gets `domain:ListDomainRecords` and `dns:QueryDns` only.

### Exporting a private key

An app that terminates TLS itself (a mail server) can fetch a certificate and its key: `GET /api/v1/certificates/{id}/bundle`, `synctl certs export ID --cert … --key …`. It needs **`certificate:ExportCertificate`**, which no managed policy but `AdministratorAccess` includes: it is off until you grant it. Every export is audited, written to the certificate's history and limited to 10 an hour per person. The key is otherwise never returned by any call.

## Limits to know

- Let's Encrypt limits certificates per registered domain per week; one wildcard certificate replaces many per-host ones.
- Cloudflare's proxy ("orange cloud") hides the origin: HTTP-01 may then fail, so use DNS-01, and turn on **Network → Traefik → Trusted proxies → Cloudflare ranges** to keep real client IPs.
- SynCloud is not a name server: name servers and registrar settings stay with you. Delegating `_acme-challenge` does not change that: the CNAME points at a name in a domain you already keep at a provider.
- SynCloud's server needs UDP and TCP port 53 reachable from the internet on the controller's address, and a challenge zone that nothing else uses. Without those, use the provider or the external way.
- A delegated challenge needs the validation domain's provider to stay connected. If it is removed the domain falls back to *by hand* (the Challenge tab says so), and the certificate keeps serving until it expires.
- Cloudflare is the first provider. Others (Route 53, DigitalOcean, Hetzner) can follow behind the same interface; any domain works without one.
