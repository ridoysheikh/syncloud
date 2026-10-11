import { useState } from "react";
import { Check, Copy } from "lucide-react";
import { IconButton } from "./controls";
import { cn } from "./cn";

/**
 * A value to copy (a secret, a URL, a command) in a field-styled box with
 * a copy button. `wrap` breaks long values over lines (commands); otherwise
 * one line, truncated.
 */
export function CopyBox({ value, wrap }: { value: string; wrap?: boolean }) {
  const [copied, setCopied] = useState(false);
  return (
    <div
      className={cn(
        "bg-bg border-line-strong rounded-input flex min-w-0 gap-1 border pl-2",
        wrap ? "items-start p-1.5" : "h-8 items-center",
      )}
    >
      <code
        title={wrap ? undefined : value}
        className={cn(
          "min-w-0 flex-1 font-mono text-xs select-all",
          wrap ? "break-all" : "truncate",
        )}
      >
        {value}
      </code>
      <IconButton
        label="Copy"
        onClick={() =>
          navigator.clipboard?.writeText(value).then(() => {
            setCopied(true);
            setTimeout(() => setCopied(false), 1500);
          })
        }
      >
        {copied ? (
          <Check className="text-ok size-3.5" />
        ) : (
          <Copy className="size-3.5" />
        )}
      </IconButton>
    </div>
  );
}
