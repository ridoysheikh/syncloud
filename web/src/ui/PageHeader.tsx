import { Fragment, type ReactNode } from "react";

/** Compact page header: breadcrumb, title, status and primary actions (§10.1). */
export function PageHeader({
  crumbs,
  title,
  status,
  actions,
}: {
  crumbs?: ReactNode[];
  title: ReactNode;
  status?: ReactNode;
  actions?: ReactNode;
}) {
  return (
    <div className="flex flex-wrap items-end justify-between gap-2">
      <div className="min-w-0">
        {crumbs && crumbs.length > 0 && (
          <div className="text-faint truncate text-xs">
            {crumbs.map((c, i) => (
              <Fragment key={i}>
                {i > 0 && " › "}
                {c}
              </Fragment>
            ))}
          </div>
        )}
        <div className="flex items-center gap-2">
          <h1 className="truncate text-base font-semibold">{title}</h1>
          {status}
        </div>
      </div>
      {actions && <div className="flex items-center gap-1.5">{actions}</div>}
    </div>
  );
}
