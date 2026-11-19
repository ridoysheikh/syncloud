import { useEffect, useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Link, useNavigate } from "@tanstack/react-router";
import { Cloud, Globe, Hand, Lock, Route, Server, Users } from "lucide-react";
import { api } from "@/lib/api";
import { useProjects } from "@/lib/workloads";
import { Alert, Field, Input, StatusBadge, Toggle } from "@/ui/controls";
import { ChipSelect, ChoiceCards } from "@/ui/choice";
import { Select } from "@/ui/select";
import { ReviewRow, Wizard } from "./Wizard";
import {
  errText,
  invalidateAll,
  useAuthorities,
  useChallengeServer,
  useDomains,
  useProviders,
  type Domain,
  type RemoteZone,
} from "./lib";

const steps = [
  "Provider",
  "Domain",
  "Access",
  "Certificates",
  "Review",
] as const;
type Step = (typeof steps)[number];

const nameRE = /^([a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?\.)+[a-z]{2,}$/;

/** Register a domain: with a DNS provider (records written for you) or without (records shown). */
export function AddDomainWizard() {
  const qc = useQueryClient();
  const navigate = useNavigate();
  const query = new URLSearchParams(window.location.search);
  const { data: providers = [] } = useProviders();
  const { data: domains = [] } = useDomains();
  const { data: server } = useChallengeServer();
  const { data: projects = [] } = useProjects();
  const { data: authorities } = useAuthorities();
  const [step, setStep] = useState<Step>("Provider");
  const [provider, setProvider] = useState(query.get("provider") ?? "");
  const [name, setName] = useState(query.get("zone") ?? "");
  const [access, setAccess] = useState<"all" | "chosen">("all");
  const [chosen, setChosen] = useState<string[]>([]);
  const [wildcards, setWildcards] = useState(true);
  const [wildcardCert, setWildcardCert] = useState(false);
  const [authority, setAuthority] = useState("");
  const [challengeZone, setChallengeZone] = useState("");
  const [way, setWay] = useState<"auto" | "builtin" | "zone">("auto");

  const { data: zones = [] } = useQuery({
    queryKey: ["dm", "provider-zones", provider],
    enabled: !!provider,
    queryFn: async () =>
      (
        await api<{ items: RemoteZone[] }>(
          "GET",
          `/dns/providers/${provider}/zones`,
        )
      ).items,
  });
  // A provider that was chosen from a link may need a moment to be listed.
  useEffect(() => {
    if (
      provider &&
      providers.length &&
      !providers.some((p) => p.id === provider)
    )
      setProvider("");
  }, [provider, providers]);

  const n = name.trim().toLowerCase();
  const taken = domains.some((d) => d.name === n);
  const inZones = !provider || zones.some((z) => z.name === n);
  const problems: Record<Step, string> = {
    Provider: "",
    Domain: !nameRE.test(n)
      ? "Enter a domain like example.com"
      : taken
        ? `${n} is already registered`
        : !inZones
          ? `The provider has no zone named ${n}`
          : "",
    Access:
      access === "chosen" && chosen.length === 0
        ? "Choose at least one project, or let all projects use it"
        : "",
    Certificates:
      way === "zone" && !challengeZone
        ? "Choose the domain that answers _acme-challenge, or keep adding the record by hand"
        : "",
    Review: "",
  };
  const create = useMutation({
    mutationFn: () =>
      api<Domain>("POST", "/domains", {
        name: n,
        provider: provider || undefined,
        wildcardPolicy: wildcardCert ? "on" : "off",
        authority: authority || undefined,
        challengeZone:
          way === "zone" && challengeZone ? challengeZone : undefined,
        challengeBuiltin: way === "builtin" ? true : undefined,
        grants:
          access === "all"
            ? undefined
            : chosen.map((p) => ({ project: p, wildcards })),
      }),
    onSuccess: (d) => {
      invalidateAll(qc);
      void navigate({ to: `/domains/${d.name}` as string });
    },
  });
  const prov = providers.find((p) => p.id === provider);
  const free = zones.filter((z) => !z.registered);
  const serverOn = !!server?.enabled;
  const validators = domains.filter(
    (x) => x.mode === "managed" && !x.challengeZone,
  );
  return (
    <Wizard
      crumbs={["Domains"]}
      title="Add a domain"
      cancelTo="/domains"
      steps={steps}
      step={step}
      onStep={setStep}
      problems={problems}
      finish={{
        label: "Add domain",
        pending: create.isPending,
        error:
          create.error && errText(create.error, "Could not add the domain"),
        onClick: () => create.mutate(),
      }}
    >
      {step === "Provider" && (
        <div className="flex flex-col gap-3">
          <p className="text-muted text-xs">
            Where does this domain's DNS live? SynCloud is not a name server: it
            either writes the records through your provider, or shows you which
            records to create.
          </p>
          <ChoiceCards
            label="DNS provider"
            value={provider || "manual"}
            onChange={(v) => setProvider(v === "manual" ? "" : v)}
            columns={2}
            options={[
              ...providers.map((p) => ({
                value: p.id,
                title: p.name,
                icon: Cloud,
                aside: (
                  <StatusBadge tone={p.status === "ok" ? "ok" : "bad"}>
                    {p.status === "ok" ? "connected" : "token refused"}
                  </StatusBadge>
                ),
                description:
                  "SynCloud creates the records a service needs, removes duplicates, and renews wildcard certificates by itself.",
              })),
              {
                value: "manual",
                title: "Manual (any DNS host)",
                icon: Globe,
                description:
                  "You keep the records at your DNS host. SynCloud lists the records to create and checks them live; wildcard certificates ask for a TXT record at each renewal.",
              },
            ]}
          />
          {providers.length === 0 && (
            <Alert tone="info">
              No DNS provider is connected.{" "}
              <Link
                to={"/domains/providers/new" as string}
                className="underline"
              >
                Connect Cloudflare
              </Link>{" "}
              to manage records here, or continue with a manual domain.
            </Alert>
          )}
        </div>
      )}
      {step === "Domain" && (
        <div className="flex max-w-md flex-col gap-2">
          {prov ? (
            <Field
              label={`Zone at ${prov.name}`}
              hint={
                free.length === 0
                  ? "Every zone this token sees is already registered."
                  : "The zones the token can manage."
              }
            >
              <Select
                value={n}
                onChange={setName}
                placeholder="Choose a zone"
                search
              >
                {free.map((z) => (
                  <option key={z.id} value={z.name}>
                    {z.name}
                  </option>
                ))}
              </Select>
            </Field>
          ) : (
            <Field
              label="Domain"
              hint="The registered domain, like example.com. Subdomains are chosen later when a service takes a name."
            >
              <Input
                value={name}
                onChange={(e) => setName(e.target.value)}
                placeholder="example.com"
                className="font-mono"
                spellCheck={false}
                autoFocus
              />
            </Field>
          )}
          {prov && prov.status !== "ok" && (
            <Alert tone="warn">
              The token of {prov.name} is refused: replace it under DNS
              providers first.
            </Alert>
          )}
        </div>
      )}
      {step === "Access" && (
        <div className="flex flex-col gap-3">
          <p className="text-muted text-xs">
            Which projects may put services on this domain and its subdomains?
            Names outside every registered domain stay free for everyone.
          </p>
          <ChoiceCards
            label="Who may use it"
            value={access}
            onChange={setAccess}
            options={[
              {
                value: "all",
                title: "All projects",
                icon: Users,
                description:
                  "Any service of any project may take this domain, a subdomain, or a wildcard under it. You can narrow it later.",
              },
              {
                value: "chosen",
                title: "Chosen projects",
                icon: Lock,
                description: "Only the projects you pick.",
              },
            ]}
          />
          {access === "chosen" && (
            <div className="flex flex-col gap-2">
              <ChipSelect
                label="Projects"
                value={chosen}
                onChange={setChosen}
                options={projects.map((p) => ({
                  value: p.name,
                  label: p.name,
                }))}
                empty="No projects yet."
                mono
              />
              <Toggle
                checked={wildcards}
                onChange={setWildcards}
                label="They may use wildcard hosts"
                hint="*.example.com and *.sub.example.com: one label, with a wildcard certificate."
              />
            </div>
          )}
        </div>
      )}
      {step === "Certificates" && (
        <div className="flex max-w-xl flex-col gap-3">
          <Toggle
            checked={wildcardCert}
            onChange={setWildcardCert}
            label={`Keep one wildcard certificate for ${n || "the domain"} and *.${n || "the domain"}`}
            hint={
              prov
                ? "Issued and renewed by itself with DNS-01: the challenge records are written and removed through the provider."
                : "Manual domain: SynCloud shows the TXT record to create (_acme-challenge) and checks it; it must be added again at every renewal, about every 60 days."
            }
          />
          <div className="flex flex-col gap-2">
            <span className="text-muted text-xs">
              How is _acme-challenge answered? (Wildcard certificates are proved
              with it.)
            </span>
            <ChoiceCards
              label="How _acme-challenge is answered"
              value={way}
              onChange={setWay}
              columns={2}
              options={[
                prov
                  ? {
                      value: "auto",
                      title: "Through the provider",
                      icon: Cloud,
                      description: `SynCloud writes the TXT record at ${prov.name} and removes it. Automatic.`,
                    }
                  : serverOn
                    ? {
                        value: "auto",
                        title: "SynCloud's challenge server",
                        icon: Server,
                        description:
                          "SynCloud answers the challenge itself. You create one CNAME once this domain is added; renewals need nobody.",
                      }
                    : {
                        value: "auto",
                        title: "By hand",
                        icon: Hand,
                        description:
                          "SynCloud shows the TXT record; you add it at every renewal, about every 60 days. Set up the challenge server to avoid that.",
                      },
                ...(prov && serverOn
                  ? [
                      {
                        value: "builtin" as const,
                        title: "SynCloud's challenge server",
                        icon: Server,
                        description:
                          "Answer it with SynCloud's own server instead of the provider: one CNAME, once.",
                      },
                    ]
                  : []),
                {
                  value: "zone" as const,
                  title: "Another domain you own",
                  icon: Route,
                  disabled: validators.length === 0,
                  aside:
                    validators.length === 0 ? (
                      <StatusBadge tone="neutral">
                        needs a domain with a provider
                      </StatusBadge>
                    ) : undefined,
                  description:
                    "Create one CNAME, once: SynCloud writes the TXT in a domain it can write through a provider.",
                },
              ]}
            />
            {!prov && !serverOn && (
              <p className="text-muted text-xs">
                <Link
                  to={"/domains/challenge-server" as string}
                  className="underline"
                >
                  Set up SynCloud's challenge server
                </Link>{" "}
                to renew wildcard certificates for domains without a provider by
                themselves.
              </p>
            )}
            {way === "zone" && (
              <Field
                label="Validation domain"
                hint="You get the CNAME to create once this domain is added."
              >
                <Select
                  value={challengeZone}
                  onChange={setChallengeZone}
                  placeholder="Choose a domain"
                  aria-label="Validation domain"
                >
                  {validators.map((x) => (
                    <option key={x.name} value={x.name}>
                      {x.name}
                    </option>
                  ))}
                </Select>
              </Field>
            )}
          </div>
          <Field
            label="Certificate authority for this domain"
            hint="Let's Encrypt unless you choose another one. Each certificate can still use its own."
          >
            <Select value={authority} onChange={setAuthority}>
              <option value="">The cluster's default</option>
              {(authorities?.items ?? [])
                .filter((a) => a.kind === "acme")
                .map((a) => (
                  <option key={a.id} value={a.id}>
                    {a.name}
                  </option>
                ))}
            </Select>
          </Field>
        </div>
      )}
      {step === "Review" && (
        <div>
          <ReviewRow k="Domain">
            <span className="font-mono">{n}</span>
          </ReviewRow>
          <ReviewRow k="DNS">
            {prov
              ? `${prov.name}: SynCloud writes the records`
              : "Manual: SynCloud shows the records to create"}
          </ReviewRow>
          <ReviewRow k="Usable by">
            {access === "all"
              ? "All projects, wildcards included"
              : `${chosen.join(", ")}${wildcards ? " (wildcards allowed)" : " (no wildcards)"}`}
          </ReviewRow>
          <ReviewRow k="_acme-challenge">
            {way === "zone" && challengeZone
              ? `Delegated by a CNAME to ${challengeZone}`
              : way === "builtin" || (!prov && serverOn)
                ? "SynCloud's challenge server (one CNAME)"
                : prov
                  ? "Written through the provider"
                  : "By hand: a TXT record at each renewal"}
          </ReviewRow>
          <ReviewRow k="Wildcard cert">
            {wildcardCert
              ? `One certificate for ${n} and *.${n}`
              : "Not kept (request one any time)"}
          </ReviewRow>
          <ReviewRow k="Authority">
            {authority
              ? (authorities?.items.find((a) => a.id === authority)?.name ??
                authority)
              : "The cluster's default (Let's Encrypt)"}
          </ReviewRow>
        </div>
      )}
    </Wizard>
  );
}
