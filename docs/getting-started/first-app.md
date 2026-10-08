# Your first app

Everything you deploy lives in a **project** (for example `shop`), which has **environments** (`production`, `staging`), which hold **services**. A service is a container image with settings, run as one or more **tasks** across your nodes.

## 1. Create a project

**Projects → New project**, or:

```sh
synctl projects create shop          # comes with a "production" environment
```

## 2. Create a service

In the project, choose **New service**. The wizard has four steps:

1. **Source:** a container image (`nginx:1.27`, `ghcr.io/acme/api:v2`), or a Git repository that SynCloud builds for you.
2. **Service:** the name, the network (public HTTP, internal TCP, or a worker with no port), the container port, the health check path, the size and the number of tasks, plus an optional **release command** such as a database migration.
3. **Variables:** environment variables. The environment's shared variables are inherited.
4. **Review:** check the settings, then **Create**.

Or from the CLI:

```sh
synctl services run web -p shop --image nginx:1.27 --port 80 --replicas 2
```

## 3. Open it

A public HTTP service gets an address at once:

```
https://web-production-shop.<base-domain>
```

The service page shows it under **Endpoints**, together with the internal name other services use (`web.production.shop.syncloud.internal`). The **Tasks** tab shows where each task runs; **Logs** and **Metrics** show what it's doing.

## 4. Change it

Edit the image, variables, ports or size, and SynCloud rolls out a new **revision**. A new task starts, passes its health check, then an old one stops. If the new revision keeps failing, the rollout stops and rolls back by itself.

```sh
synctl services run web -p shop --image nginx:1.28 --port 80 --replicas 2
synctl services deployments web -p shop     # what happened
```

## 5. Build from Git instead

Choose **Git repository** as the source in the wizard, or connect an existing service:

```sh
synctl builds connect api -p shop --url https://github.com/acme/api.git --branch main
```

SynCloud builds each new commit with your Dockerfile, or with Nixpacks if there's none, and deploys it. Connect GitHub, GitLab or Gitea once under **Integrations** to pick repositories by name and get webhooks and commit statuses ([Git builds](../guides/git-builds.md)).

## 6. Use your own domain

On the service's **Networking** tab, add `shop.example.com`, then create the DNS record the page shows. The certificate is issued as soon as DNS points at the cluster ([Domains and ports](../guides/domains-and-ports.md)).

---

Next: [Add nodes](add-nodes.md) · Learn more: [Services](../guides/services.md), [Deployments](../guides/deployments.md)
