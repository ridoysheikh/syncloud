import { Link } from "@tanstack/react-router";
import { ExternalLink, GitBranch } from "lucide-react";
import { since } from "@/lib/nodes";
import {
  AWAITING_BUILD,
  serviceState,
  serviceUrl,
  useServiceIndex,
  type Service,
} from "@/lib/workloads";
import { CardRow, HoverCard } from "@/ui/HoverCard";
import { StatusBadge } from "@/ui/controls";
import { cn, pad } from "@/ui/cn";
import { TaskDots } from "./TaskDots";

/* Services, drawn the same way everywhere (Phase 16). */

const dotTone = {
  ok: "bg-ok",
  warn: "bg-warn",
  bad: "bg-bad",
  info: "bg-info",
  neutral: "bg-neutral",
} as const;

/** The service's state badge: healthy, deploying, degraded, … */
export function ServiceState({ service }: { service: Service }) {
  const st = serviceState(service);
  return <StatusBadge tone={st.tone}>{st.label}</StatusBadge>;
}

/** The dots of a service's tasks, from its counts. */
export function ServiceTaskDots({
  service: s,
  className,
}: {
  service: Service;
  className?: string;
}) {
  return (
    <TaskDots
      running={s.running}
      starting={s.pending}
      desired={s.desiredCount}
      className={className}
    />
  );
}

/** Whether the image comes from the service's own Git builds. */
const isBuilt = (s: Service) =>
  s.spec.image === AWAITING_BUILD ||
  s.spec.image.startsWith(`@registry/${s.project}/${s.name}:`);

const imageLabel = (s: Service) =>
  s.spec.image === AWAITING_BUILD
    ? "waiting for the first build"
    : s.spec.image;

function ServiceCardBody({ s }: { s: Service }) {
  return (
    <div className="flex flex-col gap-2">
      <div className="flex items-center justify-between gap-2">
        <span className="min-w-0 truncate font-semibold">{s.name}</span>
        <ServiceState service={s} />
      </div>
      <div className="flex items-center justify-between gap-2">
        <ServiceTaskDots service={s} />
        <span className="text-muted font-mono">
          {s.running}/{s.desiredCount}
        </span>
      </div>
      <div className="flex flex-col gap-0.5">
        <CardRow k="Project">
          {s.project} / {s.environment}
        </CardRow>
        <CardRow k="Image">
          <span className="font-mono break-all">{imageLabel(s)}</span>
        </CardRow>
        <CardRow k="Revision">
          {s.revision} · {since(s.updatedAt)}
        </CardRow>
        <CardRow k="Address">
          <span className="font-mono break-all">
            {s.endpoints[0]?.replace(/^https?:\/\//, "") ||
              s.dnsName ||
              "internal"}
          </span>
        </CardRow>
        {s.status && s.spec.image !== AWAITING_BUILD && (
          <span className="text-bad break-words">{s.status}</span>
        )}
      </div>
    </div>
  );
}

/**
 * A service reference: state dot and `project/env/name` (or just the name),
 * linking to the service, with a preview card on hover.
 */
export function ServiceLink({
  project,
  environment,
  name,
  short,
  className,
}: {
  project: string;
  environment: string;
  name: string;
  /** Only the name (the page already shows the project). */
  short?: boolean;
  className?: string;
}) {
  const { data: services } = useServiceIndex();
  const s = services?.find(
    (x) =>
      x.project === project && x.environment === environment && x.name === name,
  );
  const label = short ? name : `${project}/${environment}/${name}`;
  const to: string = serviceUrl({ project, environment, name });
  const link = (
    <Link
      to={to}
      className={cn(
        "hover:text-accent inline-flex min-w-0 items-center gap-1.5",
        className,
      )}
    >
      {s && (
        <span
          aria-hidden
          className={cn(
            "size-1.5 shrink-0 rounded-full",
            dotTone[serviceState(s).tone],
          )}
        />
      )}
      <span className="truncate">{label}</span>
    </Link>
  );
  if (!s) return link;
  return (
    <HoverCard trigger={link}>{() => <ServiceCardBody s={s} />}</HoverCard>
  );
}

/** A service as a card, for the project's grid. */
export function ServiceCard({ service: s }: { service: Service }) {
  const to: string = serviceUrl(s);
  return (
    <Link
      to={to}
      className={cn(
        "bg-surface border-line hover:border-line-strong flex min-w-0 flex-col gap-1.5 rounded-md border transition-colors",
        pad,
      )}
    >
      <div className="flex items-center justify-between gap-2">
        <span className="truncate text-sm font-semibold">{s.name}</span>
        <ServiceState service={s} />
      </div>
      <div
        className="text-muted flex items-center gap-1 truncate font-mono text-xs"
        title={s.spec.image}
      >
        {isBuilt(s) && <GitBranch className="size-3 shrink-0" />}
        <span className="truncate">{imageLabel(s)}</span>
      </div>
      <div className="text-muted flex items-center justify-between gap-2 text-xs">
        <span className="flex min-w-0 items-center gap-2">
          <ServiceTaskDots service={s} />
          <span>
            <span className="text-fg font-mono">
              {s.running}/{s.desiredCount}
            </span>{" "}
            · rev {s.revision}
          </span>
        </span>
        <span className="text-faint shrink-0">{since(s.updatedAt)}</span>
      </div>
      {s.endpoints.length > 0 ? (
        <span className="text-faint inline-flex min-w-0 items-center gap-1 font-mono text-xs">
          <ExternalLink className="size-3 shrink-0" />
          <span className="truncate">
            {s.endpoints[0]!.replace(/^https?:\/\//, "")}
          </span>
        </span>
      ) : (
        <span className="text-faint truncate font-mono text-xs">
          {s.dnsName || "internal"}
        </span>
      )}
      {s.status && s.spec.image !== AWAITING_BUILD && (
        <span className="text-bad truncate text-xs" title={s.status}>
          {s.status}
        </span>
      )}
    </Link>
  );
}
