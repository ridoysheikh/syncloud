import { useEffect, useId, useRef, type ReactNode } from "react";
import { cn } from "./cn";

const sizes = {
  sm: "max-w-sm",
  md: "max-w-md",
  lg: "max-w-2xl",
  /** Terminals, editors, run logs. */
  xl: "max-w-5xl",
};

/**
 * The one modal of the dashboard, built on <dialog> (focus trapping and Esc
 * come from the browser). A title bar, a body that scrolls, and a footer that
 * stays put for the actions; the close control sits on the top-right corner
 * of every dialog, labelled with the key that does the same.
 *
 * Put a form's buttons in `footer` and point them at the form with its id
 * (`<Button type="submit" form="create-token">`), so every dialog's actions
 * sit in the same place.
 */
export function Dialog({
  open,
  onClose,
  title,
  description,
  children,
  footer,
  size = "md",
  className,
}: {
  open: boolean;
  onClose: () => void;
  title: ReactNode;
  /** A line under the title. */
  description?: ReactNode;
  children: ReactNode;
  footer?: ReactNode;
  size?: keyof typeof sizes;
  /** Classes of the body. */
  className?: string;
}) {
  const ref = useRef<HTMLDialogElement>(null);
  const titleId = useId();
  useEffect(() => {
    const d = ref.current;
    if (!d) return;
    if (open && !d.open) d.showModal();
    if (!open && d.open) d.close();
  }, [open]);

  return (
    <dialog
      ref={ref}
      onClose={onClose}
      aria-labelledby={titleId}
      className={cn(
        "bg-surface border-line-strong text-fg m-auto max-h-[90vh] w-[calc(100%-2rem)] flex-col overflow-hidden rounded-md border p-0 shadow-2xl backdrop:bg-black/60 open:flex",
        sizes[size],
      )}
    >
      {/* pe- leaves room for the corner close control. */}
      <header className="border-line flex min-h-9 shrink-0 flex-col justify-center gap-0.5 border-b py-1.5 ps-1.5 pe-12 sm:ps-2 md:ps-3">
        <h2 id={titleId} className="truncate text-sm font-semibold">
          {title}
        </h2>
        {description && (
          <p className="text-muted text-xs leading-snug">{description}</p>
        )}
      </header>
      <div
        className={cn(
          "min-h-0 flex-1 overflow-y-auto overscroll-contain p-1.5 sm:p-2 md:p-3",
          className,
        )}
      >
        {children}
      </div>
      {footer && (
        <footer className="border-line flex shrink-0 flex-wrap justify-end gap-1.5 border-t px-1.5 py-1.5 sm:px-2 md:px-3">
          {footer}
        </footer>
      )}
      {/* Last in the DOM so it does not take the initial focus. Only its
          inner edges are drawn: the dialog's border is the other two. */}
      <button
        type="button"
        onClick={() => ref.current?.close()}
        aria-label="Close"
        title="Close (Esc)"
        className="border-line-strong bg-bg/60 text-muted hover:bg-hover hover:text-fg absolute top-0 right-0 z-20 flex h-6 items-center rounded-bl-sm border-b border-l px-1.5 font-mono text-[10px] tracking-[0.08em] uppercase transition-colors"
      >
        Esc
      </button>
    </dialog>
  );
}
