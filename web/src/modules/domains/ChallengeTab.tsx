import { useState } from "react";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import {
  ArrowRight,
  Check,
  Cloud,
  Globe,
  Hand,
  RefreshCw,
  Server,
  Sparkles,
} from "lucide-react";
import { api } from "@/lib/api";
import { Panel } from "@/ui/Panel";
import { CopyBox } from "@/ui/CopyBox";
import { Alert, Button, Field, Input, StatusBadge } from "@/ui/controls";
import { ChoiceCards } from "@/ui/choice";
import { Select } from "@/ui/select";
import { confirmAction } from "@/ui/dialogs";
import { cn, gap } from "@/ui/cn";
import { Propagation } from "./Propagation";
import {
  type ChallengeMode,
  type ChallengeRoute,
  type ChallengeServer,
  type Domain,
  challengeSummary,
  errText,
  invalidateAll,
  useChallenge,
  useChallengeServer,
  useDomains,
} from "./lib";
import { TurnOnChallengeServer } from "./ChallengeServerButton";

type Way = "provider" | "builtin" | "zone" | "hand";

/** The way a domain answers _acme-challenge, from what the domain and the server say. */
export function wayOf(
  d: Pick<Domain, "mode" | "challengeZone" | "challengeBuiltin">,
  server?: Pick<ChallengeServer, "enabled">,
): Way {
  if (d.challengeZone) return "zone";
  if (server?.enabled && (d.challengeBuiltin || d.mode !== "managed"))
    return "builtin";
  return d.mode === "managed" ? "provider" : "hand";
}

const modeTitle: Record<ChallengeMode, string> = {
  direct: "Through the DNS provider",
  delegated: "Delegated to another domain",
  builtin: "SynCloud's challenge server",
  manual: "By hand",
};

/** The ways a challenge is answered, drawn as the path a certificate authority follows. */
function HowItWorks({
  mode,
  name,
  target,
}: {
  mode: ChallengeMode;
  name: string;
  target?: string;
}) {
  const step = (label: string, value: string, tone?: "accent") => (
    <div
      className={cn(
        "bg-raised border-line min-w-0 flex-1 rounded-sm border px-2 py-1.5",
        tone === "accent" && "border-line-accent",
      )}
    >
      <div className="text-faint text-[11px]">{label}</div>
      <div className="truncate font-mono text-xs" title={value}>
        {value}
      </div>
    </div>
  );
  const arrow = (
    <ArrowRight
      className="text-faint size-3.5 shrink-0 self-center"
      aria-hidden
    />
  );
  const viaCname = (mode === "delegated" || mode === "builtin") && !!target;
  return (
    <div
      className="flex flex-col gap-1.5"
      aria-label="How the authority reaches the record"
    >
      <div className="flex flex-wrap items-stretch gap-1.5 sm:flex-nowrap">
        {step("The authority asks", `TXT ${name}`)}
        {viaCname && (
          <>
            {arrow}
            {step("CNAME (set once)", target)}
          </>
        )}
        {arrow}
        {step(
          mode === "manual"
            ? "You add"
            : mode === "builtin"
              ? "SynCloud's server answers"
              : "SynCloud writes",
          viaCname ? `TXT ${target}` : `TXT ${name}`,
          "accent",
        )}
      </div>
      <p className="text-faint text-xs">
        {mode === "direct" &&
          "The domain's provider takes the record: it exists only while the order runs, and is removed after."}
        {mode === "delegated" &&
          "The authority follows the CNAME and finds the record in the validation domain, which SynCloud writes through its provider. Names and their wildcards share one CNAME."}
        {mode === "builtin" &&
          "The authority follows the CNAME to SynCloud's own challenge server, which answers with the value of the open order. Names and their wildcards share one CNAME."}
        {mode === "manual" &&
          "SynCloud shows the record and waits until public resolvers show it, so every renewal needs a person."}
      </p>
    </div>
  );
}

/** What the CNAME that delegates a name looks like at public resolvers, and how to make it. */
function CnameCard({
  route,
  zone,
  name,
}: {
  route: ChallengeRoute;
  zone: string;
  name: string;
}) {
  const qc = useQueryClient();
  const c = route.cname;
  const make = useMutation({
    mutationFn: (replace: boolean) =>
      api<ChallengeRoute>(
        "POST",
        `/domains/${encodeURIComponent(zone)}/challenge/delegation?${new URLSearchParams({ ...(name ? { name } : {}), ...(replace ? { replace: "conflicts" } : {}) })}`,
      ),
    onSuccess: () => invalidateAll(qc),
  });
  if (!c) return null;
  const conflict =
    make.error && /confirmation|other records/i.test(errText(make.error, ""));
  return (
    <div className="border-line flex flex-col gap-2 rounded-sm border p-2">
      <div className="flex flex-wrap items-center gap-2 text-xs">
        <b>CNAME</b>
        <StatusBadge tone={c.present ? "ok" : c.seen?.length ? "bad" : "warn"}>
          {c.present
            ? "in place"
            : c.seen?.length
              ? "points elsewhere"
              : c.managed
                ? "not visible yet"
                : "not created yet"}
        </StatusBadge>
        {!c.present && c.seen && c.seen.length > 0 && (
          <span className="text-faint font-mono">now: {c.seen.join(", ")}</span>
        )}
      </div>
      <div className="grid grid-cols-[4rem_1fr] items-center gap-2 text-xs">
        <span className="text-muted">Type</span>
        <span className="font-mono">CNAME</span>
        <span className="text-muted">Name</span>
        <CopyBox value={c.name} />
        <span className="text-muted">Points to</span>
        <CopyBox value={c.target} />
      </div>
      {!c.present &&
        (c.managed ? (
          <div className="flex flex-wrap items-center gap-2">
            <Button
              variant="primary"
              disabled={make.isPending}
              onClick={() => make.mutate(false)}
            >
              <Cloud className="size-3.5" />{" "}
              {make.isPending ? "Creating…" : "Create the CNAME for me"}
            </Button>
            <span className="text-faint text-xs">
              It is created in this domain's DNS provider and kept: one record,
              for good.
            </span>
          </div>
        ) : (
          <p className="text-muted text-xs">
            This domain has no provider, so create this one record at your DNS
            host. It is the only thing you ever add: every certificate and
            renewal after it needs nobody.
          </p>
        ))}
      {conflict && (
        <Alert tone="warn">
          <div className="flex flex-col gap-1.5">
            <span>{errText(make.error, "")}</span>
            <div>
              <Button
                onClick={async () =>
                  (await confirmAction(
                    `Replace the records on ${c.name} with this CNAME? A name that is a CNAME cannot hold other records.`,
                  )) && make.mutate(true)
                }
              >
                Replace them
              </Button>
            </div>
          </div>
        </Alert>
      )}
      {make.error && !conflict && (
        <Alert>{errText(make.error, "Could not create the CNAME")}</Alert>
      )}
      <Propagation
        name={c.name}
        values={[c.target]}
        type="CNAME"
        title="CNAME seen by resolvers"
      />
    </div>
  );
}

/** Check one name: where its challenge is answered and what is still to do. */
function Inspect({ d }: { d: Domain }) {
  const [text, setText] = useState("");
  const name = text.trim().toLowerCase();
  const bad = name !== "" && !(name === d.name || name.endsWith(`.${d.name}`));
  const {
    data: route,
    isFetching,
    error,
    refetch,
  } = useChallenge(d.name, bad ? "" : name);
  return (
    <Panel
      title="Check a name"
      actions={
        <Button onClick={() => void refetch()} disabled={isFetching}>
          <RefreshCw className={cn("size-3.5", isFetching && "animate-spin")} />{" "}
          Check again
        </Button>
      }
    >
      <div className="flex flex-col gap-3">
        <Field
          label="Host or wildcard"
          hint={`Leave empty for ${d.name} itself. A wildcard like *.shop.${d.name} is proved at _acme-challenge.shop.${d.name}.`}
        >
          <Input
            value={text}
            onChange={(e) => setText(e.target.value)}
            placeholder={`*.${d.name}`}
            className="font-mono"
            spellCheck={false}
            aria-label="Host to check"
          />
        </Field>
        {bad && (
          <Alert tone="warn">
            {name} is not in {d.name}.
          </Alert>
        )}
        {error && <Alert>{errText(error, "Could not check the name")}</Alert>}
        {route && !bad && (
          <div className="flex flex-col gap-3">
            <div className="flex flex-wrap items-center gap-2 text-xs">
              <span className="text-muted">Challenge name</span>
              <span className="font-mono">{route.name}</span>
              <StatusBadge tone={route.auto ? "ok" : "warn"}>
                {route.auto ? "SynCloud writes it" : "you add it"}
              </StatusBadge>
              <StatusBadge tone="neutral">{modeTitle[route.mode]}</StatusBadge>
            </div>
            <HowItWorks
              mode={route.mode}
              name={route.name}
              target={route.target}
            />
            {(route.mode === "delegated" || route.mode === "builtin") && (
              <CnameCard route={route} zone={d.name} name={name} />
            )}
            {route.note && <Alert tone="info">{route.note}</Alert>}
          </div>
        )}
      </div>
    </Panel>
  );
}

/** The _acme-challenge name of a domain: how certificate authorities are answered, and the one-time CNAME. */
export function ChallengeTab({ d }: { d: Domain }) {
  const qc = useQueryClient();
  const { data: domains = [] } = useDomains();
  const { data: server } = useChallengeServer();
  const external = domains.filter(
    (x) => x.name !== d.name && x.mode === "managed" && !x.challengeZone,
  );
  const current = wayOf(d, server);
  const [way, setWay] = useState<Way>(current);
  const [pick, setPick] = useState(d.challengeZone ?? external[0]?.name ?? "");
  const save = useMutation({
    mutationFn: (body: Record<string, unknown>) =>
      api<Domain>("PUT", `/domains/${encodeURIComponent(d.name)}`, body),
    onSuccess: () => invalidateAll(qc),
  });
  const summary = challengeSummary(d, server);
  const serverOn = !!server?.enabled;
  const apply = () =>
    save.mutate(
      way === "zone"
        ? { challengeZone: pick, challengeBuiltin: false }
        : way === "builtin"
          ? { challengeBuiltin: true, challengeZone: "" }
          : { challengeBuiltin: false, challengeZone: "" },
    );
  const dirty =
    way !== current || (way === "zone" && pick !== (d.challengeZone ?? ""));
  return (
    <div className={cn("flex flex-col", gap)}>
      <Alert tone="info">
        <div className="flex flex-col gap-0.5">
          <b>
            <span className="font-mono">_acme-challenge.{d.name}</span> is how a
            certificate authority checks you own {d.name}.
          </b>
          <span>
            Wildcard certificates (<span className="font-mono">*.{d.name}</span>
            ) can only be proved this way, with a TXT record. Right now it is
            answered <b>{summary.label}</b>: {summary.hint}.
          </span>
        </div>
      </Alert>
      <div className={cn("grid grid-cols-1 lg:grid-cols-2", gap)}>
        <Panel title="How it is answered">
          <div className="flex flex-col gap-3">
            <ChoiceCards
              label="How _acme-challenge is answered"
              value={way}
              onChange={setWay}
              columns={2}
              options={[
                {
                  value: "builtin",
                  title: "SynCloud's challenge server",
                  icon: Server,
                  description:
                    "SynCloud answers the challenge itself. One CNAME, once; no DNS provider needed.",
                  disabled: !serverOn,
                  aside: !serverOn ? (
                    <StatusBadge tone="warn">off</StatusBadge>
                  ) : undefined,
                },
                {
                  value: "provider",
                  title: "Through the DNS provider",
                  icon: Cloud,
                  description:
                    "SynCloud writes the TXT record at this domain's provider and removes it. Nothing to set up.",
                  disabled: d.mode !== "managed",
                  aside:
                    d.mode !== "managed" ? (
                      <StatusBadge tone="neutral">no provider</StatusBadge>
                    ) : undefined,
                },
                {
                  value: "zone",
                  title: "Another domain you own",
                  icon: Globe,
                  description:
                    "One CNAME, once. SynCloud writes the TXT in a domain it can write through a provider.",
                  disabled: external.length === 0 && !d.challengeZone,
                  aside:
                    external.length === 0 && !d.challengeZone ? (
                      <StatusBadge tone="neutral">
                        needs a domain with a provider
                      </StatusBadge>
                    ) : undefined,
                },
                {
                  value: "hand",
                  title: "By hand",
                  icon: Hand,
                  description:
                    "SynCloud shows the TXT record; you add it at every renewal, about every 60 days.",
                  disabled: current !== "hand",
                  aside:
                    current !== "hand" ? (
                      <StatusBadge tone="neutral">
                        only without the others
                      </StatusBadge>
                    ) : undefined,
                },
              ]}
            />
            {!serverOn && (
              <div className="flex flex-col gap-1.5">
                <p className="text-muted text-xs">
                  SynCloud's challenge server renews wildcard certificates for
                  domains without a provider by themselves: one click, then one
                  CNAME for this domain, once.
                </p>
                <TurnOnChallengeServer />
              </div>
            )}
            {way === "zone" && (
              <Field
                label="Validation domain"
                hint="A registered domain with a DNS provider. Any domain you own works; it does not have to be related."
              >
                <Select
                  value={pick}
                  onChange={setPick}
                  placeholder="Choose a domain"
                  aria-label="Validation domain"
                >
                  {(d.challengeZone &&
                  !external.some((x) => x.name === d.challengeZone)
                    ? [{ name: d.challengeZone }, ...external]
                    : external
                  ).map((x) => (
                    <option key={x.name} value={x.name}>
                      {x.name}
                    </option>
                  ))}
                </Select>
              </Field>
            )}
            {save.error && (
              <Alert>{errText(save.error, "Could not save")}</Alert>
            )}
            <div className="flex items-center gap-2">
              <Button
                variant="primary"
                disabled={save.isPending || !dirty || (way === "zone" && !pick)}
                onClick={apply}
              >
                <Check className="size-3.5" /> Save
              </Button>
              {save.isSuccess && !save.isPending && !dirty && (
                <span className="text-ok text-xs">Saved.</span>
              )}
            </div>
            {current === "zone" && way !== "zone" && (
              <p className="text-faint text-xs">
                Changing it removes the CNAME SynCloud made. A CNAME you made
                yourself stays: remove it at your DNS host.
              </p>
            )}
          </div>
        </Panel>
        <Panel
          title={
            <span className="flex items-center gap-1.5">
              <Sparkles className="text-accent size-3.5" /> What each way needs
            </span>
          }
        >
          <dl className="text-muted grid grid-cols-[7.5rem_1fr] gap-x-3 gap-y-2 text-xs">
            <dt className="text-fg font-medium">SynCloud's server</dt>
            <dd>
              The default for a domain without a provider. Turn the server on
              once for the whole cluster (one click; port 53 open), then one
              CNAME per domain. Works at any registrar and keeps your DNS
              credentials out of SynCloud.
            </dd>
            <dt className="text-fg font-medium">Provider</dt>
            <dd>
              The domain is connected to Cloudflare. Fully automatic, nothing
              for you to do. Needs a token that can edit DNS.
            </dd>
            <dt className="text-fg font-medium">Another domain</dt>
            <dd>
              The optional external way: delegate to a domain you already keep
              at a provider, so you need no port 53 of your own.
            </dd>
            <dt className="text-fg font-medium">By hand</dt>
            <dd>
              The fallback. Works everywhere; SynCloud waits for each TXT record
              and reminds you before a certificate runs out.
            </dd>
          </dl>
        </Panel>
      </div>
      <Inspect d={d} />
    </div>
  );
}
