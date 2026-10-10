import {
  Children,
  Fragment,
  isValidElement,
  useEffect,
  useId,
  useMemo,
  useRef,
  useState,
  type InputHTMLAttributes,
  type KeyboardEvent,
  type ReactElement,
  type ReactNode,
} from "react";
import { Check, ChevronDown, Search } from "lucide-react";
import { cn } from "./cn";
import { Popup } from "./popover";

/*
 * Themed replacements for <select> and <datalist> (the browser draws those
 * itself, unthemed). Select is a listbox under a field-styled button and grows
 * a search box on long lists; Combobox is a text field with suggestions.
 */

export interface SelectOption {
  value: string;
  label: ReactNode;
  /** Plain text matched by the search (defaults to the label's text). */
  text?: string;
  /** Muted text at the end of the row. */
  hint?: ReactNode;
  disabled?: boolean;
}

/** The text of a React node, for search and type-ahead. */
function textOf(n: ReactNode): string {
  if (n == null || typeof n === "boolean") return "";
  if (typeof n === "string" || typeof n === "number") return String(n);
  if (Array.isArray(n)) return n.map(textOf).join("");
  if (isValidElement(n))
    return textOf((n.props as { children?: ReactNode }).children);
  return "";
}

/** Options from <option> children, as <select> takes them. */
function fromChildren(children: ReactNode): SelectOption[] {
  const out: SelectOption[] = [];
  const walk = (nodes: ReactNode) =>
    Children.forEach(nodes, (c) => {
      if (!isValidElement(c)) return;
      const el = c as ReactElement<{
        children?: ReactNode;
        value?: string | number;
        disabled?: boolean;
      }>;
      if (el.type === Fragment) return walk(el.props.children);
      if (el.type !== "option") return;
      const text = textOf(el.props.children);
      out.push({
        value: el.props.value != null ? String(el.props.value) : text,
        label: el.props.children ?? text,
        text,
        disabled: el.props.disabled,
      });
    });
  walk(children);
  return out;
}

const sizes = {
  xs: "h-6 px-1.5 text-xs",
  sm: "h-7 px-1.5 text-xs",
  md: "h-8 px-2 text-sm",
};

/** The field look of a select trigger, shared with Input. */
const trigger =
  "bg-bg border-line-strong hover:border-line-accent/70 focus-visible:border-line-accent aria-expanded:border-line-accent rounded-input inline-flex min-w-0 items-center gap-1.5 border text-left outline-none transition-colors disabled:cursor-not-allowed disabled:opacity-50";

/** Lists longer than this get a search box (unless `search` says otherwise). */
const SEARCH_FROM = 10;
/** Lists longer than this do not reserve the width of their longest label. */
const SIZE_UP_TO = 60;

/**
 * A themed select:
 *
 *   <Select value={env} onChange={setEnv} aria-label="Environment">
 *     <option value="">All environments</option>
 *     {envs.map((e) => <option key={e}>{e}</option>)}
 *   </Select>
 *
 * Takes <option> children like <select>, or `options`. Values are strings
 * (numbers are stringified). Width follows the longest label, as a native
 * select's does, unless the caller sets one.
 */
export function Select({
  value,
  onChange,
  options: given,
  children,
  placeholder = "Select…",
  search,
  size = "md",
  mono,
  disabled,
  className,
  id,
  title,
  "aria-label": ariaLabel,
}: {
  value: string | number | undefined;
  onChange: (value: string) => void;
  options?: SelectOption[];
  /** <option> elements, when `options` is not given. */
  children?: ReactNode;
  /** Shown when the value matches no option. */
  placeholder?: string;
  /** Show a search box; by default, lists of more than ten do. */
  search?: boolean;
  /** "xs" h-6, "sm" h-7 (beside buttons), "md" h-8 (beside inputs). */
  size?: keyof typeof sizes;
  mono?: boolean;
  disabled?: boolean;
  className?: string;
  id?: string;
  title?: string;
  "aria-label"?: string;
}) {
  const options = useMemo(
    () =>
      (given ?? fromChildren(children)).map((o) => ({
        ...o,
        text: o.text ?? textOf(o.label),
      })),
    [given, children],
  );
  const v = String(value ?? "");
  const current = options.find((o) => o.value === v);
  const searchable = search ?? options.length > SEARCH_FROM;

  const [open, setOpen] = useState(false);
  const [query, setQuery] = useState("");
  const [active, setActive] = useState(-1);
  const anchor = useRef<HTMLButtonElement>(null);
  const popup = useRef<HTMLDivElement>(null);
  const list = useRef<HTMLDivElement>(null);
  const typed = useRef({ text: "", at: 0 });
  const baseId = useId();

  const shown = useMemo(() => {
    const q = query.trim().toLowerCase();
    return q
      ? options.filter((o) => `${o.text} ${o.value}`.toLowerCase().includes(q))
      : options;
  }, [options, query]);

  const enabled = (i: number) => !!shown[i] && !shown[i]!.disabled;
  const step = (from: number, dir: 1 | -1) => {
    for (let i = from + dir; i >= 0 && i < shown.length; i += dir)
      if (enabled(i)) return i;
    return from;
  };

  const show = () => {
    if (disabled) return;
    setQuery("");
    setActive(Math.max(0, options.indexOf(current!)));
    setOpen(true);
  };
  const hide = (refocus = true) => {
    setOpen(false);
    if (refocus) anchor.current?.focus();
  };
  const pick = (o: SelectOption | undefined) => {
    if (!o || o.disabled) return;
    hide();
    if (o.value !== v) onChange(o.value);
  };

  // Keep the active row in view while arrowing through a long list.
  useEffect(() => {
    if (!open) return;
    list.current
      ?.querySelector(`[data-index="${active}"]`)
      ?.scrollIntoView({ block: "nearest" });
  }, [open, active]);

  const onKeyDown = (e: KeyboardEvent) => {
    if (!open) {
      if (["ArrowDown", "ArrowUp", "Enter", " "].includes(e.key)) {
        e.preventDefault();
        show();
      }
      return;
    }
    const keys: Record<string, () => void> = {
      ArrowDown: () => setActive((i) => step(i, 1)),
      ArrowUp: () => setActive((i) => step(i, -1)),
      Home: () => setActive(step(-1, 1)),
      End: () => setActive(step(shown.length, -1)),
      Enter: () => pick(shown[active]),
      // Esc closes the list, not the dialog around it.
      Escape: () => hide(),
    };
    if (keys[e.key]) {
      e.preventDefault();
      e.stopPropagation();
      keys[e.key]!();
      return;
    }
    if (e.key === "Tab") return hide(false);
    if (!searchable && e.key.length === 1) {
      // Type-ahead: jump to the first option starting with what was typed.
      const t = typed.current;
      t.text = (e.timeStamp - t.at < 700 ? t.text : "") + e.key.toLowerCase();
      t.at = e.timeStamp;
      const i = shown.findIndex(
        (o) => !o.disabled && o.text!.toLowerCase().startsWith(t.text),
      );
      if (i >= 0) setActive(i);
    }
  };

  const optionId = (i: number) => `${baseId}-o${i}`;
  const label = current?.label ?? (
    <span className="text-faint">{placeholder}</span>
  );
  const custom = /(^|\s)(w|max-w)-/.test(className ?? "");

  return (
    <>
      <button
        ref={anchor}
        id={id}
        type="button"
        role="combobox"
        aria-haspopup="listbox"
        aria-expanded={open}
        aria-controls={open ? `${baseId}-list` : undefined}
        aria-activedescendant={
          open && !searchable && active >= 0 ? optionId(active) : undefined
        }
        aria-label={ariaLabel}
        title={title ?? current?.text}
        disabled={disabled}
        onClick={() => (open ? hide() : show())}
        onKeyDown={onKeyDown}
        className={cn(
          trigger,
          sizes[size],
          !/(^|\s)max-w-/.test(className ?? "") && "max-w-full",
          mono && "font-mono",
          className,
        )}
      >
        {/* Every label in one grid cell: the widest sets the width, the
            current one shows. Long lists skip it and size to the value. */}
        <span className="grid min-w-0 flex-1 grid-cols-[minmax(0,1fr)]">
          {!custom &&
            options.length <= SIZE_UP_TO &&
            options.map((o, i) => (
              <span
                key={i}
                aria-hidden
                className="invisible col-start-1 row-start-1 h-0 truncate"
              >
                {o.label}
              </span>
            ))}
          <span className="col-start-1 row-start-1 truncate">{label}</span>
        </span>
        <ChevronDown
          className={cn(
            "text-muted size-3.5 shrink-0 transition-transform",
            open && "rotate-180",
          )}
        />
      </button>
      <Popup
        open={open}
        anchor={anchor}
        popup={popup}
        onClose={() => hide(false)}
      >
        {searchable && (
          <label className="border-line flex h-8 shrink-0 items-center gap-1.5 border-b px-2">
            <Search className="text-muted size-3.5 shrink-0" />
            <input
              autoFocus
              value={query}
              onChange={(e) => {
                setQuery(e.target.value);
                setActive(0);
              }}
              onKeyDown={onKeyDown}
              role="searchbox"
              aria-label="Search"
              aria-controls={`${baseId}-list`}
              aria-activedescendant={
                active >= 0 && shown[active] ? optionId(active) : undefined
              }
              placeholder="Search…"
              spellCheck={false}
              autoComplete="off"
              className="h-full min-w-0 flex-1 bg-transparent text-xs outline-none"
            />
          </label>
        )}
        <div
          ref={list}
          id={`${baseId}-list`}
          role="listbox"
          aria-label={ariaLabel}
          className="min-h-0 flex-1 overflow-y-auto overscroll-contain p-1"
        >
          {shown.length === 0 && (
            <div className="text-faint px-2 py-2 text-xs">No match.</div>
          )}
          {shown.map((o, i) => {
            const on = o.value === v;
            return (
              <div
                key={`${i}:${o.value}`}
                id={optionId(i)}
                data-index={i}
                role="option"
                aria-selected={on}
                aria-disabled={o.disabled || undefined}
                title={o.text}
                // Keep focus on the trigger or the search box.
                onMouseDown={(e) => e.preventDefault()}
                onMouseMove={() => !o.disabled && active !== i && setActive(i)}
                onClick={() => pick(o)}
                className={cn(
                  "flex min-w-0 cursor-pointer items-center gap-1.5 rounded-sm px-2 py-1 text-xs",
                  i === active && !o.disabled && "bg-hover",
                  on ? "text-accent" : "text-fg",
                  mono && "font-mono",
                  o.disabled && "cursor-not-allowed opacity-50",
                )}
              >
                <span className="min-w-0 flex-1 truncate">{o.label}</span>
                {o.hint && (
                  <span className="text-muted shrink-0 truncate">{o.hint}</span>
                )}
                {/* A reserved column, so rows line up whichever is chosen. */}
                <span className="flex size-3.5 shrink-0 items-center justify-center">
                  {on && <Check className="size-3.5" strokeWidth={2.5} />}
                </span>
              </div>
            );
          })}
        </div>
      </Popup>
    </>
  );
}

/** A Select that always has a search box (long or open-ended lists). */
export function SearchSelect(props: Parameters<typeof Select>[0]) {
  return <Select search {...props} />;
}

/** Most suggestions shown at once. */
const SUGGEST_MAX = 50;

/**
 * A text field with suggestions (in place of <input list> and <datalist>).
 * Typing filters them; arrows and Enter, or a click, take one. Any value can
 * still be typed.
 */
export function Combobox({
  value,
  onChange,
  suggestions,
  bare,
  className,
  onKeyDown,
  onFocus,
  ...props
}: Omit<InputHTMLAttributes<HTMLInputElement>, "value" | "onChange" | "list"> & {
  value: string;
  onChange: (value: string) => void;
  suggestions: string[];
  /** No field styling (for an input inside a styled container). */
  bare?: boolean;
}) {
  const [open, setOpen] = useState(false);
  const [active, setActive] = useState(-1);
  const anchor = useRef<HTMLInputElement>(null);
  const popup = useRef<HTMLDivElement>(null);
  const baseId = useId();

  const shown = useMemo(() => {
    const q = value.trim().toLowerCase();
    return [...new Set(suggestions)]
      .filter((s) => s !== value && s.toLowerCase().includes(q))
      .slice(0, SUGGEST_MAX);
  }, [suggestions, value]);
  const visible = open && shown.length > 0;

  const take = (s: string) => {
    onChange(s);
    setOpen(false);
  };
  const keyDown = (e: KeyboardEvent<HTMLInputElement>) => {
    if (e.key === "ArrowDown" || e.key === "ArrowUp") {
      e.preventDefault();
      setOpen(true);
      const n = shown.length;
      if (n) setActive((i) => (e.key === "ArrowDown" ? (i + 1) % n : (i - 1 + n) % n));
      return;
    }
    if (visible && e.key === "Enter" && shown[active]) {
      e.preventDefault();
      take(shown[active]!);
      return;
    }
    if (visible && e.key === "Escape") {
      // Close the suggestions, not the dialog around the field.
      e.preventDefault();
      e.stopPropagation();
      setOpen(false);
      return;
    }
    if (e.key === "Tab") setOpen(false);
    onKeyDown?.(e);
  };

  return (
    <>
      <input
        {...props}
        ref={anchor}
        value={value}
        onChange={(e) => {
          onChange(e.target.value);
          setOpen(true);
          setActive(-1);
        }}
        onFocus={(e) => {
          setOpen(true);
          onFocus?.(e);
        }}
        onClick={() => setOpen(true)}
        onKeyDown={keyDown}
        role="combobox"
        aria-autocomplete="list"
        aria-expanded={visible}
        aria-controls={visible ? `${baseId}-list` : undefined}
        aria-activedescendant={
          visible && active >= 0 ? `${baseId}-o${active}` : undefined
        }
        autoComplete="off"
        spellCheck={false}
        className={cn(
          !bare && [
            "bg-bg border-line-strong placeholder:text-faint focus:border-line-accent rounded-input border px-2 text-sm outline-none",
            // As Input: a width or height from the caller replaces the default.
            !/(^|\s)w-/.test(className ?? "") && "w-full",
            !/(^|\s)h-/.test(className ?? "") && "h-8",
          ],
          className,
        )}
      />
      <Popup
        open={visible}
        anchor={anchor}
        popup={popup}
        onClose={() => setOpen(false)}
        maxHeight={240}
      >
        <div
          id={`${baseId}-list`}
          role="listbox"
          className="min-h-0 flex-1 overflow-y-auto overscroll-contain p-1"
        >
          {shown.map((s, i) => (
            <div
              key={s}
              id={`${baseId}-o${i}`}
              role="option"
              aria-selected={i === active}
              title={s}
              onMouseDown={(e) => e.preventDefault()}
              onMouseMove={() => active !== i && setActive(i)}
              onClick={() => take(s)}
              className={cn(
                "text-fg cursor-pointer truncate rounded-sm px-2 py-1 font-mono text-xs",
                i === active && "bg-hover",
              )}
            >
              {s}
            </div>
          ))}
        </div>
      </Popup>
    </>
  );
}
