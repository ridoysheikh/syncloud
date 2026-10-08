# Building from source

## What you need

- Go (the version in `go.mod`)
- Node.js 24 and pnpm (for the dashboard)
- Docker, for the agent, the end-to-end tests and the PostgreSQL images
- Linux. The agent manages Docker, WireGuard and nftables, so it runs on Linux only. synctl builds everywhere.

## Layout

| Path | What |
| --- | --- |
| `cmd/controller`, `cmd/agent`, `cmd/synctl` | the three binaries |
| `internal/` | everything else, one package per concern (`workload`, `dbs`, `builds`, `iam`, `mesh`, `traefik`, …) |
| `web/` | the dashboard (React, Vite, Tailwind). It's built into `internal/web/dist` and embedded in the controller. |
| `proto/` | the agent ↔ controller protocol (`make proto` regenerates `internal/gen`) |
| `images/postgres` | the managed PostgreSQL image |
| `sdk/` | the Go and TypeScript SDKs |
| `scripts/` | `install.sh` and `release.sh` |
| `tools/imagepack` | packs a Docker image into a release archive |
| `test/` | end-to-end, load and VM tests |
| `plan/PLAN.md` | the design, decisions and progress |

## Build

```sh
make build            # dashboard + bin/syncloud-controller, bin/syncloud-agent, bin/synctl
make postgres-image   # syncloud-postgres:<tag> for each major version
make release VERSION=0.1.0 RELEASE_IMAGES=0   # everything a release carries, into dist/
```

## Run it locally

```sh
make dev
```

This starts:

- the controller in development mode on `:7070`, with its state in `.data/`;
- its own agent as `ctl-0`;
- Vite on `:5173`, with hot reload.

Open http://localhost:5173. Development mode uses local addresses and no ACME. Services may run on the controller node.

For PostgreSQL in development, build the images, then point the controller at them:

```sh
make postgres-image
go run ./cmd/controller --dev --data-dir .data --postgres-image 18=syncloud-postgres:18-r1,17=syncloud-postgres:17-r2
```

## Checks before a pull request

```sh
make fmt vet test      # gofmt, go vet + TypeScript, go test -race
```

CI runs the same checks on every push and pull request.

---

Next: [Testing](testing.md) · [Releasing](releasing.md)
