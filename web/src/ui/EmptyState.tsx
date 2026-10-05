import type { LucideIcon } from "lucide-react";
import type { ReactNode } from "react";

export function EmptyState({ icon: Icon, title, children }: { icon: LucideIcon; title: string; children?: ReactNode }) {
  return (
    <div className="flex flex-col items-center justify-center gap-1.5 px-4 py-10 text-center">
      <Icon className="text-faint size-6" strokeWidth={1.5} />
      <div className="text-sm font-medium">{title}</div>
      {children && <div className="text-muted max-w-md text-xs">{children}</div>}
    </div>
  );
}
