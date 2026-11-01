/** One tag as the API lists it. */
export interface Image {
  tag: string;
  digest: string;
  sizeBytes: number;
  uniqueBytes: number;
  platforms: string[];
  created: string | null;
  pushedAt: string | null;
  lastPushedAt: string | null;
  pulls: number;
  lastPulledAt: string | null;
  cache: boolean;
  inUseBy: { project: string; environment: string; service: string }[];
  builtFrom: { id: string; ref: string; sha: string } | null;
}

/** An image: a manifest digest with every tag pointing at it. */
export interface ImageGroup {
  key: string;
  digest: string;
  /** Named tags first: "latest-main" says more than a commit id. */
  tags: string[];
  sizeBytes: number;
  uniqueBytes: number;
  platforms: string[];
  created: string | null;
  pushedAt: string | null;
  pulls: number;
  lastPulledAt: string | null;
  cache: boolean;
  inUseBy: Image["inUseBy"];
  builtFrom: Image["builtFrom"];
}

/** A commit-id tag such as the ones builds push. */
export const isIdTag = (tag: string) => /^[0-9a-f]{7,64}(-r\d+)?$/.test(tag);

/** The branch or git tag a build ref names. */
export const shortRef = (ref: string) =>
  ref.replace(/^refs\/(heads|tags)\//, "");

/**
 * Collapses per-tag rows into one row per image: a build pushes a commit id
 * and a branch tag to the same digest, and they are one thing to keep or
 * delete. Order follows the API (newest first).
 */
export function groupImages(items: Image[]): ImageGroup[] {
  const out: ImageGroup[] = [];
  const byDigest = new Map<string, ImageGroup>();
  for (const i of items) {
    let g = i.digest ? byDigest.get(i.digest) : undefined;
    if (!g) {
      g = {
        key: i.digest || `tag:${i.tag}`,
        digest: i.digest,
        tags: [],
        sizeBytes: i.sizeBytes,
        uniqueBytes: i.uniqueBytes,
        platforms: i.platforms,
        created: i.created,
        pushedAt: i.lastPushedAt ?? i.pushedAt,
        pulls: i.pulls,
        lastPulledAt: i.lastPulledAt,
        cache: i.cache,
        inUseBy: i.inUseBy,
        builtFrom: i.builtFrom,
      };
      out.push(g);
      if (i.digest) byDigest.set(i.digest, g);
    }
    g.tags.push(i.tag);
    g.builtFrom ??= i.builtFrom;
  }
  for (const g of out)
    g.tags.sort((a, b) => Number(isIdTag(a)) - Number(isIdTag(b)));
  return out;
}

/** "main" for a build of refs/heads/main, with the short commit. */
export function sourceOf(g: Pick<ImageGroup, "builtFrom">) {
  if (!g.builtFrom) return null;
  return {
    ref: shortRef(g.builtFrom.ref),
    sha: g.builtFrom.sha.slice(0, 7),
  };
}
