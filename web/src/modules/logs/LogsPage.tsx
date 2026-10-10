import { useState } from "react";
import { useProjects, useServices } from "@/lib/workloads";
import { PageHeader } from "@/ui/PageHeader";
import { cn, gap } from "@/ui/cn";
import { LogsView } from "./LogsView";
import { Select } from "@/ui/select";

/** Logs of every service, or one project/environment/service (§9.2). */
export function LogsPage() {
  const { data: projects = [] } = useProjects();
  const { data: services = [] } = useServices();
  const [project, setProject] = useState("");
  const [env, setEnv] = useState("");
  const [service, setService] = useState("");
  const envs = projects.find((p) => p.name === project)?.environments ?? [];
  const svcs = services.filter((s) => s.project === project && (!env || s.environment === env));
  return (
    <div className={cn("flex flex-col", gap)}>
      <PageHeader
        title="Logs"
        actions={
          <>
            <Select
              value={project}
              onChange={(v) => {
                setProject(v);
                setEnv("");
                setService("");
              }}
              size="sm"
            >
              <option value="">All projects</option>
              <option value="syncloud">syncloud (platform)</option>
              {projects.map((p) => (
                <option key={p.id}>{p.name}</option>
              ))}
            </Select>
            {project && project !== "syncloud" && (
              <Select value={env} onChange={setEnv} size="sm">
                <option value="">All environments</option>
                {envs.map((e) => (
                  <option key={e}>{e}</option>
                ))}
              </Select>
            )}
            {project && project !== "syncloud" && (
              <Select value={service} onChange={setService} size="sm">
                <option value="">All services</option>
                {[...new Set(svcs.map((s) => s.name))].map((s) => (
                  <option key={s}>{s}</option>
                ))}
              </Select>
            )}
          </>
        }
      />
      <LogsView filter={{ project: project || undefined, environment: env || undefined, service: service || undefined }} />
    </div>
  );
}
