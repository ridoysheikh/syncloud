import { useQuery } from "@tanstack/react-query";
import { RefreshCw } from "lucide-react";
import { api } from "@/lib/api";
import { StatusBadge } from "@/ui/controls";
import { cn } from "@/ui/cn";

interface Answer {
  server: string;
  kind: "public" | "authoritative";
  rcode: string;
  values: string[];
  error?: string;
}

/** What each resolver shows at a name: the TXT values of a challenge, or the CNAME that delegates it. */
export function Propagation({
  name,
  values,
  type = "TXT",
  title,
}: {
  name: string;
  values: string[];
  type?: "TXT" | "CNAME";
  title?: string;
}) {
  const { data, isFetching, refetch } = useQuery({
    queryKey: ["dm", "lookup", type, name],
    queryFn: () =>
      api<{ answers: Answer[] }>(
        "GET",
        `/dns/lookup?name=${encodeURIComponent(name)}&type=${type}`,
      ),
    refetchInterval: 15_000,
    retry: false,
  });
  return (
    <div className="flex flex-col gap-1">
      <div className="flex items-center gap-2">
        <span className="text-muted text-xs">
          {title ?? "Seen by resolvers"}
        </span>
        <button
          type="button"
          className="text-faint hover:text-fg"
          onClick={() => void refetch()}
          aria-label="Look again"
        >
          <RefreshCw className={cn("size-3", isFetching && "animate-spin")} />
        </button>
      </div>
      <ul className="border-line divide-line divide-y rounded-sm border text-xs">
        {(data?.answers ?? []).map((a) => {
          const have = values.filter((v) => a.values.includes(v)).length;
          const tone = a.error
            ? "bad"
            : have === values.length
              ? "ok"
              : have > 0
                ? "warn"
                : "neutral";
          return (
            <li
              key={a.kind + a.server}
              className="flex flex-wrap items-center gap-2 px-2 py-1"
            >
              <span className="text-faint w-24">
                {a.kind === "authoritative" ? "zone's own" : "public"}
              </span>
              <span className="w-32 font-mono">
                {a.server.replace(/:53$/, "")}
              </span>
              <StatusBadge tone={tone}>
                {a.error
                  ? "no answer"
                  : have === values.length
                    ? "has it"
                    : have > 0
                      ? `${have} of ${values.length}`
                      : "not yet"}
              </StatusBadge>
              {type === "CNAME" &&
                !a.error &&
                a.values.length > 0 &&
                have === 0 && (
                  <span className="text-faint font-mono">
                    {a.values.join(", ")}
                  </span>
                )}
            </li>
          );
        })}
        {!data && <li className="text-faint px-2 py-1">Asking…</li>}
      </ul>
    </div>
  );
}
