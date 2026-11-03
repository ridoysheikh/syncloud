import { useMemo } from "react";
import { useQuery } from "@tanstack/react-query";
import { api } from "@/lib/api";
import type { PickOption } from "@/ui/PickList";

/* Addresses the firewall pages offer in place of typing (the address book). */

interface AddressBook {
  yours: string;
  items: { value: string; kind: Kind; detail: string }[];
}

export type Kind = "you" | "server" | "ssh" | "policy" | "attempt" | "blocked";

const groups: Record<Kind, string> = {
  you: "You",
  server: "This server",
  ssh: "Trusted for SSH",
  policy: "In firewall policies",
  attempt: "Recent unauthorized attempts",
  blocked: "Blocked now",
};

/** The IPv4/IPv6 address or CIDR a rule or block takes. */
export function validateSource(v: string): string | null {
  const [addr = "", bits, extra] = v.split("/");
  if (extra !== undefined || v === "") return "Use an IP or a CIDR like 203.0.113.0/24";
  const v4 = /^(\d{1,3})\.(\d{1,3})\.(\d{1,3})\.(\d{1,3})$/.exec(addr);
  if (v4) {
    if (v4.slice(1).some((o) => Number(o) > 255)) return `${addr} is not an IPv4 address`;
    if (bits !== undefined && !(/^\d{1,2}$/.test(bits) && Number(bits) <= 32))
      return "An IPv4 prefix is 0–32";
    return null;
  }
  if (addr.includes(":") && /^[0-9a-fA-F:.]+$/.test(addr) && !addr.includes(":::")) {
    if (bits !== undefined && !(/^\d{1,3}$/.test(bits) && Number(bits) <= 128))
      return "An IPv6 prefix is 0–128";
    return null;
  }
  return `${v} is not an IP or a CIDR`;
}

/** The same, also accepting the cluster keyword rules understand. */
export function validateRuleSource(v: string): string | null {
  return v === "cluster" ? null : validateSource(v);
}

/** The address book, as options for a picker. `cluster` adds the keyword rules take. */
export function useAddressOptions({
  cluster = false,
  exclude = [],
}: { cluster?: boolean; exclude?: Kind[] } = {}) {
  const q = useQuery({
    queryKey: ["firewall", "address-book"],
    queryFn: () => api<AddressBook>("GET", "/firewall/address-book"),
    staleTime: 30_000,
    retry: false,
  });
  const options = useMemo<PickOption[]>(() => {
    const out: PickOption[] = [];
    if (cluster)
      out.push({
        value: "cluster",
        label: "Every node of this cluster",
        hint: "cluster",
        group: "Cluster",
      });
    for (const i of q.data?.items ?? [])
      if (!exclude.includes(i.kind))
        out.push({ value: i.value, hint: i.detail, group: groups[i.kind] });
    return out;
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [q.data, cluster, exclude.join()]);
  return { options, yours: q.data?.yours ?? "", isLoading: q.isLoading };
}

/** Ports rules usually open. */
export const portOptions: PickOption[] = [
  ["22", "SSH"],
  ["80", "HTTP"],
  ["443", "HTTPS"],
  ["53", "DNS"],
  ["123", "NTP"],
  ["3306", "MySQL / MariaDB"],
  ["5432", "PostgreSQL"],
  ["6379", "Valkey / Redis"],
  ["27017", "MongoDB"],
  ["9200", "Elasticsearch"],
  ["8080", "HTTP (alternate)"],
  ["8443", "HTTPS (alternate)"],
  ["51820", "WireGuard"],
].map(([value, hint]) => ({ value: value!, hint }));

/** A port or a range like 8000-8100. */
export function validatePorts(v: string): string | null {
  const m = /^(\d{1,5})(?:-(\d{1,5}))?$/.exec(v);
  if (!m) return "Use a port like 22 or a range like 8000-8100";
  const lo = Number(m[1]);
  const hi = m[2] === undefined ? lo : Number(m[2]);
  if (lo < 1 || hi > 65535) return "Ports are 1–65535";
  if (lo > hi) return "The range starts above its end";
  return null;
}
