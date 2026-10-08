# synctl reference

`synctl --help` and `synctl <command> --help` are always current. This page is a map of the commands.

## Global flags

| Flag | Meaning |
| --- | --- |
| `-p, --project` | project (or `$SYNCLOUD_PROJECT`) |
| `-e, --env` | environment (or `$SYNCLOUD_ENV`; default `production`) |
| `-o, --output` | `table` (default) or `json` |
| `--profile` | credentials profile (or `$SYNCLOUD_PROFILE`) |
| `--endpoint` | controller URL, overriding the profile |

## Commands

| Area | Commands |
| --- | --- |
| **Sign in** | `login`, `configure`, `whoami`, `status`, `version` |
| **Projects** | `projects create/list/update/delete/nodes/addresses/deployments`, `envs create/list/delete/lock/unlock/auto-deploy/set/unset/vars` |
| **Services** | `services run/apply/list/scale/delete/redeploy/rollback/revisions/deployments/deployment/cancel-deployment/tasks/routing/expose`, `services domains add/list/remove/check` |
| **Running things** | `tasks list/restart`, `exec`, `run`, `logs`, `jobs apply/list/run/runs/delete`, `runs get/cancel` |
| **Builds** | `builds connect/disconnect/run/list/deploy/source/sources/settings/configure` |
| **Scaling** | `autoscale set/get/off/history`, `quota list/set/unset`, `usage` |
| **Databases** | `db engines/create/list/get/delete/credentials/network/failover/metrics`, Valkey: `db keys/key/cmd/info`, PostgreSQL: `db sql/roles/role/grant/revoke/privileges/databases/database/schema/describe/rows/extension/sessions/session/settings/config/addon/replication/backup/backups/restore` |
| **Storage** | `s3 endpoints/buckets/mb/ls/cp/rm/du/bindings` |
| **Registry** | `registry info/repos/images/delete/events/gc/lifecycle/upstreams` |
| **Networking** | `sg apply/get/list/delete/check/service`, `firewall apply/get/list/delete/effective/counters/drops`, `middlewares apply/list/delete`, `traefik settings`, `network`, `domain get/set`, `certificates list/renew` |
| **Observability** | `metrics`, `traffic`, `requests`, `health services/incidents`, `alerts rules/channels/active/events`, `audit` |
| **Access** | `iam users/groups/roles/policies/attach/detach/attachments/simulate/actions/access-keys/tokens/mfa/password/settings`, `sts assume-role` |
| **Cluster** | `nodes list/join-tokens/cordon/drain/delete/rejoin-command/shell/metrics/upgrade-agents`, `pools …`, `system health/tasks/upgrade`, `backups config/run/list/download`, `integrations git …`, `integrations git-server …` |
| **Anything else** | `api METHOD PATH [-d BODY]` sends a signed request to any endpoint |

Every API operation has a synctl command. A test in the repository keeps the two in step.

## Shell completion

```sh
synctl completion bash > /etc/bash_completion.d/synctl     # also zsh, fish, powershell
```
