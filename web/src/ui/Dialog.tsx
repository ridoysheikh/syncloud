import { useEffect, useRef, type ReactNode } from "react";

/** Modal dialog built on <dialog> (focus trapping and Esc handling come from the browser). */
export function Dialog({
  open,
  onClose,
  title,
  children,
  footer,
}: {
  open: boolean;
  onClose: () => void;
  title: string;
  children: ReactNode;
  footer?: ReactNode;
}) {
  const ref = useRef<HTMLDialogElement>(null);
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
      className="bg-surface border-line-strong text-fg m-auto w-[calc(100%-2rem)] max-w-md rounded-md border p-0 backdrop:bg-black/60"
    >
      <div className="border-line flex h-9 items-center border-b px-3">
        <h2 className="text-sm font-semibold">{title}</h2>
      </div>
      <div className="p-3">{children}</div>
      {footer && <div className="border-line flex justify-end gap-1.5 border-t px-3 py-2">{footer}</div>}
    </dialog>
  );
}
