import { useMemo } from "react";
import { useQuery } from "@tanstack/react-query";
import { api } from "@/lib/api";
import { AWAITING_BUILD, useServices } from "@/lib/workloads";
import { since } from "@/lib/nodes";
import { Field } from "@/ui/controls";
import { PickOne, type PickOption } from "@/ui/PickList";
import { groupImages, type Image } from "./images";

/* An image reference chosen instead of typed: the name from what runs now,
   the private registry and common Docker Hub images, then a tag from what the
   registry (or the services running it) has. Anything else goes through
   "Custom value…". */

const REGISTRY = "@registry/";

const popular: [string, string][] = [
  ["nginx", "web server"],
  ["caddy", "web server"],
  ["httpd", "Apache web server"],
  ["traefik", "reverse proxy"],
  ["node", "Node.js"],
  ["python", "Python"],
  ["golang", "Go"],
  ["php", "PHP"],
  ["postgres", "PostgreSQL"],
  ["mysql", "MySQL"],
  ["mariadb", "MariaDB"],
  ["mongo", "MongoDB"],
  ["redis", "Redis"],
  ["valkey/valkey", "Valkey"],
  ["rabbitmq", "message broker"],
  ["minio/minio", "S3-compatible storage"],
  ["grafana/grafana", "dashboards"],
  ["busybox", "tiny utilities"],
];

/** Splits `name:tag`; a digest reference or a tag-less name has no tag. */
export function splitImage(ref: string): { name: string; tag: string } {
  if (ref.includes("@sha256:")) return { name: ref, tag: "" };
  const colon = ref.lastIndexOf(":");
  if (colon > 0 && !ref.slice(colon).includes("/"))
    return { name: ref.slice(0, colon), tag: ref.slice(colon + 1) };
  return { name: ref, tag: "" };
}

const validateName = (v: string) =>
  /\s/.test(v) || v === ""
    ? "An image name has no spaces, like ghcr.io/acme/api"
    : null;
const validateTag = (v: string) =>
  /^[A-Za-z0-9_][A-Za-z0-9_.-]{0,127}$/.test(v)
    ? null
    : "A tag is letters, digits, _ . - (up to 128)";

/** Repositories of the private registry (empty when it is off or unreadable). */
function useRegistryRepos() {
  return useQuery({
    queryKey: ["registry", "repos"],
    queryFn: async () =>
      (
        await api<{ items: { name: string; tags: number }[] }>(
          "GET",
          "/registry/repositories",
        )
      ).items,
    retry: false,
    staleTime: 30_000,
  });
}

/** Tags of a private repository, newest first. */
function useRegistryTags(repo: string) {
  return useQuery({
    queryKey: ["registry", "images", repo],
    queryFn: async () =>
      (
        await api<{ items: Image[] }>(
          "GET",
          `/registry/images?repository=${encodeURIComponent(repo)}`,
        )
      ).items,
    enabled: !!repo,
    retry: false,
    staleTime: 30_000,
  });
}

export function ImagePicker({
  value,
  onChange,
}: {
  value: string;
  onChange: (image: string) => void;
}) {
  const { name, tag } = splitImage(value);
  const repos = useRegistryRepos().data;
  const services = useServices().data;

  // Images services run, with the tags they run them at.
  const running = useMemo(() => {
    const byName = new Map<string, Set<string>>();
    for (const s of services ?? []) {
      const img = s.spec.image;
      if (!img || img === AWAITING_BUILD) continue;
      const p = splitImage(img);
      if (!byName.has(p.name)) byName.set(p.name, new Set());
      if (p.tag) byName.get(p.name)!.add(p.tag);
    }
    return byName;
  }, [services]);

  const nameOptions = useMemo<PickOption[]>(() => {
    const seen = new Set<string>();
    const out: PickOption[] = [];
    const add = (o: PickOption) => {
      if (!seen.has(o.value)) {
        seen.add(o.value);
        out.push(o);
      }
    };
    for (const n of running.keys())
      add({ value: n, group: "Running now", hint: "used by a service" });
    for (const r of repos ?? [])
      add({
        value: REGISTRY + r.name,
        group: "Private registry",
        hint: `${r.tags} ${r.tags === 1 ? "tag" : "tags"}`,
      });
    for (const [n, hint] of popular) add({ value: n, group: "Docker Hub", hint });
    return out;
  }, [running, repos]);

  const repo = name.startsWith(REGISTRY) ? name.slice(REGISTRY.length) : "";
  const regTags = useRegistryTags(repo).data;
  const tagOptions = useMemo<PickOption[]>(() => {
    const out: PickOption[] = [];
    const seen = new Set<string>();
    const add = (o: PickOption) => {
      if (!seen.has(o.value)) {
        seen.add(o.value);
        out.push(o);
      }
    };
    if (repo) {
      for (const g of groupImages((regTags ?? []).filter((i) => !i.cache))) {
        const pushed = g.pushedAt ? `pushed ${since(g.pushedAt)}` : "";
        for (const t of g.tags)
          add({ value: t, group: "In the registry", hint: pushed });
      }
    }
    for (const t of running.get(name) ?? [])
      add({ value: t, group: "Running now", hint: "used by a service" });
    if (!repo) add({ value: "latest", group: "Common", hint: "moves with each release" });
    return out;
  }, [repo, regTags, running, name]);

  // A fresh name starts at the tag a service already runs it at, else at
  // the default (latest); a private repository's tags load for the next pick.
  const setName = (n: string) => {
    if (n === name) return;
    const t = [...(running.get(n) ?? [])][0] ?? (n.startsWith(REGISTRY) ? "" : "latest");
    onChange(t ? `${n}:${t}` : n);
  };
  const setTag = (t: string) => onChange(t ? `${name}:${t}` : name);

  const digestRef = value.includes("@sha256:");
  return (
    <div className="grid grid-cols-1 gap-2 sm:grid-cols-[1fr_12rem]">
      <Field
        label="Image"
        hint="Pick one that runs now, from the private registry or on Docker Hub, or use Custom value for any other (ghcr.io/acme/api)."
      >
        <PickOne
          label="Image"
          value={name}
          onChange={setName}
          options={nameOptions}
          validate={validateName}
          customLabel="Image name"
          customPlaceholder="ghcr.io/acme/api"
          searchPlaceholder="Search images…"
          placeholder="Choose an image"
        />
      </Field>
      <Field label="Tag" hint={digestRef ? "Pinned by digest" : undefined}>
        <PickOne
          label="Tag"
          value={tag}
          onChange={setTag}
          options={tagOptions}
          validate={validateTag}
          customLabel="Tag"
          customPlaceholder="1.27"
          searchPlaceholder="Search tags…"
          placeholder="latest"
          none={digestRef ? undefined : { label: "No tag (Docker uses latest)", value: "" }}
          disabled={!name || digestRef}
        />
      </Field>
    </div>
  );
}
