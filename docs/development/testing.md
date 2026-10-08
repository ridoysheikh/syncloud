# Testing

Every change should pass the unit tests. Changes to how things run on nodes also get an end-to-end test.

## Unit tests

```sh
make test        # go test ./...
cd web && npx tsc --noEmit
```

`internal/api` has two consistency tests:

- every route matches the OpenAPI spec (`spec_test`);
- every API operation has a synctl command (`internal/cli/cli_test`).

## End-to-end tests

`make e2e` runs every script in `test/e2e/`. Each script builds the binaries and starts three Docker-in-Docker nodes (`sc-e2e-ctl`, `sc-e2e-w1`, `sc-e2e-w2`) on an isolated Docker network. Nothing touches the host's networking. You need Docker with privileged containers.

Run one script with `test/e2e/<name>.sh`. Add `KEEP=1` to leave the nodes running for inspection.

| Script | Covers |
| --- | --- |
| `mesh.sh` | WireGuard mesh, host firewall |
| `services.sh`, `deploy.sh`, `lifecycle.sh` | Services, rolling deploys, circuit breaker, jobs, lifecycle |
| `registry.sh`, `builds.sh` | Private registry, Git builds |
| `traffic.sh`, `metrics.sh`, `autoscale.sh`, `alerts.sh` | Traefik routing, metrics, autoscaling, alerts |
| `secgroups.sh`, `routing.sh` | Security groups, IPAM/DNS, middlewares |
| `iam.sh` | IAM, quotas, Cloud Shell |
| `pools.sh` | Node pools with a fake cloud (`test/e2e/cloudsim`), cluster autoscaling, edge nodes |
| `integrations.sh` | A real Gitea connected with a token: repositories and branches listed, the push webhook created automatically, builds reported as commit statuses, pushes delivered through Traefik with the global Traefik settings |
| `gitserver.sh` | The built-in Forgejo turned on with the real system tasks: provisioned and connected, a service built from it by webhook with commit statuses, turned off (data kept) and on again |
| `storage.sh` | MinIO deployed as a service and registered as an S3 endpoint, the bucket browser, bindings, the metrics explorer |
| `upgrade.sh` | Controller upgrade and rollback, agent rollout and rollback, uninstall |
| `restore.sh` | Restore drill on a new host with a different IP |
| `chaos.sh` | A worker freezes and returns, the controller is killed, the mesh is partitioned |

## Load test

```sh
test/load/run.sh                         # 50 nodes, 40 services × 50 tasks
NODES=100 SERVICES=80 test/load/run.sh
```

The controller runs in a container limited to 4 CPUs and 8 GB. `test/load/fakeagent` simulates the nodes: each one joins, keeps an mTLS stream, sends heartbeats and reports its tasks as running, without Docker.

The script prints:

- how long scheduling takes,
- the controller's CPU and memory at steady state,
- API latency (p50 and p95),
- the duration of a rolling update.

## VM lab: a real small cluster on one machine

`test/vmlab/vmlab.sh` runs three Ubuntu 24.04 VMs under QEMU/KVM. It is rootless: no bridges and no sudo on the host. SynCloud is installed with the real `install.sh`, systemd units, kernel WireGuard and the system tasks, on deliberately small machines:

| VM | Address | Size | Role |
| --- | --- | --- | --- |
| ctl | 10.77.0.11 | 2 vCPU, 2 GB | controller and ctl-0 |
| w1 | 10.77.0.12 | 1 vCPU, 1 GB | worker |
| w2 | 10.77.0.13 | 1 vCPU, 1 GB | worker |

```sh
test/vmlab/vmlab.sh up        # download the image once, start the VMs
test/vmlab/vmlab.sh install   # build, install, create the admin, join w1 and w2
test/vmlab/vmlab.sh deploy    # after code changes: rebuild and replace the binaries
test/vmlab/vmlab.sh ssh w1    # or: ssh ctl -- journalctl -u syncloud-controller
test/vmlab/vmlab.sh down      # stop (disks kept); destroy deletes them
```

- **State:** VMs, disks and keys live in `~/.cache/syncloud-vmlab`.
- **Host ports:** SSH is forwarded to `127.0.0.1:2221` to `2223`. The controller's ports 80 and 443 are forwarded to `127.0.0.1:18080` and `18443`.
- **Private network:** the VMs reach each other over a private network (a QEMU multicast socket) and the internet over QEMU user networking. There is no public IP, so ACME cannot issue certificates. The lab therefore also exercises the self-signed path:
  - pinned join commands;
  - the registry certificate handed to nodes;
  - the trust command for `docker push`.
- **Sample app:** `test/vmlab/hello` is a small Go API built into an image. It exposes `/`, `/healthz`, `/burn?ms=` (CPU, for autoscaling) and `/fail` (500).

### What it found (2026-10-07)

All of the following are fixed:

- **Join commands on private networks:** they failed on the self-signed certificate. Join commands now pin the controller's public key (`curl --pinnedpubkey`, `join.sh --pin` and `syncloud-agent join --pin`) instead of skipping the check.
- **Registry pushes and pulls on private networks:** Docker refused the self-signed registry, and the token endpoint on the dashboard host as well. Nodes now get the certificate bundle with each pull and install it under `/etc/docker/certs.d`. BuildKit gets it through its `buildkitd` config. **Registry → Push an image** shows a one-paste trust command for other machines.
- **Built-in Git server on private networks:** clones, polls and Forgejo's webhooks failed for the same reason. The controller's poller, the builds and Forgejo itself (through a mounted trust directory) now trust the cluster's own self-signed certificates.
- **Zero-downtime rollouts:** about one request per rollout hung for 5 s. Retired tasks were stopped while Traefik, which polls every 2 s, still routed to them. Tasks now leave the routes, keep running for `deployment.drainSeconds` (default 5), then stop. Measured result: 0 failures in 1,250 requests over three rollouts.
- **Rollout placement:** after a rollout, every task could end up on one node, because spreading counted the old tasks being replaced. Only the new revision counts now.
- **Worker crashes:** a hard-killed worker cost 9 of 300 requests. Traefik now probes each task's HTTP health check every 2 s, and its connect timeout to tasks is 2 s (it was 30 s), so the retry moves quickly. Result: 1 failure, the request in flight at the moment of the crash.
- **Builds on small nodes:** builds never started on 1-vCPU workers, because they reserved 1 CPU and 1 GB. They now reserve 0.25 CPU and 512 MB, with CPU unlimited. They may run on the controller node (BuildKit's default place), and a build waiting for room says so.
- **Two commits built at once:** the older one could be deployed last and win. A build is no longer auto-deployed when a newer commit of its branch is already deployed.
- **Installer and dashboard:** the installer refused anything under 4 GB, while the docs say 2 GB. The platform uses about 650 MB, so 2 GB now warns and 1 vCPU / 1 GB workers are fine. The Overview's Services tile showed a leftover placeholder.

**Measured on the lab:** ~1 ms p50 and ~2 ms p95 through Traefik over WireGuard (0.4 ms RTT between VMs); about 650 MB used on the controller VM and about 300 MB per worker.
