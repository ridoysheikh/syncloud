import { Power } from "lucide-react";
import { Alert, Button } from "@/ui/controls";
import { errText, useSetChallengeServer } from "./lib";

/**
 * One click turns SynCloud's challenge server on: the zone is
 * _acme-challenge.<public ip>.sslip.io, which sslip.io already delegates to
 * this controller, so nothing has to be created at any DNS host.
 */
export function TurnOnChallengeServer({
  ip,
  label = "Turn on SynCloud's challenge server",
}: {
  /** Overrides the detected address (behind NAT); kept when it changes. */
  ip?: string;
  label?: string;
}) {
  const on = useSetChallengeServer();
  return (
    <div className="flex flex-col gap-1.5">
      <div>
        <Button
          variant="primary"
          disabled={on.isPending}
          onClick={() =>
            on.mutate({
              enabled: true,
              ...(ip?.trim() ? { ip: ip.trim() } : {}),
            })
          }
        >
          <Power className="size-3.5" /> {on.isPending ? "Turning on…" : label}
        </Button>
      </div>
      {on.error && (
        <Alert>{errText(on.error, "Could not turn the server on")}</Alert>
      )}
    </div>
  );
}
