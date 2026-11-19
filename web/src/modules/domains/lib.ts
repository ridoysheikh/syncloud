import { useEffect } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { ApiError, api } from "@/lib/api";
import { subscribe } from "@/lib/stream";

/* Domains, DNS and certificates (Phase 28): types and queries shared by the
   Domains pages and the domain picker. */

export const errText = (e: unknown, fallback: string) =>
  e instanceof ApiError ? e.message : fallback;

export interface Grant {
  project: string;
  suffix?: string;
  wildcards: boolean;
}

export interface Domain {
  name: string;
  mode: "managed" | "manual";
  providerId?: string;
  provider?: string;
  nameServers?: string[];
  wildcardPolicy: "on" | "off";
  authorityId?: string;
  /** The domain whose provider holds this domain's _acme-challenge records (a CNAME delegates them there). */
  challengeZone?: string;
  /** Answered by SynCloud's own challenge server although the domain has a provider. */
  challengeBuiltin?: boolean;
  grants: Grant[];
  createdAt: string;
  use: { services: number; certificates: number };
}

export interface DnsProvider {
  id: string;
  name: string;
  type: "cloudflare";
  status: "ok" | "error";
  lastError?: string;
  domains: number;
  createdAt: string;
}

export interface RemoteZone {
  id: string;
  name: string;
  status?: string;
  nameServers?: string[];
  registered: boolean;
}

export interface DnsRecord {
  id: string;
  type: string;
  name: string;
  content: string;
  ttl: number;
  proxied: boolean;
  priority?: number;
  comment?: string;
  managed: boolean;
}

export interface PlanStep {
  action: "create" | "keep" | "update" | "delete";
  reason: "missing" | "ok" | "duplicate" | "stale" | "conflict" | "settings";
  record: DnsRecord;
  detail?: string;
  needsConfirm?: boolean;
}

export interface PointResult {
  plan: { steps: PlanStep[] };
  applied?: {
    Created: number;
    Updated: number;
    Deleted: number;
    Skipped?: PlanStep[];
  };
  manual?: DnsRecord[];
}

export interface Challenge {
  name: string;
  value: string;
  names: string[];
  wildcard: boolean;
  auto: boolean;
  present: boolean;
  /** direct: the TXT is written at the name; delegated: the name is a CNAME to target; manual: you add it. */
  mode: ChallengeMode;
  target?: string;
  cnameOk?: boolean;
  cnameManaged?: boolean;
}

export type ChallengeMode = "direct" | "delegated" | "builtin" | "manual";

/** Where the _acme-challenge record of a name goes (GET /domains/{zone}/challenge). */
export interface ChallengeRoute {
  name: string;
  host: string;
  zone?: string;
  mode: ChallengeMode;
  auto: boolean;
  target?: string;
  targetZone?: string;
  cname?: {
    name: string;
    target: string;
    present: boolean;
    seen?: string[];
    managed: boolean;
  };
  note?: string;
}

export interface Order {
  state: "awaiting_dns" | "verifying" | "issuing";
  manual: boolean;
  challenges: Challenge[];
  createdAt: string;
  expiresAt?: string;
  lastCheckedAt?: string;
  lastError?: string;
}

export interface Certificate {
  id: string;
  host: string;
  names: string[];
  wildcard: boolean;
  issuer: "acme" | "self-signed";
  status:
    | "valid"
    | "pending"
    | "failed"
    | "rate_limited"
    | "self_signed"
    | "awaiting_dns"
    | "verifying"
    | "revoked";
  source: "acme" | "imported" | "origin-ca" | "self-signed";
  method: string;
  managed: boolean;
  zone?: string;
  authorityId: string;
  authority: string;
  fallbackId?: string;
  issuerName?: string;
  keyType?: string;
  autoRenew: boolean;
  notBefore?: string;
  notAfter: string;
  daysLeft: number;
  serial?: string;
  fingerprint?: string;
  revokedAt?: string;
  alertDays: number[];
  pinnedHosts: string[];
  trusted: boolean;
  usedBy: string[];
  lastError: string;
  failures: number;
  nextAttemptAt: string | null;
  order?: Order;
  createdAt?: string;
  updatedAt: string;
}

export interface Authority {
  id: string;
  name: string;
  kind: "acme" | "cloudflare-origin";
  preset?: string;
  directoryUrl?: string;
  email?: string;
  eabKid?: string;
  hasEab: boolean;
  keyType?: string;
  providerId?: string;
  status: "ok" | "new" | "error";
  lastError?: string;
  builtin: boolean;
  trusted: boolean;
  default: boolean;
  fallback: boolean;
  certificates: number;
}

export interface AuthorityPreset {
  id: string;
  name: string;
  directoryUrl: string;
  needsEab: boolean;
  hint: string;
}

export interface CertEvent {
  id: number;
  at: string;
  kind: string;
  detail: string;
  actor: string;
}

export interface CertPlan {
  state: "covered" | "issuing" | "automatic" | "needs_certificate";
  certificateId?: string;
  names?: string[];
  validUntil?: string;
  note: string;
}

export const domainsKey = ["dm", "domains"] as const;
export const certsKey = ["dm", "certificates"] as const;

export function useDomains() {
  return useQuery({
    queryKey: domainsKey,
    queryFn: async () =>
      (await api<{ items: Domain[] }>("GET", "/domains")).items,
  });
}

export function useDomain(name: string) {
  return useQuery({
    queryKey: ["dm", "domain", name],
    queryFn: () => api<Domain>("GET", `/domains/${encodeURIComponent(name)}`),
  });
}

/** How a name's _acme-challenge is answered, with what public resolvers show of a delegating CNAME. */
export function useChallenge(zone: string, name: string) {
  return useQuery({
    queryKey: ["dm", "challenge", zone, name],
    queryFn: () =>
      api<ChallengeRoute>(
        "GET",
        `/domains/${encodeURIComponent(zone)}/challenge${name ? `?name=${encodeURIComponent(name)}` : ""}`,
      ),
    refetchInterval: 20_000,
    retry: false,
  });
}

/** SynCloud's own ACME challenge DNS server (acme-dns style). */
export interface ChallengeServer {
  enabled: boolean;
  zone: string;
  ns: string;
  ip: string;
  listen: string;
  listening: boolean;
  error?: string;
  records: number;
  needed: {
    type: string;
    name: string;
    content: string;
    zone?: string;
    managed: boolean;
  }[];
  domains: string[];
}

export function useChallengeServer() {
  return useQuery({
    queryKey: ["dm", "challenge-server"],
    queryFn: () => api<ChallengeServer>("GET", "/domains/challenge-server"),
  });
}

export function useProviders() {
  return useQuery({
    queryKey: ["dm", "providers"],
    queryFn: async () =>
      (await api<{ items: DnsProvider[] }>("GET", "/dns/providers")).items,
  });
}

export function useAuthorities() {
  return useQuery({
    queryKey: ["dm", "authorities"],
    queryFn: () =>
      api<{ items: Authority[]; presets: AuthorityPreset[] }>(
        "GET",
        "/certificate-authorities",
      ),
  });
}

/** Certificates, kept live: the stream carries each change. */
export function useCertificates(params: { zone?: string } = {}) {
  const qc = useQueryClient();
  useEffect(
    () =>
      subscribe(
        "certificate.updated",
        () => void qc.invalidateQueries({ queryKey: certsKey }),
      ),
    [qc],
  );
  const q = new URLSearchParams();
  if (params.zone) q.set("zone", params.zone);
  return useQuery({
    queryKey: [...certsKey, params.zone ?? ""],
    queryFn: async () =>
      (await api<{ items: Certificate[] }>("GET", `/certificates?${q}`)).items,
    refetchInterval: 30_000,
  });
}

export function useCertificate(id: string) {
  const qc = useQueryClient();
  useEffect(
    () =>
      subscribe(
        "certificate.updated",
        () =>
          void qc.invalidateQueries({ queryKey: ["dm", "certificate", id] }),
      ),
    [qc, id],
  );
  return useQuery({
    queryKey: ["dm", "certificate", id],
    queryFn: () =>
      api<Certificate>("GET", `/certificates/${encodeURIComponent(id)}`),
    refetchInterval: (q) => (q.state.data?.order ? 10_000 : 60_000),
  });
}

export function invalidateAll(qc: ReturnType<typeof useQueryClient>) {
  void qc.invalidateQueries({ queryKey: ["dm"] });
}

// ── presentation ────────────────────────────────────────────────────────────

export const statusTone = {
  valid: "ok",
  pending: "info",
  failed: "bad",
  rate_limited: "warn",
  self_signed: "neutral",
  awaiting_dns: "warn",
  verifying: "info",
  revoked: "bad",
} as const;

export const statusLabel: Record<Certificate["status"], string> = {
  valid: "valid",
  pending: "requesting",
  failed: "failed",
  rate_limited: "rate limited",
  self_signed: "self-signed",
  awaiting_dns: "waiting for DNS",
  verifying: "verifying",
  revoked: "revoked",
};

export const sourceLabel: Record<Certificate["source"], string> = {
  acme: "ACME",
  imported: "Imported",
  "origin-ca": "Origin CA",
  "self-signed": "Self-signed",
};

export const methodLabel: Record<string, string> = {
  "http-01": "HTTP-01",
  "dns-01": "DNS-01",
  import: "Imported",
  "origin-ca": "Origin CA",
  "self-signed": "Self-signed",
};

/** "in 12 days" / "expired 3 days ago". */
export function expiry(
  c: Pick<Certificate, "notAfter" | "daysLeft" | "status">,
): string {
  if (
    !c.notAfter ||
    c.notAfter.startsWith("0001") ||
    c.notAfter.startsWith("1970")
  )
    return "-";
  const d = c.daysLeft;
  if (d < 0) return `expired ${-d} day${d === -1 ? "" : "s"} ago`;
  if (d === 0) return "today";
  return `in ${d} day${d === 1 ? "" : "s"}`;
}

export const expiryTone = (
  c: Pick<Certificate, "daysLeft" | "status">,
): "ok" | "warn" | "bad" | undefined =>
  c.status === "revoked" || c.daysLeft < 0
    ? "bad"
    : c.daysLeft < 14
      ? "warn"
      : c.daysLeft < 90
        ? undefined
        : "ok";

export const dateOnly = (iso?: string) =>
  iso && !iso.startsWith("0001") && !iso.startsWith("1970")
    ? iso.slice(0, 10)
    : "-";

/** One line on how a domain answers _acme-challenge, from what the domain itself says.
 *  `server` is SynCloud's own challenge server: it answers for a domain without a provider on its own. */
export function challengeSummary(
  d: Pick<Domain, "mode" | "provider" | "challengeZone" | "challengeBuiltin">,
  server?: Pick<ChallengeServer, "enabled">,
): { label: string; hint: string } {
  if (d.challengeZone)
    return {
      label: `via ${d.challengeZone}`,
      hint: "a CNAME delegates _acme-challenge there; renewals need nobody",
    };
  if (server?.enabled && (d.challengeBuiltin || d.mode !== "managed"))
    return {
      label: "by SynCloud",
      hint: "one CNAME to SynCloud's own challenge server; renewals need nobody",
    };
  if (d.mode === "managed")
    return {
      label: "by the provider",
      hint: "SynCloud writes the TXT record and removes it",
    };
  return {
    label: "by hand",
    hint: "set up SynCloud's challenge server, or add a TXT record at each renewal",
  };
}

/** Whether SynCloud answers a name's challenge by itself, and through what (for a domain, or a name outside every domain). */
export function challengeWay(
  zone: Pick<Domain, "mode" | "challengeZone" | "challengeBuiltin"> | undefined,
  server?: Pick<ChallengeServer, "enabled">,
): { auto: boolean; via?: string } {
  if (zone?.challengeZone) return { auto: true, via: zone.challengeZone };
  if (
    server?.enabled &&
    (!zone || zone.challengeBuiltin || zone.mode !== "managed")
  )
    return { auto: true, via: "SynCloud's challenge server" };
  return { auto: zone?.mode === "managed" };
}
