import { useEffect, useMemo, useState, type FormEvent, type ReactNode } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Link, useNavigate, useParams } from "@tanstack/react-router";
import { Check, Download, KeyRound, Plus, Shield, ShieldCheck, ShieldX, Trash2, UserPlus, Users, X } from "lucide-react";
import { api, ApiError } from "@/lib/api";
import { useProjects } from "@/lib/workloads";
import { PageHeader } from "@/ui/PageHeader";
import { Panel } from "@/ui/Panel";
import { DataTable } from "@/ui/DataTable";
import { EmptyState } from "@/ui/EmptyState";
import { Alert, Button, Field, IconButton, Input, StatusBadge } from "@/ui/controls";
import { cn, gap } from "@/ui/cn";

// ── types and hooks ─────────────────────────────────────────────────────────

export interface IamUser {
  id: string;
  email: string;
  name: string;
  kind: "user" | "service";
  isRoot: boolean;
  disabled: boolean;
  mfaEnabled: boolean;
  groups: string[];
  policies: string[];
  lastLoginAt: string | null;
  createdAt: string;
}
interface IamGroup {
  id: string;
  name: string;
  description: string;
  members: string[];
  policies: string[];
}
interface IamPolicy {
  id: string;
  name: string;
  description: string;
  managed: boolean;
  perProject: boolean;
  document: unknown;
  attachedTo: number;
}
interface IamRole {
  id: string;
  name: string;
  description: string;
  trust: { users: string[]; groups: string[]; requireMfa: boolean };
  maxSessionSeconds: number;
  policies: string[];
}
interface Attachment {
  principalType: "user" | "group" | "role";
  principalId: string;
  policy: string;
}

const sel = "bg-bg border-line-strong focus:border-accent h-8 rounded-sm border px-2 text-sm outline-none";
const when = (iso: string | null) => (iso ? new Date(iso).toLocaleString() : "never");
const errText = (e: unknown, fallback: string) => (e instanceof ApiError ? e.message : fallback);

const items = <T,>(path: string) => async () => (await api<{ items: T[] }>("GET", path)).items;
export const useIamUsers = () => useQuery({ queryKey: ["iam", "users"], queryFn: items<IamUser>("/iam/users") });
const useIamGroups = () => useQuery({ queryKey: ["iam", "groups"], queryFn: items<IamGroup>("/iam/groups") });
const useIamPolicies = () => useQuery({ queryKey: ["iam", "policies"], queryFn: items<IamPolicy>("/iam/policies") });
const useIamRoles = () => useQuery({ queryKey: ["iam", "roles"], queryFn: items<IamRole>("/iam/roles") });
const useAttachments = () => useQuery({ queryKey: ["iam", "attachments"], queryFn: items<Attachment>("/iam/attachments") });

function Forbidden({ error }: { error: unknown }) {
  if (!error) return null;
  return <Alert tone="warn">{errText(error, "Unavailable")}</Alert>;
}

// ── attached policies (users, groups, roles) ────────────────────────────────

function AttachedPolicies({ type, id }: { type: Attachment["principalType"]; id: string }) {
  const qc = useQueryClient();
  const { data: atts = [] } = useAttachments();
  const { data: policies = [] } = useIamPolicies();
  const { data: projects = [] } = useProjects();
  const [policy, setPolicy] = useState("");
  const [project, setProject] = useState("");
  const mine = atts.filter((a) => a.principalType === type && a.principalId === id);
  const chosen = policies.find((p) => p.id === policy);
  const name = (p: string) => policies.find((x) => x.id === p)?.name ?? p;
  const refresh = () => qc.invalidateQueries({ queryKey: ["iam"] });
  const attach = useMutation({
    mutationFn: () => api("POST", "/iam/attachments", { principalType: type, principalId: id, policy: chosen?.perProject ? `${policy}:${project}` : policy }),
    onSuccess: () => {
      setPolicy("");
      void refresh();
    },
  });
  const detach = useMutation({
    mutationFn: (p: string) => api("DELETE", "/iam/attachments", { principalType: type, principalId: id, policy: p }),
    onSuccess: refresh,
  });
  return (
    <Panel title="Policies">
      <div className="flex flex-col gap-2">
        {mine.length === 0 && <span className="text-faint text-xs">No policies attached: nothing is allowed (except managing one's own keys and MFA).</span>}
        {mine.map((a) => (
          <div key={a.policy} className="flex items-center gap-2 text-xs">
            <Shield className="text-muted size-3.5" />
            <span className="font-mono">{name(a.policy)}</span>
            <IconButton label="Detach" onClick={() => detach.mutate(a.policy)}>
              <X className="size-3.5" />
            </IconButton>
          </div>
        ))}
        <div className="flex flex-wrap items-center gap-1.5">
          <select value={policy} onChange={(e) => setPolicy(e.target.value)} className={sel}>
            <option value="">attach a policy…</option>
            {policies.map((p) => (
              <option key={p.id} value={p.id}>
                {p.name}
                {p.managed ? (p.perProject ? " (managed, per project)" : " (managed)") : ""}
              </option>
            ))}
          </select>
          {chosen?.perProject && (
            <select value={project} onChange={(e) => setProject(e.target.value)} className={sel}>
              <option value="">project…</option>
              {projects.map((p) => (
                <option key={p.name}>{p.name}</option>
              ))}
            </select>
          )}
          <Button disabled={!policy || (chosen?.perProject && !project) || attach.isPending} onClick={() => attach.mutate()}>
            <Plus className="size-3.5" /> Attach
          </Button>
        </div>
        {(attach.error || detach.error) && <Alert>{errText(attach.error ?? detach.error, "Could not change policies")}</Alert>}
      </div>
    </Panel>
  );
}

// ── users ───────────────────────────────────────────────────────────────────

export function UsersPage() {
  const { data: users = [], isLoading, error } = useIamUsers();
  return (
    <div className={cn("flex flex-col", gap)}>
      <PageHeader
        crumbs={["IAM"]}
        title="Users"
        actions={
          <Link to={"/iam/users/new" as string}>
            <Button variant="primary">
              <UserPlus className="size-3.5" /> New user
            </Button>
          </Link>
        }
      />
      <Forbidden error={error} />
      <Panel flush>
        <DataTable
          rows={users}
          rowKey={(u) => u.id}
          empty={!isLoading && !error && <EmptyState icon={Users} title="No users" />}
          columns={[
            {
              header: "User",
              cell: (u) => (
                <Link to={`/iam/users/${u.id}` as string} className="hover:text-accent flex flex-col py-1">
                  <span className="font-medium">{u.kind === "service" ? u.name : u.email}</span>
                  <span className="text-faint">{u.kind === "service" ? "service account" : u.name}</span>
                </Link>
              ),
            },
            {
              header: "State",
              cell: (u) => (
                <div className="flex gap-1">
                  {u.isRoot && <StatusBadge tone="info">root</StatusBadge>}
                  <StatusBadge tone={u.disabled ? "bad" : "ok"}>{u.disabled ? "disabled" : "active"}</StatusBadge>
                  {u.mfaEnabled && <StatusBadge tone="ok">MFA</StatusBadge>}
                </div>
              ),
            },
            { header: "Groups", cell: (u) => <span className="text-muted">{u.groups.length ? u.groups.join(", ") : "—"}</span> },
            { header: "Policies", className: "w-full", cell: (u) => <span className="font-mono text-xs">{u.policies.join(", ") || "—"}</span> },
            { header: "Last sign-in", cell: (u) => <span className="text-muted whitespace-nowrap">{u.kind === "service" ? "—" : when(u.lastLoginAt)}</span> },
          ]}
        />
      </Panel>
    </div>
  );
}

export function NewUserPage() {
  const navigate = useNavigate();
  const qc = useQueryClient();
  const [kind, setKind] = useState<"user" | "service">("user");
  const [email, setEmail] = useState("");
  const [name, setName] = useState("");
  const [password, setPassword] = useState("");
  const create = useMutation({
    mutationFn: () => api<IamUser>("POST", "/iam/users", { kind, email, name, password }),
    onSuccess: (u) => {
      void qc.invalidateQueries({ queryKey: ["iam"] });
      void navigate({ to: `/iam/users/${u.id}` as string });
    },
  });
  return (
    <form
      onSubmit={(e) => {
        e.preventDefault();
        create.mutate();
      }}
      className={cn("flex flex-col", gap)}
    >
      <PageHeader
        crumbs={["IAM", "Users"]}
        title="New user"
        actions={
          <>
            <Button variant="ghost" onClick={() => navigate({ to: "/iam/users" as string })}>
              Cancel
            </Button>
            <Button type="submit" variant="primary" disabled={create.isPending}>
              Create
            </Button>
          </>
        }
      />
      <Panel title="Kind">
        <div className="grid max-w-2xl grid-cols-2 gap-2">
          {(
            [
              ["user", "Person", "Signs in to the dashboard with a password (and MFA); can also have keys and tokens."],
              ["service", "Service account", "For CI and automation: access keys and tokens only, no console sign-in."],
            ] as const
          ).map(([k, t, d]) => (
            <button type="button" key={k} onClick={() => setKind(k)} className={cn("border-line rounded-sm border p-2 text-left", kind === k && "border-accent bg-hover")}>
              <div className="text-sm">{t}</div>
              <div className="text-muted mt-0.5 text-xs">{d}</div>
            </button>
          ))}
        </div>
      </Panel>
      <Panel title="Details">
        <div className="grid max-w-2xl grid-cols-1 gap-2 sm:grid-cols-2">
          {kind === "user" && (
            <Field label="Email">
              <Input type="email" value={email} onChange={(e) => setEmail(e.target.value)} required autoFocus />
            </Field>
          )}
          <Field label={kind === "user" ? "Name" : "Name (letters, digits, - _ .)"}>
            <Input value={name} onChange={(e) => setName(e.target.value)} placeholder={kind === "user" ? "Ann Example" : "github-deployer"} required />
          </Field>
          {kind === "user" && (
            <Field label="Initial password" hint="12 characters or more; they can change it">
              <Input type="password" value={password} onChange={(e) => setPassword(e.target.value)} autoComplete="new-password" required />
            </Field>
          )}
        </div>
        <p className="text-muted mt-2 text-xs">New users can do nothing until a policy is attached to them or to one of their groups.</p>
      </Panel>
      {create.error && <Alert>{errText(create.error, "Could not create the user")}</Alert>}
    </form>
  );
}

function UserKeys({ userId }: { userId: string }) {
  const qc = useQueryClient();
  const key = ["iam", "access-keys", userId];
  const { data: keys = [] } = useQuery({ queryKey: key, queryFn: items<{ id: string; description: string; createdAt: string; lastUsedAt: string | null }>(`/iam/access-keys?userId=${userId}`) });
  const [desc, setDesc] = useState("");
  const [secret, setSecret] = useState<{ id: string; secretAccessKey: string } | null>(null);
  const create = useMutation({
    mutationFn: () => api<{ id: string; secretAccessKey: string }>("POST", `/iam/access-keys?userId=${userId}`, { description: desc }),
    onSuccess: (k) => {
      setSecret(k);
      setDesc("");
      void qc.invalidateQueries({ queryKey: key });
    },
  });
  const del = useMutation({ mutationFn: (id: string) => api("DELETE", `/iam/access-keys/${id}?userId=${userId}`), onSuccess: () => qc.invalidateQueries({ queryKey: key }) });
  return (
    <Panel title={`Access keys (${keys.length}/2)`}>
      <div className="flex flex-col gap-1.5 text-xs">
        {keys.map((k) => (
          <div key={k.id} className="flex items-center gap-2">
            <KeyRound className="text-muted size-3.5" />
            <span className="font-mono">{k.id}</span>
            <span className="text-muted">{k.description}</span>
            <span className="text-faint">last used {when(k.lastUsedAt)}</span>
            <IconButton label="Delete" onClick={() => confirm(`Delete ${k.id}?`) && del.mutate(k.id)}>
              <Trash2 className="size-3.5" />
            </IconButton>
          </div>
        ))}
        {keys.length < 2 && (
          <div className="flex gap-1.5">
            <Input value={desc} onChange={(e) => setDesc(e.target.value)} placeholder="description, e.g. GitHub Actions" className="h-7 max-w-xs" />
            <Button onClick={() => create.mutate()} disabled={create.isPending}>
              <Plus className="size-3.5" /> Create key
            </Button>
          </div>
        )}
        {secret && (
          <Alert tone="info">
            Save the secret now; it is not shown again. <span className="font-mono">{secret.id}</span> / <span className="font-mono break-all">{secret.secretAccessKey}</span>
          </Alert>
        )}
        {create.error && <Alert>{errText(create.error, "Could not create the key")}</Alert>}
      </div>
    </Panel>
  );
}

export function UserPage() {
  const { id } = useParams({ strict: false }) as { id: string };
  const qc = useQueryClient();
  const navigate = useNavigate();
  const { data: users } = useIamUsers();
  const { data: groups = [] } = useIamGroups();
  const u = users?.find((x) => x.id === id);
  const [password, setPassword] = useState("");
  const refresh = () => qc.invalidateQueries({ queryKey: ["iam"] });
  const update = useMutation({ mutationFn: (body: Record<string, unknown>) => api("PUT", `/iam/users/${id}`, body), onSuccess: refresh });
  const del = useMutation({
    mutationFn: () => api("DELETE", `/iam/users/${id}`),
    onSuccess: () => {
      void refresh();
      void navigate({ to: "/iam/users" as string });
    },
  });
  const resetMfa = useMutation({ mutationFn: () => api("DELETE", `/iam/mfa?userId=${id}`, { code: "" }), onSuccess: refresh });
  const membership = useMutation({
    mutationFn: ({ g, add }: { g: IamGroup; add: boolean }) =>
      api("PUT", `/iam/groups/${g.id}`, { name: g.name, description: g.description, members: add ? [...g.members, id] : g.members.filter((m) => m !== id) }),
    onSuccess: refresh,
  });
  if (!u) return <PageHeader crumbs={["IAM", "Users"]} title={users ? "User not found" : "…"} />;
  return (
    <div className={cn("flex flex-col", gap)}>
      <PageHeader
        crumbs={["IAM", "Users"]}
        title={u.kind === "service" ? u.name : u.email}
        actions={
          !u.isRoot && (
            <Button variant="ghost" onClick={() => confirm(`Delete ${u.email}? Their keys and sessions stop working.`) && del.mutate()}>
              <Trash2 className="size-3.5" /> Delete
            </Button>
          )
        }
      />
      <Panel title="Account">
        <div className="flex max-w-3xl flex-col gap-2 text-xs">
          <div className="flex flex-wrap gap-2">
            <StatusBadge tone={u.disabled ? "bad" : "ok"}>{u.disabled ? "disabled" : "active"}</StatusBadge>
            <StatusBadge tone={u.mfaEnabled ? "ok" : "neutral"}>{u.mfaEnabled ? "MFA on" : "no MFA"}</StatusBadge>
            <span className="text-muted">{u.kind === "service" ? "Service account" : u.name}</span>
            <span className="text-faint">created {when(u.createdAt)}</span>
          </div>
          {!u.isRoot && (
            <div className="flex flex-wrap items-center gap-1.5">
              <Button onClick={() => update.mutate({ disabled: !u.disabled })}>{u.disabled ? "Enable" : "Disable"}</Button>
              {u.mfaEnabled && <Button onClick={() => confirm("Turn MFA off for this user (lost device)?") && resetMfa.mutate()}>Reset MFA</Button>}
              {u.kind === "user" && (
                <>
                  <Input type="password" value={password} onChange={(e) => setPassword(e.target.value)} placeholder="new password" className="h-7 max-w-48" autoComplete="new-password" />
                  <Button disabled={password.length < 12} onClick={() => update.mutate({ password }, { onSuccess: () => setPassword("") })}>
                    Reset password
                  </Button>
                </>
              )}
            </div>
          )}
          {(update.error || resetMfa.error || del.error) && <Alert>{errText(update.error ?? resetMfa.error ?? del.error, "Could not update")}</Alert>}
        </div>
      </Panel>
      <Panel title="Groups">
        <div className="flex flex-wrap gap-x-3 gap-y-1 text-xs">
          {groups.length === 0 && <span className="text-faint">No groups yet.</span>}
          {groups.map((g) => (
            <label key={g.id} className="flex items-center gap-1.5">
              <input type="checkbox" checked={g.members.includes(id)} onChange={(e) => membership.mutate({ g, add: e.target.checked })} />
              {g.name}
            </label>
          ))}
        </div>
      </Panel>
      <AttachedPolicies type="user" id={id} />
      <UserKeys userId={id} />
    </div>
  );
}

// ── groups ──────────────────────────────────────────────────────────────────

export function GroupsPage() {
  const { data: groups = [], isLoading, error } = useIamGroups();
  return (
    <div className={cn("flex flex-col", gap)}>
      <PageHeader
        crumbs={["IAM"]}
        title="Groups"
        actions={
          <Link to={"/iam/groups/new" as string}>
            <Button variant="primary">
              <Plus className="size-3.5" /> New group
            </Button>
          </Link>
        }
      />
      <Forbidden error={error} />
      <Panel flush>
        <DataTable
          rows={groups}
          rowKey={(g) => g.id}
          empty={!isLoading && !error && <EmptyState icon={Users} title="No groups">Groups give the same policies to many users.</EmptyState>}
          columns={[
            {
              header: "Group",
              cell: (g) => (
                <Link to={`/iam/groups/${g.id}` as string} className="hover:text-accent font-medium">
                  {g.name}
                </Link>
              ),
            },
            { header: "Members", cell: (g) => <span className="text-muted">{g.members.length}</span> },
            { header: "Policies", className: "w-full", cell: (g) => <span className="font-mono text-xs">{g.policies.join(", ") || "—"}</span> },
            { header: "Description", cell: (g) => <span className="text-faint">{g.description || "—"}</span> },
          ]}
        />
      </Panel>
    </div>
  );
}

export function GroupPage() {
  const { id } = useParams({ strict: false }) as { id?: string };
  const isNew = !id;
  const navigate = useNavigate();
  const qc = useQueryClient();
  const { data: groups } = useIamGroups();
  const { data: users = [] } = useIamUsers();
  const g = groups?.find((x) => x.id === id);
  const [name, setName] = useState("");
  const [description, setDescription] = useState("");
  const [members, setMembers] = useState<string[]>([]);
  useEffect(() => {
    if (g) {
      setName(g.name);
      setDescription(g.description);
      setMembers(g.members);
    }
  }, [g]);
  const save = useMutation({
    mutationFn: () => (isNew ? api<IamGroup>("POST", "/iam/groups", { name, description, members }) : api<IamGroup>("PUT", `/iam/groups/${id}`, { name, description, members })),
    onSuccess: (x) => {
      void qc.invalidateQueries({ queryKey: ["iam"] });
      if (isNew) void navigate({ to: `/iam/groups/${x.id}` as string });
    },
  });
  const del = useMutation({
    mutationFn: () => api("DELETE", `/iam/groups/${id}`),
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: ["iam"] });
      void navigate({ to: "/iam/groups" as string });
    },
  });
  return (
    <form
      onSubmit={(e) => {
        e.preventDefault();
        save.mutate();
      }}
      className={cn("flex flex-col", gap)}
    >
      <PageHeader
        crumbs={["IAM", "Groups"]}
        title={isNew ? "New group" : g?.name ?? "Group"}
        actions={
          <>
            {!isNew && (
              <Button variant="ghost" onClick={() => confirm(`Delete group ${g?.name}?`) && del.mutate()}>
                <Trash2 className="size-3.5" /> Delete
              </Button>
            )}
            <Button type="submit" variant="primary" disabled={!name || save.isPending}>
              {isNew ? "Create group" : "Save"}
            </Button>
          </>
        }
      />
      <Panel title="Group">
        <div className="grid max-w-2xl grid-cols-1 gap-2 sm:grid-cols-2">
          <Field label="Name">
            <Input value={name} onChange={(e) => setName(e.target.value)} placeholder="developers" autoFocus={isNew} />
          </Field>
          <Field label="Description">
            <Input value={description} onChange={(e) => setDescription(e.target.value)} />
          </Field>
        </div>
      </Panel>
      <Panel title="Members">
        <div className="flex flex-wrap gap-x-3 gap-y-1 text-xs">
          {users.map((u) => (
            <label key={u.id} className="flex items-center gap-1.5">
              <input type="checkbox" checked={members.includes(u.id)} onChange={(e) => setMembers(e.target.checked ? [...members, u.id] : members.filter((m) => m !== u.id))} />
              {u.kind === "service" ? u.name : u.email}
            </label>
          ))}
        </div>
      </Panel>
      {!isNew && id && <AttachedPolicies type="group" id={id} />}
      {save.isSuccess && !isNew && <Alert tone="info">Saved.</Alert>}
      {(save.error || del.error) && <Alert>{errText(save.error ?? del.error, "Could not save")}</Alert>}
    </form>
  );
}

// ── roles ───────────────────────────────────────────────────────────────────

export function RolesPage() {
  const { data: roles = [], isLoading, error } = useIamRoles();
  return (
    <div className={cn("flex flex-col", gap)}>
      <PageHeader
        crumbs={["IAM"]}
        title="Roles"
        actions={
          <Link to={"/iam/roles/new" as string}>
            <Button variant="primary">
              <Plus className="size-3.5" /> New role
            </Button>
          </Link>
        }
      />
      <Alert tone="info">
        A role carries policies that trusted users take on for a while with <span className="font-mono">synctl sts assume-role NAME</span> (15 minutes to
        12 hours), e.g. for CI pipelines or for temporary admin rights.
      </Alert>
      <Forbidden error={error} />
      <Panel flush>
        <DataTable
          rows={roles}
          rowKey={(r) => r.id}
          empty={!isLoading && !error && <EmptyState icon={Shield} title="No roles" />}
          columns={[
            {
              header: "Role",
              cell: (r) => (
                <Link to={`/iam/roles/${r.id}` as string} className="hover:text-accent font-medium">
                  {r.name}
                </Link>
              ),
            },
            { header: "Trusts", cell: (r) => <span className="text-muted">{`${r.trust.users.length} users, ${r.trust.groups.length} groups`}</span> },
            { header: "MFA", cell: (r) => (r.trust.requireMfa ? <StatusBadge tone="ok">required</StatusBadge> : <span className="text-faint">—</span>) },
            { header: "Max session", cell: (r) => <span className="text-muted">{Math.round(r.maxSessionSeconds / 60)} min</span> },
            { header: "Policies", className: "w-full", cell: (r) => <span className="font-mono text-xs">{r.policies.join(", ") || "—"}</span> },
          ]}
        />
      </Panel>
    </div>
  );
}

export function RolePage() {
  const { id } = useParams({ strict: false }) as { id?: string };
  const isNew = !id;
  const navigate = useNavigate();
  const qc = useQueryClient();
  const { data: roles } = useIamRoles();
  const { data: users = [] } = useIamUsers();
  const { data: groups = [] } = useIamGroups();
  const r = roles?.find((x) => x.id === id);
  const [name, setName] = useState("");
  const [description, setDescription] = useState("");
  const [trustUsers, setTrustUsers] = useState<string[]>([]);
  const [trustGroups, setTrustGroups] = useState<string[]>([]);
  const [requireMfa, setRequireMfa] = useState(true);
  const [minutes, setMinutes] = useState(60);
  useEffect(() => {
    if (r) {
      setName(r.name);
      setDescription(r.description);
      setTrustUsers(r.trust.users);
      setTrustGroups(r.trust.groups);
      setRequireMfa(r.trust.requireMfa);
      setMinutes(Math.round(r.maxSessionSeconds / 60));
    }
  }, [r]);
  const body = { name, description, trust: { users: trustUsers, groups: trustGroups, requireMfa }, maxSessionSeconds: minutes * 60 };
  const save = useMutation({
    mutationFn: () => (isNew ? api<IamRole>("POST", "/iam/roles", body) : api<IamRole>("PUT", `/iam/roles/${id}`, body)),
    onSuccess: (x) => {
      void qc.invalidateQueries({ queryKey: ["iam"] });
      if (isNew) void navigate({ to: `/iam/roles/${x.id}` as string });
    },
  });
  const del = useMutation({
    mutationFn: () => api("DELETE", `/iam/roles/${id}`),
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: ["iam"] });
      void navigate({ to: "/iam/roles" as string });
    },
  });
  const toggle = (list: string[], set: (v: string[]) => void, x: string, on: boolean) => set(on ? [...list, x] : list.filter((y) => y !== x));
  return (
    <form
      onSubmit={(e) => {
        e.preventDefault();
        save.mutate();
      }}
      className={cn("flex flex-col", gap)}
    >
      <PageHeader
        crumbs={["IAM", "Roles"]}
        title={isNew ? "New role" : r?.name ?? "Role"}
        actions={
          <>
            {!isNew && (
              <Button variant="ghost" onClick={() => confirm(`Delete role ${r?.name}?`) && del.mutate()}>
                <Trash2 className="size-3.5" /> Delete
              </Button>
            )}
            <Button type="submit" variant="primary" disabled={!name || save.isPending}>
              {isNew ? "Create role" : "Save"}
            </Button>
          </>
        }
      />
      <Panel title="Role">
        <div className="grid max-w-3xl grid-cols-1 gap-2 sm:grid-cols-3">
          <Field label="Name">
            <Input value={name} onChange={(e) => setName(e.target.value)} placeholder="deployer" autoFocus={isNew} />
          </Field>
          <Field label="Description">
            <Input value={description} onChange={(e) => setDescription(e.target.value)} />
          </Field>
          <Field label="Longest session" hint="minutes, 15 to 720">
            <Input type="number" min={15} max={720} value={minutes} onChange={(e) => setMinutes(Number(e.target.value))} />
          </Field>
        </div>
      </Panel>
      <Panel title="Who may assume it">
        <div className="flex flex-col gap-2 text-xs">
          <div className="flex flex-wrap gap-x-3 gap-y-1">
            {users.map((u) => (
              <label key={u.id} className="flex items-center gap-1.5">
                <input type="checkbox" checked={trustUsers.includes(u.id)} onChange={(e) => toggle(trustUsers, setTrustUsers, u.id, e.target.checked)} />
                {u.kind === "service" ? u.name : u.email}
              </label>
            ))}
          </div>
          <div className="flex flex-wrap gap-x-3 gap-y-1">
            {groups.map((g) => (
              <label key={g.id} className="flex items-center gap-1.5">
                <input type="checkbox" checked={trustGroups.includes(g.id)} onChange={(e) => toggle(trustGroups, setTrustGroups, g.id, e.target.checked)} />
                group {g.name}
              </label>
            ))}
          </div>
          <label className="flex items-center gap-1.5">
            <input type="checkbox" checked={requireMfa} onChange={(e) => setRequireMfa(e.target.checked)} /> Only when signed in with MFA
          </label>
        </div>
      </Panel>
      {!isNew && id && <AttachedPolicies type="role" id={id} />}
      {(save.error || del.error) && <Alert>{errText(save.error ?? del.error, "Could not save")}</Alert>}
    </form>
  );
}

// ── policies and the simulator ──────────────────────────────────────────────

export function PoliciesPage() {
  const { data: policies = [], isLoading, error } = useIamPolicies();
  return (
    <div className={cn("flex flex-col", gap)}>
      <PageHeader
        crumbs={["IAM"]}
        title="Policies"
        actions={
          <Link to={"/iam/policies/new" as string}>
            <Button variant="primary">
              <Plus className="size-3.5" /> New policy
            </Button>
          </Link>
        }
      />
      <Forbidden error={error} />
      <Panel flush>
        <DataTable
          rows={policies}
          rowKey={(p) => p.id}
          empty={!isLoading && !error && <EmptyState icon={Shield} title="No policies" />}
          columns={[
            {
              header: "Policy",
              cell: (p) => (
                <Link to={`/iam/policies/${p.id}` as string} className="hover:text-accent font-medium">
                  {p.name}
                </Link>
              ),
            },
            {
              header: "Kind",
              cell: (p) => <StatusBadge tone={p.managed ? "info" : "neutral"}>{p.managed ? (p.perProject ? "managed, per project" : "managed") : "custom"}</StatusBadge>,
            },
            { header: "Attached", cell: (p) => <span className="text-muted">{p.attachedTo}</span> },
            { header: "Description", className: "w-full", cell: (p) => <span className="text-muted">{p.description || "—"}</span> },
          ]}
        />
      </Panel>
      <SimulatorPanel />
    </div>
  );
}

const examplePolicy = `{
  "Version": "2026-01",
  "Statement": [
    {
      "Sid": "ReadShop",
      "Effect": "Allow",
      "Action": ["service:Get*", "service:List*", "logs:*"],
      "Resource": "srn:syncloud:project/shop/*"
    },
    {
      "Sid": "NoDeletesWithoutMFA",
      "Effect": "Deny",
      "Action": "service:DeleteService",
      "Resource": "*",
      "Condition": { "Bool": { "syn:MFAPresent": "false" } }
    }
  ]
}`;

export function PolicyPage() {
  const { id } = useParams({ strict: false }) as { id?: string };
  const isNew = !id;
  const navigate = useNavigate();
  const qc = useQueryClient();
  const { data: policies } = useIamPolicies();
  const p = policies?.find((x) => x.id === id);
  const [name, setName] = useState("");
  const [description, setDescription] = useState("");
  const [doc, setDoc] = useState(examplePolicy);
  useEffect(() => {
    if (p) {
      setName(p.name);
      setDescription(p.description);
      setDoc(JSON.stringify(p.document, null, 2));
    }
  }, [p]);
  let parseError = "";
  try {
    JSON.parse(doc);
  } catch (e) {
    parseError = (e as Error).message;
  }
  const save = useMutation({
    mutationFn: () => {
      const body = { name, description, document: JSON.parse(doc) };
      return isNew ? api<IamPolicy>("POST", "/iam/policies", body) : api<IamPolicy>("PUT", `/iam/policies/${id}`, body);
    },
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: ["iam"] });
      void navigate({ to: "/iam/policies" as string });
    },
  });
  const del = useMutation({
    mutationFn: () => api("DELETE", `/iam/policies/${id}`),
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: ["iam"] });
      void navigate({ to: "/iam/policies" as string });
    },
  });
  const readOnly = p?.managed;
  return (
    <form
      onSubmit={(e) => {
        e.preventDefault();
        save.mutate();
      }}
      className={cn("flex flex-col", gap)}
    >
      <PageHeader
        crumbs={["IAM", "Policies"]}
        title={isNew ? "New policy" : p?.name ?? "Policy"}
        actions={
          !readOnly && (
            <>
              {!isNew && (
                <Button variant="ghost" onClick={() => confirm(`Delete policy ${p?.name}? It is detached everywhere.`) && del.mutate()}>
                  <Trash2 className="size-3.5" /> Delete
                </Button>
              )}
              <Button type="submit" variant="primary" disabled={!name || !!parseError || save.isPending}>
                {isNew ? "Create policy" : "Save"}
              </Button>
            </>
          )
        }
      />
      {readOnly && (
        <Alert tone="info">
          A managed policy: built in and read-only.{p?.perProject && " Attach it for a project (e.g. Developer:shop); PROJECT below stands for that project."}
        </Alert>
      )}
      {!readOnly && (
        <Panel title="Policy">
          <div className="grid max-w-2xl grid-cols-1 gap-2 sm:grid-cols-2">
            <Field label="Name">
              <Input value={name} onChange={(e) => setName(e.target.value)} placeholder="shop-readers" autoFocus={isNew} />
            </Field>
            <Field label="Description">
              <Input value={description} onChange={(e) => setDescription(e.target.value)} />
            </Field>
          </div>
        </Panel>
      )}
      <Panel title="Document">
        <textarea
          value={doc}
          onChange={(e) => setDoc(e.target.value)}
          readOnly={readOnly}
          spellCheck={false}
          rows={22}
          className="bg-bg border-line-strong focus:border-accent w-full rounded-sm border p-2 font-mono text-xs outline-none"
        />
        {parseError && <p className="text-bad mt-1 text-xs">{parseError}</p>}
        <p className="text-muted mt-1.5 text-xs">
          Actions are <span className="font-mono">service:OperationName</span> (wildcards allowed; see IAM › Policies actions or <span className="font-mono">synctl iam actions</span>).
          Resources: <span className="font-mono">srn:syncloud:project/NAME/env/ENV/service/NAME</span>, <span className="font-mono">…/job/NAME</span>,{" "}
          <span className="font-mono">srn:syncloud:registry/REPO</span>, <span className="font-mono">srn:syncloud:node/ID</span> or <span className="font-mono">*</span>. Conditions:{" "}
          <span className="font-mono">syn:MFAPresent</span>, <span className="font-mono">syn:SourceIp</span>, <span className="font-mono">syn:CredentialType</span>. A Deny always wins.
        </p>
      </Panel>
      {(save.error || del.error) && <Alert>{errText(save.error ?? del.error, "Could not save")}</Alert>}
    </form>
  );
}

function SimulatorPanel() {
  const { data: users = [] } = useIamUsers();
  const { data: roles = [] } = useIamRoles();
  const { data: actions = [] } = useQuery({ queryKey: ["iam", "actions"], queryFn: items<{ action: string }>("/iam/actions") });
  const [principal, setPrincipal] = useState("");
  const [action, setAction] = useState("service:ScaleService");
  const [resource, setResource] = useState("srn:syncloud:project/shop/env/production/service/web");
  const [mfa, setMfa] = useState(false);
  const sim = useMutation({ mutationFn: () => api<{ allowed: boolean; reason: string }>("POST", "/iam/simulate", { principal, action, resource, mfa }) });
  const actionNames = useMemo(() => [...new Set(actions.map((a) => a.action))], [actions]);
  return (
    <Panel title="Policy simulator">
      <form
        onSubmit={(e: FormEvent) => {
          e.preventDefault();
          sim.mutate();
        }}
        className="flex flex-wrap items-end gap-2"
      >
        <Field label="Who">
          <select value={principal} onChange={(e) => setPrincipal(e.target.value)} className={sel}>
            <option value="">choose…</option>
            {users.map((u) => (
              <option key={u.id} value={u.id}>
                {u.kind === "service" ? u.name : u.email}
              </option>
            ))}
            {roles.map((r) => (
              <option key={r.id} value={`role:${r.name}`}>
                role {r.name}
              </option>
            ))}
          </select>
        </Field>
        <Field label="Action">
          <Input list="iam-actions" value={action} onChange={(e) => setAction(e.target.value)} className="w-72 font-mono" />
        </Field>
        <Field label="Resource">
          <Input value={resource} onChange={(e) => setResource(e.target.value)} className="w-[32rem] font-mono" />
        </Field>
        <label className="flex h-8 items-center gap-1.5 text-xs">
          <input type="checkbox" checked={mfa} onChange={(e) => setMfa(e.target.checked)} /> with MFA
        </label>
        <Button type="submit" variant="primary" disabled={!principal || sim.isPending}>
          Simulate
        </Button>
        <datalist id="iam-actions">
          {actionNames.map((a) => (
            <option key={a} value={a} />
          ))}
        </datalist>
      </form>
      {sim.data && (
        <div className={cn("mt-2 flex items-center gap-2 text-xs", sim.data.allowed ? "text-ok" : "text-bad")}>
          {sim.data.allowed ? <ShieldCheck className="size-4" /> : <ShieldX className="size-4" />}
          <span className="font-medium">{sim.data.allowed ? "Allowed" : "Denied"}</span>
          <span className="text-muted">{sim.data.reason}</span>
        </div>
      )}
      {sim.error && <Alert>{errText(sim.error, "Simulation failed")}</Alert>}
    </Panel>
  );
}

// ── audit log ───────────────────────────────────────────────────────────────

interface AuditEvent {
  id: number;
  at: string;
  actor: string;
  actorId: string;
  action: string;
  resource: string;
  ip: string;
  userAgent: string;
  detail: Record<string, unknown> | null;
}

export function AuditPage() {
  const [actor, setActor] = useState("");
  const [action, setAction] = useState("");
  const [text, setText] = useState("");
  const [since, setSince] = useState("24h");
  const [before, setBefore] = useState<number[]>([]);
  const q = new URLSearchParams({ limit: "100", since });
  if (actor) q.set("actor", actor);
  if (action) q.set("action", action);
  if (text) q.set("q", text);
  if (before.length) q.set("before", String(before[before.length - 1]));
  const { data, isLoading, error } = useQuery({ queryKey: ["audit", q.toString()], queryFn: items<AuditEvent>(`/audit?${q}`) });
  const exportQ = new URLSearchParams(q);
  exportQ.delete("limit");
  exportQ.delete("before");
  return (
    <div className={cn("flex flex-col", gap)}>
      <PageHeader
        crumbs={["IAM"]}
        title="Audit log"
        actions={
          <a href={`/api/v1/audit/export?${exportQ}`} download>
            <Button>
              <Download className="size-3.5" /> Export CSV
            </Button>
          </a>
        }
      />
      <Panel>
        <div className="flex flex-wrap items-end gap-2">
          <Field label="Who">
            <Input value={actor} onChange={(e) => setActor(e.target.value)} placeholder="email" className="w-56" />
          </Field>
          <Field label="Action">
            <Input value={action} onChange={(e) => setAction(e.target.value)} placeholder="service:*" className="w-56 font-mono" />
          </Field>
          <Field label="Search">
            <Input value={text} onChange={(e) => setText(e.target.value)} placeholder="resource, IP, detail" className="w-56" />
          </Field>
          <Field label="Since">
            <select value={since} onChange={(e) => setSince(e.target.value)} className={sel}>
              <option value="1h">1 hour</option>
              <option value="24h">24 hours</option>
              <option value="168h">7 days</option>
              <option value="720h">30 days</option>
              <option value="87600h">all</option>
            </select>
          </Field>
        </div>
      </Panel>
      <Forbidden error={error} />
      <Panel flush>
        <DataTable
          rows={data ?? []}
          rowKey={(e) => String(e.id)}
          empty={!isLoading && !error && <EmptyState icon={Shield} title="No events" />}
          columns={[
            { header: "When", cell: (e) => <span className="text-muted whitespace-nowrap">{new Date(e.at).toLocaleString()}</span> },
            { header: "Who", cell: (e) => <span className="whitespace-nowrap">{e.actor || <span className="text-faint">anonymous</span>}</span> },
            {
              header: "Action",
              cell: (e) => (
                <span className="flex items-center gap-1 font-mono text-xs whitespace-nowrap">
                  {e.detail?.denied === true && <StatusBadge tone="bad">denied</StatusBadge>}
                  {e.action}
                </span>
              ),
            },
            { header: "Resource", className: "w-full", cell: (e) => <span className="text-muted font-mono text-xs break-all">{e.resource}</span> },
            {
              header: "Detail",
              cell: (e) => (
                <span className="text-faint block max-w-56 truncate font-mono text-[11px]" title={JSON.stringify(e.detail)}>
                  {e.detail ? JSON.stringify(e.detail) : ""}
                </span>
              ),
            },
            { header: "IP", cell: (e) => <span className="text-muted font-mono text-xs">{e.ip}</span> },
          ]}
        />
      </Panel>
      <div className="flex gap-2">
        {before.length > 0 && <Button onClick={() => setBefore(before.slice(0, -1))}>Newer</Button>}
        {(data?.length ?? 0) === 100 && <Button onClick={() => setBefore([...before, data![data!.length - 1]!.id])}>Older</Button>}
      </div>
    </div>
  );
}

// ── my security ─────────────────────────────────────────────────────────────

interface MyPermissions {
  statements: { policy: string; effect: string; action: string[]; resource: string[] }[];
  mfa: boolean;
  mfaEnabled: boolean;
  requireMfa: boolean;
  credentialType: string;
}

function Section({ title, children }: { title: string; children: ReactNode }) {
  return (
    <Panel title={title}>
      <div className="flex max-w-3xl flex-col gap-2 text-xs">{children}</div>
    </Panel>
  );
}

export function SecurityPage() {
  const qc = useQueryClient();
  const { data: me } = useQuery({ queryKey: ["iam", "me"], queryFn: () => api<MyPermissions>("GET", "/iam/me/permissions") });
  const [enroll, setEnroll] = useState<{ secret: string; uri: string } | null>(null);
  const [code, setCode] = useState("");
  const [current, setCurrent] = useState("");
  const [next, setNext] = useState("");
  const refresh = () => qc.invalidateQueries({ queryKey: ["iam"] });
  const begin = useMutation({ mutationFn: () => api<{ secret: string; uri: string }>("POST", "/iam/mfa"), onSuccess: setEnroll });
  const enable = useMutation({
    mutationFn: () => api("POST", "/iam/mfa/enable", { code }),
    onSuccess: () => {
      setEnroll(null);
      setCode("");
      void refresh();
    },
  });
  const disable = useMutation({ mutationFn: () => api("DELETE", "/iam/mfa", { code }), onSuccess: () => { setCode(""); void refresh(); } });
  const pw = useMutation({ mutationFn: () => api("POST", "/iam/password", { current, new: next }), onSuccess: () => { setCurrent(""); setNext(""); } });
  const settings = useQuery({ queryKey: ["iam", "settings"], queryFn: () => api<{ requireMfa: boolean }>("GET", "/iam/settings"), retry: false });
  const setRequire = useMutation({ mutationFn: (v: boolean) => api("PUT", "/iam/settings", { requireMfa: v }), onSuccess: () => qc.invalidateQueries({ queryKey: ["iam"] }) });
  return (
    <div className={cn("flex flex-col", gap)}>
      <PageHeader crumbs={["IAM"]} title="My security" />
      {me?.requireMfa && !me.mfaEnabled && <Alert tone="warn">Multi-factor authentication is required: set it up below to use the rest of the dashboard.</Alert>}
      <Section title="Multi-factor authentication">
        {me?.mfaEnabled ? (
          <>
            <span>
              <StatusBadge tone="ok">on</StatusBadge> Sign-ins ask for a code from your authenticator app.
              {me.mfa ? " This session was signed in with MFA." : ""}
            </span>
            <div className="flex gap-1.5">
              <Input value={code} onChange={(e) => setCode(e.target.value)} placeholder="current code" className="h-7 w-32" inputMode="numeric" />
              <Button disabled={code.length !== 6} onClick={() => disable.mutate()}>
                Turn off
              </Button>
            </div>
          </>
        ) : enroll ? (
          <>
            <span>Add this account to your authenticator app (Google Authenticator, 1Password, …), then enter the code it shows.</span>
            <span>
              Secret: <span className="bg-bg rounded-sm px-1 font-mono">{enroll.secret}</span>
            </span>
            <span className="text-muted font-mono break-all">{enroll.uri}</span>
            <div className="flex gap-1.5">
              <Input value={code} onChange={(e) => setCode(e.target.value)} placeholder="123456" className="h-7 w-32" inputMode="numeric" autoFocus />
              <Button variant="primary" disabled={code.length !== 6} onClick={() => enable.mutate()}>
                <Check className="size-3.5" /> Turn on
              </Button>
            </div>
          </>
        ) : (
          <>
            <span className="text-muted">Protect sign-ins with a time-based code (TOTP).</span>
            <div>
              <Button variant="primary" onClick={() => begin.mutate()}>
                Set up
              </Button>
            </div>
          </>
        )}
        {(begin.error || enable.error || disable.error) && <Alert>{errText(begin.error ?? enable.error ?? disable.error, "Could not change MFA")}</Alert>}
      </Section>
      <Section title="Password">
        <div className="flex flex-wrap gap-1.5">
          <Input type="password" value={current} onChange={(e) => setCurrent(e.target.value)} placeholder="current password" className="h-7 w-48" autoComplete="current-password" />
          <Input type="password" value={next} onChange={(e) => setNext(e.target.value)} placeholder="new password (12+)" className="h-7 w-48" autoComplete="new-password" />
          <Button disabled={!current || next.length < 12 || pw.isPending} onClick={() => pw.mutate()}>
            Change
          </Button>
        </div>
        {pw.isSuccess && <span className="text-ok">Changed; your other sessions were signed out.</span>}
        {pw.error && <Alert>{errText(pw.error, "Could not change the password")}</Alert>}
      </Section>
      {settings.data && (
        <Section title="Account settings">
          <label className="flex items-center gap-2">
            <input type="checkbox" checked={settings.data.requireMfa} onChange={(e) => setRequire.mutate(e.target.checked)} />
            Require MFA for every person (service accounts use keys)
          </label>
          {setRequire.error && <Alert>{errText(setRequire.error, "Could not change the setting")}</Alert>}
        </Section>
      )}
      <Panel title="What I can do" flush>
        <DataTable
          rows={me?.statements ?? []}
          rowKey={(s) => s.policy + s.effect + s.action.join() + s.resource.join()}
          empty={<EmptyState icon={Shield} title="No policies: only your own keys and MFA" />}
          columns={[
            { header: "Policy", cell: (s) => <span className="font-mono text-xs">{s.policy}</span> },
            { header: "Effect", cell: (s) => <StatusBadge tone={s.effect === "Allow" ? "ok" : "bad"}>{s.effect}</StatusBadge> },
            { header: "Actions", cell: (s) => <span className="font-mono text-xs">{s.action.join(", ")}</span> },
            { header: "Resources", className: "w-full", cell: (s) => <span className="text-muted font-mono text-xs break-all">{s.resource.join(", ")}</span> },
          ]}
        />
      </Panel>
    </div>
  );
}

// ── device login approval (synctl login) ────────────────────────────────────

export function DevicePage() {
  const [code, setCode] = useState(() => new URLSearchParams(window.location.search).get("code") ?? "");
  const approve = useMutation({ mutationFn: () => api("POST", "/auth/device/approve", { userCode: code }) });
  return (
    <div className={cn("flex flex-col", gap)}>
      <PageHeader crumbs={["IAM"]} title="Sign in synctl" />
      <Panel>
        {approve.isSuccess ? (
          <div className="text-ok flex items-center gap-2 text-sm">
            <ShieldCheck className="size-4" /> Approved. synctl is signed in as you for 12 hours; you can close this page.
          </div>
        ) : (
          <form
            onSubmit={(e) => {
              e.preventDefault();
              approve.mutate();
            }}
            className="flex max-w-md flex-col gap-2"
          >
            <p className="text-muted text-xs">Only approve a code you see in your own terminal: whoever runs that synctl gets your permissions.</p>
            <Field label="Code shown by synctl login">
              <Input value={code} onChange={(e) => setCode(e.target.value.toUpperCase())} placeholder="ABCD-EFGH" className="font-mono" autoFocus />
            </Field>
            <div>
              <Button type="submit" variant="primary" disabled={code.length < 9 || approve.isPending}>
                Approve
              </Button>
            </div>
            {approve.error && <Alert>{errText(approve.error, "Could not approve")}</Alert>}
          </form>
        )}
      </Panel>
    </div>
  );
}
