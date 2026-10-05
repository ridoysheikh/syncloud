import { useState } from "react";
import { useProjects, useServices } from "@/lib/workloads";
import { PageHeader } from "@/ui/PageHeader";
import { cn, gap } from "@/ui/cn";
import { LogsView } from "./LogsView";

/** Logs of every service, or one project/environment/service (§9.2). */
export function LogsPage() {
  const { data: projects = [] } = useProjects();
  const { data: services = [] } = useServices();
  const [project, setProject] = useState("");
  const [env, setEnv] = useState("");
  const [service, setService] = useState("");
  const envs = projects.find((p) => p.name === project)?.environments ?? [];
  const svcs = services.filter((s) => s.project === project && (!env || s.environment === env));
  const sel = "bg-bg border-line h-7 rounded-sm border px-1.5 text-xs";
  return (
    <div className={cn("flex flex-col", gap)}>
      <PageHeader
        title="Logs"
        actions={
          <>
            <select
              value={project}
              onChange={(e) => {
                setProject(e.target.value);
                setEnv("");
                setService("");
              }}
              className={sel}
            >
              <option value="">All projects</option>
              <option value="syncloud">syncloud (platform)</option>
              {projects.map((p) => (
                <option key={p.id}>{p.name}</option>
              ))}
            </select>
            {project && project !== "syncloud" && (
              <select value={env} onChange={(e) => setEnv(e.target.value)} className={sel}>
                <option value="">All environments</option>
                {envs.map((e) => (
                  <option key={e}>{e}</option>
                ))}
              </select>
            )}
            {project && project !== "syncloud" && (
              <select value={service} onChange={(e) => setService(e.target.value)} className={sel}>
                <option value="">All services</option>
                {[...new Set(svcs.map((s) => s.name))].map((s) => (
                  <option key={s}>{s}</option>
                ))}
              </select>
            )}
          </>
        }
      />
      <LogsView filter={{ project: project || undefined, environment: env || undefined, service: service || undefined }} />
    </div>
  );
}
