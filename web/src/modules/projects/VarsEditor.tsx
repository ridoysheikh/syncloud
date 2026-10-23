import { useState } from "react";
import {
  ClipboardPaste,
  Eye,
  EyeOff,
  FileText,
  Plus,
  Trash2,
} from "lucide-react";
import { Button, IconButton, Input } from "@/ui/controls";
import { cn } from "@/ui/cn";

export interface VarRow {
  key: string;
  value: string;
}

export const toRows = (vars?: Record<string, string>): VarRow[] =>
  Object.entries(vars ?? {})
    .sort(([a], [b]) => a.localeCompare(b))
    .map(([key, value]) => ({ key, value }));

export const toVars = (rows: VarRow[]): Record<string, string> =>
  Object.fromEntries(
    rows.filter((r) => r.key.trim()).map((r) => [r.key.trim(), r.value]),
  );

const keyRE = /^[A-Za-z_][A-Za-z0-9_]*$/;

/** First problem with a set of rows, or "". */
export function varsError(rows: VarRow[]): string {
  const seen = new Set<string>();
  for (const r of rows) {
    const k = r.key.trim();
    if (!k) continue;
    if (!keyRE.test(k)) return `"${k}" is not a valid variable name`;
    if (k.startsWith("SYNCLOUD_"))
      return `${k}: the SYNCLOUD_ prefix is reserved`;
    if (seen.has(k)) return `${k} is listed twice`;
    seen.add(k);
  }
  return "";
}

/** Parses KEY=VALUE lines (a .env file); comments and blank lines are skipped. */
function parseDotenv(text: string): VarRow[] {
  const rows: VarRow[] = [];
  for (const raw of text.split("\n")) {
    const line = raw.trim();
    if (!line || line.startsWith("#")) continue;
    const i = line.indexOf("=");
    if (i <= 0) continue;
    let value = line.slice(i + 1).trim();
    if (
      value.length >= 2 &&
      (value[0] === '"' || value[0] === "'") &&
      value.at(-1) === value[0]
    )
      value = value.slice(1, -1);
    rows.push({
      key: line
        .slice(0, i)
        .replace(/^export\s+/, "")
        .trim(),
      value,
    });
  }
  return rows;
}

/** Writes rows as a .env file, quoting values parseDotenv would otherwise change. */
function toDotenv(rows: VarRow[]): string {
  return rows
    .filter((r) => r.key.trim())
    .map((r) => {
      const v = r.value;
      const quote = v !== v.trim() || /^["']/.test(v);
      return `${r.key.trim()}=${quote ? `"${v}"` : v}`;
    })
    .join("\n");
}

/**
 * Key/value editor for environment variables. `inherited` lists variables
 * that come from elsewhere (the environment's shared variables) and shows
 * which of them the rows override.
 */
export function VarsEditor({
  rows,
  onChange,
  inherited,
  inheritedLabel = "Shared",
}: {
  rows: VarRow[];
  onChange: (rows: VarRow[]) => void;
  inherited?: Record<string, string>;
  inheritedLabel?: string;
}) {
  const [reveal, setReveal] = useState(false);
  // "paste" adds variables from a .env; "edit" replaces all rows with one.
  const [mode, setMode] = useState<"" | "paste" | "edit">("");
  const [text, setText] = useState("");
  const set = (i: number, patch: Partial<VarRow>) =>
    onChange(rows.map((r, j) => (j === i ? { ...r, ...patch } : r)));
  const own = new Set(rows.map((r) => r.key.trim()));
  const inheritedRows = toRows(inherited);

  // Values with line breaks can't be written as one .env line.
  const multiline = rows.some((r) => r.key.trim() && r.value.includes("\n"));
  const toggle = (m: "paste" | "edit") => {
    setMode(mode === m ? "" : m);
    setText(m === "edit" && mode !== m ? toDotenv(rows) : "");
  };

  const applyText = () => {
    const byKey = new Map(
      (mode === "edit" ? [] : rows.filter((r) => r.key.trim())).map((r) => [
        r.key.trim(),
        r,
      ]),
    );
    for (const p of parseDotenv(text)) byKey.set(p.key, p);
    onChange([...byKey.values()]);
    setText("");
    setMode("");
  };

  return (
    <div className="flex flex-col gap-2">
      <div className="flex flex-wrap items-center justify-between gap-1">
        <span className="text-faint text-xs">
          {rows.filter((r) => r.key.trim()).length} variable
          {rows.filter((r) => r.key.trim()).length === 1 ? "" : "s"}
        </span>
        <div className="flex flex-wrap gap-1">
          <Button
            variant="ghost"
            onClick={() => setReveal(!reveal)}
            type="button"
          >
            {reveal ? (
              <EyeOff className="size-3.5" />
            ) : (
              <Eye className="size-3.5" />
            )}
            {reveal ? "Hide values" : "Show values"}
          </Button>
          <Button
            variant="ghost"
            onClick={() => toggle("edit")}
            disabled={multiline}
            title={
              multiline
                ? "A value has line breaks; edit the variables one by one"
                : undefined
            }
            type="button"
          >
            <FileText className="size-3.5" /> Edit as .env
          </Button>
          <Button variant="ghost" onClick={() => toggle("paste")} type="button">
            <ClipboardPaste className="size-3.5" /> Paste .env
          </Button>
        </div>
      </div>
      {mode && (
        <div className="flex flex-col gap-1.5">
          <textarea
            value={text}
            onChange={(e) => setText(e.target.value)}
            placeholder={"DATABASE_URL=postgres://…\nLOG_LEVEL=info"}
            spellCheck={false}
            autoFocus
            className={cn(
              "bg-bg border-line-strong w-full rounded-input border p-2 font-mono text-xs",
              mode === "edit" ? "h-64" : "h-32",
            )}
          />
          {mode === "edit" && (
            <span className="text-faint text-xs">
              Replaces all variables with these lines. Values are shown in plain
              text.
            </span>
          )}
          <div className="flex gap-1">
            <Button
              type="button"
              onClick={applyText}
              disabled={mode === "paste" && !text.trim()}
            >
              {mode === "edit" ? "Apply" : "Add variables"}
            </Button>
            <Button variant="ghost" type="button" onClick={() => setMode("")}>
              Cancel
            </Button>
          </div>
        </div>
      )}
      {mode !== "edit" && (
        <div className="flex flex-col gap-1">
          {rows.map((r, i) => (
            <div
              key={i}
              className="grid grid-cols-[minmax(0,2fr)_minmax(0,3fr)_auto] gap-1"
            >
              <Input
                value={r.key}
                onChange={(e) => set(i, { key: e.target.value })}
                placeholder="NAME"
                className={cn(
                  "h-7 font-mono text-xs",
                  r.key.trim() && !keyRE.test(r.key.trim()) && "border-bad",
                )}
              />
              <Input
                value={r.value}
                onChange={(e) => set(i, { value: e.target.value })}
                placeholder="value"
                type={reveal ? "text" : "password"}
                autoComplete="off"
                className="h-7 font-mono text-xs"
              />
              <IconButton
                label="Remove"
                type="button"
                onClick={() => onChange(rows.filter((_, j) => j !== i))}
              >
                <Trash2 className="size-3.5" />
              </IconButton>
            </div>
          ))}
          <div>
            <Button
              variant="ghost"
              type="button"
              onClick={() => onChange([...rows, { key: "", value: "" }])}
            >
              <Plus className="size-3.5" /> Add variable
            </Button>
          </div>
        </div>
      )}
      {inheritedRows.length > 0 && (
        <div className="border-line flex flex-col gap-0.5 border-t pt-2">
          <span className="text-muted text-xs">{inheritedLabel} variables</span>
          {inheritedRows.map((r) => (
            <div
              key={r.key}
              className={cn(
                "grid grid-cols-[minmax(0,2fr)_minmax(0,3fr)_auto] gap-1 font-mono text-xs",
                own.has(r.key) && "text-faint line-through",
              )}
            >
              <span className="truncate">{r.key}</span>
              <span className="truncate">{reveal ? r.value : "••••••"}</span>
              <span className="text-faint font-sans no-underline">
                {own.has(r.key) ? "overridden" : "inherited"}
              </span>
            </div>
          ))}
        </div>
      )}
    </div>
  );
}
