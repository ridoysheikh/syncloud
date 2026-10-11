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
- **trigger:** manual, Git push (with the commit), new image push, rollback, automatic rollback, variables, redeploy;
- **who** started it, when, and how long it took;
- **what changed:** the image and its digest, ports, resources, and variables by name (never their values).

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

- **Redeploy** restarts every task, one at a time. It also picks up the newest push of the image's tag, so a new `:latest` (or `:main`, `:v2`, …) is deployed. Use it to update the image or recover from a bad state: `synctl services redeploy api`.
- **Cancel** stops a deployment in progress. A rollout that has started rolls back; one waiting on its pre-deploy commands stops them: `synctl services cancel-deployment api 42`.

## Image updates

A service that runs a ready-made image (not built from Git) refers to it by a tag, such as `ghcr.io/acme/api:main`. A tag can point to a new image after every push, so each revision records the exact image the tag pointed to when the revision was made (its **digest**, `sha256:…`):

- every node runs that exact image, even one that pulled the tag earlier;
- a rollback returns to the exact old image, not to whatever the tag points to now;
- changing a variable or a setting keeps the image as it is; only a new image, a **Redeploy** or an image update moves to a newer push.

The service page shows the digest next to the image. To pin an image yourself, give it with its digest (`nginx@sha256:…`); it is then never updated.

New pushes are deployed from the service's **Deploy › Image updates**:

- **Deploy new pushes automatically** watches the registry. Images in the built-in registry are deployed within seconds of a push. Images in other registries are checked every 5 minutes, or at an interval you choose (1 minute to 1 hour). **Check now** checks at once.
- **Deploy webhook** gives a secret URL for CI to call after it pushes. The newest push of the tag is deployed at once, with any registry, whether automatic deploys are on or not:

  ```sh
  docker push ghcr.io/acme/api:main
  curl -fsS -X POST "$SYNCLOUD_DEPLOY_HOOK"
  ```

  The URL is shown once, when it is made. **Make a new URL** replaces it; **Remove** turns it off. The call answers `202` with the deployment started, `200` when the tag has not changed, `423` when the environment is locked, and `409` when the environment deploys by hand.

From the CLI:

```sh
synctl services auto-deploy on api -p shop --interval 5m    # watch the registry
synctl services auto-deploy check api -p shop               # check now
synctl services auto-deploy webhook api -p shop             # make a deploy webhook URL
synctl services auto-deploy get api -p shop
```

Deployments started this way show **New image push** as their trigger. The environment's policy applies, as for Git builds: nothing is deployed to a locked environment, or to one that deploys by hand.

Private registries need their credentials under **Registry › Upstreams**, for checking tags as for pulling. If a registry can't be reached, deploys go ahead with the image as it is on the nodes, and the image update tries again later.

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
