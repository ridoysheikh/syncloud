# Scaling and placement

## Scale by hand

Use the **±** buttons on a service's page, or:

```sh
synctl services scale web=4 -p shop
```

## Autoscaling

Autoscaling keeps one metric near a target by adding and removing tasks between a minimum and a maximum. Set it on the **Autoscaling** tab, or:

```sh
synctl autoscale set web -p shop --min 2 --max 10 --cpu 60       # average CPU, % of the task's cpu
synctl autoscale set api -p shop --min 1 --max 20 --rps 50       # requests per second per task
synctl autoscale set api -p shop --min 2 --max 8 --latency 250   # p95 latency in ms
synctl autoscale history web -p shop                              # what changed and why
synctl autoscale off web -p shop
```

| Behavior | Default |
| --- | --- |
| checked every | 15 s |
| scale out at most every | 60 s (`--scale-out-cooldown`) |
| scale in after | 4 checks below target (`--scale-in-checks`) and 300 s since the last change (`--scale-in-cooldown`) |

The tab shows the latest measurement, the target, and each change with its reason. `--paused` saves the policy without acting on it.

To add **servers** automatically when tasks don't fit, see [node pool autoscaling](../operations/nodes-and-pools.md#provider-backed-pools-and-autoscaling).

## Size

Each task has CPU and memory (`resources.cpu` and `resources.memory`, defaults 0.1 CPU and 128 MiB). Each one is **shared** (the default) or **reserved**. Set this under the service's **Placement › Resources**, with `"cpuMode": "reserved"` / `"memoryMode": "reserved"` in the spec, or with `synctl services run … --reserve-cpu --reserve-memory`.

| | Shared (default) | Reserved |
| --- | --- | --- |
| **CPU** | What the task is expected to use. It never stops a task from being placed: a busy node is slower, not full. When a node is busy, tasks get CPU in proportion to their `cpu`. | Set aside on the node. A task goes only where that many cores are not reserved by others, and keeps them when the node is busy. |
| **Memory** | What the task is expected to use. A task goes to a node with that much memory really free. Memory it doesn't use stays free for others. | Set aside on the node whether used or not, and kept for the task when memory runs low. |

A node's free memory is its memory minus whichever is larger: what's reserved on it, or what it really uses. Database members reserve their memory; their CPU is shared. During a rollout, the old revision's reservations don't count against the new one, so a deploy never waits for room it is about to free.

- `resources.memoryLimit` is the hard limit (default twice `memory`). A task above it is killed and replaced.
- `resources.cpuLimit` is an optional CPU cap.

A task's real use is on the **Metrics** tab. Shared suits most services. Reserve for latency-sensitive work that must keep its CPU or memory when a node is busy.

## Placement

| Setting | Effect |
| --- | --- |
| **Spread** (default) | tasks go to different nodes, so losing a node costs as few as possible |
| **Pack** | tasks fill as few nodes as possible, leaving room for big services |
| **Only these nodes** | the service runs on the nodes you pick, within its project's allowed nodes |
| **Pools** | `"placement": {"pools": ["gpu"]}` runs it only in those [node pools](../operations/nodes-and-pools.md#node-pools) |

Set these on the service's **Placement** tab. Changing them moves tasks with a rolling update. A project can also be limited to some nodes ([Which nodes a project may use](../operations/nodes-and-pools.md#which-nodes-a-project-may-use)).

## Quotas

Administrators can cap a project's CPU, memory, tasks and more under **Projects → Quotas & usage**, or with `synctl quota set`. A change that would exceed a quota is refused, and says which limit it hit. Daily usage per project is under the same page and in `synctl usage`.

---

Next: [PostgreSQL](postgres.md)
