import { Construction } from "lucide-react";
import { Panel } from "@/ui/Panel";
import { PageHeader } from "@/ui/PageHeader";
import { EmptyState } from "@/ui/EmptyState";
import { gap, cn } from "@/ui/cn";

/** Page for a feature that is designed in the plan but not built yet. */
export function planned(crumbs: string[], title: string, phase: string, section: string, summary: string) {
  return function PlannedPage() {
    return (
      <div className={cn("flex flex-col", gap)}>
        <PageHeader crumbs={crumbs} title={title} />
        <Panel>
          <EmptyState icon={Construction} title={`Arrives in ${phase}`}>
            {summary} <span className="text-faint">Design: plan {section}.</span>
          </EmptyState>
        </Panel>
      </div>
    );
  };
}
