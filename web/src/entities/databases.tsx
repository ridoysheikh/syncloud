import { Link } from "@tanstack/react-router";
import { Database as DatabaseIcon } from "lucide-react";
import { dbUrl } from "@/lib/databases";
import type { Task } from "@/lib/workloads";

/** A database member in a task list: its database, linking there, and its member name. */
export function DatabaseLink({ task: t }: { task: Task }) {
  const owner = t.project ? `${t.project}/${t.environment}/` : "";
  return (
    <Link
      to={dbUrl({ name: t.database ?? "" }) as string}
      className="hover:text-accent inline-flex min-w-0 items-center gap-1.5"
      title={`${t.engine ?? "database"} member ${t.member ?? ""}`}
    >
      <DatabaseIcon className="text-muted size-3 shrink-0" />
      <span className="truncate">
        {owner}
        {t.database}
      </span>
      {t.member && <span className="text-faint font-mono">{t.member}</span>}
    </Link>
  );
}
