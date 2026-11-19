import { useState } from "react";
import { Plus, Sparkles, X } from "lucide-react";
import { Button, Input } from "@/ui/controls";
import { cn } from "@/ui/cn";
import type { Domain } from "./lib";

export const nameRE =
  /^(\*\.)?([a-z0-9_]([a-z0-9_-]{0,61}[a-z0-9_])?\.)+[a-z0-9-]{2,}$/;

/** Why a certificate name is not acceptable ("" when it is). */
export function nameProblem(n: string): string {
  if (!nameRE.test(n))
    return `${n} is not a host name (a name like app.example.com, or *.example.com for a wildcard)`;
  if (/^\*\..*\*/.test(n)) return "only one leading *. is allowed";
  return "";
}

/**
 * The names of a certificate: type them, or add a registered domain and
 * its wildcard in one click. A wildcard covers one label (*.example.com is
 * not example.com and not a.b.example.com), so the editor says so.
 */
export function NamesEditor({
  value,
  onChange,
  domains = [],
  preferDomain,
}: {
  value: string[];
  onChange: (v: string[]) => void;
  domains?: Domain[];
  preferDomain?: string;
}) {
  const [text, setText] = useState("");
  const add = (...ns: string[]) => {
    const next = [...value];
    for (const n of ns.map((x) => x.trim().toLowerCase()).filter(Boolean))
      if (!next.includes(n)) next.push(n);
    onChange(next);
  };
  const commit = () => {
    add(...text.split(/[\s,]+/));
    setText("");
  };
  const quick = domains
    .filter((d) => !preferDomain || d.name === preferDomain)
    .slice(0, 6);
  return (
    <div className="flex flex-col gap-2">
      <div className="flex gap-1.5">
        <Input
          value={text}
          onChange={(e) => setText(e.target.value)}
          onKeyDown={(e) => {
            if (e.key === "Enter" || e.key === ",") {
              e.preventDefault();
              commit();
            }
          }}
          placeholder="shop.example.com or *.example.com"
          className="max-w-md font-mono"
          spellCheck={false}
          aria-label="Add a name"
        />
        <Button type="button" disabled={!text.trim()} onClick={commit}>
          <Plus className="size-3.5" /> Add
        </Button>
      </div>
      {quick.length > 0 && (
        <div className="flex flex-wrap items-center gap-1.5 text-xs">
          <span className="text-faint">Quick add</span>
          {quick.map((d) => (
            <span key={d.name} className="flex items-center gap-1">
              <Button type="button" onClick={() => add(d.name)}>
                {d.name}
              </Button>
              <Button type="button" onClick={() => add(`*.${d.name}`)}>
                <Sparkles className="size-3" /> *.{d.name}
              </Button>
              <Button type="button" onClick={() => add(d.name, `*.${d.name}`)}>
                both
              </Button>
            </span>
          ))}
        </div>
      )}
      {value.length > 0 && (
        <ul className="flex flex-wrap gap-1" aria-label="Names">
          {value.map((n) => {
            const bad = nameProblem(n);
            return (
              <li
                key={n}
                title={bad || undefined}
                className={cn(
                  "bg-raised flex h-7 items-center gap-1 rounded-sm border pl-2 font-mono text-xs",
                  bad
                    ? "border-bad/40 text-bad"
                    : n.startsWith("*.")
                      ? "border-line-accent text-accent"
                      : "border-line",
                )}
              >
                {n}
                <button
                  type="button"
                  aria-label={`Remove ${n}`}
                  className="text-muted hover:text-fg px-1"
                  onClick={() => onChange(value.filter((x) => x !== n))}
                >
                  <X className="size-3" />
                </button>
              </li>
            );
          })}
        </ul>
      )}
      <p className="text-faint text-xs">
        A wildcard covers one label:{" "}
        <span className="font-mono">*.example.com</span> covers{" "}
        <span className="font-mono">a.example.com</span>, not{" "}
        <span className="font-mono">example.com</span> and not{" "}
        <span className="font-mono">a.b.example.com</span>: add those names too
        (they share one TXT record), or{" "}
        <span className="font-mono">*.b.example.com</span> for the next level.
      </p>
    </div>
  );
}

/** The TXT names a set of certificate names needs for DNS validation. */
export function challengeNames(
  names: string[],
): { name: string; for: string[] }[] {
  const m = new Map<string, string[]>();
  for (const n of names) {
    const base = n.replace(/^\*\./, "");
    const key = `_acme-challenge.${base}`;
    m.set(key, [...(m.get(key) ?? []), n]);
  }
  return [...m].map(([name, f]) => ({ name, for: f }));
}
