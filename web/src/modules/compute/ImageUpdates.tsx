import { useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { RefreshCw, Webhook } from "lucide-react";
import { api, ApiError } from "@/lib/api";
import { since } from "@/lib/nodes";
import { Panel } from "@/ui/Panel";
import { Alert, Button, Toggle } from "@/ui/controls";
import { ChoiceField } from "@/ui/choice";
import { Select } from "@/ui/select";
import { CopyBox } from "@/ui/CopyBox";
import { confirmAction } from "@/ui/dialogs";

/* Service › Deploy › Image updates: deploy new pushes of the image's tag. */

interface AutoDeploy {
  watchable: boolean;
  reason?: string;
  git: boolean;
  builtIn: boolean;
  autoDeploy: boolean;
  pollSeconds: number;
  webhook: boolean;
  checkedAt: string | null;
  lastDigest: string;
  lastError: string;
  lastDeployAt: string | null;
}

interface ImageUpdate {
  deployed: boolean;
  digest: string;
  revision: number;
  message: string;
}

const intervals = [
  { value: "60", label: "every minute" },
  { value: "300", label: "every 5 minutes" },
  { value: "900", label: "every 15 minutes" },
  { value: "3600", label: "every hour" },
];

const errText = (e: unknown, fallback: string) =>
  e instanceof ApiError ? e.message : fallback;

const short = (d: string) => d.replace(/^(sha256:.{12}).*$/, "$1");

export function ImageUpdatesPanel({
  path,
  image,
}: {
  path: string;
  image: string;
}) {
  const qc = useQueryClient();
  const key = ["auto-deploy", path];
  const q = useQuery({
    queryKey: key,
    queryFn: () => api<AutoDeploy>("GET", `${path}/auto-deploy`),
    refetchInterval: 15_000,
  });
  const [hook, setHook] = useState<string | null>(null);
  const refresh = () => {
    void qc.invalidateQueries({ queryKey: key });
    void qc.invalidateQueries({ queryKey: ["services"] });
    void qc.invalidateQueries({ queryKey: ["deployments"] });
  };
  const set = useMutation({
    mutationFn: (v: { autoDeploy: boolean; pollSeconds: number }) =>
      api<AutoDeploy>("PUT", `${path}/auto-deploy`, v),
    onSuccess: (v) => qc.setQueryData(key, v),
  });
  const check = useMutation({
    mutationFn: () => api<ImageUpdate>("POST", `${path}/auto-deploy/check`),
    onSettled: refresh,
  });
  const createHook = useMutation({
    mutationFn: () =>
      api<{ path: string }>("POST", `${path}/auto-deploy/webhook`),
    onSuccess: (r) => {
      setHook(window.location.origin + r.path);
      refresh();
    },
  });
  const removeHook = useMutation({
    mutationFn: () => api("DELETE", `${path}/auto-deploy/webhook`),
    onSuccess: () => {
      setHook(null);
      refresh();
    },
  });

  const v = q.data;
  if (!v) return null;
  if (!v.watchable) {
    // Built from Git: its builds deploy it (the Build panel).
    if (v.git) return null;
    return (
      <Panel title="Image updates">
        <p className="text-muted text-xs">{v.reason}</p>
      </Panel>
    );
  }
  return (
    <Panel
      title="Image updates"
      actions={
        <Button onClick={() => check.mutate()} disabled={check.isPending}>
          <RefreshCw className="size-3.5" />
          {check.isPending ? "Checking…" : "Check now"}
        </Button>
      }
    >
      <div className="flex flex-col gap-3 text-xs">
        <p className="text-muted">
          Deploy new pushes of{" "}
          <code className="text-fg font-mono break-all">{image}</code>. Each
          revision runs the exact image its tag pointed to when it was made, so
          a push never reaches running tasks on its own: it is deployed here,
          by a redeploy, or by your CI calling the deploy webhook.
        </p>

        <Toggle
          checked={v.autoDeploy}
          disabled={set.isPending}
          onChange={(on) =>
            set.mutate({ autoDeploy: on, pollSeconds: v.pollSeconds })
          }
          label="Deploy new pushes automatically"
          hint={
            v.builtIn
              ? "The image is in the built-in registry: a push is deployed within seconds."
              : "SynCloud checks the registry for a newer push of the tag and deploys it."
          }
        />
        {v.autoDeploy && !v.builtIn && (
          <ChoiceField label="Check the registry">
            <div>
              <Select
                size="sm"
                value={String(v.pollSeconds)}
                onChange={(s) =>
                  set.mutate({ autoDeploy: true, pollSeconds: Number(s) })
                }
                aria-label="Check interval"
                options={
                  intervals.some((i) => i.value === String(v.pollSeconds))
                    ? intervals
                    : [
                        ...intervals,
                        {
                          value: String(v.pollSeconds),
                          label: `every ${v.pollSeconds} seconds`,
                        },
                      ]
                }
              />
            </div>
          </ChoiceField>
        )}

        <div className="text-faint flex flex-wrap gap-x-4 gap-y-1">
          <span>
            Last checked{" "}
            <span className="text-muted">
              {v.checkedAt ? since(v.checkedAt) : "never"}
            </span>
          </span>
          {v.lastDigest && (
            <span>
              Tag points to{" "}
              <code className="text-muted font-mono">{short(v.lastDigest)}</code>
            </span>
          )}
          {v.lastDeployAt && (
            <span>
              Last deployed{" "}
              <span className="text-muted">{since(v.lastDeployAt)}</span>
            </span>
          )}
        </div>
        {v.lastError && <Alert tone="warn">{v.lastError}</Alert>}
        {check.data && <Alert tone="info">{check.data.message}</Alert>}
        {check.error && (
          <Alert>{errText(check.error, "Could not check the registry")}</Alert>
        )}
        {set.error && (
          <Alert>{errText(set.error, "Could not save the setting")}</Alert>
        )}

        <section className="border-line flex flex-col gap-1.5 border-t pt-3">
          <h3 className="flex items-center gap-1.5 font-medium">
            <Webhook className="text-muted size-3.5" /> Deploy webhook
          </h3>
          <p className="text-muted">
            For CI: call this URL after <code className="font-mono">docker push</code>{" "}
            and the newest push of the tag is deployed at once. It works with
            any registry, whether automatic deploys are on or not. The URL is
            the secret: it is shown only when created.
          </p>
          {hook && (
            <div className="flex flex-col gap-1.5">
              <CopyBox value={hook} />
              <CopyBox wrap value={`curl -fsS -X POST ${hook}`} />
              <Alert tone="warn">
                Copy it now: it is not shown again. Store it as a CI secret.
              </Alert>
            </div>
          )}
          <div className="flex flex-wrap items-center gap-2">
            <Button
              onClick={async () => {
                if (
                  v.webhook &&
                  !(await confirmAction(
                    "Replace the deploy webhook? The current URL stops working.",
                  ))
                )
                  return;
                createHook.mutate();
              }}
              disabled={createHook.isPending}
            >
              {v.webhook ? "Make a new URL" : "Create webhook URL"}
            </Button>
            {v.webhook && (
              <Button
                variant="danger"
                onClick={async () => {
                  if (
                    await confirmAction(
                      "Remove the deploy webhook? CI calls to it will fail.",
                    )
                  )
                    removeHook.mutate();
                }}
                disabled={removeHook.isPending}
              >
                Remove
              </Button>
            )}
            {v.webhook && !hook && (
              <span className="text-faint">A webhook URL is active.</span>
            )}
          </div>
          {(createHook.error || removeHook.error) && (
            <Alert>
              {errText(createHook.error ?? removeHook.error, "Could not change the webhook")}
            </Alert>
          )}
        </section>
      </div>
    </Panel>
  );
}
