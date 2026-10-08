# Release commands and jobs

## Release commands (migrations before each deploy)

A **pre-deploy** command runs in the new revision's image before any task is replaced, for example a database migration. A **post-deploy** command runs after the rollout, for example a smoke test or a cache warm-up.

Set them on the service's **Deploy** tab. Each command has a timeout and a retry count, and they run one at a time, in order.

| | Pre-deploy | Post-deploy |
| --- | --- | --- |
| Runs | before the rollout | after it succeeds |
| Gets | the new revision's image, variables, network and security groups | the same |
| If it fails | the deployment stops; the running version stays | the deployment stays; the failure shows on its timeline |

The command runs with `sh -c`, so `npm run migrate && npm run seed` works whatever the image's entrypoint.

**A new service waits for its migrations.** Give a release command in the new-service wizard, or as `releaseCommands` when creating a service. The service's first tasks start only after it passes:

```json
{
  "name": "api",
  "image": "ghcr.io/acme/api:1.0.0",
  "releaseCommands": {
    "preDeploy": [{ "command": "npm run migrate", "timeout": 600, "retries": 1 }]
  }
}
```

Each deployment's page shows its commands' runs and logs.

**Rollbacks skip pre-deploy commands** unless you ask for them ([Roll back](deployments.md#roll-back)).

## After-build checks (Git services)

For a service [built from Git](git-builds.md), checks can run inside the freshly built image **before** it's deployed, for example `npm test` or `./manage.py check --deploy`. A failing check marks the build failed, and nothing is deployed. Set them on the **Builds** tab, or with:

```sh
synctl builds configure api -p shop --check "npm test" --check "npm run lint"
```

## Jobs

A job runs a command with a service's image and settings, as a short-lived task.

| Kind | When it runs |
| --- | --- |
| **Scheduled** | on a cron schedule, in a time zone you choose |
| **One-off** | when you start it |
| **Pre- and post-deploy** | the release commands above |

Manage jobs on the project's **Jobs** page, or:

```sh
echo '{"name":"nightly-report","service":"api","command":["node","report.js"],
       "schedule":"0 2 * * *","timezone":"Europe/Berlin"}' | synctl jobs apply -p shop -f -
synctl jobs run nightly-report -p shop
synctl jobs runs nightly-report -p shop     # history, with logs
```

Runs follow the service's placement and security groups, can retry, have a timeout, and keep their logs.

## Run a command once

To run a one-off command with a service's image and variables, such as a migration by hand or a console:

```sh
synctl run service/api -p shop -- rails db:migrate
```

To get a shell in a running task instead:

```sh
synctl exec service/api -p shop -- sh
```

---

Next: [Git builds](git-builds.md)
