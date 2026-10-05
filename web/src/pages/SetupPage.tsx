import { useState, type FormEvent } from "react";
import { useNavigate } from "@tanstack/react-router";
import { useQueryClient } from "@tanstack/react-query";
import { api, ApiError, type User } from "@/lib/api";
import { Alert, Button, Field, Input } from "@/ui/controls";
import { AuthLayout } from "./AuthLayout";

const MIN_PASSWORD = 12;

/** First-run wizard, step 1: exchange the setup token for the root account (§5.0). */
export function SetupPage() {
  const navigate = useNavigate();
  const qc = useQueryClient();
  const [form, setForm] = useState({ setupToken: "", name: "", email: "", password: "", confirm: "" });
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
      const user = await api<User>("POST", "/setup", body);
      qc.setQueryData(["auth", "me"], user);
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
