import { useQuery } from "@tanstack/react-query";
import { backupQuery } from "@/lib/backups";
import { since } from "@/lib/nodes";
import { Panel } from "@/ui/Panel";
import { CopyBox } from "@/ui/CopyBox";

/**
 * The recovery key is managed only on the controller host: no page, API or
 * synctl command can show or change it. A new key is shown once, when it is
 * made with the host command below, and is never stored.
 */
export function RecoveryKeyPanel() {
  const { data } = useQuery(backupQuery);
  const setAt = data?.recoveryKeySetAt ?? null;
  return (
    <Panel title="Recovery key">
      <div className="flex flex-col gap-2 text-xs">
        <p className="text-muted">
          Restoring a backup on any machine needs the recovery key. It is shown
          once, when it is made, and is not stored on this instance, so keep it
          offline. To get a new one, run this as root on the controller host:
          it shows the new key once and re-keys the stored backups, so the old
          key no longer opens them.
        </p>
        <CopyBox value="sudo syncloud-controller recovery-key rotate" />
        <p className="text-muted">
          Check an offline copy with{" "}
          <code className="font-mono">sudo syncloud-controller recovery-key verify</code>.
          {setAt && (
            <>
              {" "}
              Last set <span title={new Date(setAt).toLocaleString()}>{since(setAt)}</span>.
            </>
          )}
        </p>
      </div>
    </Panel>
  );
}
