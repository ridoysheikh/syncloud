import { useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { ExternalLink, Eye, EyeOff } from "lucide-react";
import { api, ApiError } from "@/lib/api";
import { Panel } from "@/ui/Panel";
import { Alert, Button, StatusBadge, Toggle } from "@/ui/controls";
import { GiteaIcon, GitHubIcon, GitLabIcon } from "@/ui/brands";
import { useGitConnections } from "./RepoPicker";
import { confirmAction } from "@/ui/dialogs";
import { LazyItems } from "@/ui/paging";

interface GitServer {
  enabled: boolean;
  state: "off" | "starting" | "provisioning" | "ready" | "failed";
  url: string;
  image: string;
  adminUser: string;
  connection: string;
  problem?: string;
}

const tone = { off: "neutral", starting: "info", provisioning: "info", ready: "ok", failed: "bad" } as const;

/**
 * Where code comes from (§5.8): the built-in Git server (Forgejo, on or off)
 * and connections to GitHub or self-hosted GitLab and Gitea.
 */
export function GitServerPanel() {
  const qc = useQueryClient();
  const server = useQuery({
    queryKey: ["gitserver"],
    queryFn: () => api<GitServer>("GET", "/gitserver"),
    refetchInterval: (q) => (q.state.data?.enabled && q.state.data.state !== "ready" ? 3000 : 30_000),
  });
  const conns = useGitConnections();
  const [creds, setCreds] = useState<{ username: string; password: string } | null>(null);
  const toggle = useMutation({
    mutationFn: (enabled: boolean) => api<GitServer>("PUT", "/gitserver", { enabled }),
    onSuccess: (g) => {
      qc.setQueryData(["gitserver"], g);
      setCreds(null);
      void qc.invalidateQueries({ queryKey: ["integrations"] });
      void qc.invalidateQueries({ queryKey: ["system"] });
    },
  });
  const reveal = useMutation({
    mutationFn: () => api<{ username: string; password: string }>("GET", "/gitserver/credentials"),
    onSuccess: setCreds,
  });
  const g = server.data;
  const external = (conns.data ?? []).filter((c) => !(g?.enabled && c.name === g.connection));
  const has = (k: string) => external.some((c) => (k === "github" ? c.kind === "github" || c.kind === "github-app" : c.kind === k));

  return (
    <Panel title="Git">
      <div className="grid grid-cols-1 gap-4 lg:grid-cols-2">
        <div className="flex flex-col gap-2">
          <div className="flex items-center gap-2">
            <GiteaIcon />
            <span className="text-sm font-medium">Built-in Git server</span>
            {g && <StatusBadge tone={tone[g.state]}>{g.state}</StatusBadge>}
          </div>
          <Toggle
            checked={!!g?.enabled}
            disabled={!g || toggle.isPending}
            onChange={async (on) => {
              if (!on && !(await confirmAction("Turn the built-in Git server off? Its repositories are kept and come back when you turn it on again."))) return;
              toggle.mutate(on);
            }}
            label="Host Git repositories on this cluster (Forgejo)"
            hint={
              g?.enabled
                ? `Connected as “${g.connection}”: pick its repositories when you create a service; pushes build and deploy like from GitHub.`
                : "A private GitHub-like server at git.<your domain>, with no outside account needed. About 100 MB of memory."
            }
          />
          {g?.enabled && g.url && (
            <div className="flex flex-wrap items-center gap-1.5 text-xs">
              <a href={g.url} target="_blank" rel="noreferrer">
                <Button disabled={g.state !== "ready"}>
                  Open {g.url.replace(/^https?:\/\//, "").replace(/\/$/, "")} <ExternalLink className="size-3" />
                </Button>
              </a>
              <Button variant="ghost" onClick={() => (creds ? setCreds(null) : reveal.mutate())} disabled={reveal.isPending}>
                {creds ? <EyeOff className="size-3.5" /> : <Eye className="size-3.5" />} Administrator sign-in
              </Button>
            </div>
          )}
          {creds && (
            <div className="text-xs">
              User <span className="font-mono select-all">{creds.username}</span>, password{" "}
              <span className="font-mono select-all">{creds.password}</span>. Add people's accounts from its Site administration.
            </div>
          )}
          {g?.problem && g.state !== "ready" && <Alert tone={g.state === "failed" ? "bad" : "info"}>{g.problem}</Alert>}
          {(toggle.error || reveal.error) && <Alert>{(toggle.error ?? reveal.error) instanceof ApiError ? (toggle.error ?? reveal.error)!.message : "Request failed"}</Alert>}
        </div>

        <div className="flex flex-col gap-2">
          <span className="text-sm font-medium">External Git providers</span>
          <p className="text-muted text-xs">
            Connect a hosted or self-hosted platform once; services then pick repositories by name, and SynCloud sets up webhooks and commit
            statuses.
          </p>
          <div className="flex flex-wrap gap-1.5">
            <Link to={"/integrations/git/new?kind=github-app" as string}>
              <Button variant={has("github") ? "default" : "primary"}>
                <GitHubIcon className="size-3.5" /> {has("github") ? "Connect another GitHub" : "Connect GitHub"}
              </Button>
            </Link>
            <Link to={"/integrations/git/new?kind=gitlab" as string}>
              <Button>
                <GitLabIcon className="size-3.5" /> GitLab (cloud or self-hosted)
              </Button>
            </Link>
            <Link to={"/integrations/git/new?kind=gitea" as string}>
              <Button>
                <GiteaIcon className="size-3.5" /> Self-hosted Gitea / Forgejo
              </Button>
            </Link>
          </div>
          {external.length > 0 && (
            <ul className="flex flex-col gap-0.5 text-xs">
              <LazyItems items={external}>
                {(c) => (
                  <li key={c.id}>
                    <Link to={`/integrations/git/${c.name}` as string} className="hover:text-accent">
                      <span className="font-medium">{c.name}</span>{" "}
                      <span className="text-muted">
                        {c.webUrl.replace(/^https?:\/\//, "")} as {c.account} · {c.services.length} service{c.services.length === 1 ? "" : "s"}
                      </span>
                    </Link>
                  </li>
                )}
              </LazyItems>
            </ul>
          )}
        </div>
      </div>
    </Panel>
  );
}
