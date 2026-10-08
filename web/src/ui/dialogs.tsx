import {
  useEffect,
  useRef,
  useState,
  useSyncExternalStore,
  type ReactNode,
} from "react";
import { AlertTriangle, Info } from "lucide-react";
import { Dialog } from "./Dialog";
import { Button, Input } from "./controls";
import { cn } from "./cn";

/**
 * App dialogs in place of the browser's confirm(), alert() and prompt():
 *
 *   if (await confirmDialog({ title: "Delete orders?", message: "…", tone: "danger" })) del.mutate();
 *
 * One <DialogHost/> at the root renders them, one at a time.
 */

type Tone = "default" | "danger" | "warn";

export interface ConfirmOptions {
  title: string;
  message?: ReactNode;
  /** The confirming button's label (default "Confirm", or "Delete" for danger). */
  confirmLabel?: string;
  cancelLabel?: string;
  tone?: Tone;
  /** The user must type this (a name) before confirming. */
  typeToConfirm?: string;
  /** A checkbox the user can tick; see confirmChoice. */
  option?: { label: ReactNode; hint?: ReactNode; checked?: boolean };
}

export interface AlertOptions {
  title: string;
  message?: ReactNode;
  tone?: Tone;
  okLabel?: string;
}

interface Pending {
  id: number;
  kind: "confirm" | "alert";
  opts: ConfirmOptions & AlertOptions;
  resolve: (ok: boolean, option: boolean) => void;
}

let queue: Pending[] = [];
let nextID = 1;
const listeners = new Set<() => void>();
const emit = () => listeners.forEach((l) => l());

function push(kind: Pending["kind"], opts: ConfirmOptions & AlertOptions) {
  return new Promise<{ ok: boolean; option: boolean }>((resolve) => {
    queue = [
      ...queue,
      {
        id: nextID++,
        kind,
        opts,
        resolve: (ok, option) => resolve({ ok, option }),
      },
    ];
    emit();
  });
}

/** Asks the user to confirm; resolves true when they do. */
export async function confirmDialog(
  opts: ConfirmOptions | string,
): Promise<boolean> {
  return (
    await push("confirm", typeof opts === "string" ? { title: opts } : opts)
  ).ok;
}

/**
 * Asks the user to confirm, with the checkbox of opts.option: resolves
 * whether they confirmed and whether the box was ticked.
 */
export function confirmChoice(
  opts: ConfirmOptions,
): Promise<{ ok: boolean; option: boolean }> {
  return push("confirm", opts);
}

/** Tells the user something; resolves when they close it. */
export async function alertDialog(opts: AlertOptions | string): Promise<void> {
  await push("alert", typeof opts === "string" ? { title: opts } : opts);
}

function settle(p: Pending, ok: boolean, option = false) {
  queue = queue.filter((x) => x.id !== p.id);
  emit();
  p.resolve(ok, option);
}

const subscribe = (l: () => void) => {
  listeners.add(l);
  return () => listeners.delete(l);
};

/** Renders the pending dialog; mount once. */
export function DialogHost() {
  const current = useSyncExternalStore(subscribe, () => queue[0]);
  if (!current) return null;
  return <PendingDialog key={current.id} p={current} />;
}

function PendingDialog({ p }: { p: Pending }) {
  const { opts } = p;
  const tone = opts.tone ?? "default";
  const [typed, setTyped] = useState("");
  const [option, setOption] = useState(!!opts.option?.checked);
  const confirmRef = useRef<HTMLButtonElement>(null);
  const blocked =
    p.kind === "confirm" &&
    !!opts.typeToConfirm &&
    typed !== opts.typeToConfirm;
  useEffect(() => {
    if (!opts.typeToConfirm) confirmRef.current?.focus();
  }, [opts.typeToConfirm]);
  const Icon = tone === "default" ? Info : AlertTriangle;
  const ok = () => !blocked && settle(p, true, option);
  return (
    <Dialog
      open
      title={opts.title}
      onClose={() => settle(p, false)}
      footer={
        p.kind === "alert" ? (
          <Button
            ref={confirmRef}
            variant="primary"
            onClick={() => settle(p, true)}
          >
            {opts.okLabel ?? "OK"}
          </Button>
        ) : (
          <>
            <Button variant="ghost" onClick={() => settle(p, false)}>
              {opts.cancelLabel ?? "Cancel"}
            </Button>
            <Button
              ref={confirmRef}
              variant={tone === "danger" ? "danger" : "primary"}
              disabled={blocked}
              onClick={ok}
            >
              {opts.confirmLabel ?? (tone === "danger" ? "Delete" : "Confirm")}
            </Button>
          </>
        )
      }
    >
      <div className="flex gap-2.5 text-xs">
        <Icon
          className={cn(
            "mt-0.5 size-4 shrink-0",
            tone === "danger"
              ? "text-bad"
              : tone === "warn"
                ? "text-warn"
                : "text-accent",
          )}
        />
        <div className="flex min-w-0 flex-1 flex-col gap-2">
          {opts.message && (
            <div className="text-muted break-words">{opts.message}</div>
          )}
          {p.kind === "confirm" && opts.option && (
            <label className="flex cursor-pointer items-start gap-2">
              <input
                type="checkbox"
                checked={option}
                onChange={(e) => setOption(e.target.checked)}
                className="mt-0.5 size-3.5 shrink-0"
              />
              <span className="flex min-w-0 flex-col gap-0.5">
                <span className="text-fg">{opts.option.label}</span>
                {opts.option.hint && (
                  <span className="text-faint">{opts.option.hint}</span>
                )}
              </span>
            </label>
          )}
          {p.kind === "confirm" && opts.typeToConfirm && (
            <form
              onSubmit={(e) => {
                e.preventDefault();
                ok();
              }}
              className="flex flex-col gap-1"
            >
              <label className="text-muted">
                Type{" "}
                <code className="text-fg font-mono break-all">
                  {opts.typeToConfirm}
                </code>{" "}
                to confirm.
              </label>
              <Input
                autoFocus
                value={typed}
                onChange={(e) => setTyped(e.target.value)}
                aria-label="Confirmation"
                spellCheck={false}
                autoComplete="off"
                className="font-mono"
              />
            </form>
          )}
        </div>
      </div>
    </Dialog>
  );
}

/**
 * confirmDialog from one sentence such as "Delete pool x? It must be
 * empty.": the question is the title, the rest the message; the leading
 * verb names the button and destructive verbs make it a danger dialog.
 */
export function confirmAction(
  text: string,
  opts: Partial<ConfirmOptions> = {},
): Promise<boolean> {
  const i = text.indexOf("?");
  const title = i >= 0 ? text.slice(0, i + 1) : text;
  const rest = i >= 0 ? text.slice(i + 1).trim() : "";
  const words = title.split(/\s+/);
  const verb = words[0] ?? "";
  const off = /\boff\b/.test(title);
  const danger =
    off ||
    /^(Delete|Remove|Drop|Terminate|End|Reset|Stop|Cancel|Revoke|Disable|Purge|Forget|Detach|Evict|Drain|Kill)$/.test(
      verb,
    );
  let confirmLabel = verb.replace(/[?,.]$/, "") || "Confirm";
  let cancelLabel: string | undefined;
  if (verb === "Turn") confirmLabel = off ? "Turn off" : "Turn on";
  if (verb === "Roll") confirmLabel = "Roll out";
  if (verb === "Cancel") {
    confirmLabel =
      `Cancel ${words[1] === "the" ? (words[2] ?? "it") : "it"}`.replace(
        /[?,.]$/,
        "",
      );
    cancelLabel = "Keep";
  }
  return confirmDialog({
    title,
    message: rest || undefined,
    tone: danger ? "danger" : "default",
    confirmLabel,
    cancelLabel,
    ...opts,
  });
}
