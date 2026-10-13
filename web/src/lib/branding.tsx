import { useEffect, useRef, useState } from "react";
import { queryOptions, useQuery } from "@tanstack/react-query";
import { ImagePlus, X } from "lucide-react";
import { api } from "@/lib/api";
import { cn } from "@/ui/cn";
import { Button } from "@/ui/controls";

/** The workspace name, browser title and logo (Phase 21). */
export interface Branding {
  name: string;
  title: string;
  hasLogo: boolean;
  version: string;
}

export const DEFAULT_BRANDING: Branding = {
  name: "SynCloud",
  title: "SynCloud",
  hasLogo: false,
  version: "default",
};

export const brandingQuery = queryOptions({
  queryKey: ["branding"],
  queryFn: () => api<Branding>("GET", "/branding"),
  staleTime: 60_000,
});

export function useBranding(): Branding {
  return useQuery(brandingQuery).data ?? DEFAULT_BRANDING;
}

export const logoSrc = (b: Branding) =>
  `/branding/logo.png?v=${encodeURIComponent(b.version)}`;

/** Keeps the browser tab's title and icon in step with the branding. */
export function useBrandingHead() {
  const b = useBranding();
  useEffect(() => {
    document.title = b.title;
    const v = encodeURIComponent(b.version);
    const icons: [string, string][] = [
      ["icon", "favicon.png"],
      ["apple-touch-icon", "apple-touch-icon.png"],
    ];
    for (const [rel, file] of icons) {
      let link = document.querySelector<HTMLLinkElement>(`link[rel="${rel}"]`);
      if (!link) {
        link = document.createElement("link");
        link.rel = rel;
        document.head.appendChild(link);
      }
      link.href = `/branding/${file}?v=${v}`;
    }
  }, [b.title, b.version]);
}

/** Logo and workspace name, as in the header. */
export function BrandMark({
  branding,
  size = "md",
  logo,
}: {
  branding?: Branding;
  size?: "md" | "lg";
  logo?: string;
}) {
  const live = useBranding();
  const b = branding ?? live;
  return (
    <div className="flex min-w-0 items-center gap-2">
      <img
        src={logo ?? logoSrc(b)}
        alt=""
        className={cn(
          "shrink-0 rounded-sm",
          size === "lg" ? "size-6" : "size-5",
        )}
      />
      <span
        className={cn(
          "truncate font-semibold tracking-tight",
          size === "lg" && "text-base",
        )}
        title={b.name}
      >
        {b.name}
      </span>
    </div>
  );
}

const ACCEPT = ["image/png", "image/jpeg", "image/webp"];
const MAX_BYTES = 512 * 1024;

/** Reads an image file as a data URL, checking type and size first. */
export function readLogo(file: File): Promise<string> {
  if (!ACCEPT.includes(file.type))
    return Promise.reject(new Error("Use a PNG, JPEG or WebP image."));
  if (file.size > MAX_BYTES)
    return Promise.reject(new Error("The logo may be at most 512 KB."));
  return new Promise((resolve, reject) => {
    const r = new FileReader();
    r.onload = () => resolve(String(r.result));
    r.onerror = () => reject(new Error("Could not read the file."));
    r.readAsDataURL(file);
  });
}

/**
 * Picks a logo: shows the current one (or the default), and lets the user
 * choose a file or go back to the default.
 */
export function LogoPicker({
  value,
  fallback,
  onChange,
  onError,
  disabled,
}: {
  /** A chosen image as a data URL; null = none chosen. */
  value: string | null;
  /** What shows when nothing is chosen. */
  fallback: string;
  onChange: (dataUrl: string | null) => void;
  onError: (msg: string) => void;
  disabled?: boolean;
}) {
  const input = useRef<HTMLInputElement>(null);
  const [drag, setDrag] = useState(false);
  const pick = async (f?: File) => {
    if (!f) return;
    try {
      onChange(await readLogo(f));
    } catch (e) {
      onError(e instanceof Error ? e.message : "Could not read the file.");
    }
  };
  return (
    <div
      className={cn(
        "border-line flex items-center gap-3 rounded-sm border border-dashed p-2",
        drag && "bg-hover border-line-strong",
      )}
      onDragOver={(e) => {
        e.preventDefault();
        setDrag(true);
      }}
      onDragLeave={() => setDrag(false)}
      onDrop={(e) => {
        e.preventDefault();
        setDrag(false);
        void pick(e.dataTransfer.files[0]);
      }}
    >
      <img
        src={value ?? fallback}
        alt="Logo preview"
        className="bg-raised size-12 shrink-0 rounded-sm object-contain"
      />
      <div className="flex min-w-0 flex-1 flex-col gap-1 text-xs">
        <span className="text-muted">
          Square PNG, JPEG or WebP, up to 512 KB. Drop it here or choose a file.
        </span>
        <div className="flex flex-wrap gap-1.5">
          <Button
            type="button"
            onClick={() => input.current?.click()}
            disabled={disabled}
          >
            <ImagePlus className="size-3.5" /> Choose image
          </Button>
          {value && (
            <Button
              type="button"
              variant="ghost"
              onClick={() => onChange(null)}
              disabled={disabled}
            >
              <X className="size-3.5" /> Clear
            </Button>
          )}
        </div>
      </div>
      <input
        ref={input}
        type="file"
        accept={ACCEPT.join(",")}
        className="hidden"
        onChange={(e) => {
          void pick(e.target.files?.[0]);
          e.target.value = "";
        }}
      />
    </div>
  );
}
