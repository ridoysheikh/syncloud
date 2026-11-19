import { useState } from "react";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import { Link, useNavigate, useParams } from "@tanstack/react-router";
import {
  Cloud,
  Landmark,
  Pencil,
  Plus,
  ShieldCheck,
  Star,
  Trash2,
  Zap,
} from "lucide-react";
import { api } from "@/lib/api";
import { PageHeader } from "@/ui/PageHeader";
import { Panel } from "@/ui/Panel";
import { DataTable } from "@/ui/DataTable";
import { EmptyState } from "@/ui/EmptyState";
import {
  Alert,
  Button,
  Field,
  IconButton,
  Input,
  StatusBadge,
  Toggle,
} from "@/ui/controls";
import { ChoiceCards } from "@/ui/choice";
import { Select } from "@/ui/select";
import { confirmAction } from "@/ui/dialogs";
import { cn, gap } from "@/ui/cn";
import {
  errText,
  invalidateAll,
  useAuthorities,
  useProviders,
  type Authority,
} from "./lib";

/* Domains › Authorities (Phase 28): who issues certificates. Let's Encrypt is
   built in and the default; others are added with their directory and, for
   most providers, an external account binding (a key id and an HMAC key). */

const tone = { ok: "ok", new: "neutral", error: "bad" } as const;

export function AuthoritiesPage() {
  const qc = useQueryClient();
  const { data, isLoading } = useAuthorities();
  const items = data?.items ?? [];
  const act = useMutation({
    mutationFn: ({ id, body }: { id: string; body: Record<string, unknown> }) =>
      api("PUT", `/certificate-authorities/${id}`, body),
    onSuccess: () => invalidateAll(qc),
  });
  const test = useMutation({
    mutationFn: (id: string) =>
      api<Authority>("POST", `/certificate-authorities/${id}/test`),
    onSuccess: () => invalidateAll(qc),
  });
  const remove = useMutation({
    mutationFn: (id: string) => api("DELETE", `/certificate-authorities/${id}`),
    onSuccess: () => invalidateAll(qc),
  });
  const fallback = items.find((a) => a.fallback);
  return (
    <div className={cn("flex flex-col", gap)}>
      <PageHeader
        crumbs={["Domains", "SSL certificates"]}
        title="Certificate authorities"
        actions={
          <Link to={"/domains/authorities/new" as string}>
            <Button variant="primary">
              <Plus className="size-3.5" /> Add authority
            </Button>
          </Link>
        }
      />
      <p className="text-muted text-xs">
        Let's Encrypt issues certificates unless you choose another authority
        for a domain or a certificate. Most other authorities come from
        providers and want an external account binding. A <b>fallback</b>{" "}
        authority is tried when the first one keeps failing or is rate limited,
        so a renewal does not wait on one CA.
      </p>
      {fallback && (
        <Alert tone="info">
          Fallback: <b>{fallback.name}</b> takes over when an authority fails
          repeatedly or is rate limited.
        </Alert>
      )}
      <Panel title="Authorities" flush>
        <DataTable
          rows={items}
          rowKey={(a) => a.id}
          empty={
            !isLoading && (
              <EmptyState icon={Landmark} title="No authorities">
                Let's Encrypt is built in.
              </EmptyState>
            )
          }
          columns={[
            {
              header: "Name",
              cell: (a) => (
                <span className="flex items-center gap-1.5">
                  {a.kind === "cloudflare-origin" ? (
                    <Cloud className="text-muted size-3.5" />
                  ) : a.trusted ? (
                    <ShieldCheck className="text-ok size-3.5" />
                  ) : (
                    <Landmark className="text-muted size-3.5" />
                  )}
                  <span className="font-medium">{a.name}</span>
                  {a.default && <StatusBadge tone="ok">default</StatusBadge>}
                  {a.fallback && (
                    <StatusBadge tone="info">fallback</StatusBadge>
                  )}
                </span>
              ),
            },
            {
              header: "Kind",
              cell: (a) =>
                a.kind === "cloudflare-origin"
                  ? "Cloudflare origin CA"
                  : a.builtin
                    ? "ACME (built in)"
                    : "ACME",
            },
            {
              header: "Directory",
              className: "max-w-[22rem]",
              cell: (a) =>
                a.kind === "cloudflare-origin" ? (
                  <span className="text-faint">Cloudflare API</span>
                ) : (
                  <span
                    className="block truncate font-mono"
                    title={a.directoryUrl}
                  >
                    {a.directoryUrl}
                  </span>
                ),
            },
            {
              header: "Binding",
              cell: (a) =>
                a.hasEab ? (
                  <span className="font-mono">{a.eabKid}</span>
                ) : (
                  <span className="text-faint">none</span>
                ),
            },
            {
              header: "Trusted",
              cell: (a) =>
                a.trusted ? (
                  "by browsers"
                ) : (
                  <span className="text-warn">not by browsers</span>
                ),
            },
            {
              header: "Status",
              cell: (a) => (
                <span className="flex items-center gap-1.5">
                  <StatusBadge tone={tone[a.status]}>
                    {a.status === "new" ? "not tested" : a.status}
                  </StatusBadge>
                  {a.lastError && (
                    <span
                      className="text-bad block max-w-xs truncate"
                      title={a.lastError}
                    >
                      {a.lastError}
                    </span>
                  )}
                </span>
              ),
            },
            { header: "Certificates", cell: (a) => a.certificates },
            {
              header: "",
              cell: (a) => (
                <span className="flex items-center justify-end gap-0.5">
                  {a.kind === "acme" && (
                    <IconButton
                      label="Test: register the account"
                      onClick={() => test.mutate(a.id)}
                    >
                      <Zap
                        className={cn(
                          "size-3.5",
                          test.isPending &&
                            test.variables === a.id &&
                            "text-warn animate-pulse",
                        )}
                      />
                    </IconButton>
                  )}
                  {a.kind === "acme" && !a.default && (
                    <IconButton
                      label="Make it the default"
                      onClick={() =>
                        act.mutate({ id: a.id, body: { default: true } })
                      }
                    >
                      <Star className="size-3.5" />
                    </IconButton>
                  )}
                  {a.kind === "acme" && (
                    <button
                      className="text-muted hover:text-fg hover:bg-hover rounded-btn h-7 px-1.5 text-xs"
                      onClick={() =>
                        act.mutate({
                          id: a.id,
                          body: { fallback: !a.fallback },
                        })
                      }
                      title={
                        a.fallback
                          ? "Stop using it as the fallback"
                          : "Use it as the fallback"
                      }
                    >
                      {a.fallback ? "no fallback" : "fallback"}
                    </button>
                  )}
                  {!a.builtin && (
                    <>
                      <Link to={`/domains/authorities/${a.id}` as string}>
                        <IconButton label="Edit">
                          <Pencil className="size-3.5" />
                        </IconButton>
                      </Link>
                      <IconButton
                        label="Remove"
                        onClick={async () =>
                          (await confirmAction(
                            `Remove ${a.name}? Certificates that use it must be moved first.`,
                          )) && remove.mutate(a.id)
                        }
                      >
                        <Trash2 className="size-3.5" />
                      </IconButton>
                    </>
                  )}
                </span>
              ),
            },
          ]}
        />
      </Panel>
      {(act.error || test.error || remove.error) && (
        <Alert>
          {errText(
            act.error ?? test.error ?? remove.error,
            "The change failed",
          )}
        </Alert>
      )}
      {test.data && (
        <Alert tone={test.data.status === "ok" ? "info" : "bad"}>
          {test.data.name}:{" "}
          {test.data.status === "ok"
            ? "the account is registered"
            : test.data.lastError}
        </Alert>
      )}
    </div>
  );
}

type Kind = "zerossl" | "google" | "sslcom" | "buypass" | "custom" | "origin";

/** Add or change an authority. */
export function AuthorityFormPage() {
  const { id } = useParams({ strict: false }) as { id?: string };
  const qc = useQueryClient();
  const navigate = useNavigate();
  const { data } = useAuthorities();
  const { data: providers = [] } = useProviders();
  const editing = id ? data?.items.find((a) => a.id === id) : undefined;
  const presets = data?.presets ?? [];
  const [kind, setKind] = useState<Kind>("custom");
  const [name, setName] = useState("");
  const [dir, setDir] = useState("");
  const [email, setEmail] = useState("");
  const [kid, setKid] = useState("");
  const [hmac, setHmac] = useState("");
  const [provider, setProvider] = useState("");
  const [useEab, setUseEab] = useState(false);
  const [loaded, setLoaded] = useState("");
  if (editing && loaded !== editing.id) {
    setLoaded(editing.id);
    setName(editing.name);
    setDir(editing.directoryUrl ?? "");
    setEmail(editing.email ?? "");
    setKid(editing.eabKid ?? "");
    setProvider(editing.providerId ?? "");
  }
  const preset = presets.find((p) => p.id === kind);
  const pick = (k: Kind) => {
    setKind(k);
    const p = presets.find((x) => x.id === k);
    if (!editing) {
      if (p && p.id !== "custom") {
        setName(p.name);
        setDir(p.directoryUrl);
      } else if (k === "origin") {
        setName("Cloudflare origin");
      }
    }
  };
  const save = useMutation({
    mutationFn: () => {
      const body = {
        name,
        directoryUrl: dir,
        email,
        eabKid: kid,
        eabHmac: hmac,
        preset: kind === "origin" ? undefined : kind,
        kind: kind === "origin" ? "cloudflare-origin" : "acme",
        provider,
      };
      return editing
        ? api<Authority>("PUT", `/certificate-authorities/${editing.id}`, {
            name,
            directoryUrl: dir,
            email,
            ...(hmac ? { eabKid: kid, eabHmac: hmac } : {}),
          })
        : api<Authority>("POST", "/certificate-authorities", {
            ...body,
            ...(needsEab ? {} : { eabKid: "", eabHmac: "" }),
          });
    },
    onSuccess: () => {
      invalidateAll(qc);
      void navigate({ to: "/domains/authorities" as string });
    },
  });
  const origin = editing
    ? editing.kind === "cloudflare-origin"
    : kind === "origin";
  const needsEab = !origin && (preset?.needsEab || editing?.hasEab || useEab);
  const problem = !name.trim()
    ? "Name it"
    : origin
      ? !editing && !provider
        ? "Choose the DNS provider (Cloudflare) that signs origin certificates"
        : ""
      : !/^https?:\/\//.test(dir)
        ? "The directory URL starts with https://"
        : preset?.needsEab && !editing && (!kid || !hmac)
          ? `${preset.name} needs the external account binding: a key id and an HMAC key`
          : needsEab && !editing && (!kid || !hmac)
            ? "Give both the key id and the HMAC key"
            : editing && hmac && !kid
              ? "Give the key id with the new HMAC key"
              : "";
  return (
    <div className={cn("flex flex-col", gap)}>
      <PageHeader
        crumbs={["Domains", "Authorities"]}
        title={editing ? `Edit ${editing.name}` : "Add a certificate authority"}
      />
      {!editing && (
        <Panel title="Which authority">
          <ChoiceCards
            label="Authority"
            value={kind}
            onChange={pick}
            columns={3}
            options={[
              ...presets
                .filter((p) => p.id !== "custom")
                .map((p) => ({
                  value: p.id as Kind,
                  title: p.name,
                  icon: Landmark,
                  aside: p.needsEab ? (
                    <StatusBadge tone="info">needs keys</StatusBadge>
                  ) : undefined,
                  description: p.hint,
                })),
              {
                value: "custom" as Kind,
                title: "Other ACME authority",
                icon: Landmark,
                description:
                  "Any CA that speaks ACME: its directory URL, and its key id and HMAC key if it asks for them.",
              },
              {
                value: "origin" as Kind,
                title: "Cloudflare origin CA",
                icon: Cloud,
                aside: (
                  <StatusBadge tone="warn">not publicly trusted</StatusBadge>
                ),
                description:
                  "Long-lived certificates for hosts behind Cloudflare's proxy (SSL mode Full strict).",
              },
            ]}
          />
        </Panel>
      )}
      <Panel title="Details">
        <div className="grid max-w-2xl grid-cols-1 gap-3">
          <Field label="Name">
            <Input value={name} onChange={(e) => setName(e.target.value)} />
          </Field>
          {origin ? (
            !editing && (
              <Field
                label="DNS provider"
                hint="The Cloudflare connection whose token has SSL and Certificates: Edit."
              >
                <Select
                  value={provider}
                  onChange={setProvider}
                  placeholder="Choose a provider"
                >
                  {providers.map((p) => (
                    <option key={p.id} value={p.id}>
                      {p.name}
                    </option>
                  ))}
                </Select>
              </Field>
            )
          ) : (
            <>
              <Field
                label="Directory URL"
                hint="The ACME directory the authority publishes."
              >
                <Input
                  value={dir}
                  onChange={(e) => setDir(e.target.value)}
                  className="font-mono"
                  placeholder="https://acme.example.net/directory"
                  spellCheck={false}
                />
              </Field>
              <Field
                label="Account email"
                hint="Where the authority writes about expiry and problems. Optional."
              >
                <Input
                  value={email}
                  onChange={(e) => setEmail(e.target.value)}
                  type="email"
                />
              </Field>
              {needsEab && (
                <div className="grid grid-cols-1 gap-3 sm:grid-cols-2">
                  <Field label="Key id (EAB)" hint={preset?.hint}>
                    <Input
                      value={kid}
                      onChange={(e) => setKid(e.target.value)}
                      className="font-mono"
                      autoComplete="off"
                      spellCheck={false}
                    />
                  </Field>
                  <Field
                    label="HMAC key (EAB)"
                    hint={
                      editing?.hasEab
                        ? "Set. Type a new one to replace it."
                        : "Base64, as the provider shows it. Sealed, never shown again."
                    }
                  >
                    <Input
                      type="password"
                      value={hmac}
                      onChange={(e) => setHmac(e.target.value)}
                      className="font-mono"
                      autoComplete="off"
                      placeholder={editing?.hasEab ? "unchanged" : ""}
                    />
                  </Field>
                </div>
              )}
              {!preset?.needsEab && !editing?.hasEab && (
                <Toggle
                  checked={useEab}
                  onChange={setUseEab}
                  label="This authority asks for an external account binding (a key id and an HMAC key)"
                />
              )}
            </>
          )}
          {save.error && <Alert>{errText(save.error, "Could not save")}</Alert>}
          {problem && <Alert tone="warn">{problem}</Alert>}
          <div className="flex gap-1.5">
            <Button
              variant="primary"
              disabled={!!problem || save.isPending}
              onClick={() => save.mutate()}
            >
              {save.isPending ? "Saving…" : editing ? "Save" : "Add authority"}
            </Button>
            <Link to={"/domains/authorities" as string}>
              <Button variant="ghost">Cancel</Button>
            </Link>
          </div>
        </div>
      </Panel>
    </div>
  );
}
