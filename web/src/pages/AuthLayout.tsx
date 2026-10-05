import type { ReactNode } from "react";

export function AuthLayout({ title, subtitle, children }: { title: string; subtitle?: ReactNode; children: ReactNode }) {
  return (
    <div className="flex min-h-full items-center justify-center p-4">
      <div className="w-full max-w-sm">
        <div className="mb-3 flex items-center gap-2">
          <img src="/favicon.svg" alt="" className="size-6" />
          <span className="text-base font-semibold tracking-tight">SynCloud</span>
        </div>
        <div className="bg-surface border-line rounded-md border p-3 md:p-4">
          <h1 className="text-base font-semibold">{title}</h1>
          {subtitle && <p className="text-muted mt-0.5 text-xs">{subtitle}</p>}
          <div className="mt-3">{children}</div>
        </div>
      </div>
    </div>
  );
}
