import { useTasks } from "@/lib/workloads";
import { TasksTable } from "@/entities/tasks";
import { PageHeader } from "@/ui/PageHeader";
import { Panel } from "@/ui/Panel";
import { cn, gap } from "@/ui/cn";

/** Every active task across all nodes. */
export function TasksPage() {
  const { data = [], isLoading } = useTasks();
  return (
    <div className={cn("flex flex-col", gap)}>
      <PageHeader crumbs={["Compute"]} title="Tasks" />
      <Panel title={`Active tasks (${data.length})`} flush>
        <TasksTable tasks={data} loading={isLoading} />
      </Panel>
    </div>
  );
}
