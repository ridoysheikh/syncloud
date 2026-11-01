import { useQuery } from "@tanstack/react-query";
import { api, ApiError } from "@/lib/api";
import { bytes, since } from "@/lib/nodes";
import { Dialog } from "@/ui/Dialog";
import { Alert, Button } from "@/ui/controls";
import { StatTile } from "@/ui/StatTile";

interface Details {
  tag: string;
  digest: string;
  sizeBytes: number;
  totalBytes: number;
  platforms: string[];
  platform: string;
  created: string | null;
  cache: boolean;
  layers: { digest: string; size: number; command?: string }[];
  variants: { platform: string; digest: string; sizeBytes: number }[];
  entrypoint: string[] | null;
  cmd: string[] | null;
  workingDir: string;
  user: string;
  ports: string[];
  envNames: string[];
  labels: Record<string, string>;
}

function Row({ k, children }: { k: string; children: React.ReactNode }) {
  return (
    <div className="grid grid-cols-[7rem_minmax(0,1fr)] gap-2 py-0.5 text-xs">
      <span className="text-muted">{k}</span>
      <span className="min-w-0 font-mono break-all">{children}</span>
    </div>
  );
}

/** One image in depth: layers with the Dockerfile steps that made them, platforms and settings. */
export function ImageDetails({
  repo,
  tag,
  onClose,
}: {
  repo: string;
  tag: string | null;
  onClose: () => void;
}) {
  const { data: d, error, isLoading } = useQuery({
    queryKey: ["registry", "image-details", repo, tag],
    queryFn: () =>
      api<Details>(
        "GET",
        `/registry/images/details?repository=${encodeURIComponent(repo)}&tag=${encodeURIComponent(tag!)}`,
      ),
    enabled: !!tag,
  });
  const biggest = Math.max(1, ...(d?.layers ?? []).map((l) => l.size));
  const cmd = (a: string[] | null) => (a && a.length ? a.join(" ") : "—");
  return (
    <Dialog
      open={!!tag}
      onClose={onClose}
      size="xl"
      title={
        <span className="font-mono">
          {repo}:{tag}
        </span>
      }
      description={d && <span className="font-mono break-all">{d.digest}</span>}
      footer={<Button onClick={onClose}>Close</Button>}
    >
      {isLoading && <div className="text-muted text-xs">Reading the image…</div>}
      {error && (
        <Alert>
          {error instanceof ApiError ? error.message : "Could not read the image"}
        </Alert>
      )}
      {d && (
        <div className="flex flex-col gap-3">
          <div className="grid grid-cols-2 gap-2 md:grid-cols-4">
            <StatTile label="Size" value={bytes(d.sizeBytes)} hint={d.platform} />
            <StatTile
              label="All platforms"
              value={bytes(d.totalBytes)}
              hint="with attestations"
            />
            <StatTile label="Layers" value={d.layers.length} />
            <StatTile
              label="Built"
              value={d.created ? since(d.created) : "—"}
            />
          </div>
          {d.cache && (
            <Alert tone="info">
              This is the builders' layer cache, not a runnable image. Builds
              replace it; lifecycle rules leave it alone.
            </Alert>
          )}
          {d.layers.length > 0 && (
            <div>
              <h3 className="text-muted mb-1 text-xs font-medium uppercase">
                Layers
              </h3>
              <div className="border-line rounded-sm border">
                <table className="w-full border-collapse text-xs">
                  <thead>
                    <tr className="border-line text-muted border-b text-left">
                      <th className="h-7 px-2 font-medium">#</th>
                      <th className="px-2 font-medium">Size</th>
                      <th className="w-full px-2 font-medium">Step</th>
                    </tr>
                  </thead>
                  <tbody>
                    {d.layers.map((l, i) => (
                      <tr
                        key={l.digest}
                        className="border-line border-b align-top last:border-b-0"
                      >
                        <td className="text-faint px-2 py-1 font-mono">
                          {i + 1}
                        </td>
                        <td className="px-2 py-1 whitespace-nowrap">
                          <div className="flex items-center gap-1.5">
                            <div className="bg-raised h-1 w-16 overflow-hidden rounded-sm">
                              <div
                                className="bg-accent h-full"
                                style={{ width: `${(l.size / biggest) * 100}%` }}
                              />
                            </div>
                            <span className="font-mono tabular-nums">
                              {bytes(l.size)}
                            </span>
                          </div>
                        </td>
                        <td
                          className="max-w-0 px-2 py-1 font-mono break-all"
                          title={l.digest}
                        >
                          {l.command || (
                            <span className="text-faint">
                              {l.digest.slice(7, 19)}
                            </span>
                          )}
                        </td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              </div>
            </div>
          )}
          {!d.cache && (
            <div>
              <h3 className="text-muted mb-1 text-xs font-medium uppercase">
                Image
              </h3>
              <Row k="Entrypoint">{cmd(d.entrypoint)}</Row>
              <Row k="Command">{cmd(d.cmd)}</Row>
              <Row k="Working dir">{d.workingDir || "—"}</Row>
              <Row k="User">{d.user || "—"}</Row>
              <Row k="Ports">{d.ports.join(", ") || "—"}</Row>
              <Row k="Variables">{d.envNames.join(", ") || "—"}</Row>
              {d.variants.length > 1 && (
                <Row k="Platforms">
                  {d.variants
                    .map((v) => `${v.platform} (${bytes(v.sizeBytes)})`)
                    .join(", ")}
                </Row>
              )}
              {Object.entries(d.labels).map(([k, v]) => (
                <Row key={k} k={k.length > 18 ? `${k.slice(0, 17)}…` : k}>
                  {v}
                </Row>
              ))}
              <p className="text-faint mt-1 text-xs">
                Variable values are not shown: images can bake secrets in.
              </p>
            </div>
          )}
        </div>
      )}
    </Dialog>
  );
}
