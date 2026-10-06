# Testing

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
