import { Link } from "@tanstack/react-router";
import {
  ShieldAlert,
  ShieldCheck,
  ShieldQuestion,
  Sparkles,
} from "lucide-react";
import { StatusBadge } from "@/ui/controls";
import { cn } from "@/ui/cn";
import {
  type Certificate,
  methodLabel,
  sourceLabel,
  statusLabel,
  statusTone,
} from "./lib";

/** The state of a certificate as a badge. */
export function CertStatus({
  c,
}: {
  c: Pick<Certificate, "status" | "order">;
}) {
  return (
    <StatusBadge tone={statusTone[c.status] ?? "neutral"}>
      {statusLabel[c.status] ?? c.status}
    </StatusBadge>
  );
}

/** The names a certificate covers, wildcards marked. */
export function CertNames({
  names,
  max = 3,
  className,
}: {
  names: string[];
  max?: number;
  className?: string;
}) {
  const shown = names.slice(0, max);
  return (
    <span
      className={cn(
        "inline-flex min-w-0 flex-wrap items-center gap-1",
        className,
      )}
    >
      {shown.map((n) => (
        <span
          key={n}
          className={cn(
            "border-line bg-raised rounded-sm border px-1.5 font-mono text-xs",
            n.startsWith("*.") && "border-line-accent text-accent",
          )}
        >
          {n}
        </span>
      ))}
      {names.length > max && (
        <span className="text-muted text-xs">+{names.length - max} more</span>
      )}
    </span>
  );
}

/** Where a certificate comes from, with whether browsers trust it. */
export function CertSource({
  c,
}: {
  c: Pick<
    Certificate,
    "source" | "method" | "authority" | "trusted" | "issuerName"
  >;
}) {
  const Icon =
    c.source === "self-signed"
      ? ShieldQuestion
      : c.trusted
        ? ShieldCheck
        : ShieldAlert;
  const who =
    c.source === "imported"
      ? (c.issuerName ?? "provider")
      : c.authority || sourceLabel[c.source];
  return (
    <span
      className="inline-flex items-center gap-1.5"
      title={
        c.trusted
          ? "Browsers trust this certificate"
          : "Browsers do not trust this certificate"
      }
    >
      <Icon
        className={cn("size-3.5 shrink-0", c.trusted ? "text-ok" : "text-warn")}
      />
      <span>{who}</span>
      <span className="text-faint">{methodLabel[c.method] ?? c.method}</span>
    </span>
  );
}

export function CertLink({
  c,
}: {
  c: Pick<Certificate, "id" | "names" | "host" | "wildcard">;
}) {
  return (
    <Link
      to={`/domains/certificates/${c.id}` as string}
      className="hover:text-accent inline-flex items-center gap-1 font-mono"
    >
      {c.wildcard && (
        <Sparkles className="text-accent size-3" aria-label="wildcard" />
      )}
      {c.names?.[0] ?? c.host}
      {c.names && c.names.length > 1 && (
        <span className="text-muted font-sans">+{c.names.length - 1}</span>
      )}
    </Link>
  );
}
