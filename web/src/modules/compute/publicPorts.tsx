import { useEffect, useState } from "react";
import { useQueries, useQuery } from "@tanstack/react-query";
import { api } from "@/lib/api";

/* Public TCP/UDP ports (Phase 26): the live check against the port table
   and the presets of the new-service wizard. */

export interface PortCheck {
  protocol: string;
  port: number;
  state: "free" | "reserved" | "host" | "taken";
  reason?: string;
}

/** Waits until the value stops changing for a moment. */
export function useDebounced<T>(value: T, ms = 300): T {
  const [v, setV] = useState(value);
  useEffect(() => {
    const t = setTimeout(() => setV(value), ms);
    return () => clearTimeout(t);
  }, [value, ms]);
  return v;
}

const validNumber = (p: number) => Number.isInteger(p) && p >= 1 && p <= 65535;

/** The query that checks one port on a project's load balancer. */
export function portCheckQuery(
  project: string,
  protocol: "tcp" | "udp",
  port: number,
  own?: { environment: string; service: string },
) {
  const q = new URLSearchParams({ protocol, port: String(port) });
  if (own) {
    q.set("environment", own.environment);
    q.set("service", own.service);
  }
  return {
    queryKey: ["port-check", project, protocol, port, own?.environment, own?.service],
    queryFn: () =>
      api<PortCheck>("GET", `/projects/${encodeURIComponent(project)}/ports/check?${q}`),
    enabled: validNumber(port) && !!project,
    staleTime: 5_000,
    retry: false,
  };
}

/** Checks a port on the project's load balancer as the user types. */
export function usePortCheck(
  project: string,
  protocol: "tcp" | "udp",
  port: number,
  own?: { environment: string; service: string },
) {
  const settled = useDebounced(port);
  return useQuery(portCheckQuery(project, protocol, settled, own));
}

/**
 * Checks several ports at once and says why the first unusable one can't be
 * used ("" when they all can, or when the checks can't be made, for example
 * without permission: the controller refuses then, and says why).
 */
export function usePortsProblem(
  project: string,
  ports: { name: string; protocol: "tcp" | "udp"; port: number }[],
): string {
  const wanted = ports.filter((p) => validNumber(p.port));
  const key = JSON.stringify(wanted);
  const settled = useDebounced(key);
  const list: typeof wanted = JSON.parse(settled);
  const results = useQueries({
    queries: list.map((p) => portCheckQuery(project, p.protocol, p.port)),
  });
  if (settled !== key || results.some((r) => r.isPending && r.fetchStatus !== "idle")) {
    return wanted.length ? "Checking the public ports…" : "";
  }
  for (const [i, r] of results.entries()) {
    const c = r.data;
    if (!c || c.state === "free") continue;
    const p = list[i]!;
    const where = `${p.name}: ${p.protocol.toUpperCase()} ${p.port}`;
    return c.state === "reserved"
      ? `${where} is not available: ${c.reason}`
      : c.state === "host"
        ? `${where} is in use on the node by ${c.reason}`
        : `${where} is taken by ${c.reason}; choose another port or give the project its own load balancer`;
  }
  return "";
}

/** One line under a port field: free, or why not. */
export function PortCheckHint({
  check,
  port,
}: {
  check: ReturnType<typeof usePortCheck>;
  port: number;
}) {
  if (!(port >= 1 && port <= 65535)) {
    return <span className="text-bad">A port is 1–65535.</span>;
  }
  if (check.isError) return null; // no permission to check: the save says
  const c = check.data;
  if (!c || c.port !== port) return <span>Checking…</span>;
  switch (c.state) {
    case "free":
      return <span className="text-ok">Free on the project's load balancer.</span>;
    case "reserved":
      return <span className="text-bad">Not available: {c.reason}.</span>;
    case "host":
      return (
        <span className="text-bad">
          In use on the node by {c.reason}: Traefik could not listen on it.
        </span>
      );
    case "taken":
      return (
        <span className="text-bad">
          Taken by {c.reason}. Choose another port, or give the project its own
          load balancer (Project › Load balancer).
        </span>
      );
  }
}

/** Whether a check lets the user go on. */
export const portUsable = (c: PortCheck | undefined, port: number) =>
  !c || c.port !== port || c.state === "free";

export interface PresetPort {
  name: string;
  protocol: "tcp" | "udp";
  port: number;
  tls?: boolean;
  proxyProtocol?: boolean;
}

export interface Preset {
  id: string;
  label: string;
  hint: string;
  ports: PresetPort[];
}

/** Common servers that clients reach on their own ports. */
export const presets: Preset[] = [
  {
    id: "mail",
    label: "Mail server",
    hint: "SMTP 25, submission 587, SMTPS 465 and IMAPS 993 (TLS at the load balancer), with the real client IP.",
    ports: [
      { name: "smtp", protocol: "tcp", port: 25, proxyProtocol: true },
      { name: "submission", protocol: "tcp", port: 587, proxyProtocol: true },
      { name: "smtps", protocol: "tcp", port: 465, tls: true, proxyProtocol: true },
      { name: "imaps", protocol: "tcp", port: 993, tls: true, proxyProtocol: true },
    ],
  },
  {
    id: "mqtt",
    label: "MQTT broker",
    hint: "MQTT 1883, and 8883 with TLS at the load balancer.",
    ports: [
      { name: "mqtt", protocol: "tcp", port: 1883 },
      { name: "mqtts", protocol: "tcp", port: 8883, tls: true },
    ],
  },
  {
    id: "minecraft-java",
    label: "Minecraft Java",
    hint: "TCP 25565.",
    ports: [{ name: "game", protocol: "tcp", port: 25565 }],
  },
  {
    id: "minecraft-bedrock",
    label: "Minecraft Bedrock",
    hint: "UDP 19132.",
    ports: [{ name: "game", protocol: "udp", port: 19132 }],
  },
];
