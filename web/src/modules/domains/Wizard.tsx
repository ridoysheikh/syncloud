import type { ReactNode } from "react";
import { Link } from "@tanstack/react-router";
import { Check, ChevronLeft, ChevronRight } from "lucide-react";
import { PageHeader } from "@/ui/PageHeader";
import { Panel } from "@/ui/Panel";
import { Alert, Button } from "@/ui/controls";
import { cn, gap } from "@/ui/cn";

/**
 * A full-page wizard: the steps on the left, one panel per step, Back and
 * Next below. A step with a problem stops Next, and the steps after it can
 * not be jumped to.
 */
export function Wizard<S extends string>({
  crumbs,
  title,
  cancelTo,
  steps,
  step,
  onStep,
  problems,
  finish,
  children,
}: {
  crumbs: ReactNode[];
  title: string;
  cancelTo: string;
  steps: readonly S[];
  step: S;
  onStep: (s: S) => void;
  problems: Record<S, string>;
  finish: {
    label: string;
    pending?: boolean;
    error?: ReactNode;
    onClick: () => void;
  };
  children: ReactNode;
}) {
  const idx = steps.indexOf(step);
  const blocked = steps.slice(0, idx + 1).find((s) => problems[s]);
  const last = idx === steps.length - 1;
  return (
    <div className={cn("flex flex-col", gap)}>
      <PageHeader
        crumbs={crumbs}
        title={title}
        actions={
          <Link to={cancelTo as string}>
            <Button variant="ghost">Cancel</Button>
          </Link>
        }
      />
      <div className={cn("grid grid-cols-1 md:grid-cols-[12rem_1fr]", gap)}>
        <ol className="flex gap-1 md:flex-col">
          {steps.map((s, i) => (
            <li key={s}>
              <button
                type="button"
                onClick={() =>
                  i <= idx || !steps.slice(0, i).find((x) => problems[x])
                    ? onStep(s)
                    : undefined
                }
                className={cn(
                  "flex w-full items-center gap-2 rounded-sm px-2 py-1.5 text-left text-xs",
                  s === step ? "bg-raised text-fg" : "text-muted hover:text-fg",
                )}
              >
                <span
                  className={cn(
                    "flex size-5 shrink-0 items-center justify-center rounded-full border text-[10px]",
                    i < idx
                      ? "border-accent bg-accent text-accent-fg"
                      : s === step
                        ? "border-line-accent text-accent"
                        : "border-line",
                  )}
                >
                  {i < idx ? <Check className="size-3" /> : i + 1}
                </span>
                <span className="hidden sm:inline">{s}</span>
              </button>
            </li>
          ))}
        </ol>
        <div className="flex min-w-0 flex-col gap-2">
          <Panel title={step}>{children}</Panel>
          {blocked && blocked !== step && (
            <Alert tone="warn">
              Fix the {blocked} step: {problems[blocked]}
            </Alert>
          )}
          {problems[step] && <Alert tone="warn">{problems[step]}</Alert>}
          {finish.error && <Alert>{finish.error}</Alert>}
          <div className="flex justify-between">
            <Button
              variant="ghost"
              disabled={idx === 0}
              onClick={() => onStep(steps[idx - 1] ?? step)}
            >
              <ChevronLeft className="size-3.5" /> Back
            </Button>
            {last ? (
              <Button
                variant="primary"
                disabled={!!blocked || finish.pending}
                onClick={finish.onClick}
              >
                {finish.pending ? "Working…" : finish.label}
              </Button>
            ) : (
              <Button
                variant="primary"
                disabled={!!problems[step]}
                onClick={() => onStep(steps[idx + 1] ?? step)}
              >
                Next <ChevronRight className="size-3.5" />
              </Button>
            )}
          </div>
        </div>
      </div>
    </div>
  );
}

/** A label and its value in a review. */
export function ReviewRow({ k, children }: { k: string; children: ReactNode }) {
  return (
    <div className="border-line grid grid-cols-[8rem_1fr] gap-2 border-b py-1.5 text-xs last:border-b-0">
      <span className="text-muted">{k}</span>
      <span className="min-w-0 break-words">{children}</span>
    </div>
  );
}

/** A multi-line text box (a PEM, a list). */
export function TextArea({
  className,
  ...props
}: React.TextareaHTMLAttributes<HTMLTextAreaElement>) {
  return (
    <textarea
      {...props}
      className={cn(
        "bg-bg border-line-strong placeholder:text-faint focus:border-line-accent rounded-input w-full border px-2 py-1.5 font-mono text-xs outline-none",
        className,
      )}
    />
  );
}
