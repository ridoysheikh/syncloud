import {
  useRef,
  type ComponentType,
  type KeyboardEvent,
  type ReactNode,
} from "react";
import { Check } from "lucide-react";

/** Any icon component (lucide or the brand icons). */
type Icon = ComponentType<{ className?: string }>;
import { cn } from "./cn";

/*
 * Selection controls (Phase 16b). A chosen item is shown by a blended accent
 * border and a tinted fill with a small check mark, never a bare checkbox or
 * radio button.
 */

/** Classes of a selectable surface, selected or not. */
export const selectable = (selected: boolean, disabled?: boolean) =>
  cn(
    "border transition-colors outline-none focus-visible:border-line-accent",
    selected
      ? "border-line-accent bg-btn-primary/15"
      : "border-line-strong bg-surface hover:bg-hover/60",
    disabled ? "cursor-not-allowed opacity-50" : "cursor-pointer",
  );

/** Arrow keys move the choice within a radiogroup, as native radios do. */
function useArrowKeys<T>(values: T[], value: T, onChange: (v: T) => void) {
  const ref = useRef<HTMLDivElement>(null);
  const onKeyDown = (e: KeyboardEvent) => {
    const step =
      e.key === "ArrowRight" || e.key === "ArrowDown"
        ? 1
        : e.key === "ArrowLeft" || e.key === "ArrowUp"
          ? -1
          : 0;
    if (!step || values.length === 0) return;
    e.preventDefault();
    const i = Math.max(0, values.indexOf(value));
    const next = (i + step + values.length) % values.length;
    onChange(values[next]!);
    ref.current?.querySelectorAll<HTMLElement>("[role=radio]")[next]?.focus();
  };
  return { ref, onKeyDown };
}

/**
 * A selectable card: title, description, an optional icon, and any extra
 * content (meters, badges). `multi` makes it a checkbox; otherwise a radio.
 */
export function ChoiceCard({
  selected,
  onSelect,
  title,
  description,
  icon: IconC,
  aside,
  children,
  disabled,
  multi,
  className,
  tabIndex,
}: {
  selected: boolean;
  onSelect: () => void;
  title: ReactNode;
  description?: ReactNode;
  icon?: Icon;
  /** Top-right content beside the title (a badge). */
  aside?: ReactNode;
  children?: ReactNode;
  disabled?: boolean;
  multi?: boolean;
  className?: string;
  tabIndex?: number;
}) {
  return (
    <button
      type="button"
      role={multi ? "checkbox" : "radio"}
      aria-checked={selected}
      disabled={disabled}
      tabIndex={tabIndex}
      onClick={onSelect}
      className={cn(
        "relative flex min-w-0 flex-col gap-1.5 rounded-md p-2.5 text-left text-xs",
        selectable(selected, disabled),
        className,
      )}
    >
      <span className="flex min-w-0 items-start gap-2">
        {IconC && (
          <span
            className={cn(
              "border-line-strong bg-bg mt-px flex size-7 shrink-0 items-center justify-center rounded-sm border",
              selected && "border-line-accent",
            )}
          >
            <IconC
              className={cn(
                "size-3.5",
                selected ? "text-accent" : "text-muted",
              )}
            />
          </span>
        )}
        <span className="flex min-w-0 flex-1 flex-col gap-0.5">
          <span className="flex min-w-0 items-center gap-1.5">
            <span
              className={cn(
                "min-w-0 truncate text-sm font-medium",
                selected ? "text-accent" : "text-fg",
              )}
            >
              {title}
            </span>
            {aside && <span className="ml-auto shrink-0">{aside}</span>}
            <span
              aria-hidden
              className={cn(
                "flex size-4 shrink-0 items-center justify-center rounded-full transition-opacity",
                !aside && "ml-auto",
                selected ? "bg-accent opacity-100" : "opacity-0",
              )}
            >
              <Check className="text-accent-fg size-3" strokeWidth={3} />
            </span>
          </span>
          {description && (
            <span className="text-muted leading-snug">{description}</span>
          )}
        </span>
      </span>
      {children}
    </button>
  );
}

export interface Choice<T extends string> {
  value: T;
  title: ReactNode;
  description?: ReactNode;
  icon?: Icon;
  aside?: ReactNode;
  disabled?: boolean;
}

/** One choice among a few explained options, as a grid of cards. */
export function ChoiceCards<T extends string>({
  value,
  onChange,
  options,
  label,
  columns = 2,
  className,
}: {
  value: T;
  onChange: (v: T) => void;
  options: Choice<T>[];
  /** Accessible name of the group. */
  label: string;
  /** Columns from the `sm` breakpoint (one column on phones). */
  columns?: 2 | 3 | 4;
  className?: string;
}) {
  const keys = useArrowKeys(
    options.filter((o) => !o.disabled).map((o) => o.value),
    value,
    onChange,
  );
  return (
    <div
      ref={keys.ref}
      role="radiogroup"
      aria-label={label}
      onKeyDown={keys.onKeyDown}
      className={cn(
        "grid grid-cols-1 gap-1.5 sm:gap-2",
        columns === 2 && "sm:grid-cols-2",
        columns === 3 && "sm:grid-cols-3",
        columns === 4 && "sm:grid-cols-2 lg:grid-cols-4",
        className,
      )}
    >
      {options.map((o) => (
        <ChoiceCard
          key={o.value}
          selected={o.value === value}
          onSelect={() => onChange(o.value)}
          title={o.title}
          description={o.description}
          icon={o.icon}
          aside={o.aside}
          disabled={o.disabled}
          tabIndex={o.value === value ? 0 : -1}
        />
      ))}
    </div>
  );
}

/** Multi-select of short items as toggle chips. */
export function ChipSelect<T extends string>({
  value,
  onChange,
  options,
  label,
  empty,
  mono,
  disabled,
}: {
  value: T[];
  onChange: (v: T[]) => void;
  options: { value: T; label: ReactNode; title?: string }[];
  label: string;
  /** Shown when there is nothing to choose. */
  empty?: ReactNode;
  mono?: boolean;
  disabled?: boolean;
}) {
  if (options.length === 0)
    return (
      <span className="text-faint text-xs">
        {empty ?? "Nothing to choose."}
      </span>
    );
  const all = options.map((o) => o.value);
  const toggle = (v: T) =>
    onChange(value.includes(v) ? value.filter((x) => x !== v) : [...value, v]);
  return (
    <div className="flex flex-col gap-1.5">
      <div role="group" aria-label={label} className="flex flex-wrap gap-1">
        {options.map((o) => {
          const on = value.includes(o.value);
          return (
            <button
              key={o.value}
              type="button"
              role="checkbox"
              aria-checked={on}
              disabled={disabled}
              title={o.title}
              onClick={() => toggle(o.value)}
              className={cn(
                "inline-flex h-7 max-w-full min-w-0 items-center gap-1.5 rounded-sm px-2 text-xs",
                selectable(on, disabled),
                on ? "text-fg" : "text-muted hover:text-fg",
                mono && "font-mono",
              )}
            >
              <span
                aria-hidden
                className={cn(
                  "flex size-3.5 shrink-0 items-center justify-center rounded-full",
                  on ? "bg-accent" : "border-line-strong border",
                )}
              >
                {on && (
                  <Check
                    className="text-accent-fg size-2.5"
                    strokeWidth={3.5}
                  />
                )}
              </span>
              <span className="truncate">{o.label}</span>
            </button>
          );
        })}
      </div>
      {options.length > 6 && !disabled && (
        <span className="text-faint flex items-center gap-2 text-xs">
          {value.length} of {options.length} selected
          <button
            type="button"
            className="text-muted hover:text-fg"
            onClick={() => onChange(all)}
          >
            All
          </button>
          <button
            type="button"
            className="text-muted hover:text-fg"
            onClick={() => onChange([])}
          >
            None
          </button>
        </span>
      )}
    </div>
  );
}

/** Two to four short options in one control (replaces a small select). */
export function Segmented<T extends string>({
  value,
  onChange,
  options,
  label,
  size = "md",
}: {
  value: T;
  onChange: (v: T) => void;
  options: { value: T; label: ReactNode; icon?: Icon }[];
  label: string;
  /** "md" matches h-8 inputs, "sm" h-7 buttons. */
  size?: "sm" | "md";
}) {
  const keys = useArrowKeys(
    options.map((o) => o.value),
    value,
    onChange,
  );
  return (
    <div
      ref={keys.ref}
      role="radiogroup"
      aria-label={label}
      onKeyDown={keys.onKeyDown}
      className={cn(
        "bg-bg border-line-strong rounded-input inline-flex max-w-full items-center gap-0.5 overflow-x-auto border p-0.5",
        size === "md" ? "h-8" : "h-7",
      )}
    >
      {options.map((o) => {
        const on = o.value === value;
        const Icon = o.icon;
        return (
          <button
            key={o.value}
            type="button"
            role="radio"
            aria-checked={on}
            tabIndex={on ? 0 : -1}
            onClick={() => onChange(o.value)}
            className={cn(
              "inline-flex h-full items-center gap-1.5 rounded-[3px] border px-2 text-xs whitespace-nowrap transition-colors outline-none",
              on
                ? "border-line-accent bg-btn-primary/25 text-fg"
                : "text-muted hover:text-fg hover:bg-hover border-transparent focus-visible:border-line-accent",
            )}
          >
            {Icon && <Icon className="size-3.5" />}
            {o.label}
          </button>
        );
      })}
    </div>
  );
}

/**
 * A labelled group for a choice control. Unlike Field it is not a <label>:
 * a label around several buttons would forward clicks to the first one.
 */
export function ChoiceField({
  label,
  hint,
  children,
}: {
  label: string;
  hint?: ReactNode;
  children: ReactNode;
}) {
  return (
    <div className="flex flex-col gap-1">
      <span className="text-muted text-xs">{label}</span>
      {children}
      {hint && <span className="text-faint text-xs">{hint}</span>}
    </div>
  );
}
