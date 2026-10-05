import type { ButtonHTMLAttributes, InputHTMLAttributes, ReactNode } from "react";
import { cn } from "./cn";

type ButtonVariant = "primary" | "default" | "ghost" | "danger";

export function Button({
  variant = "default",
  className,
  ...props
}: ButtonHTMLAttributes<HTMLButtonElement> & { variant?: ButtonVariant }) {
  return (
    <button
      {...props}
      className={cn(
        "inline-flex h-7 items-center justify-center gap-1.5 rounded-sm border px-2.5 text-xs font-medium whitespace-nowrap transition-colors disabled:cursor-not-allowed disabled:opacity-50",
        variant === "primary" && "bg-accent text-accent-fg border-accent hover:brightness-110",
        variant === "default" && "bg-raised border-line-strong hover:bg-hover",
        variant === "ghost" && "text-muted hover:text-fg hover:bg-hover border-transparent",
        variant === "danger" && "bg-bad/10 text-bad border-bad/40 hover:bg-bad/20",
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
        "text-muted hover:text-fg hover:bg-hover inline-flex size-7 items-center justify-center rounded-sm transition-colors",
        className,
      )}
    />
  );
}

export function Input({ className, ...props }: InputHTMLAttributes<HTMLInputElement>) {
  return (
    <input
      {...props}
      className={cn(
        "bg-bg border-line-strong placeholder:text-faint focus:border-accent h-8 w-full rounded-sm border px-2 text-sm outline-none",
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
        tone === "bad" && "border-bad/40 bg-bad/10 text-bad",
        tone === "warn" && "border-warn/40 bg-warn/10 text-warn",
        tone === "info" && "border-info/40 bg-info/10 text-info",
      )}
    >
      {children}
    </div>
  );
}
