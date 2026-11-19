import { useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { Search } from "lucide-react";
import { api } from "@/lib/api";
import { PageHeader } from "@/ui/PageHeader";
import { Panel } from "@/ui/Panel";
import { DataTable } from "@/ui/DataTable";
import { EmptyState } from "@/ui/EmptyState";
import { Alert, Button, Input, StatusBadge } from "@/ui/controls";
import { Segmented } from "@/ui/choice";
import { Select } from "@/ui/select";
import { cn, gap } from "@/ui/cn";
import { errText } from "./lib";

interface Answer {
  server: string;
  kind: "public" | "authoritative";
  rcode: string;
  values: string[];
  ttl?: number;
  error?: string;
}

const types = ["A", "AAAA", "CNAME", "TXT", "MX", "NS", "SRV", "CAA", "SOA"];

/** Domains › Lookup: what public resolvers and the zone's own servers answer. */
export function LookupPage() {
  const [name, setName] = useState("");
  const [type, setType] = useState("A");
  const [via, setVia] = useState<"" | "public" | "authoritative">("");
  const [asked, setAsked] = useState<{
    name: string;
    type: string;
    via: string;
  } | null>(null);
  const { data, error, isFetching, refetch } = useQuery({
    queryKey: ["dm", "lookup", asked],
    enabled: !!asked,
    retry: false,
    queryFn: () =>
      api<{ answers: Answer[] }>(
        "GET",
        `/dns/lookup?${new URLSearchParams({ name: asked!.name, type: asked!.type, ...(asked!.via ? { via: asked!.via } : {}) })}`,
      ),
  });
  const go = () => {
    const n = name.trim();
    if (!n) return;
    if (asked && asked.name === n && asked.type === type && asked.via === via)
      void refetch();
    else setAsked({ name: n, type, via });
  };
  const answers = data?.answers ?? [];
  const sets = new Set(
    answers.filter((a) => !a.error).map((a) => a.values.join("|")),
  );
  return (
    <div className={cn("flex flex-col", gap)}>
      <PageHeader crumbs={["Domains"]} title="DNS lookup" />
      <p className="text-muted text-xs">
        Ask public resolvers and the domain's own name servers what a name
        answers: for a record you just created, or a TXT challenge that is not
        seen yet.
      </p>
      <Panel title="Query">
        <div className="flex flex-wrap items-end gap-1.5">
          <div className="min-w-60 flex-1">
            <Input
              value={name}
              onChange={(e) => setName(e.target.value)}
              onKeyDown={(e) => e.key === "Enter" && go()}
              placeholder="shop.example.com or _acme-challenge.example.com"
              className="font-mono"
              spellCheck={false}
              aria-label="Name"
            />
          </div>
          <Select value={type} onChange={setType} aria-label="Record type">
            {types.map((t) => (
              <option key={t}>{t}</option>
            ))}
          </Select>
          <Segmented
            label="From"
            value={via}
            onChange={setVia}
            options={[
              { value: "", label: "Both" },
              { value: "public", label: "Public resolvers" },
              { value: "authoritative", label: "Zone's servers" },
            ]}
          />
          <Button
            variant="primary"
            onClick={go}
            disabled={!name.trim() || isFetching}
          >
            <Search className="size-3.5" /> {isFetching ? "Asking…" : "Look up"}
          </Button>
        </div>
      </Panel>
      {error && <Alert>{errText(error, "The lookup failed")}</Alert>}
      {asked && !error && (
        <Panel title="Answers" flush>
          <DataTable
            rows={answers}
            rowKey={(a) => a.kind + a.server}
            empty={
              !isFetching && (
                <EmptyState icon={Search} title="No server answered">
                  Nothing to show.
                </EmptyState>
              )
            }
            columns={[
              {
                header: "From",
                cell: (a) =>
                  a.kind === "authoritative"
                    ? "zone's server"
                    : "public resolver",
              },
              {
                header: "Server",
                cell: (a) => (
                  <span className="font-mono">
                    {a.server.replace(/:53$/, "")}
                  </span>
                ),
              },
              {
                header: "Result",
                cell: (a) => (
                  <StatusBadge
                    tone={a.error ? "bad" : a.values.length ? "ok" : "neutral"}
                  >
                    {a.error
                      ? "no answer"
                      : a.values.length
                        ? "answered"
                        : (a.rcode || "empty").toLowerCase()}
                  </StatusBadge>
                ),
              },
              {
                header: "Answer",
                className: "w-full whitespace-normal",
                cell: (a) =>
                  a.error ? (
                    <span className="text-bad">{a.error}</span>
                  ) : a.values.length ? (
                    <span className="font-mono break-all whitespace-normal">
                      {a.values.join("  ·  ")}
                    </span>
                  ) : (
                    <span className="text-faint">-</span>
                  ),
              },
              { header: "TTL", cell: (a) => (a.ttl ? `${a.ttl}s` : "") },
            ]}
          />
        </Panel>
      )}
      {sets.size > 1 && (
        <Alert tone="warn">
          The servers disagree: a change is still spreading. Look again in a
          minute.
        </Alert>
      )}
    </div>
  );
}
