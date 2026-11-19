import { useState } from "react";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import { Link, useNavigate } from "@tanstack/react-router";
import {
  ChevronDown,
  Cloud,
  Globe,
  Landmark,
  Lock,
  Upload,
} from "lucide-react";
import { api } from "@/lib/api";
import { Alert, Button, StatusBadge, Toggle } from "@/ui/controls";
import { ChoiceCards, Segmented } from "@/ui/choice";
import { Select } from "@/ui/select";
import { Field } from "@/ui/controls";
import { CertNames } from "./CertBits";
import { NamesEditor, challengeNames, nameProblem } from "./NamesEditor";
import { ReviewRow, Wizard } from "./Wizard";
import {
  errText,
  invalidateAll,
  useAuthorities,
  useDomains,
  useChallengeServer,
  challengeWay,
  type Certificate,
} from "./lib";

const steps = ["Names", "Authority", "Validation", "Key", "Review"] as const;
type Step = (typeof steps)[number];

const hasWildcard = (ns: string[]) => ns.some((n) => n.startsWith("*."));

/** Ask for a certificate: names, who issues it, how the names are proved, the key. */
export function RequestCertificateWizard() {
  const qc = useQueryClient();
  const navigate = useNavigate();
  const query = new URLSearchParams(window.location.search);
  const { data: domains = [] } = useDomains();
  const { data: server } = useChallengeServer();
  const { data: authorities } = useAuthorities();
  const first = query.get("domain") ?? "";
  const [step, setStep] = useState<Step>("Names");
  const [names, setNames] = useState<string[]>(
    first ? [first, `*.${first}`] : [],
  );
  const [authority, setAuthority] = useState("");
  const [advanced, setAdvanced] = useState(false);
  const [method, setMethod] = useState<
    "auto" | "http-01" | "dns-01" | "self-signed"
  >("auto");
  const [keyType, setKeyType] = useState("ecdsa-p256");
  const [autoRenew, setAutoRenew] = useState(true);
  const [fallback, setFallback] = useState("");

  const acme = (authorities?.items ?? []).filter((a) => a.kind === "acme");
  const origin = (authorities?.items ?? []).filter(
    (a) => a.kind === "cloudflare-origin",
  );
  const def = acme.find((a) => a.default) ?? acme[0];
  const authId = authority || def?.id || "";
  const chosenAuthority = (authorities?.items ?? []).find(
    (a) => a.id === authId,
  );
  const isOrigin = chosenAuthority?.kind === "cloudflare-origin";
  const selfSigned = method === "self-signed";
  const wild = hasWildcard(names);
  const effective = isOrigin
    ? "origin-ca"
    : selfSigned
      ? "self-signed"
      : method === "auto"
        ? wild
          ? "dns-01"
          : "http-01"
        : method;

  // Which names can be proved automatically: their domain has a provider.
  const zoneOf = (n: string) =>
    domains
      .filter(
        (d) =>
          n.replace(/^\*\./, "") === d.name ||
          n.replace(/^\*\./, "").endsWith("." + d.name),
      )
      .sort((a, b) => b.name.length - a.name.length)[0];
  const challenges = challengeNames(names).map((c) => {
    const ways = c.for.map((n) => challengeWay(zoneOf(n), server));
    return {
      ...c,
      auto: !!c.for.length && ways.every((w) => w.auto),
      // Through what, when SynCloud cannot write the name directly: another domain, or its own server.
      via: ways.find((w) => w.via)?.via,
    };
  });
  const allAuto = challenges.length > 0 && challenges.every((c) => c.auto);

  const problems: Record<Step, string> = {
    Names:
      names.length === 0
        ? "Add at least one name"
        : (names.map(nameProblem).find(Boolean) ?? ""),
    Authority: !authId && !selfSigned ? "Choose an authority" : "",
    Validation:
      effective === "http-01" && wild ? "A wildcard needs DNS validation" : "",
    Key: "",
    Review: "",
  };
  const create = useMutation({
    mutationFn: () =>
      api<Certificate>("POST", "/certificates", {
        names,
        authority: selfSigned ? undefined : authId,
        method: isOrigin
          ? undefined
          : effective === "origin-ca"
            ? undefined
            : effective,
        keyType: isOrigin ? undefined : keyType,
        autoRenew: selfSigned ? false : autoRenew,
        fallback: fallback || undefined,
      }),
    onSuccess: (c) => {
      invalidateAll(qc);
      void navigate({ to: `/domains/certificates/${c.id}` as string });
    },
  });
  return (
    <Wizard
      crumbs={["Domains", "SSL certificates"]}
      title="Request a certificate"
      cancelTo="/domains/certificates"
      steps={steps}
      step={step}
      onStep={setStep}
      problems={problems}
      finish={{
        label: "Request certificate",
        pending: create.isPending,
        error:
          create.error &&
          errText(create.error, "Could not request the certificate"),
        onClick: () => create.mutate(),
      }}
    >
      {step === "Names" && (
        <div className="flex flex-col gap-3">
          <p className="text-muted text-xs">
            Up to 100 names in one certificate. Add a wildcard such as{" "}
            <span className="font-mono">*.example.com</span> or{" "}
            <span className="font-mono">*.sub.example.com</span> to cover every
            service below it with one certificate.
          </p>
          <NamesEditor
            value={names}
            onChange={setNames}
            domains={domains}
            preferDomain={first || undefined}
          />
        </div>
      )}
      {step === "Authority" && (
        <div className="flex flex-col gap-3">
          <p className="text-muted text-xs">
            Who issues it. Let's Encrypt is free, trusted by browsers and renews
            by itself.
          </p>
          <ChoiceCards
            label="Certificate authority"
            value={selfSigned ? "" : authId}
            onChange={(v) => {
              setAuthority(v);
              if (selfSigned) setMethod("auto");
            }}
            columns={2}
            options={[
              ...acme.map((a) => ({
                value: a.id,
                title: a.name,
                icon: Landmark,
                aside: a.default ? (
                  <StatusBadge tone="ok">default</StatusBadge>
                ) : a.status === "error" ? (
                  <StatusBadge tone="bad">error</StatusBadge>
                ) : undefined,
                description:
                  a.id === "letsencrypt"
                    ? "Free, automatic, trusted by every browser."
                    : a.id === "letsencrypt-staging"
                      ? "A test CA: its certificates are not trusted."
                      : (a.directoryUrl ?? ""),
              })),
              ...origin.map((a) => ({
                value: a.id,
                title: a.name,
                icon: Cloud,
                aside: (
                  <StatusBadge tone="warn">not publicly trusted</StatusBadge>
                ),
                description:
                  "Cloudflare origin certificate: valid only behind Cloudflare's proxy with SSL mode Full (strict). Long-lived.",
              })),
            ]}
          />
          <div className="flex flex-wrap items-center gap-2 text-xs">
            <Link to={"/domains/certificates/import" as string}>
              <Button>
                <Upload className="size-3.5" /> I already have a certificate
                from a provider
              </Button>
            </Link>
            <Link to={"/domains/authorities/new" as string}>
              <Button variant="ghost">
                Add another authority (ZeroSSL, Google…)
              </Button>
            </Link>
          </div>
          <div>
            <button
              type="button"
              onClick={() => setAdvanced(!advanced)}
              className="text-muted hover:text-fg inline-flex items-center gap-1 text-xs"
            >
              <ChevronDown
                className={advanced ? "size-3.5" : "size-3.5 -rotate-90"}
              />{" "}
              Advanced
            </button>
            {advanced && (
              <div className="pt-2">
                <ChoiceCards
                  label="Advanced"
                  value={selfSigned ? "self-signed" : ""}
                  onChange={(v) =>
                    setMethod(v === "self-signed" ? "self-signed" : "auto")
                  }
                  options={[
                    {
                      value: "self-signed",
                      title: "Self-signed",
                      icon: Lock,
                      aside: (
                        <StatusBadge tone="warn">
                          not trusted by browsers
                        </StatusBadge>
                      ),
                      description:
                        "For private or internal names, or a test. Nobody should be asked to click through a warning on a public site.",
                    },
                  ]}
                />
              </div>
            )}
          </div>
        </div>
      )}
      {step === "Validation" && (
        <div className="flex flex-col gap-3">
          {isOrigin || selfSigned ? (
            <Alert tone="info">
              {isOrigin
                ? "The provider signs an origin certificate for you: no validation step."
                : "A self-signed certificate is made here: nothing to prove."}
            </Alert>
          ) : (
            <>
              <ChoiceCards
                label="Validation"
                value={
                  wild
                    ? "dns-01"
                    : method === "auto"
                      ? "http-01"
                      : (method as "http-01" | "dns-01")
                }
                onChange={(v) => setMethod(v)}
                options={[
                  {
                    value: "http-01",
                    title: "HTTP-01",
                    icon: Globe,
                    disabled: wild,
                    description: wild
                      ? "Not possible for wildcard names."
                      : "The authority fetches a file over port 80. Needs DNS to point at the cluster first.",
                  },
                  {
                    value: "dns-01",
                    title: "DNS-01",
                    icon: Cloud,
                    description: allAuto
                      ? "SynCloud writes the TXT record through your DNS provider, and removes it. Renews by itself."
                      : "A TXT record proves the name. Works behind a proxy and for wildcards.",
                  },
                ]}
              />
              {effective === "dns-01" && (
                <div className="flex flex-col gap-2">
                  <span className="text-muted text-xs">
                    TXT records this needs
                  </span>
                  <ul className="border-line divide-line divide-y rounded-sm border text-xs">
                    {challenges.map((c) => (
                      <li
                        key={c.name}
                        className="flex flex-wrap items-center gap-2 px-2 py-1.5"
                      >
                        <span className="font-mono">{c.name}</span>
                        <span className="text-faint">
                          proves {c.for.join(", ")}
                        </span>
                        <span className="ml-auto">
                          {c.auto ? (
                            <StatusBadge tone="ok">
                              {c.via
                                ? `written by SynCloud via ${c.via}`
                                : "written by SynCloud"}
                            </StatusBadge>
                          ) : (
                            <StatusBadge tone="warn">you add it</StatusBadge>
                          )}
                        </span>
                      </li>
                    ))}
                  </ul>
                  {!allAuto && (
                    <Alert tone="warn">
                      Without a DNS provider on the domain you add the TXT
                      record by hand, and again at every renewal (about every 60
                      days); an alert warns before it expires. Connect
                      Cloudflare, or set up SynCloud's challenge server (Domains
                      → Challenge server) and add one CNAME, to make it
                      automatic.
                    </Alert>
                  )}
                </div>
              )}
            </>
          )}
        </div>
      )}
      {step === "Key" && (
        <div className="flex max-w-xl flex-col gap-3">
          {!isOrigin && !selfSigned && (
            <Field
              label="Key type"
              hint="ECDSA is smaller and faster. RSA is for old clients."
            >
              <Segmented
                label="Key type"
                value={keyType}
                onChange={setKeyType}
                options={[
                  { value: "ecdsa-p256", label: "ECDSA P-256" },
                  { value: "rsa-2048", label: "RSA 2048" },
                  { value: "rsa-3072", label: "RSA 3072" },
                  { value: "rsa-4096", label: "RSA 4096" },
                ]}
              />
            </Field>
          )}
          {selfSigned ? (
            <Alert tone="info">
              A self-signed certificate lasts a year and is made again on
              request.
            </Alert>
          ) : (
            <Toggle
              checked={autoRenew}
              onChange={setAutoRenew}
              label="Renew by itself"
              hint="30 days before it expires. Off: an alert warns you instead."
            />
          )}
          {!isOrigin && !selfSigned && (
            <Field
              label="Fallback authority"
              hint="Tried when the authority fails repeatedly or is rate limited."
            >
              <Select value={fallback} onChange={setFallback}>
                <option value="">The cluster's fallback (if any)</option>
                {acme
                  .filter((a) => a.id !== authId)
                  .map((a) => (
                    <option key={a.id} value={a.id}>
                      {a.name}
                    </option>
                  ))}
              </Select>
            </Field>
          )}
        </div>
      )}
      {step === "Review" && (
        <div>
          <ReviewRow k="Names">
            <CertNames names={names} max={50} />
          </ReviewRow>
          <ReviewRow k="Issued by">
            {selfSigned
              ? "SynCloud (self-signed, not trusted by browsers)"
              : (chosenAuthority?.name ?? authId)}
          </ReviewRow>
          <ReviewRow k="Validation">
            {effective === "dns-01"
              ? allAuto
                ? "DNS-01, automatic (TXT records written through the provider)"
                : "DNS-01, with TXT records you add"
              : effective === "http-01"
                ? "HTTP-01"
                : effective}
          </ReviewRow>
          {!isOrigin && !selfSigned && <ReviewRow k="Key">{keyType}</ReviewRow>}
          <ReviewRow k="Renewal">
            {selfSigned
              ? "made again on request"
              : autoRenew
                ? "by itself"
                : "by hand (alerts)"}
          </ReviewRow>
        </div>
      )}
    </Wizard>
  );
}
