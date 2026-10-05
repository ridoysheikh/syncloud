// Thin client for the controller's public API (§5.1). Errors carry the
// server's stable error code so callers can branch on it.

export class ApiError extends Error {
  constructor(
    readonly status: number,
    readonly code: string,
    message: string,
  ) {
    super(message);
  }
}

export async function api<T>(method: string, path: string, body?: unknown): Promise<T> {
  const res = await fetch(`/api/v1${path}`, {
    method,
    credentials: "same-origin",
    headers: body === undefined ? undefined : { "Content-Type": "application/json" },
    body: body === undefined ? undefined : JSON.stringify(body),
  });
  if (res.status === 204) return undefined as T;
  const data = await res.json().catch(() => null);
  if (!res.ok) {
    const err = data?.error;
    throw new ApiError(res.status, err?.code ?? "unknown", err?.message ?? res.statusText);
  }
  return data as T;
}

export interface SystemStatus {
  version: string;
  setupRequired: boolean;
  baseDomain: string;
}

export interface User {
  id: string;
  email: string;
  name: string;
  isRoot: boolean;
  createdAt: string;
}
