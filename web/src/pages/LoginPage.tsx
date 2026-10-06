import { useState, type FormEvent } from "react";
import { useNavigate } from "@tanstack/react-router";
import { useQueryClient } from "@tanstack/react-query";
import { api, ApiError, type User } from "@/lib/api";
import { Alert, Button, Field, Input } from "@/ui/controls";
import { AuthLayout } from "./AuthLayout";

export function LoginPage() {
  const navigate = useNavigate();
  const qc = useQueryClient();
  const [email, setEmail] = useState("");
  const [password, setPassword] = useState("");
  const [otp, setOtp] = useState("");
  const [needOtp, setNeedOtp] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  const submit = async (e: FormEvent) => {
    e.preventDefault();
    setError(null);
    setBusy(true);
    try {
      const user = await api<User>("POST", "/auth/login", { email, password, otp });
      qc.setQueryData(["auth", "me"], user);
      const back = new URLSearchParams(window.location.search).get("next");
      await navigate({ to: (back && back.startsWith("/") ? back : "/") as string });
    } catch (err) {
      if (err instanceof ApiError && err.code === "mfa_required") {
        setNeedOtp(true);
        if (!otp) return;
      }
      setError(err instanceof ApiError ? err.message : "Could not reach the controller.");
    } finally {
      setBusy(false);
    }
  };

  return (
    <AuthLayout title="Sign in">
      <form onSubmit={submit} className="flex flex-col gap-2.5">
        <Field label="Email">
          <Input type="email" value={email} onChange={(e) => setEmail(e.target.value)} autoComplete="username" required autoFocus />
        </Field>
        <Field label="Password">
          <Input
            type="password"
            value={password}
            onChange={(e) => setPassword(e.target.value)}
            autoComplete="current-password"
            required
          />
        </Field>
        {needOtp && (
          <Field label="Authenticator code">
            <Input value={otp} onChange={(e) => setOtp(e.target.value)} inputMode="numeric" autoComplete="one-time-code" placeholder="123456" autoFocus />
          </Field>
        )}
        {error && <Alert>{error}</Alert>}
        <Button type="submit" variant="primary" disabled={busy} className="mt-1 h-8">
          {busy ? "Signing in…" : "Sign in"}
        </Button>
      </form>
    </AuthLayout>
  );
}
