import type { ButtonHTMLAttributes, InputHTMLAttributes, ReactNode, Ref } from "react";
import { cn } from "./cn";

type ButtonVariant = "primary" | "default" | "ghost" | "danger";

export function Button({
  variant = "default",
  className,
  ...props
}: ButtonHTMLAttributes<HTMLButtonElement> & { variant?: ButtonVariant; ref?: Ref<HTMLButtonElement> }) {
  return (
    <button
      {...props}
      className={cn(
        "inline-flex h-7 items-center justify-center gap-1.5 rounded-btn border px-2.5 text-xs font-medium whitespace-nowrap transition-colors disabled:cursor-not-allowed disabled:opacity-50",
        variant === "primary" && "bg-btn-primary hover:bg-btn-primary-hover border-btn-primary-hover/60 text-white",
        variant === "default" && "bg-btn border-line-strong hover:bg-btn-hover hover:border-line-strong text-fg",
        variant === "ghost" && "text-muted hover:text-fg hover:bg-hover border-transparent",
        variant === "danger" && "bg-btn text-bad border-bad/20 hover:bg-bad/10 hover:border-bad/35",
        className,
      )}
    />
  );
}

export function IconButton({
  label,
  className,
  ...props
}: ButtonHTMLAttributes<HTMLButtonElement> & { label: string }) {
  return (
    <button
      {...props}
      aria-label={label}
      title={label}
      className={cn(
        "text-muted hover:text-fg hover:bg-hover rounded-btn inline-flex size-7 items-center justify-center transition-colors",
        className,
      )}
    />
  );
}

export function Input({ className, ...props }: InputHTMLAttributes<HTMLInputElement>) {
  // A width or height from the caller replaces the default (utilities of the
  // same kind would otherwise fight over CSS order).
  const width = /(^|\s)w-/.test(className ?? "") ? "" : "w-full";
  const height = /(^|\s)h-/.test(className ?? "") ? "" : "h-8";
  return (
    <input
      {...props}
      className={cn(
        "bg-bg border-line-strong placeholder:text-faint focus:border-line-accent rounded-input border px-2 text-sm outline-none",
        width,
        height,
        className,
      )}
    />
  );
}

export function Field({ label, hint, children }: { label: string; hint?: ReactNode; children: ReactNode }) {
  return (
    <label className="flex flex-col gap-1">
      <span className="text-muted text-xs">{label}</span>
      {children}
      {hint && <span className="text-faint text-xs">{hint}</span>}
    </label>
  );
}

const dot: Record<string, string> = {
  ok: "bg-ok",
  warn: "bg-warn",
  bad: "bg-bad",
  info: "bg-info",
  neutral: "bg-neutral",
};

export function StatusBadge({ tone, children }: { tone: keyof typeof dot; children: ReactNode }) {
  return (
    <span className="border-line bg-raised inline-flex h-5 items-center gap-1.5 rounded-sm border px-1.5 text-xs">
      <span className={cn("size-1.5 rounded-full", dot[tone])} />
      {children}
    </span>
  );
}

export function Alert({ tone = "bad", children }: { tone?: "bad" | "warn" | "info"; children: ReactNode }) {
  return (
    <div
      role="alert"
      className={cn(
        "rounded-sm border px-2 py-1.5 text-xs",
        tone === "bad" && "border-bad/20 bg-bad/10 text-bad",
        tone === "warn" && "border-warn/20 bg-warn/10 text-warn",
        tone === "info" && "border-info/20 bg-info/10 text-info",
      )}
    >
      {children}
    </div>
  );
}

/** An on/off switch with a label and an optional hint. */
export function Toggle({
  checked,
  onChange,
  label,
  hint,
  disabled,
}: {
  checked: boolean;
  onChange: (v: boolean) => void;
  label: ReactNode;
  hint?: ReactNode;
  disabled?: boolean;
}) {
  return (
    <label className={cn("flex items-start gap-2", disabled ? "opacity-50" : "cursor-pointer")}>
      <button
        type="button"
        role="switch"
        aria-checked={checked}
        disabled={disabled}
        onClick={() => onChange(!checked)}
        className={cn(
          "mt-0.5 inline-flex h-4 w-7 shrink-0 items-center rounded-full border p-px transition-colors",
          checked ? "bg-btn-primary border-btn-primary-hover" : "bg-btn border-line-strong",
        )}
      >
        <span className={cn("size-3 rounded-full transition-transform", checked ? "translate-x-3 bg-white" : "bg-muted translate-x-0")} />
      </button>
      <span className="flex flex-col gap-0.5">
        <span className="text-xs">{label}</span>
        {hint && <span className="text-faint text-xs">{hint}</span>}
      </span>
    </label>
  );
}
