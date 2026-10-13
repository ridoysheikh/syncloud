import { useEffect, useState, type FormEvent } from "react";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import { Trash2 } from "lucide-react";
import { api, ApiError } from "@/lib/api";
import {
  BrandMark,
  logoSrc,
  LogoPicker,
  useBranding,
  type Branding,
} from "@/lib/branding";
import { PageHeader } from "@/ui/PageHeader";
import { Panel } from "@/ui/Panel";
import { Alert, Button, Field, Input } from "@/ui/controls";
import { confirmAction } from "@/ui/dialogs";
import { cn, gap } from "@/ui/cn";

/** Workspace settings: branding (Phase 21). */
export function GeneralPage() {
  return (
    <div className={cn("flex flex-col", gap)}>
      <PageHeader crumbs={["Settings"]} title="General" />
      <BrandingPanel />
    </div>
  );
}

function BrandingPanel() {
  const qc = useQueryClient();
  const current = useBranding();
  const [name, setName] = useState(current.name);
  const [title, setTitle] = useState(
    current.title === current.name ? "" : current.title,
  );
  const [logo, setLogo] = useState<string | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [saved, setSaved] = useState(false);
  useEffect(() => {
    setName(current.name);
    setTitle(current.title === current.name ? "" : current.title);
  }, [current.name, current.title]);

  const done = (b: Branding) => {
    qc.setQueryData(["branding"], b);
    setSaved(true);
  };
  const save = useMutation({
    mutationFn: async () => {
      let b = await api<Branding>("PUT", "/branding", { name, title });
      if (logo) b = await api<Branding>("PUT", "/branding/logo", { logo });
      return b;
    },
    onSuccess: (b) => {
      done(b);
      setLogo(null);
    },
    onError: (e) =>
      setError(e instanceof ApiError ? e.message : "Could not save"),
  });
  const remove = useMutation({
    mutationFn: () => api<Branding>("DELETE", "/branding/logo"),
    onSuccess: done,
    onError: (e) =>
      setError(e instanceof ApiError ? e.message : "Could not remove the logo"),
  });

  const submit = (e: FormEvent) => {
    e.preventDefault();
    setError(null);
    setSaved(false);
    save.mutate();
  };
  const preview: Branding = {
    ...current,
    name: name.trim() || "SynCloud",
    title: title.trim() || name.trim() || "SynCloud",
  };
  const icon = logo ?? logoSrc(current);
  const dirty =
    !!logo || name !== current.name || (title || name) !== current.title;

  return (
    <Panel title="Branding">
      <form
        onSubmit={submit}
        className="grid grid-cols-1 gap-3 text-xs lg:grid-cols-2"
      >
        <div className="flex flex-col gap-2.5">
          <Field
            label="Workspace name"
            hint="Shown in the header, on the sign-in page and in alert emails. Empty: SynCloud."
          >
            <Input
              value={name}
              onChange={(e) => setName(e.target.value)}
              maxLength={40}
              placeholder="SynCloud"
            />
          </Field>
          <Field
            label="Browser title"
            hint="The browser tab; the workspace name when empty."
          >
            <Input
              value={title}
              onChange={(e) => setTitle(e.target.value)}
              maxLength={60}
              placeholder={name || "SynCloud"}
            />
          </Field>
          <Field label="Logo" hint="The browser icon is made from it.">
            <LogoPicker
              value={logo}
              fallback={logoSrc(current)}
              onChange={setLogo}
              onError={setError}
            />
          </Field>
          {current.hasLogo && !logo && (
            <div>
              <Button
                type="button"
                variant="ghost"
                disabled={remove.isPending}
                onClick={async () =>
                  (await confirmAction(
                    "Remove the logo? The default SynCloud logo comes back.",
                  )) && remove.mutate()
                }
              >
                <Trash2 className="size-3.5" /> Remove logo
              </Button>
            </div>
          )}
        </div>
        <div className="flex flex-col gap-2.5">
          <span className="text-muted">Preview</span>
          <div className="border-line overflow-hidden rounded-sm border">
            <div className="bg-raised flex items-center gap-1.5 px-2 py-1 text-[11px]">
              <img
                src={icon}
                alt=""
                className="size-3.5 shrink-0 rounded-[2px]"
              />
              <span className="min-w-0 truncate">{preview.title}</span>
            </div>
            <div className="bg-surface border-line flex h-10 items-center border-t px-3">
              <BrandMark branding={preview} logo={icon} />
            </div>
          </div>
          {error && <Alert>{error}</Alert>}
          {saved && !dirty && (
            <Alert tone="info">
              Saved. Open tabs pick up the new title and icon right away.
            </Alert>
          )}
          <div>
            <Button
              type="submit"
              variant="primary"
              disabled={!dirty || save.isPending}
            >
              {save.isPending ? "Saving…" : "Save"}
            </Button>
          </div>
        </div>
      </form>
    </Panel>
  );
}
