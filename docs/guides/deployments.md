# Deployments and rollbacks

Every change to a service (a new image, a variable, a port, a build) creates a **revision** and rolls it out as a **deployment**.

## How a rollout works

1. Pre-deploy [release commands](release-commands.md) run first, if the service has any. If one fails, the deployment stops here and nothing changes.
2. A new task starts and must pass its health check.
3. An old task leaves the load balancer, keeps serving its in-flight requests for `drainSeconds` (5 by default), then stops.
4. Steps 2 and 3 repeat until every task runs the new revision.
5. Post-deploy commands run, for example smoke tests.

If new tasks keep failing, the **circuit breaker** stops the rollout and returns to the previous revision by itself. These are the `deployment` settings of a spec:

```json
"deployment": { "circuitBreaker": true, "rollback": true, "drainSeconds": 5 }
```

## History

Each service's **Deployments** tab, and the project's **Deployments** tab for all of its services, lists every rollout with:

- **status:** running pre-deploy, in progress, succeeded, failed, rolled back, superseded or cancelled;
- **trigger:** manual, Git push (with the commit), rollback, automatic rollback, variables, redeploy;
- **who** started it, when, and how long it took;
- **what changed:** the image, ports, resources, and variables by name (never their values).

Open one for its step-by-step timeline, the logs of its release commands, and its tasks.

```sh
synctl services deployments api -p shop
synctl services deployment api 42 -p shop      # changes, timeline, hook runs
synctl projects deployments shop
```

## Roll back

On the **Revisions** tab, choose **Roll back** next to the revision you want. Or run:

```sh
synctl services rollback api 7 -p shop
```

The old revision's settings roll out as a new deployment, with the same health checks and draining.

- **Release commands are skipped by default,** because migrations usually only move forward. Tick **Run pre-deploy jobs** (or pass `--run-hooks`) when the old revision has a down-migration to run.
- **Compare** next to any revision shows its differences from the current one.
- **The rollback window:** image cleanup keeps the images of each service's last 10 revisions, so they can be rolled back to. Change it per project under **Settings → General**, from 1 to 50, or with `synctl projects update shop --rollback-window 20`. An older revision whose image is gone is marked, and can't be rolled back.

## Redeploy and cancel

- **Redeploy** restarts every task, one at a time, with unchanged settings. Use it to pick up a new `:latest` image or recover from a bad state: `synctl services redeploy api`.
- **Cancel** stops a deployment in progress. A rollout that has started rolls back; one waiting on its pre-deploy commands stops them: `synctl services cancel-deployment api 42`.

## Locking an environment

During a release freeze or an incident, lock an environment under project **Settings → Environments**:

```sh
synctl envs lock production -p shop --reason "release freeze until Monday"
synctl envs unlock production -p shop
```

While it's locked:

- deploys, redeploys and shared-variable changes are refused with the reason, as HTTP `423 Locked` for API callers;
- **rollbacks, cancels and scaling still work**;
- builds still run, but don't deploy.

The project and service pages show the lock as a banner.

The same panel turns off **automatic deploys from builds** for an environment: builds run, and you deploy each one by hand from the **Builds** tab (`synctl envs auto-deploy production off`).

---

Next: [Release commands and jobs](release-commands.md)
