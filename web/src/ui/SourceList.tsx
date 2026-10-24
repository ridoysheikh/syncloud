import { useState, type KeyboardEvent } from "react";
import { X } from "lucide-react";
import { Input } from "./controls";
import { cn } from "./cn";

const sourceRE = /^[0-9a-fA-F:.]+(\/\d{1,3})?$/;

/** A list of IPs or CIDRs as removable chips, with an input to add more
 * (Enter, comma or space adds; pasting a list adds each). */
export function SourceList({
  value,
  onChange,
  placeholder = "203.0.113.0/24",
  label,
  empty = "none",
}: {
  value: string[];
  onChange: (v: string[]) => void;
  placeholder?: string;
  label: string;
  /** Shown when the list is empty. */
  empty?: string;
}) {
  const [text, setText] = useState("");
  const [bad, setBad] = useState("");
  const add = (raw: string) => {
    const parts = raw.split(/[\s,]+/).map((s) => s.trim()).filter(Boolean);
    const wrong = parts.filter((p) => !sourceRE.test(p));
    setBad(wrong.join(", "));
    const next = [...value];
    for (const p of parts) if (sourceRE.test(p) && !next.includes(p)) next.push(p);
    if (next.length !== value.length) onChange(next);
    setText(wrong.join(" "));
  };
  const onKey = (e: KeyboardEvent<HTMLInputElement>) => {
    if (e.key === "Enter" || e.key === "," || e.key === " ") {
      e.preventDefault();
      if (text.trim()) add(text);
    } else if (e.key === "Backspace" && !text && value.length) {
      onChange(value.slice(0, -1));
    }
  };
  return (
    <div className="flex flex-col gap-1">
      <div
        role="group"
        aria-label={label}
        className="border-line-strong bg-bg rounded-input flex min-h-8 flex-wrap items-center gap-1 border px-1 py-0.5"
      >
        {value.length === 0 && !text && (
          <span className="text-faint px-1 text-xs">{empty}</span>
        )}
        {value.map((s) => (
          <span
            key={s}
            className="border-line bg-raised inline-flex h-6 items-center gap-1 rounded-sm border pr-0.5 pl-1.5 font-mono text-xs"
          >
            {s}
            <button
              type="button"
              aria-label={`Remove ${s}`}
              className="text-muted hover:text-fg rounded-sm p-0.5"
              onClick={() => onChange(value.filter((x) => x !== s))}
            >
              <X className="size-3" />
            </button>
          </span>
        ))}
        <Input
          aria-label={`Add to ${label}`}
          value={text}
          onChange={(e) => setText(e.target.value)}
          onKeyDown={onKey}
          onBlur={() => text.trim() && add(text)}
          onPaste={(e) => {
            const t = e.clipboardData.getData("text");
            if (/[\s,]/.test(t.trim())) {
              e.preventDefault();
              add(t);
            }
          }}
          placeholder={value.length ? "" : placeholder}
          spellCheck={false}
          className={cn(
            "h-6 min-w-32 flex-1 border-0 bg-transparent px-1 font-mono text-xs focus:ring-0",
          )}
        />
      </div>
      {bad && <span className="text-bad text-xs">Not an IP or CIDR: {bad}</span>}
    </div>
  );
}
