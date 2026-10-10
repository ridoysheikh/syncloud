# Services

## The building blocks

| Thing | What it is |
| --- | --- |
| **Project** | A group of apps that belong together (`shop`). It is the unit of access control and quotas. |
| **Environment** | A copy of the project's world (`production`, `staging`), with its own services, variables and DNS names. |
| **Service** | A container image plus settings, kept running as N **tasks**. |
| **Task** | One running container on one node. |
| **Revision** | A numbered snapshot of a service's settings. Every change creates one; deploys and rollbacks move between them. |

Services only exist inside a project environment. Create them with **New service** in the project, or `synctl services run` / `synctl services apply`.

## Settings

| Setting | Notes |
| --- | --- |
| **Image** | Any registry image (`nginx:1.27`, `ghcr.io/acme/api:v2`), or `@registry/<project>/<name>:<tag>` from the built-in registry. Private registries: **Registry → Upstreams**. |
| **Command / entrypoint** | Override the image's. |
| **Ports** | Each has a name, a container port and a protocol: `http` (gets an HTTPS address), `tcp` or `udp`. No ports makes a worker. |
| **Health check** | `http` (a path), `tcp` (the port accepts connections) or `cmd` (exit 0). Only healthy tasks get traffic, and unhealthy ones are replaced. |
| **Resources** | CPU and memory per task, each **shared** (the default: placement goes by real usage) or **reserved** (set aside on the node). Optional hard limits; the memory limit defaults to twice the memory. See [Size](scaling.md#size). |
| **Desired count** | How many tasks. Scale with the **±** buttons, `synctl services scale`, or [autoscaling](scaling.md). |
| **Placement** | Spread across nodes (default) or pack, and optionally only some nodes ([Scaling and placement](scaling.md)). |

As JSON, for `synctl services apply -f`:

```json
{
  "name": "api",
  "image": "ghcr.io/acme/api:1.4.0",
  "ports": [{ "name": "http", "container": 8080 }],
  "env": { "LOG_LEVEL": "info" },
  "resources": { "cpu": 0.25, "memory": 256 },
  "health": { "type": "http", "path": "/healthz" },
  "desiredCount": 2
}
```

## Variables

A service's environment variables come from three places. Later ones win:

1. **Shared variables** of its environment: the project's **Shared variables** tab, or `synctl envs set KEY=VALUE`. Every service in the environment inherits them.
2. **S3 bindings:** `S3_BUCKET`, `AWS_ACCESS_KEY_ID` and the rest ([S3 storage](storage.md)).
3. **The service's own variables:** its **Variables** tab, which also takes a pasted `.env` file.

Changing a variable rolls out a new revision. Values are masked in the dashboard. They're stored in the controller's database, which goes into the encrypted [controller backups](../operations/backup-restore.md).

Database credentials come ready to paste: `synctl db credentials NAME` prints the URLs.

## How services find each other

Every service has a stable internal name and a virtual IP, load-balanced across its healthy tasks from every node:

```
<service>.<environment>.<project>.syncloud.internal
```

Inside the same environment, the short name works too: `http://api:8080`. Traffic between services goes over the private network and follows [security groups](network-security.md): by default, services of the same environment can reach each other.

## Day-to-day

| To… | Dashboard | CLI |
| --- | --- | --- |
| see tasks and where they run | **Tasks** tab | `synctl services tasks api` |
| read logs | **Logs** tab | `synctl logs -f api` |
| open a shell in a task | **Tasks → ⌨** | `synctl exec service/api -- sh` |
| restart every task (same settings) | **Redeploy** | `synctl services redeploy api` |
| replace one task | **Tasks → ↻** | `synctl tasks restart TASK` |
| stop it (keep the settings) | scale to 0 | `synctl services scale api=0` |
| delete it | **Delete** | `synctl services delete api` |

## Environments

Create one empty, or copy another: its shared variables, services (stopped unless you ask), jobs and security group memberships come along. Domains, public ports, Git sources, S3 bindings and databases do not.

```sh
synctl envs create staging -p shop --from production
```

Project **Settings → Environments** also lets you **lock deploys** and turn off automatic deploys from builds, per environment ([Deployments](deployments.md#locking-an-environment)).

---

Next: [Deployments and rollbacks](deployments.md)
