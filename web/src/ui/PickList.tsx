import {
  useEffect,
  useId,
  useMemo,
  useRef,
  useState,
  type KeyboardEvent,
  type ReactNode,
} from "react";
import { Check, ChevronDown, PencilLine, Plus, Search, X } from "lucide-react";
import { Input } from "./controls";
import { cn } from "./cn";
import { Popup } from "./popover";

/*
 * Pickers for values the system already knows (addresses, images, peers):
 * choose from a searchable list, and type a value of your own only through
 * the explicit "Custom value" row. PickList takes many values as chips,
 * PickOne a single one.
 */

export interface PickOption {
  value: string;
  /** Shown instead of the value. */
  label?: ReactNode;
  /** Muted text at the end of the row. */
  hint?: ReactNode;
  /** Rows of one group sit under its heading; keep them together. */
  group?: string;
}

/** Most rows drawn at once; the search narrows the rest. */
const SHOWN_MAX = 100;

interface PanelProps {
  options: PickOption[];
  selected: string[];
  /** Called with a listed or a custom value. */
  onPick: (value: string) => void;
  onClose: () => void;
  /** A message when a custom value is not acceptable. */
  validate?: (value: string) => string | null;
  customLabel: string;
  customPlaceholder?: string;
  searchPlaceholder: string;
  mono?: boolean;
  /** The panel closes after a pick (single choice). */
  closeOnPick: boolean;
  /** Offered as the first row in the list (a "none" choice). */
  none?: { label: string; value: string };
}

/** The search box, the rows and the custom-value form shared by both pickers. */
function Panel({
  options,
  selected,
  onPick,
  onClose,
  validate,
  customLabel,
  customPlaceholder,
  searchPlaceholder,
  mono,
  closeOnPick,
  none,
}: PanelProps) {
  const [mode, setMode] = useState<"list" | "custom">("list");
  const [query, setQuery] = useState("");
  const [custom, setCustom] = useState("");
  const [active, setActive] = useState(0);
  const list = useRef<HTMLDivElement>(null);
  const baseId = useId();

  const shown = useMemo(() => {
    const q = query.trim().toLowerCase();
    const rows = q
      ? options.filter((o) =>
          `${o.value} ${typeof o.label === "string" ? o.label : ""} ${typeof o.hint === "string" ? o.hint : ""}`
            .toLowerCase()
            .includes(q),
        )
      : options;
    return rows.slice(0, SHOWN_MAX);
  }, [options, query]);

  const rows = useMemo(
    () => (none && !query.trim() ? [{ value: none.value, label: none.label } as PickOption, ...shown] : shown),
    [none, query, shown],
  );

  // Keep the active row in view while arrowing through a long list.
  useEffect(() => {
    list.current
      ?.querySelector(`[data-index="${active}"]`)
      ?.scrollIntoView({ block: "nearest" });
  }, [active]);

  const typed = query.trim();
  const typedProblem = typed ? validate?.(typed) ?? null : "empty";
  const canUseTyped =
    !!typed && !typedProblem && !options.some((o) => o.value === typed);
  const customProblem = custom.trim() ? validate?.(custom.trim()) ?? null : null;

  const pick = (value: string) => {
    onPick(value);
    if (closeOnPick) onClose();
  };
  const submitCustom = () => {
    const v = custom.trim();
    if (!v || validate?.(v)) return;
    pick(v);
    setCustom("");
    setQuery("");
    setMode("list");
  };
  const openCustom = () => {
    setCustom(typed);
    setMode("custom");
  };

  const onKey = (e: KeyboardEvent) => {
    if (e.key === "Escape") {
      // Closes the picker, not the dialog around it.
      e.preventDefault();
      e.stopPropagation();
      if (mode === "custom") setMode("list");
      else onClose();
      return;
    }
    if (e.key === "Tab") return onClose();
    if (mode !== "list") return;
    if (e.key === "ArrowDown" || e.key === "ArrowUp") {
      e.preventDefault();
      const n = rows.length;
      if (n) setActive((i) => (e.key === "ArrowDown" ? (i + 1) % n : (i - 1 + n) % n));
    } else if (e.key === "Enter") {
      e.preventDefault();
      if (rows[active]) pick(rows[active]!.value);
      else if (canUseTyped) pick(typed);
    }
  };

  if (mode === "custom") {
    return (
      <div className="flex w-72 max-w-full flex-col gap-1.5 p-2" onKeyDown={onKey}>
        <span className="text-muted text-xs">{customLabel}</span>
        <Input
          autoFocus
          value={custom}
          onChange={(e) => setCustom(e.target.value)}
          onKeyDown={(e) => {
            if (e.key === "Enter") {
              e.preventDefault();
              submitCustom();
            }
          }}
          placeholder={customPlaceholder}
          aria-label={customLabel}
          spellCheck={false}
          className={cn("h-7 text-xs", mono && "font-mono")}
        />
        {customProblem && <span className="text-bad text-xs">{customProblem}</span>}
        <div className="flex justify-end gap-1.5">
          <button
            type="button"
            onClick={() => setMode("list")}
            className="text-muted hover:text-fg h-6 rounded-sm px-2 text-xs"
          >
            Back to the list
          </button>
          <button
            type="button"
            disabled={!custom.trim() || !!customProblem}
            onClick={submitCustom}
            className="bg-btn border-line-strong hover:bg-btn-hover text-fg h-6 rounded-sm border px-2 text-xs disabled:cursor-not-allowed disabled:opacity-50"
          >
            Add
          </button>
        </div>
      </div>
    );
  }

  let lastGroup: string | undefined;
  return (
    <>
      <label className="border-line flex h-8 shrink-0 items-center gap-1.5 border-b px-2">
        <Search className="text-muted size-3.5 shrink-0" />
        <input
          autoFocus
          value={query}
          onChange={(e) => {
            setQuery(e.target.value);
            setActive(0);
          }}
          onKeyDown={onKey}
          role="searchbox"
          aria-label={searchPlaceholder}
          aria-controls={`${baseId}-list`}
          placeholder={searchPlaceholder}
          spellCheck={false}
          autoComplete="off"
          className="h-full min-w-0 flex-1 bg-transparent text-xs outline-none"
        />
      </label>
      <div
        ref={list}
        id={`${baseId}-list`}
        role="listbox"
        className="min-h-0 flex-1 overflow-y-auto overscroll-contain p-1"
      >
        {rows.length === 0 && (
          <div className="text-faint px-2 py-2 text-xs">
            Nothing known matches{typed ? ` “${typed}”` : ""}.
          </div>
        )}
        {rows.map((o, i) => {
          const on = selected.includes(o.value);
          const heading = o.group && o.group !== lastGroup ? o.group : null;
          lastGroup = o.group;
          return (
            <div key={`${i}:${o.value}`}>
              {heading && (
                <div className="text-faint px-2 pt-1.5 pb-0.5 text-[10px] font-medium uppercase">
                  {heading}
                </div>
              )}
              <div
                data-index={i}
                role="option"
                aria-selected={on}
                title={o.value}
                onMouseDown={(e) => e.preventDefault()}
                onMouseMove={() => active !== i && setActive(i)}
                onClick={() => pick(o.value)}
                className={cn(
                  "flex min-w-0 cursor-pointer items-center gap-1.5 rounded-sm px-2 py-1 text-xs",
                  i === active && "bg-hover",
                  on ? "text-accent" : "text-fg",
                )}
              >
                <span className={cn("min-w-0 flex-1 truncate", mono && !o.label && "font-mono")}>
                  {o.label ?? o.value}
                </span>
                {o.hint && <span className="text-muted max-w-[45%] shrink-0 truncate">{o.hint}</span>}
                <span className="flex size-3.5 shrink-0 items-center justify-center">
                  {on && <Check className="size-3.5" strokeWidth={2.5} />}
                </span>
              </div>
            </div>
          );
        })}
      </div>
      <div className="border-line shrink-0 border-t p-1">
        {canUseTyped && (
          <button
            type="button"
            onMouseDown={(e) => e.preventDefault()}
            onClick={() => pick(typed)}
            className="hover:bg-hover text-fg flex w-full min-w-0 items-center gap-1.5 rounded-sm px-2 py-1 text-left text-xs"
          >
            <Plus className="text-muted size-3.5 shrink-0" />
            <span className="min-w-0 truncate">
              Use <span className="font-mono">{typed}</span> as it is
            </span>
          </button>
        )}
        <button
          type="button"
          onMouseDown={(e) => e.preventDefault()}
          onClick={openCustom}
          className="hover:bg-hover text-muted hover:text-fg flex w-full items-center gap-1.5 rounded-sm px-2 py-1 text-left text-xs"
        >
          <PencilLine className="size-3.5 shrink-0" />
          Custom value…
        </button>
      </div>
    </>
  );
}

interface CommonProps {
  options: PickOption[];
  /** The message when a custom value is not acceptable, else null. */
  validate?: (value: string) => string | null;
  customLabel?: string;
  customPlaceholder?: string;
  searchPlaceholder?: string;
  mono?: boolean;
  label: string;
}

/** Many values as removable chips, added from the list or as a custom value. */
export function PickList({
  value,
  onChange,
  options,
  validate,
  customLabel = "Custom value",
  customPlaceholder,
  searchPlaceholder = "Search…",
  mono = true,
  label,
  empty = "none",
  addLabel = "Add",
}: CommonProps & {
  value: string[];
  onChange: (v: string[]) => void;
  /** Shown when nothing is chosen. */
  empty?: string;
  addLabel?: string;
}) {
  const [open, setOpen] = useState(false);
  const anchor = useRef<HTMLDivElement>(null);
  const popup = useRef<HTMLDivElement>(null);
  // Chosen values stay in the list (checked) so they can be taken off there.
  const byValue = useMemo(() => new Map(options.map((o) => [o.value, o])), [options]);
  const toggle = (v: string) =>
    onChange(value.includes(v) ? value.filter((x) => x !== v) : [...value, v]);
  return (
    <div className="flex flex-col gap-1">
      <div
        ref={anchor}
        role="group"
        aria-label={label}
        className="border-line-strong bg-bg rounded-input flex min-h-8 flex-wrap items-center gap-1 border px-1 py-0.5"
      >
        {value.length === 0 && <span className="text-faint px-1 text-xs">{empty}</span>}
        {value.map((v) => (
          <span
            key={v}
            title={byValue.get(v)?.hint ? `${v} · ${String(byValue.get(v)!.hint)}` : v}
            className="border-line bg-raised inline-flex h-6 max-w-full items-center gap-1 rounded-sm border pr-0.5 pl-1.5 text-xs"
          >
            <span className={cn("min-w-0 truncate", mono && "font-mono")}>{v}</span>
            <button
              type="button"
              aria-label={`Remove ${v}`}
              className="text-muted hover:text-fg rounded-sm p-0.5"
              onClick={() => onChange(value.filter((x) => x !== v))}
            >
              <X className="size-3" />
            </button>
          </span>
        ))}
        <button
          type="button"
          aria-haspopup="listbox"
          aria-expanded={open}
          onClick={() => setOpen((o) => !o)}
          className="text-muted hover:text-fg hover:bg-hover ml-auto inline-flex h-6 items-center gap-1 rounded-sm px-1.5 text-xs"
        >
          <Plus className="size-3" />
          {addLabel}
        </button>
      </div>
      <Popup open={open} anchor={anchor} popup={popup} onClose={() => setOpen(false)} maxHeight={320}>
        <Panel
          options={options}
          selected={value}
          onPick={toggle}
          onClose={() => setOpen(false)}
          validate={validate}
          customLabel={customLabel}
          customPlaceholder={customPlaceholder}
          searchPlaceholder={searchPlaceholder}
          mono={mono}
          closeOnPick={false}
        />
      </Popup>
    </div>
  );
}

/** One value, chosen from the list or typed as a custom value. */
export function PickOne({
  value,
  onChange,
  options,
  validate,
  customLabel = "Custom value",
  customPlaceholder,
  searchPlaceholder = "Search…",
  mono = true,
  label,
  placeholder = "Choose…",
  none,
  className,
  size = "md",
  disabled,
}: CommonProps & {
  value: string;
  onChange: (v: string) => void;
  placeholder?: string;
  /** A choice that clears the value, offered first. */
  none?: { label: string; value: string };
  className?: string;
  size?: "sm" | "md";
  disabled?: boolean;
}) {
  const [open, setOpen] = useState(false);
  const anchor = useRef<HTMLButtonElement>(null);
  const popup = useRef<HTMLDivElement>(null);
  const current = options.find((o) => o.value === value);
  const custom = value !== "" && !current && value !== none?.value;
  return (
    <>
      <button
        ref={anchor}
        type="button"
        role="combobox"
        aria-haspopup="listbox"
        aria-expanded={open}
        aria-label={label}
        title={value}
        disabled={disabled}
        onClick={() => setOpen((o) => !o)}
        className={cn(
          "bg-bg border-line-strong hover:border-line-accent/70 aria-expanded:border-line-accent rounded-input inline-flex w-full min-w-0 items-center gap-1.5 border text-left outline-none transition-colors disabled:cursor-not-allowed disabled:opacity-50",
          size === "md" ? "h-8 px-2 text-sm" : "h-7 px-1.5 text-xs",
          className,
        )}
      >
        <span className={cn("min-w-0 flex-1 truncate", mono && value && !current?.label && "font-mono")}>
          {value ? (current?.label ?? value) : <span className="text-faint">{placeholder}</span>}
        </span>
        {custom && <span className="text-faint shrink-0 text-[10px] uppercase">custom</span>}
        <ChevronDown className={cn("text-muted size-3.5 shrink-0 transition-transform", open && "rotate-180")} />
      </button>
      <Popup open={open} anchor={anchor} popup={popup} onClose={() => setOpen(false)} maxHeight={320}>
        <Panel
          options={options}
          selected={value ? [value] : []}
          onPick={onChange}
          onClose={() => setOpen(false)}
          validate={validate}
          customLabel={customLabel}
          customPlaceholder={customPlaceholder}
          searchPlaceholder={searchPlaceholder}
          mono={mono}
          closeOnPick
          none={none}
        />
      </Popup>
    </>
  );
}
