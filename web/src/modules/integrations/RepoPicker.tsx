import { useEffect, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { Check, ExternalLink, Link2, Lock, Plus, Search } from "lucide-react";
import { api, ApiError } from "@/lib/api";
import { Alert, Input } from "@/ui/controls";
import { cn } from "@/ui/cn";
import { useLazyList } from "@/ui/paging";
import { ChoiceCards } from "@/ui/choice";
import { GiteaIcon, GitHubIcon, GitLabIcon } from "@/ui/brands";
import { Combobox } from "@/ui/select";

export type GitKind = "github-app" | "github" | "gitlab" | "gitea";

export interface GitConnection {
  id: string;
  kind: GitKind;
  name: string;
  apiUrl: string;
  webUrl: string;
  account: string;
  appSlug?: string;
  installUrl?: string;
  createdAt: string;
  services: string[];
  /** project/env/service -> the repository it builds. */
  serviceRepos?: Record<string, string>;
  installations?: string[];
  problem?: string;
}

export interface GitRepo {
  fullName: string;
  cloneUrl: string;
  webUrl: string;
  defaultBranch: string;
  private: boolean;
}

export const kindLabel: Record<GitKind, string> = {
  "github-app": "GitHub App",
  github: "GitHub",
  gitlab: "GitLab",
  gitea: "Gitea / Forgejo",
};

export function useGitConnections() {
  return useQuery({
    queryKey: ["integrations", "git"],
    queryFn: async () => (await api<{ items: GitConnection[] }>("GET", "/integrations/git")).items,
    staleTime: 30_000,
  });
}

export function useRepos(connection: string, q: string) {
  return useQuery({
    queryKey: ["integrations", "git", connection, "repos", q],
    queryFn: async () => (await api<{ items: GitRepo[] }>("GET", `/integrations/git/${connection}/repos?q=${encodeURIComponent(q)}`)).items,
    enabled: connection !== "",
    staleTime: 60_000,
    retry: false,
  });
}

export function useBranches(connection: string, repo: string) {
  return useQuery({
    queryKey: ["integrations", "git", connection, "branches", repo],
    queryFn: async () => (await api<{ items: string[] }>("GET", `/integrations/git/${connection}/branches?repo=${encodeURIComponent(repo)}`)).items,
    enabled: connection !== "" && repo !== "",
    staleTime: 60_000,
    retry: false,
  });
}

/**
 * Pick where a service builds from: a repository of a connected provider
 * (webhook and commit statuses set up automatically), or any Git URL.
 * connection "" means the URL mode, handled by the caller.
 */
export function RepoPicker({
  connections,
  connection,
  repo,
  onConnection,
  onRepo,
}: {
  connections: GitConnection[];
  connection: string;
  repo: string;
  onConnection: (name: string) => void;
  onRepo: (r: GitRepo) => void;
}) {
  const [q, setQ] = useState("");
  const [debounced, setDebounced] = useState("");
  useEffect(() => {
    const t = setTimeout(() => setDebounced(q.trim()), 250);
    return () => clearTimeout(t);
  }, [q]);
  const repos = useRepos(connection, debounced);
  const lazyRepos = useLazyList(repos.data ?? []);
  return (
    <div className="flex flex-col gap-2">
      {connections.length > 0 && (
      <ChoiceCards
        label="Repository source"
        value={connection}
        onChange={onConnection}
        columns={3}
        options={[
          ...connections.map((c) => ({
            value: c.name,
            title: c.name,
            description: `${kindLabel[c.kind]} · ${c.account}`,
            icon: c.kind === "gitlab" ? GitLabIcon : c.kind === "gitea" ? GiteaIcon : GitHubIcon,
          })),
          { value: "", title: "Any Git URL", description: "A public repository, or one reached with a token.", icon: Link2 },
        ]}
      />
      )}
      <div className="flex text-xs">
        <Link to={"/integrations/git/new" as string} target="_blank" className="text-accent flex items-center gap-1 hover:underline">
          <Plus className="size-3" /> Connect GitHub, GitLab or Gitea
        </Link>
      </div>
      {connection !== "" && (
        <div className="flex flex-col gap-1">
          <span className="text-muted text-xs">Repository</span>
          <div className="flex flex-col gap-1">
            <div className="relative">
              <Search className="text-faint absolute top-2 left-2 size-3.5" />
              <Input value={q} onChange={(e) => setQ(e.target.value)} placeholder="Filter repositories" className="pl-7" />
            </div>
            {repos.error && <Alert>{repos.error instanceof ApiError ? repos.error.message : "Could not list repositories"}</Alert>}
            <div className="border-line max-h-56 overflow-y-auto rounded-sm border">
              {repos.isLoading && <div className="text-muted p-2 text-xs">Loading repositories…</div>}
              {repos.data?.length === 0 && (
                <div className="text-muted p-2 text-xs">
                  No repositories{debounced && ` matching “${debounced}”`}. A GitHub App only sees the repositories it is installed on.
                </div>
              )}
              {lazyRepos.shown.map((r) => (
                <button
                  type="button"
                  key={r.fullName}
                  onClick={() => onRepo(r)}
                  className={cn(
                    "border-line flex w-full items-center gap-2 border-b px-2 py-1.5 text-left text-xs last:border-b-0",
                    repo === r.fullName ? "bg-hover" : "hover:bg-hover/60",
                  )}
                >
                  <Check className={cn("size-3.5 shrink-0", repo === r.fullName ? "text-accent" : "invisible")} />
                  <span className="truncate font-mono">{r.fullName}</span>
                  {r.private && <Lock className="text-faint size-3 shrink-0" aria-label="private" />}
                  <span className="text-faint ml-auto shrink-0 font-mono">{r.defaultBranch}</span>
                  <a
                    href={r.webUrl}
                    target="_blank"
                    rel="noreferrer"
                    onClick={(e) => e.stopPropagation()}
                    className="text-faint hover:text-fg shrink-0"
                    aria-label="Open on the Git host"
                  >
                    <ExternalLink className="size-3" />
                  </a>
                </button>
              ))}
              {lazyRepos.more}
            </div>
          </div>
        </div>
      )}
    </div>
  );
}

/** Branch input with the repository's branches as suggestions. */
export function BranchInput({
  connection,
  repo,
  value,
  onChange,
}: {
  connection: string;
  repo: string;
  value: string;
  onChange: (v: string) => void;
}) {
  const branches = useBranches(connection, repo);
  return (
    <Combobox
      value={value}
      onChange={onChange}
      suggestions={branches.data ?? []}
      className="font-mono"
      placeholder="default branch"
    />
  );
}
