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
}
