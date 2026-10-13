import { useState, type FormEvent } from "react";
import { useNavigate } from "@tanstack/react-router";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { api, ApiError, type User } from "@/lib/api";
import { statusQuery } from "@/lib/auth";
import { BrandMark, DEFAULT_BRANDING, LogoPicker, type Branding } from "@/lib/branding";
import { Alert, Button, Field, Input } from "@/ui/controls";
import { AuthLayout } from "./AuthLayout";

const MIN_PASSWORD = 12;

/** First-run wizard, step 1: exchange the setup token for the root account (§5.0). */
export function SetupPage() {
  const navigate = useNavigate();
  const qc = useQueryClient();
  const [form, setForm] = useState({
    setupToken: "",
    workspaceName: "",
    title: "",
    name: "",
    email: "",
    password: "",
    confirm: "",
    recoveryKeySuffix: "",
  });
  const [logo, setLogo] = useState<string | null>(null);
  const { data: status } = useQuery(statusQuery);
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  const set = (k: keyof typeof form) => (e: { target: { value: string } }) => setForm({ ...form, [k]: e.target.value });

  const submit = async (e: FormEvent) => {
    e.preventDefault();
    setError(null);
    if (form.password.length < MIN_PASSWORD) return setError(`Password must be at least ${MIN_PASSWORD} characters.`);
    if (form.password !== form.confirm) return setError("Passwords do not match.");
    setBusy(true);
    try {
      const { confirm: _, ...body } = form;
      const user = await api<User>("POST", "/setup", { ...body, logo: logo ?? "" });
      qc.setQueryData(["auth", "me"], user);
      await qc.invalidateQueries({ queryKey: ["branding"] });
      await qc.invalidateQueries({ queryKey: ["system", "status"] });
      await navigate({ to: "/" });
    } catch (err) {
      setError(err instanceof ApiError ? err.message : "Could not reach the controller.");
    } finally {
      setBusy(false);
    }
  };

  return (
    <AuthLayout
      title="Set up SynCloud"
      subtitle="Create the root account. The setup token was printed in the controller's terminal when it started."
    >
      <form onSubmit={submit} className="flex flex-col gap-2.5">
        <Field label="Setup token">
          <Input
            value={form.setupToken}
            onChange={set("setupToken")}
            placeholder="syn_setup_…"
            className="font-mono text-xs"
            autoComplete="off"
            spellCheck={false}
            required
            autoFocus
          />
        </Field>
        {status?.recoveryConfirmRequired && (
          <Field label="Recovery key: last 6 characters" hint="Printed with the setup token; dashes don't count (a key ending …-T5JV2-IRWKX gives 2IRWKX). You need the key to restore backups, so store it safely first.">
            <Input
              value={form.recoveryKeySuffix}
              onChange={set("recoveryKeySuffix")}
              placeholder="ABC234"
              className="font-mono text-xs uppercase"
              autoComplete="off"
              spellCheck={false}
              maxLength={80}
              minLength={6}
              required
            />
          </Field>
        )}
        <WorkspaceFields
          name={form.workspaceName}
          title={form.title}
          logo={logo}
          onName={set("workspaceName")}
          onTitle={set("title")}
          onLogo={setLogo}
          onError={setError}
        />
        <h2 className="text-muted mt-1 text-[11px] font-medium tracking-wide uppercase">Root account</h2>
        <Field label="Name">
          <Input value={form.name} onChange={set("name")} autoComplete="name" required maxLength={100} />
        </Field>
        <Field label="Email">
          <Input type="email" value={form.email} onChange={set("email")} autoComplete="email" required />
        </Field>
        <Field label="Password" hint={`At least ${MIN_PASSWORD} characters.`}>
          <Input type="password" value={form.password} onChange={set("password")} autoComplete="new-password" required />
        </Field>
        <Field label="Confirm password">
          <Input type="password" value={form.confirm} onChange={set("confirm")} autoComplete="new-password" required />
        </Field>
        {error && <Alert>{error}</Alert>}
        <Button type="submit" variant="primary" disabled={busy} className="mt-1 h-8">
          {busy ? "Creating account…" : "Create root account"}
        </Button>
      </form>
    </AuthLayout>
  );
}

/** Workspace name, tab title and logo, with a live preview (Phase 21). */
function WorkspaceFields({
  name,
  title,
  logo,
  onName,
  onTitle,
  onLogo,
  onError,
}: {
  name: string;
  title: string;
  logo: string | null;
  onName: (e: { target: { value: string } }) => void;
  onTitle: (e: { target: { value: string } }) => void;
  onLogo: (v: string | null) => void;
  onError: (msg: string) => void;
}) {
  const preview: Branding = {
    ...DEFAULT_BRANDING,
    name: name.trim() || DEFAULT_BRANDING.name,
    title: title.trim() || name.trim() || DEFAULT_BRANDING.title,
  };
  const icon = logo ?? "/logo-square.png";
  return (
    <>
      <h2 className="text-muted mt-1 text-[11px] font-medium tracking-wide uppercase">Workspace</h2>
      <Field label="Workspace name" hint="Shown in the header, on the sign-in page and in alert emails.">
        <Input value={name} onChange={onName} placeholder="Acme Cloud" maxLength={40} required />
      </Field>
      <Field label="Browser title (optional)" hint="The browser tab; the workspace name when empty.">
        <Input value={title} onChange={onTitle} placeholder={name || "Acme Cloud Console"} maxLength={60} />
      </Field>
      <Field label="Logo (optional)">
        <LogoPicker value={logo} fallback="/logo-square.png" onChange={onLogo} onError={onError} />
      </Field>
      <div className="border-line overflow-hidden rounded-sm border" aria-label="Preview">
        <div className="bg-raised flex items-center gap-1.5 px-2 py-1 text-[11px]">
          <img src={icon} alt="" className="size-3.5 shrink-0 rounded-[2px]" />
          <span className="min-w-0 truncate">{preview.title}</span>
        </div>
        <div className="bg-surface border-line flex h-8 items-center border-t px-2 text-xs">
          <BrandMark branding={preview} logo={icon} />
        </div>
      </div>
      <p className="text-muted text-xs">All of this can be changed later under Settings → General.</p>
    </>
  );
}
