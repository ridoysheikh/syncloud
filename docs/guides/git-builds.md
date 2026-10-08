# Git builds

Connect a service to a Git branch, and SynCloud builds each new commit into an image in its own registry and deploys it.

## Connect a repository

The quickest way is to connect your Git host once ([Integrations](integrations.md)): GitHub in one click, or GitLab, Gitea and Forgejo with a token. Then pick the repository by name. SynCloud creates the push webhook and reports each build as a commit status.

| Where | How |
| --- | --- |
| a new service | **New service → Git repository** |
| an existing service | its **Builds** tab |
| the CLI | see below |

```sh
# a connected host: webhook and commit statuses are set up for you
synctl builds connect api -p shop --connection github --repo acme/api

# any Git URL (add the webhook shown on the Builds tab by hand, or rely on polling)
SYNCLOUD_GIT_TOKEN=ghp_… synctl builds connect api -p shop \
  --url https://github.com/acme/api.git --branch main
```

You can also keep code in the cluster with the [built-in Git server](integrations.md#built-in-git-server).

## What gets built

| Builder | Used when |
| --- | --- |
| **Automatic** (default) | the Dockerfile if the repository has one, else Nixpacks, else a static site |
| **Dockerfile** | always the Dockerfile at `--dockerfile` (relative to `--context`) |
| **Nixpacks** | detects the language (Node, Python, Go, Ruby, PHP, Java…) and builds without a Dockerfile |
| **Static site** | serves the files (a folder with `index.html`) on port 80 |

Options for monorepos and releases:

- `--context services/api`: build from a sub-folder.
- `--path "services/api/**" --path "!services/api/docs/**"`: only commits that change these paths trigger a build.
- `--branch "release/*"`: any matching branch. `--tags "v*"` also builds new tags.
- `--no-auto-deploy`: build, but deploy each build by hand.

## Build settings

On the **Builds** tab, under **Build settings**:

- **Install, build and start commands** override what Nixpacks detects.
- **Build variables** are available while building, as Docker build args and environment variables. They're encrypted and never shown again. Runtime variables are separate ([Services › Variables](services.md#variables)).
- **After-build checks** run inside the new image before it's deployed, for example `npm test`. A failing check fails the build ([Release commands](release-commands.md#after-build-checks-git-services)).

```sh
synctl builds configure api -p shop --build-cmd "npm run build" --var NODE_ENV=production --check "npm test"
synctl builds settings api -p shop
```

## Builds and deploys

```sh
synctl builds list api -p shop          # status, commit, duration
synctl builds run api -p shop --wait    # build the branch head now and stream the log
synctl builds deploy BUILD_ID           # deploy a build by hand (or an older one)
```

- A build that succeeds is deployed as a new revision with the trigger **Git**, showing the commit and branch ([Deployments](deployments.md)).
- If a newer commit of the same branch is already deployed, an older build isn't deployed over it.
- When an environment is [locked](deployments.md#locking-an-environment) or its automatic deploys are off, builds still run but wait to be deployed by hand.

## Where builds run

Builds run in BuildKit, on any node with room, or on the one you name with the controller's `--build-node`. Each build reserves 0.25 CPU and 512 MB. Images go to the built-in registry as `@registry/<project>/<service>:<commit>`. Cleanup keeps the images that the project's [rollback window](deployments.md#roll-back) needs.

---

Next: [Domains and ports](domains-and-ports.md)
