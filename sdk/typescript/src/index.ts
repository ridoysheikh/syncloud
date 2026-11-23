// TypeScript SDK for the SynCloud API (§7.1). Works in browsers, Node 20+,
// Deno and Bun (fetch and Web Crypto). Requests are signed like synctl does
// (SYN1-HMAC-SHA256: the secret never travels), or carry a personal access
// token.
//
//   const sc = new SynCloud({ endpoint: "https://203-0-113-10.sslip.io",
//     accessKeyId: process.env.SYNCLOUD_ACCESS_KEY_ID, secretAccessKey: process.env.SYNCLOUD_SECRET_ACCESS_KEY });
//   const { items } = await sc.get("/api/v1/services");
//   await sc.post("/api/v1/projects/shop/environments/production/services/web/scale", { desiredCount: 3 });

export interface Credentials {
  endpoint: string;
  accessKeyId?: string;
  secretAccessKey?: string;
  /** Temporary credentials (synctl login, sts assume-role, Cloud Shell). */
  sessionToken?: string;
  /** A personal access token, used when no access key is given. */
  token?: string;
}

export class SynCloudError extends Error {
  constructor(
    readonly status: number,
    readonly code: string,
    message: string,
  ) {
    super(`${code} (HTTP ${status}): ${message}`);
  }
}

const ALGORITHM = "SYN1-HMAC-SHA256";
const enc = new TextEncoder();

function hex(buf: ArrayBuffer): string {
  return [...new Uint8Array(buf)].map((b) => b.toString(16).padStart(2, "0")).join("");
}

async function sha256(data: Uint8Array): Promise<string> {
  return hex(await crypto.subtle.digest("SHA-256", data as BufferSource));
}

async function hmac(secret: string, msg: string): Promise<string> {
  const key = await crypto.subtle.importKey("raw", enc.encode(secret), { name: "HMAC", hash: "SHA-256" }, false, ["sign"]);
  return hex(await crypto.subtle.sign("HMAC", key, enc.encode(msg)));
}

/** Go's url.QueryEscape: spaces as +, the rest percent-encoded like encodeURIComponent plus !'()*. */
function queryEscape(s: string): string {
  return encodeURIComponent(s)
    .replace(/[!'()*]/g, (c) => "%" + c.charCodeAt(0).toString(16).toUpperCase())
    .replace(/%20/g, "+");
}

/** Sorted keys and values, as the server canonicalizes them. */
export function canonicalQuery(search: string): string {
  const params = new URLSearchParams(search);
  const keys = [...new Set([...params.keys()])].sort();
  const parts: string[] = [];
  for (const k of keys) {
    for (const v of params.getAll(k).sort()) parts.push(`${queryEscape(k)}=${queryEscape(v)}`);
  }
  return parts.join("&");
}

function synDate(d: Date): string {
  return d.toISOString().replace(/[-:]/g, "").replace(/\.\d{3}/, "");
}

/** Signing headers for one request (exported for custom transports). */
export async function sign(method: string, url: URL, body: Uint8Array, keyId: string, secret: string, now = new Date()): Promise<Record<string, string>> {
  const date = synDate(now);
  const bodyHash = await sha256(body);
  const sts = [ALGORITHM, date, method.toUpperCase(), url.pathname, canonicalQuery(url.search), url.host.toLowerCase(), bodyHash].join("\n");
  return {
    "X-Syn-Date": date,
    "X-Syn-Content-Sha256": bodyHash,
    Authorization: `${ALGORITHM} Credential=${keyId}, Signature=${await hmac(secret, sts)}`,
  };
}

export class SynCloud {
  constructor(private readonly creds: Credentials) {
    if (!/^https?:\/\//.test(creds.endpoint)) throw new Error("endpoint must be http(s)://host[:port]");
  }

  async request<T = unknown>(method: string, path: string, body?: unknown): Promise<T> {
    const url = new URL(path, this.creds.endpoint);
    const raw = body === undefined ? new Uint8Array() : enc.encode(JSON.stringify(body));
    const headers: Record<string, string> = { Accept: "application/json" };
    if (body !== undefined) headers["Content-Type"] = "application/json";
    if (this.creds.accessKeyId && this.creds.secretAccessKey) {
      Object.assign(headers, await sign(method, url, raw, this.creds.accessKeyId, this.creds.secretAccessKey));
      if (this.creds.sessionToken) headers["X-Syncloud-Session-Token"] = this.creds.sessionToken;
    } else if (this.creds.token) {
      headers.Authorization = `Bearer ${this.creds.token}`;
    }
    const res = await fetch(url, { method, headers, body: body === undefined ? undefined : (raw as BufferSource) });
    if (res.status === 204) return undefined as T;
    const data = await res.json().catch(() => null);
    if (!res.ok) throw new SynCloudError(res.status, data?.error?.code ?? "unknown", data?.error?.message ?? res.statusText);
    return data as T;
  }

  get<T = unknown>(path: string) {
    return this.request<T>("GET", path);
  }
  post<T = unknown>(path: string, body?: unknown) {
    return this.request<T>("POST", path, body ?? {});
  }
  put<T = unknown>(path: string, body: unknown) {
    return this.request<T>("PUT", path, body);
  }
  delete<T = unknown>(path: string, body?: unknown) {
    return this.request<T>("DELETE", path, body);
  }

  /** Domains, DNS records and certificates (what an app that manages its own names uses). */
  get domains() {
    const e = encodeURIComponent;
    return {
      list: () => this.get<{ items: Domain[] }>("/api/v1/domains"),
      get: (name: string) => this.get<Domain>(`/api/v1/domains/${e(name)}`),
      records: (zone: string, q: { type?: string; name?: string; q?: string } = {}) =>
        this.get<{ items: DnsRecord[] }>(`/api/v1/domains/${e(zone)}/records?${new URLSearchParams(q as Record<string, string>)}`),
      /** `name` may be a label, the full name or "@". An exact duplicate, or a CNAME beside other records, is a 409. */
      createRecord: (zone: string, r: DnsRecordInput) => this.post<DnsRecord>(`/api/v1/domains/${e(zone)}/records`, r),
      updateRecord: (zone: string, id: string, r: DnsRecordInput) => this.put<DnsRecord>(`/api/v1/domains/${e(zone)}/records/${e(id)}`, r),
      deleteRecord: (zone: string, id: string) => this.delete<void>(`/api/v1/domains/${e(zone)}/records/${e(id)}`),
      /** Makes a name reach the cluster, with no duplicates. `plan: true` only shows what would change. */
      point: (zone: string, body: { name: string; targets?: string[]; project?: string }, opt: { plan?: boolean; replaceConflicts?: boolean } = {}) =>
        this.post<PointResult>(`/api/v1/domains/${e(zone)}/records/point?${opt.plan ? "plan=true&" : ""}${opt.replaceConflicts ? "replace=conflicts" : ""}`, body),
      /** Where the _acme-challenge TXT record of a name goes (written at the name, delegated by a CNAME, or by hand), and whether the delegating CNAME is in place. A wildcard is written `*.name`. */
      challenge: (zone: string, name?: string) => this.get<ChallengeRoute>(`/api/v1/domains/${e(zone)}/challenge${name ? `?name=${e(name)}` : ""}`),
      /** Creates the CNAME that delegates a name's _acme-challenge to the domain's validation domain (the domain needs a provider; otherwise the answer shows the CNAME to create by hand). */
      delegateChallenge: (zone: string, name?: string, opt: { replaceConflicts?: boolean } = {}) =>
        this.post<ChallengeRoute>(
          `/api/v1/domains/${e(zone)}/challenge/delegation?${new URLSearchParams({ ...(name ? { name } : {}), ...(opt.replaceConflicts ? { replace: "conflicts" } : {}) })}`,
        ),
      /** SynCloud's own ACME challenge DNS server: its zone (`_acme-challenge.<ip>.sslip.io`), whether it listens, and the domains it answers for. */
      challengeServer: () => this.get<ChallengeServer>("/api/v1/domains/challenge-server"),
      /** Turns the server on or off in one step: nothing to create at any DNS host (sslip.io delegates the zone to the controller). `ip` overrides the detected address and is kept when it changes. */
      setChallengeServer: (enabled: boolean, opt: { ip?: string } = {}) => this.put<ChallengeServer>("/api/v1/domains/challenge-server", { enabled, ...opt }),
      lookup: (name: string, type = "A", via?: "public" | "authoritative") =>
        this.get<{ answers: DnsAnswer[] }>(`/api/v1/dns/lookup?${new URLSearchParams({ name, type, ...(via ? { via } : {}) })}`),
    };
  }

  get certificates() {
    const e = encodeURIComponent;
    return {
      list: (q: { status?: string; zone?: string; q?: string } = {}) =>
        this.get<{ items: CertificateInfo[] }>(`/api/v1/certificates?${new URLSearchParams(q as Record<string, string>)}`),
      get: (id: string) => this.get<CertificateInfo>(`/api/v1/certificates/${e(id)}`),
      /** Wildcards (`*.example.com`, `*.sub.example.com`) are validated with DNS; a manual order lists the TXT records to create in `order.challenges`. */
      request: (body: { names: string[]; authority?: string; method?: "http-01" | "dns-01"; keyType?: string; autoRenew?: boolean }) =>
        this.post<CertificateInfo>("/api/v1/certificates", body),
      verify: (id: string) => this.post<CertificateInfo>(`/api/v1/certificates/${e(id)}/verify`),
      renew: (id: string) => this.post<void>(`/api/v1/certificates/${e(id)}/renew`),
      revoke: (id: string) => this.post<CertificateInfo>(`/api/v1/certificates/${e(id)}/revoke`),
      delete: (id: string, force = false) => this.delete<void>(`/api/v1/certificates/${e(id)}${force ? "?force=true" : ""}`),
      import: (body: { chain?: string; key?: string; passphrase?: string; pkcs12?: string; password?: string; replace?: string }) =>
        this.post<{ certificate: CertificateInfo; warnings: string[] }>("/api/v1/certificates/import", body),
      events: (id: string) => this.get<{ items: { at: string; kind: string; detail: string; actor: string }[] }>(`/api/v1/certificates/${e(id)}/events`),
      /** The chain and the PRIVATE KEY: needs the certificate:ExportCertificate permission (nobody has it until it is granted). */
      export: (id: string) => this.get<{ certificate: string; privateKey: string }>(`/api/v1/certificates/${e(id)}/bundle`),
    };
  }
}

export interface Domain {
  name: string;
  mode: "managed" | "manual";
  provider: string;
  nameServers: string[];
  wildcardPolicy: "on" | "off";
  /** The domain whose provider holds this domain's _acme-challenge records (a CNAME delegates them there). */
  challengeZone?: string;
  /** Answered by SynCloud's own challenge server although the domain has a provider. */
  challengeBuiltin?: boolean;
  grants: { project: string; suffix: string; wildcards: boolean }[];
  use: { services: number; certificates: number };
}

export interface ChallengeServer {
  enabled: boolean;
  /** `_acme-challenge.<ip>.sslip.io` ("" when off). */
  zone: string;
  /** `<ip>.sslip.io`: where sslip.io delegates the zone. */
  ns: string;
  ip: string;
  listen: string;
  listening: boolean;
  error?: string;
  records: number;
  /** The zone is `_acme-challenge.<ip>.sslip.io`, which sslip.io delegates to the controller (false: an older zone under one of your domains). */
  sslip: boolean;
  domains: string[];
}

/** How a name's DNS-01 challenge is answered. */
export interface ChallengeRoute {
  /** `_acme-challenge.<host>`: what the certificate authority looks up. */
  name: string;
  host: string;
  zone?: string;
  /** direct: the TXT is written at the name; delegated: the name is a CNAME to `target` in a domain with a provider; builtin: a CNAME to SynCloud's own challenge server; manual: you add the TXT. */
  mode: "direct" | "delegated" | "builtin" | "manual";
  auto: boolean;
  target?: string;
  targetZone?: string;
  cname?: { name: string; target: string; present: boolean; seen?: string[]; managed: boolean };
  note?: string;
}

export interface DnsRecordInput {
  type: "A" | "AAAA" | "CNAME" | "TXT" | "MX" | "SRV" | "CAA" | "NS";
  name: string;
  content: string;
  ttl?: number;
  proxied?: boolean;
  priority?: number;
}

export interface DnsRecord extends DnsRecordInput {
  id: string;
  /** Created by SynCloud. */
  managed: boolean;
}

export interface PointResult {
  plan: { steps: { action: "create" | "keep" | "update" | "delete"; reason: string; record: DnsRecord; needsConfirm?: boolean }[] };
  applied?: { Created: number; Updated: number; Deleted: number };
  /** A domain without a provider: the records to create by hand. */
  manual?: DnsRecord[];
}

export interface DnsAnswer {
  server: string;
  kind: "public" | "authoritative";
  rcode: string;
  values: string[];
  ttl?: number;
  error?: string;
}

export interface CertificateInfo {
  id: string;
  names: string[];
  status: string;
  source: "acme" | "imported" | "origin-ca" | "self-signed";
  method: string;
  authority: string;
  keyType: string;
  autoRenew: boolean;
  notAfter: string;
  daysLeft: number;
  wildcard: boolean;
  usedBy: string[];
  lastError: string;
  order?: { state: string; challenges: { name: string; value: string; auto: boolean; present: boolean; mode?: "direct" | "delegated" | "builtin" | "manual"; target?: string; cnameOk?: boolean }[] };
}
