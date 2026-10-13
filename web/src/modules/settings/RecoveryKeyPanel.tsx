import { useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Eye, EyeOff, KeyRound } from "lucide-react";
import { api, ApiError } from "@/lib/api";
import { meQuery } from "@/lib/auth";
import { Panel } from "@/ui/Panel";
import { CopyBox } from "@/ui/CopyBox";
import { Alert, Button } from "@/ui/controls";
import { confirmAction } from "@/ui/dialogs";

interface RecoveryKey {
  stored: boolean;
  recoveryKey: string;
  rekey?: { rekeyed: number; skipped: string[]; failed: string[] };
  rekeyError?: string;
  backupError?: string;
  storeError?: string;
}

/** The recovery key kept on this instance (root user only, Phase 19b). */
export function RecoveryKeyPanel() {
  const qc = useQueryClient();
  const me = useQuery(meQuery);
  const [shown, setShown] = useState(false);
  const isRoot = !!me.data?.isRoot;
  const key = useQuery({
    queryKey: ["backups", "recovery-key"],
    queryFn: () => api<RecoveryKey>("GET", "/backups/recovery-key"),
    enabled: isRoot,
    retry: false,
  });
  const create = useMutation({
    mutationFn: () => api<RecoveryKey>("POST", "/backups/recovery-key"),
    onSuccess: (k) => {
      qc.setQueryData(["backups", "recovery-key"], k);
      setShown(true);
      void qc.invalidateQueries({ queryKey: ["backups"] });
    },
  });
  if (!isRoot) return null;
  const k = create.data ?? key.data;
  return (
    <Panel
      title="Recovery key"
      actions={
        k?.stored && (
          <Button onClick={() => setShown((s) => !s)}>
            {shown ? (
              <EyeOff className="size-3.5" />
            ) : (
              <Eye className="size-3.5" />
            )}
            {shown ? "Hide" : "Show"}
          </Button>
        )
      }
    >
      <div className="flex flex-col gap-2 text-xs">
        <p className="text-muted">
          Restoring a backup on any machine needs this key. It is kept on this
          instance, so you can open it here at any time. Keep a copy offline as
          well: if this server is lost, the copy here is lost with it.
        </p>
        {key.error && (
          <Alert>
            {key.error instanceof ApiError
              ? key.error.message
              : "Could not read the key"}
          </Alert>
        )}
        {k?.stored && shown && <CopyBox value={k.recoveryKey} wrap />}
        {k && !k.stored && (
          <>
            <Alert tone="warn">
              This install was created before recovery keys were kept, so its
              key can't be shown. Create a new one: it is kept here, and the
              stored backups are re-keyed so the new key opens them all.
              Services and data are not affected.
            </Alert>
            <div>
              <Button
                variant="primary"
                disabled={create.isPending}
                onClick={async () =>
                  (await confirmAction(
                    "Create a new recovery key? The old key will no longer open this install's backups once they are re-keyed.",
                  )) && create.mutate()
                }
              >
                <KeyRound className="size-3.5" />
                {create.isPending ? "Creating…" : "Create recovery key"}
              </Button>
            </div>
          </>
        )}
        {create.data?.rekey && (
          <Alert tone="info">
            Re-keyed {create.data.rekey.rekeyed} stored backups.
            {create.data.rekey.skipped.length > 0 &&
              ` Left as they were (another master key): ${create.data.rekey.skipped.join(", ")}.`}
            {create.data.rekey.failed.length > 0 &&
              ` Failed: ${create.data.rekey.failed.join("; ")}.`}
          </Alert>
        )}
        {create.data?.storeError && (
          <Alert>
            The key is in use but could not be kept, so copy it now:{" "}
            {create.data.storeError}
          </Alert>
        )}
        {(create.data?.rekeyError || create.data?.backupError) && (
          <Alert tone="warn">
            {[create.data.rekeyError, create.data.backupError]
              .filter(Boolean)
              .join(" · ")}
          </Alert>
        )}
        {create.error && (
          <Alert>
            {create.error instanceof ApiError
              ? create.error.message
              : "Could not create the key"}
          </Alert>
        )}
      </div>
    </Panel>
  );
}
