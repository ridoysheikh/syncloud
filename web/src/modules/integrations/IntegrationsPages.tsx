import { useState, type FormEvent, type ReactNode } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Link, useNavigate, useParams } from "@tanstack/react-router";
import {
  ArrowRight,
  Bell,
  Cloud,
  DatabaseBackup,
  ExternalLink,
  GitBranch,
  Globe,
  HardDrive,
  Lock,
  Package,
  Plug,
  Search,
  Trash2,
} from "lucide-react";
import { api, ApiError } from "@/lib/api";
import { since } from "@/lib/nodes";
import { PageHeader } from "@/ui/PageHeader";
import { Panel } from "@/ui/Panel";
import { DataTable } from "@/ui/DataTable";
import { EmptyState } from "@/ui/EmptyState";
import { Alert, Button, Field, Input, StatusBadge } from "@/ui/controls";
import { cn, gap } from "@/ui/cn";
import { GiteaIcon, GitHubIcon, GitLabIcon } from "@/ui/brands";
import { kindLabel, useGitConnections, useRepos, type GitConnection, type GitKind } from "./RepoPicker";

const providers: { kind: GitKind; title: string; icon: typeof GitHubIcon; blurb: string; recommended?: boolean }[] = [
  {
    kind: "github-app",
    title: "GitHub",
    icon: GitHubIcon,
    blurb: "One click: SynCloud creates a GitHub App, you pick the repositories. Webhooks and commit statuses work with no tokens to manage.",
    recommended: true,
  },
  { kind: "gitlab", title: "GitLab", icon: GitLabIcon, blurb: "gitlab.com or self-managed, with an access token (api scope)." },
  { kind: "gitea", title: "Gitea / Forgejo", icon: GiteaIcon, blurb: "Your own Gitea, Forgejo or Codeberg, with an access token." },
  { kind: "github", title: "GitHub (token)", icon: GitHubIcon, blurb: "A personal access token instead of an app, e.g. for GitHub Enterprise without app rights." },
];

const errText = (e: unknown, fallback: string) => (e instanceof ApiError ? e.message : fallback);

function KindIcon({ kind, className }: { kind: GitKind; className?: string }) {
  const Icon = kind === "gitlab" ? GitLabIcon : kind === "gitea" ? GiteaIcon : GitHubIcon;
  return <Icon className={className} />;
}

/** Integrations (§5.8, §9.3, §16): every connection to an outside service in one place. */
function useCount(key: string, path: string) {
  return useQuery({
    queryKey: ["integrations", "count", key],
    queryFn: async () => (await api<{ items: unknown[] }>("GET", path)).items.length,
    retry: false,
  }).data;
}

export function IntegrationsPage() {
  const params = new URLSearchParams(window.location.search);
  const conns = useGitConnections();
  const channels = useCount("channels", "/alerts/channels");
  const s3 = useCount("s3", "/s3/endpoints");
  const upstreams = useCount("upstreams", "/registry/upstreams");
  const clouds = useCount("clouds", "/cloud-providers");
  const others: { title: string; icon: typeof Bell; to: string; n?: number; what: [string, string] | null; children: ReactNode }[] = [
    { title: "Notifications", icon: Bell, to: "/monitoring/alerts?tab=channels", n: channels, what: ["channel", "channels"], children: "Slack, Discord, Telegram, email and webhooks for alerts." },
    { title: "S3 storage", icon: HardDrive, to: "/storage", n: s3, what: ["endpoint", "endpoints"], children: "AWS S3, Cloudflare R2, B2, Wasabi, MinIO: buckets for services and backups." },
    { title: "Container registries", icon: Package, to: "/registry/upstreams", n: upstreams, what: ["registry", "registries"], children: "Docker Hub, GHCR, Quay and private registries for pulls." },
    { title: "Cloud providers", icon: Cloud, to: "/compute/node-pools", n: clouds, what: ["provider", "providers"], children: "Hetzner, DigitalOcean or a webhook to add and remove nodes." },
    { title: "Backups", icon: DatabaseBackup, to: "/settings/backups", children: "Encrypted platform backups to any S3 endpoint.", what: null },
    { title: "Domains and DNS", icon: Globe, to: "/settings/domains", children: "Your own domain, certificates and DNS checks.", what: null },
  ];
  return (
    <div className={cn("flex flex-col", gap)}>
      <PageHeader crumbs={["Integrations"]} title="Integrations" />
      {params.get("installed") === "github" && (
        <Alert tone="info">
          The GitHub App is installed. Pick its repositories when you create a service, or on a service's Builds tab: SynCloud builds every
          push and reports the result on the commit.
        </Alert>
      )}
      {params.get("error") && <Alert>{params.get("error")}</Alert>}

      <Panel title="Git providers">
        <div className="flex flex-col gap-3">
          <div className={cn("grid grid-cols-1 sm:grid-cols-2 xl:grid-cols-4", gap)}>
            {providers.map((p) => (
              <Link
                key={p.kind}
                to={("/integrations/git/new?kind=" + p.kind) as string}
                className="border-line hover:border-line-strong hover:bg-hover/50 rounded-btn flex flex-col gap-1.5 border p-2.5"
              >
                <span className="flex items-center gap-2 text-sm font-medium">
                  <p.icon className="size-4" /> {p.title}
                  {p.recommended && <span className="text-accent text-[10px] font-normal tracking-wide uppercase">Recommended</span>}
                </span>
                <span className="text-muted text-xs">{p.blurb}</span>
                <span className="text-accent mt-auto flex items-center gap-1 text-xs">
                  Connect <ArrowRight className="size-3" />
                </span>
              </Link>
            ))}
          </div>
          <DataTable
            rows={conns.data ?? []}
            rowKey={(c) => c.id}
            empty={
              !conns.isLoading && (
                <EmptyState icon={Plug} title="No Git provider connected">
                  Connect one to pick repositories by name; SynCloud then sets up webhooks and reports build statuses on commits.
                </EmptyState>
              )
            }
            columns={[
              {
                header: "Connection",
                cell: (c) => (
                  <Link to={`/integrations/git/${c.name}` as string} className="hover:text-accent flex items-center gap-1.5 font-medium">
                    <KindIcon kind={c.kind} className="size-3.5" /> {c.name}
                  </Link>
                ),
              },
              { header: "Type", cell: (c) => <span className="text-muted">{kindLabel[c.kind]}</span> },
              { header: "Account", cell: (c) => <span className="font-mono">{c.account}</span> },
              { header: "Server", cell: (c) => <span className="text-muted font-mono">{c.webUrl.replace(/^https?:\/\//, "")}</span> },
              { header: "Services", className: "w-full", cell: (c) => <span className="text-muted">{c.services.length || "—"}</span> },
              { header: "Added", cell: (c) => <span className="text-muted whitespace-nowrap">{since(c.createdAt)}</span> },
            ]}
          />
        </div>
      </Panel>

      <Panel title="More integrations">
        <div className={cn("grid grid-cols-1 sm:grid-cols-2 xl:grid-cols-3", gap)}>
          {others.map((o) => (
            <Link key={o.title} to={o.to as string} className="border-line hover:border-line-strong hover:bg-hover/50 rounded-btn flex gap-2.5 border p-2.5">
              <o.icon className="text-muted mt-0.5 size-4 shrink-0" />
              <span className="flex min-w-0 flex-col gap-0.5">
                <span className="flex items-center gap-2 text-sm font-medium">
                  {o.title}
                  {o.n !== undefined && o.what && (
                    <StatusBadge tone={o.n > 0 ? "ok" : "neutral"}>
                      {o.n} {o.what[o.n === 1 ? 0 : 1]}
                    </StatusBadge>
                  )}
                </span>
                <span className="text-muted text-xs">{o.children}</span>
              </span>
            </Link>
          ))}
        </div>
      </Panel>
    </div>
  );
}

/** Creating a token, with the scopes filled in where the host allows it. */
function tokenHelp(kind: GitKind, server: string) {
  const base = (server.trim() || (kind === "gitlab" ? "https://gitlab.com" : "https://github.com")).replace(/\/+$/, "");
  switch (kind) {
    case "github":
      return {
        href: `${base}/settings/tokens/new?scopes=repo,admin:repo_hook&description=SynCloud`,
        scopes: "classic token with repo and admin:repo_hook, or a fine-grained token with Contents (read), Commit statuses and Webhooks (write)",
      };
    case "gitlab":
      return { href: `${base}/-/user_settings/personal_access_tokens?name=SynCloud&scopes=api`, scopes: "the api scope" };
    default:
      return { href: server.trim() ? `${base}/user/settings/applications` : "", scopes: "repository (read and write) and user (read)" };
  }
}

const privateHost = (h: string) => /^(localhost|127\.|10\.|192\.168\.|172\.(1[6-9]|2\d|3[01])\.)/.test(h) || h.endsWith(".localhost");

/** Full-page wizard: connect a Git provider (§5.8). */
export function GitConnectPage() {
  const navigate = useNavigate();
  const qc = useQueryClient();
  const [kind, setKind] = useState<GitKind | null>(() => (new URLSearchParams(window.location.search).get("kind") as GitKind | null) ?? null);
  const [name, setName] = useState("");
  const [server, setServer] = useState("");
  const [token, setToken] = useState("");
  const [org, setOrg] = useState("");
  const conns = useGitConnections();
  const defaultName = kind === "github-app" ? "github" : (kind ?? "");
  const finalName = name.trim() || (conns.data?.some((c) => c.name === defaultName) ? "" : defaultName);

  const addToken = useMutation({
    mutationFn: () => api<GitConnection>("POST", "/integrations/git", { kind, name: finalName, url: server.trim(), token: token.trim() }),
    onSuccess: (c) => {
      void qc.invalidateQueries({ queryKey: ["integrations"] });
      void navigate({ to: `/integrations/git/${c.name}` as string });
    },
  });
  const startApp = useMutation({
    mutationFn: () => api<{ postUrl: string; manifest: string }>("POST", "/integrations/github/manifest", { name: finalName, org: org.trim(), githubUrl: server.trim() }),
    onSuccess: (m) => {
      // GitHub's manifest flow is a form POST from the browser.
      const form = document.createElement("form");
      form.method = "post";
      form.action = m.postUrl;
      const input = document.createElement("input");
      input.type = "hidden";
      input.name = "manifest";
      input.value = m.manifest;
      form.appendChild(input);
      document.body.appendChild(form);
      form.submit();
    },
  });
  const submit = (e: FormEvent) => {
    e.preventDefault();
    if (kind === "github-app") startApp.mutate();
    else addToken.mutate();
  };
  const help = kind && kind !== "github-app" ? tokenHelp(kind, server) : null;
  const unreachable = privateHost(window.location.hostname);

  return (
    <div className={cn("flex flex-col", gap)}>
      <PageHeader crumbs={["Integrations"]} title="Connect a Git provider" />
      <Panel title="1 · Provider">
        <div className={cn("grid grid-cols-1 sm:grid-cols-2 xl:grid-cols-4", gap)}>
          {providers.map((p) => (
            <button
              type="button"
              key={p.kind}
              onClick={() => setKind(p.kind)}
              className={cn(
                "rounded-btn flex flex-col gap-1 border p-2.5 text-left",
                kind === p.kind ? "border-accent bg-hover" : "border-line hover:border-line-strong",
              )}
            >
              <span className="flex items-center gap-2 text-sm font-medium">
                <p.icon className="size-4" /> {p.title}
              </span>
              <span className="text-muted text-xs">{p.blurb}</span>
            </button>
          ))}
        </div>
      </Panel>
      {kind && (
        <Panel title={kind === "github-app" ? "2 · Create the GitHub App" : `2 · ${kindLabel[kind]} access token`}>
          <form onSubmit={submit} className="flex max-w-2xl flex-col gap-3">
            {kind === "github-app" && (
              <div className="text-muted flex flex-col gap-1 text-xs">
                <p>GitHub asks you to confirm a new private app owned by you or your organization, with these permissions:</p>
                <ul className="ml-4 list-disc">
                  <li>Contents and metadata: read (to build)</li>
                  <li>Commit statuses: write (building / deployed / failed on each commit)</li>
                  <li>Push and pull request events, delivered to this dashboard</li>
                </ul>
                <p>Then you choose which repositories it may see. No token is stored; SynCloud mints short-lived ones.</p>
              </div>
            )}
            {unreachable && (
              <Alert tone="warn">
                This dashboard runs at {window.location.host}, which the Git host probably cannot reach: pushes are then picked up by polling
                (every minute) instead of webhooks. Set a public domain under Settings › Domains for instant builds.
              </Alert>
            )}
            <div className="grid grid-cols-1 gap-3 sm:grid-cols-2">
              <Field label="Connection name" hint="How services refer to it.">
                <Input value={name} onChange={(e) => setName(e.target.value)} placeholder={finalName || "e.g. work-github"} />
              </Field>
              {kind === "github-app" ? (
                <Field label="Organization" hint="Leave empty to create the app on your own account.">
                  <Input value={org} onChange={(e) => setOrg(e.target.value)} placeholder="acme" />
                </Field>
              ) : (
                <Field
                  label="Server URL"
                  hint={kind === "gitea" ? "Required, e.g. https://git.example.com or https://codeberg.org" : kind === "gitlab" ? "Empty for gitlab.com" : "Empty for github.com"}
                >
                  <Input
                    value={server}
                    onChange={(e) => setServer(e.target.value)}
                    placeholder={kind === "gitea" ? "https://git.example.com" : kind === "gitlab" ? "https://gitlab.com" : "https://github.com"}
                  />
                </Field>
              )}
            </div>
            {kind === "github-app" && (
              <details className="text-xs">
                <summary className="text-muted cursor-pointer">GitHub Enterprise</summary>
                <div className="mt-2 max-w-sm">
                  <Field label="GitHub Enterprise URL" hint="Empty for github.com">
                    <Input value={server} onChange={(e) => setServer(e.target.value)} placeholder="https://github.example.com" />
                  </Field>
                </div>
              </details>
            )}
            {help && (
              <Field
                label="Access token"
                hint={
                  <>
                    Needs {help.scopes}.{" "}
                    {help.href && (
                      <a href={help.href} target="_blank" rel="noreferrer" className="text-accent hover:underline">
                        Create one <ExternalLink className="inline size-3" />
                      </a>
                    )}{" "}
                    Checked before saving, then stored encrypted.
                  </>
                }
              >
                <Input type="password" value={token} onChange={(e) => setToken(e.target.value)} autoComplete="off" />
              </Field>
            )}
            {(addToken.error || startApp.error) && <Alert>{errText(addToken.error ?? startApp.error, "Could not connect")}</Alert>}
            <div className="flex items-center gap-2">
              <Button
                type="submit"
                variant="primary"
                disabled={!finalName || addToken.isPending || startApp.isPending || (kind !== "github-app" && (!token.trim() || (kind === "gitea" && !server.trim())))}
              >
                {kind === "github-app" ? (
                  <>
                    <GitHubIcon className="size-3.5" /> {startApp.isPending ? "Opening GitHub…" : "Create GitHub App on GitHub"}
                  </>
                ) : addToken.isPending ? (
                  "Checking the token…"
                ) : (
                  "Connect"
                )}
              </Button>
              <Link to={"/integrations" as string}>
                <Button variant="ghost" type="button">
                  Cancel
                </Button>
              </Link>
            </div>
          </form>
        </Panel>
      )}
    </div>
  );
}

/** One Git connection: where it is installed, its repositories and services. */
export function GitConnectionPage() {
  const { name } = useParams({ strict: false }) as { name: string };
  const qc = useQueryClient();
  const navigate = useNavigate();
  const [q, setQ] = useState("");
  const conn = useQuery({
    queryKey: ["integrations", "git", name, "detail"],
    queryFn: () => api<GitConnection>("GET", `/integrations/git/${name}`),
    retry: false,
  });
  const repos = useRepos(conn.data ? name : "", q.trim());
  const del = useMutation({
    mutationFn: () => api("DELETE", `/integrations/git/${name}`),
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: ["integrations"] });
      void navigate({ to: "/integrations" as string });
    },
  });
  if (conn.error) {
    return (
      <div className={cn("flex flex-col", gap)}>
        <PageHeader crumbs={["Integrations"]} title={name} />
        <Alert>{errText(conn.error, "Not found")}</Alert>
      </div>
    );
  }
  const c = conn.data;
  if (!c) return <PageHeader crumbs={["Integrations"]} title={name} />;
  const appPage = c.appSlug ? `${c.webUrl.replace(/\/+$/, "")}/apps/${c.appSlug}` : "";
  const usedBy = (repo: string) =>
    Object.entries(c.serviceRepos ?? {})
      .filter(([, r]) => r.toLowerCase() === repo.toLowerCase())
      .map(([sv]) => sv);
  return (
    <div className={cn("flex flex-col", gap)}>
      <PageHeader
        crumbs={["Integrations", "Git providers"]}
        title={
          <span className="flex items-center gap-2">
            <KindIcon kind={c.kind} /> {c.name}
          </span>
        }
        status={<StatusBadge tone={c.problem ? "bad" : "ok"}>{c.problem ? "unreachable" : "connected"}</StatusBadge>}
        actions={
          <Button
            variant="danger"
            disabled={c.services.length > 0 || del.isPending}
            title={c.services.length > 0 ? "Disconnect its services first" : undefined}
            onClick={() => confirm(`Remove the connection ${c.name}?`) && del.mutate()}
          >
            <Trash2 className="size-3.5" /> Remove
          </Button>
        }
      />
      {c.problem && <Alert>{c.problem}</Alert>}
      {del.error && <Alert>{errText(del.error, "Could not remove")}</Alert>}
      <div className={cn("grid grid-cols-1 lg:grid-cols-3", gap)}>
        <Panel title="Connection">
          <dl className="grid grid-cols-[6rem_1fr] gap-y-1.5 text-xs">
            <dt className="text-muted">Type</dt>
            <dd>{kindLabel[c.kind]}</dd>
            <dt className="text-muted">Account</dt>
            <dd className="font-mono">{c.account}</dd>
            <dt className="text-muted">Server</dt>
            <dd className="font-mono break-all">{c.webUrl}</dd>
            <dt className="text-muted">Added</dt>
            <dd>{since(c.createdAt)}</dd>
            {c.kind === "github-app" && (
              <>
                <dt className="text-muted">Installed on</dt>
                <dd className="font-mono">{c.installations?.length ? c.installations.join(", ") : "nowhere yet"}</dd>
              </>
            )}
          </dl>
          {c.kind === "github-app" && (
            <div className="mt-3 flex flex-wrap gap-1.5">
              <a href={c.installUrl} target="_blank" rel="noreferrer">
                <Button variant="primary">
                  <GitHubIcon className="size-3.5" /> {c.installations?.length ? "Install on more repositories" : "Install the app"}
                </Button>
              </a>
              {appPage && (
                <a href={appPage} target="_blank" rel="noreferrer">
                  <Button>
                    App on GitHub <ExternalLink className="size-3" />
                  </Button>
                </a>
              )}
            </div>
          )}
        </Panel>
        <Panel title={`Services · ${c.services.length}`}>
          {c.services.length === 0 ? (
            <p className="text-muted text-xs">
              None yet. Pick a repository from this connection when creating a service, or on a service's Builds tab.
            </p>
          ) : (
            <ul className="flex flex-col gap-1 text-xs">
              {c.services.map((s) => {
                const [p, e, n] = s.split("/");
                return (
                  <li key={s}>
                    <Link to={`/projects/${p}/${e}/services/${n}?tab=builds` as string} className="hover:text-accent font-mono">
                      {s}
                    </Link>
                  </li>
                );
              })}
            </ul>
          )}
        </Panel>
        <Panel title="How builds are triggered">
          <p className="text-muted text-xs">
            {c.kind === "github-app"
              ? "GitHub sends every push of an installed repository to this cluster's app webhook."
              : "SynCloud adds a push webhook to each repository a service builds from, and removes it when the service disconnects."}{" "}
            Polling stays on as a safety net. Each build's state is shown on its commit as{" "}
            <span className="font-mono">syncloud/&lt;project&gt;/&lt;env&gt;/&lt;service&gt;</span>.
          </p>
        </Panel>
      </div>
      <Panel
        title={`Repositories${repos.data ? ` · ${repos.data.length}` : ""}`}
        flush
        actions={
          <div className="relative">
            <Search className="text-faint absolute top-1.5 left-1.5 size-3.5" />
            <Input value={q} onChange={(e) => setQ(e.target.value)} placeholder="Filter" className="h-7 w-48 pl-6 text-xs" />
          </div>
        }
      >
        {repos.error ? (
          <div className="p-2">
            <Alert>{errText(repos.error, "Could not list repositories")}</Alert>
          </div>
        ) : (
          <DataTable
            rows={repos.data ?? []}
            rowKey={(r) => r.fullName}
            empty={
              !repos.isLoading && (
                <EmptyState icon={GitBranch} title="No repositories">
                  {c.kind === "github-app" ? "Install the app on an account or on repositories first." : "The token cannot see any repository."}
                </EmptyState>
              )
            }
            columns={[
              {
                header: "Repository",
                className: "w-full",
                cell: (r) => (
                  <a href={r.webUrl} target="_blank" rel="noreferrer" className="hover:text-accent flex items-center gap-1.5 font-mono">
                    {r.fullName} {r.private && <Lock className="text-faint size-3" />}
                  </a>
                ),
              },
              { header: "Default branch", cell: (r) => <span className="text-muted font-mono">{r.defaultBranch}</span> },
              {
                header: "Built by",
                cell: (r) => <span className="text-muted font-mono whitespace-nowrap">{usedBy(r.fullName).join(", ") || "—"}</span>,
              },
            ]}
          />
        )}
      </Panel>
    </div>
  );
}
