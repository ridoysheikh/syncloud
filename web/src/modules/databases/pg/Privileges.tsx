import { useMemo, useState } from "react";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import { Check, Plus, Undo2 } from "lucide-react";
import { api } from "@/lib/api";
import {
  pgUrl,
  usePgPrivileges,
  usePgRoles,
  type PgObjectRef,
  type PgPrivilegeChange,
} from "@/lib/pg";
import { Alert, Button } from "@/ui/controls";
import { cn } from "@/ui/cn";
import { errText, SqlBlock, inlineSelect } from "./shared";

const PLATFORM = ["syncloud_admin", "replicator"];

/** Who can do what on one object, editable as a grid. */
export function PrivilegesEditor({
  path,
  db,
  object,
}: {
  path: string;
  db: string;
  object: PgObjectRef;
}) {
  const qc = useQueryClient();
  const [check, setCheck] = useState("");
  const privs = usePgPrivileges(path, db, object, check);
  const roles = usePgRoles(path);
  // grantee -> privilege -> wanted (true grant, false revoke)
  const [edits, setEdits] = useState<Record<string, Record<string, boolean>>>(
    {},
  );
  const [added, setAdded] = useState<string[]>([]);
  const [pick, setPick] = useState("");
  const [ran, setRan] = useState<string[]>([]);
  const p = privs.data;

  const grantees = useMemo(() => {
    const g = (p?.grants ?? []).map((x) => x.grantee);
    return [...g, ...added.filter((a) => !g.includes(a))];
  }, [p, added]);
  const has = (grantee: string, priv: string) =>
    priv in (p?.grants.find((g) => g.grantee === grantee)?.privileges ?? {});
  const want = (grantee: string, priv: string) =>
    edits[grantee]?.[priv] ?? has(grantee, priv);
  const changes = useMemo(() => {
    const out: PgPrivilegeChange[] = [];
    for (const [grantee, m] of Object.entries(edits)) {
      const grant = Object.keys(m).filter((k) => m[k] && !has(grantee, k));
      const revoke = Object.keys(m).filter((k) => !m[k] && has(grantee, k));
      if (grant.length) out.push({ privileges: grant, grantee, object });
      if (revoke.length)
        out.push({ revoke: true, privileges: revoke, grantee, object });
    }
    return out;
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [edits, p, object]);

  const apply = useMutation({
    mutationFn: () =>
      api<{ statements: string[] }>(
        "POST",
        pgUrl(`${path}/pg/privileges`, db),
        {
          changes,
        },
      ),
    onSuccess: (r) => {
      setRan(r.statements);
      setEdits({});
      setAdded([]);
      void qc.invalidateQueries({ queryKey: ["pg", path, "privileges"] });
    },
  });

  if (privs.error) return <Alert>{errText(privs.error)}</Alert>;
  if (!p) return <p className="text-faint text-xs">Loading privileges…</p>;
  const roleNames = (roles.data ?? [])
    .filter((r) => !r.protected || r.predefined)
    .map((r) => r.name);
  const choices = ["PUBLIC", ...roleNames].filter((r) => !grantees.includes(r));
  const toggle = (grantee: string, priv: string) =>
    setEdits((e) => ({
      ...e,
      [grantee]: { ...e[grantee], [priv]: !want(grantee, priv) },
    }));

  return (
    <div className="flex flex-col gap-3 text-xs">
      <p className="text-muted">
        <span className="font-mono">{p.object}</span> is owned by{" "}
        <span className="font-mono">{p.owner}</span>, which always has every
        privilege. A filled box is granted; ✱ means with grant option.
      </p>
      <div className="overflow-x-auto">
        <table className="border-collapse">
          <thead>
            <tr className="border-line border-b">
              <th className="text-muted h-7 pr-4 text-left font-medium">
                Grantee
              </th>
              {p.available.map((a) => (
                <th
                  key={a}
                  className="text-muted px-2 text-center font-medium whitespace-nowrap"
                >
                  {a}
                </th>
              ))}
            </tr>
          </thead>
          <tbody>
            {grantees.map((g) => {
              const locked = PLATFORM.includes(g);
              const grants = p.grants.find((x) => x.grantee === g)?.privileges;
              return (
                <tr key={g} className="border-line border-b last:border-b-0">
                  <td className="h-8 pr-4 font-mono whitespace-nowrap">
                    {g}
                    {g === p.owner && (
                      <span className="text-faint ml-1 font-sans">owner</span>
                    )}
                  </td>
                  {p.available.map((a) => {
                    const on = want(g, a);
                    const changed =
                      edits[g]?.[a] !== undefined && on !== has(g, a);
                    return (
                      <td key={a} className="px-2 text-center">
                        <button
                          type="button"
                          disabled={locked || g === p.owner}
                          onClick={() => toggle(g, a)}
                          aria-label={`${a} for ${g}`}
                          className={cn(
                            "inline-flex size-5 items-center justify-center rounded-sm border",
                            on
                              ? "bg-accent/20 border-accent text-accent"
                              : "border-line-strong",
                            changed && "ring-warn ring-1",
                            (locked || g === p.owner) && "opacity-60",
                          )}
                        >
                          {on && <Check className="size-3" />}
                        </button>
                        {grants?.[a] && (
                          <span
                            className="text-accent ml-0.5"
                            title="with grant option"
                          >
                            ✱
                          </span>
                        )}
                      </td>
                    );
                  })}
                </tr>
              );
            })}
          </tbody>
        </table>
      </div>
      <div className="flex flex-wrap items-center gap-2">
        <select
          className={cn(inlineSelect, "max-w-56")}
          value={pick}
          onChange={(e) => setPick(e.target.value)}
          aria-label="Role to add"
        >
          <option value="">Add a grantee…</option>
          {choices.map((r) => (
            <option key={r}>{r}</option>
          ))}
        </select>
        <Button
          disabled={!pick}
          onClick={() => {
            setAdded((a) => [...a, pick]);
            setPick("");
          }}
        >
          <Plus className="size-3.5" /> Add
        </Button>
        <span className="flex-1" />
        {changes.length > 0 && (
          <Button variant="ghost" onClick={() => (setEdits({}), setAdded([]))}>
            <Undo2 className="size-3.5" /> Discard
          </Button>
        )}
        <Button
          variant="primary"
          disabled={!changes.length || apply.isPending}
          onClick={() => apply.mutate()}
        >
          Apply {changes.length ? `(${changes.length})` : ""}
        </Button>
      </div>
      {apply.error && <Alert>{errText(apply.error)}</Alert>}
      {ran.length > 0 && <SqlBlock sql={ran.map((s) => s + ";").join("\n")} />}
      <div className="border-line flex flex-col gap-2 border-t pt-3">
        <div className="flex flex-wrap items-center gap-2">
          <span className="text-muted">Check what a role can do here:</span>
          <select
            className={cn(inlineSelect, "max-w-56")}
            value={check}
            onChange={(e) => setCheck(e.target.value)}
            aria-label="Role to check"
          >
            <option value="">choose a role</option>
            {(roles.data ?? [])
              .filter((r) => !r.predefined)
              .map((r) => (
                <option key={r.name}>{r.name}</option>
              ))}
          </select>
        </div>
        {p.effective && (
          <p>
            <span className="font-mono">{p.role}</span>{" "}
            {Object.values(p.effective).some(Boolean)
              ? "can "
              : "has no rights here "}
            {Object.entries(p.effective)
              .filter(([, ok]) => ok)
              .map(([k]) => k)
              .join(", ")}
            <span className="text-faint">
              {" "}
              (directly, through PUBLIC, or through roles it inherits)
            </span>
          </p>
        )}
      </div>
      {object.kind === "schema" && (
        <SchemaBulk path={path} db={db} schema={object.name ?? ""} />
      )}
      {object.kind === "schema" && p.defaults && p.defaults.length > 0 && (
        <DefaultsList
          path={path}
          db={db}
          schema={object.name ?? ""}
          defaults={p.defaults}
        />
      )}
    </div>
  );
}

const bulkPrivs: Record<string, string[]> = {
  tables: [
    "SELECT",
    "INSERT",
    "UPDATE",
    "DELETE",
    "TRUNCATE",
    "REFERENCES",
    "TRIGGER",
    "MAINTAIN",
  ],
  sequences: ["USAGE", "SELECT", "UPDATE"],
  functions: ["EXECUTE"],
};

/** Grants on every table, sequence or function of a schema, now or later. */
function SchemaBulk({
  path,
  db,
  schema,
}: {
  path: string;
  db: string;
  schema: string;
}) {
  const qc = useQueryClient();
  const roles = usePgRoles(path);
  const [what, setWhat] = useState<keyof typeof bulkPrivs>("tables");
  const [selected, setSelected] = useState<string[]>(["SELECT"]);
  const [grantee, setGrantee] = useState("");
  const [future, setFuture] = useState(true);
  const [forRole, setForRole] = useState("app");
  const [revoke, setRevoke] = useState(false);
  const [ran, setRan] = useState<string[]>([]);
  const run = useMutation({
    mutationFn: () => {
      const base = {
        revoke,
        privileges: selected,
        grantee,
        object: { schema },
      };
      const changes: PgPrivilegeChange[] = [{ ...base, target: `all-${what}` }];
      if (future) changes.push({ ...base, target: `default-${what}`, forRole });
      return api<{ statements: string[] }>(
        "POST",
        pgUrl(`${path}/pg/privileges`, db),
        { changes },
      );
    },
    onSuccess: (r) => {
      setRan(r.statements);
      void qc.invalidateQueries({ queryKey: ["pg", path, "privileges"] });
    },
  });
  const names = (roles.data ?? [])
    .filter((r) => !r.protected)
    .map((r) => r.name);
  return (
    <div className="border-line flex flex-col gap-2 border-t pt-3">
      <h3 className="text-muted font-medium">Everything in this schema</h3>
      <div className="flex flex-wrap items-center gap-2">
        <select
          className={cn(inlineSelect, "w-24")}
          value={revoke ? "revoke" : "grant"}
          onChange={(e) => setRevoke(e.target.value === "revoke")}
          aria-label="Grant or revoke"
        >
          <option value="grant">Grant</option>
          <option value="revoke">Revoke</option>
        </select>
        <div className="flex flex-wrap gap-x-2 gap-y-1">
          {(bulkPrivs[what] ?? []).map((pv) => (
            <label key={pv} className="flex items-center gap-1">
              <input
                type="checkbox"
                checked={selected.includes(pv)}
                onChange={() =>
                  setSelected((s) =>
                    s.includes(pv) ? s.filter((x) => x !== pv) : [...s, pv],
                  )
                }
              />
              {pv}
            </label>
          ))}
        </div>
        <span>on all</span>
        <select
          className={cn(inlineSelect, "w-28")}
          value={what}
          onChange={(e) => {
            const w = e.target.value as keyof typeof bulkPrivs;
            setWhat(w);
            setSelected((bulkPrivs[w] ?? []).slice(0, 1));
          }}
          aria-label="Object type"
        >
          <option value="tables">tables</option>
          <option value="sequences">sequences</option>
          <option value="functions">functions</option>
        </select>
        <span>{revoke ? "from" : "to"}</span>
        <select
          className={cn(inlineSelect, "max-w-48")}
          value={grantee}
          onChange={(e) => setGrantee(e.target.value)}
          aria-label="Grantee"
        >
          <option value="">role…</option>
          <option>PUBLIC</option>
          {names.map((n) => (
            <option key={n}>{n}</option>
          ))}
        </select>
      </div>
      <label className="flex flex-wrap items-center gap-1.5">
        <input
          type="checkbox"
          checked={future}
          onChange={(e) => setFuture(e.target.checked)}
        />
        and on ones created later by
        <select
          className={cn(inlineSelect, "max-w-40")}
          value={forRole}
          onChange={(e) => setForRole(e.target.value)}
          disabled={!future}
          aria-label="Creator role"
        >
          {names.map((n) => (
            <option key={n}>{n}</option>
          ))}
        </select>
        <span className="text-faint">(default privileges)</span>
      </label>
      <div>
        <Button
          variant="primary"
          disabled={!grantee || !selected.length || run.isPending}
          onClick={() => run.mutate()}
        >
          {revoke ? "Revoke" : "Grant"}
        </Button>
      </div>
      {run.error && <Alert>{errText(run.error)}</Alert>}
      {ran.length > 0 && <SqlBlock sql={ran.map((s) => s + ";").join("\n")} />}
    </div>
  );
}

function DefaultsList({
  path,
  db,
  schema,
  defaults,
}: {
  path: string;
  db: string;
  schema: string;
  defaults: NonNullable<ReturnType<typeof usePgPrivileges>["data"]>["defaults"];
}) {
  const qc = useQueryClient();
  const drop = useMutation({
    mutationFn: (d: NonNullable<typeof defaults>[number]) =>
      api("POST", pgUrl(`${path}/pg/privileges`, db), {
        changes: [
          {
            revoke: true,
            privileges: Object.keys(d.privileges),
            grantee: d.grantee,
            target: `default-${d.objectType}s`,
            object: { schema },
            forRole: d.forRole,
          },
        ],
      }),
    onSuccess: () =>
      qc.invalidateQueries({ queryKey: ["pg", path, "privileges"] }),
  });
  return (
    <div className="border-line flex flex-col gap-1.5 border-t pt-3">
      <h3 className="text-muted font-medium">
        Default privileges in this schema
      </h3>
      {(defaults ?? []).map((d, i) => (
        <div key={i} className="flex flex-wrap items-center gap-2">
          <span>
            {d.objectType}s created by{" "}
            <span className="font-mono">{d.forRole}</span> grant{" "}
            <span className="font-mono">
              {Object.keys(d.privileges).join(", ")}
            </span>{" "}
            to <span className="font-mono">{d.grantee}</span>
          </span>
          {["table", "sequence", "function"].includes(d.objectType) && (
            <Button
              variant="ghost"
              onClick={() => drop.mutate(d)}
              disabled={drop.isPending}
            >
              Remove
            </Button>
          )}
        </div>
      ))}
      {drop.error && <Alert>{errText(drop.error)}</Alert>}
    </div>
  );
}
