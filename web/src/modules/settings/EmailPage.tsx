import { useEffect, useState, type FormEvent } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Lock, LockOpen, Mail, Send, ShieldCheck } from "lucide-react";
import { api, ApiError } from "@/lib/api";
import { meQuery } from "@/lib/auth";
import { useSMTP, type SMTPView } from "@/modules/monitoring/AlertsPage";
import { PageHeader } from "@/ui/PageHeader";
import { Panel } from "@/ui/Panel";
import { Alert, Button, Field, Input, StatusBadge } from "@/ui/controls";
import { ChoiceField, Segmented } from "@/ui/choice";
import { confirmAction } from "@/ui/dialogs";
import { cn, gap } from "@/ui/cn";

type Security = SMTPView["security"];
const defaultPort: Record<Security, number> = {
  starttls: 587,
  tls: 465,
  none: 25,
};

const errText = (e: unknown, def: string) =>
  e instanceof ApiError ? e.message : def;

/** The SMTP server alert emails go through (Phase 20a). */
export function EmailPage() {
  const smtp = useSMTP();
  return (
    <div className={cn("flex flex-col", gap)}>
      <PageHeader crumbs={["Settings"]} title="Email" />
      <p className="text-muted text-xs">
        Alert emails (Monitoring → Alerts) are sent through this server. Any
        SMTP service works: your mail provider, Amazon SES, Postmark, Mailgun,
        SendGrid or your own.
      </p>
      <div className={cn("grid grid-cols-1 xl:grid-cols-[1fr_22rem]", gap)}>
        {smtp.data && <SMTPForm current={smtp.data} />}
        {smtp.data?.configured && <TestPanel />}
      </div>
      {smtp.error && (
        <Alert>
          {errText(smtp.error, "Could not read the email settings")}
        </Alert>
      )}
    </div>
  );
}

function SMTPForm({ current }: { current: SMTPView }) {
  const qc = useQueryClient();
  const [f, setF] = useState({ ...current, password: "" });
  useEffect(() => setF({ ...current, password: "" }), [current]);
  const set = <K extends keyof typeof f>(k: K, v: (typeof f)[K]) =>
    setF((x) => ({ ...x, [k]: v }));
  const save = useMutation({
    mutationFn: () =>
      api<SMTPView>("PUT", "/settings/smtp", {
        host: f.host,
        port: Number(f.port) || 0,
        security: f.security,
        username: f.username,
        password: f.password,
        from: f.from,
        fromName: f.fromName,
      }),
    onSuccess: (v) => qc.setQueryData(["settings", "smtp"], v),
  });
  const disable = useMutation({
    mutationFn: () => api("DELETE", "/settings/smtp"),
    onSuccess: () => qc.invalidateQueries({ queryKey: ["settings", "smtp"] }),
  });
  const submit = (e: FormEvent) => {
    e.preventDefault();
    save.mutate();
  };
  return (
    <Panel
      title="SMTP server"
      actions={
        current.configured ? (
          <StatusBadge tone="ok">configured</StatusBadge>
        ) : (
          <StatusBadge tone="warn">not set up</StatusBadge>
        )
      }
    >
      <form
        onSubmit={submit}
        className="flex max-w-2xl flex-col gap-2.5 text-xs"
      >
        <div className="grid grid-cols-1 gap-2.5 sm:grid-cols-[1fr_7rem]">
          <Field label="Host">
            <Input
              value={f.host}
              onChange={(e) => set("host", e.target.value)}
              placeholder="smtp.example.com"
              className="font-mono"
              required
            />
          </Field>
          <Field label="Port">
            <Input
              type="number"
              min={1}
              max={65535}
              value={f.port || ""}
              placeholder={String(defaultPort[f.security])}
              onChange={(e) => set("port", Number(e.target.value))}
            />
          </Field>
        </div>
        <ChoiceField label="Security">
          <Segmented
            label="Security"
            value={f.security}
            onChange={(v) =>
              setF((x) => ({ ...x, security: v, port: defaultPort[v] }))
            }
            options={[
              { value: "starttls", label: "STARTTLS", icon: ShieldCheck },
              { value: "tls", label: "TLS", icon: Lock },
              { value: "none", label: "None", icon: LockOpen },
            ]}
          />
        </ChoiceField>
        {f.security === "none" && (
          <Alert tone="warn">
            Without TLS, the password and every email cross the network in clear
            text.
          </Alert>
        )}
        <div className="grid grid-cols-1 gap-2.5 sm:grid-cols-2">
          <Field label="Username" hint="Empty: no sign-in">
            <Input
              value={f.username}
              onChange={(e) => set("username", e.target.value)}
              autoComplete="off"
            />
          </Field>
          <Field
            label="Password"
            hint={
              current.hasPassword ? "Empty keeps the saved password" : undefined
            }
          >
            <Input
              type="password"
              value={f.password}
              onChange={(e) => set("password", e.target.value)}
              placeholder={current.hasPassword ? "••••••••" : ""}
              autoComplete="new-password"
            />
          </Field>
          <Field label="From address">
            <Input
              type="email"
              value={f.from}
              onChange={(e) => set("from", e.target.value)}
              placeholder="alerts@example.com"
              required
            />
          </Field>
          <Field label="From name" hint="Empty: the workspace name">
            <Input
              value={f.fromName}
              onChange={(e) => set("fromName", e.target.value)}
              maxLength={60}
            />
          </Field>
        </div>
        {save.error && <Alert>{errText(save.error, "Could not save")}</Alert>}
        {disable.error && (
          <Alert>{errText(disable.error, "Could not turn email off")}</Alert>
        )}
        {save.isSuccess && (
          <Alert tone="info">Saved. Send a test email to check it.</Alert>
        )}
        <div className="flex flex-wrap gap-1.5">
          <Button type="submit" variant="primary" disabled={save.isPending}>
            <Mail className="size-3.5" /> Save
          </Button>
          {current.configured && (
            <Button
              type="button"
              variant="ghost"
              disabled={disable.isPending}
              onClick={async () =>
                (await confirmAction(
                  "Turn email off? Email alerts stop until an SMTP server is set again; other channels keep working.",
                )) && disable.mutate()
              }
            >
              Turn off
            </Button>
          )}
        </div>
      </form>
    </Panel>
  );
}

function TestPanel() {
  const me = useQuery(meQuery);
  const [to, setTo] = useState("");
  useEffect(() => {
    if (me.data?.email) setTo((t) => t || me.data!.email);
  }, [me.data]);
  const test = useMutation({
    mutationFn: () =>
      api<{ sent: boolean; error?: string }>("POST", "/settings/smtp/test", {
        to,
      }),
  });
  return (
    <Panel title="Send a test email">
      <form
        onSubmit={(e) => {
          e.preventDefault();
          test.mutate();
        }}
        className="flex flex-col gap-2.5 text-xs"
      >
        <Field label="To">
          <Input
            type="email"
            value={to}
            onChange={(e) => setTo(e.target.value)}
            required
          />
        </Field>
        <div>
          <Button type="submit" disabled={!to || test.isPending}>
            <Send className="size-3.5" />{" "}
            {test.isPending ? "Sending…" : "Send test"}
          </Button>
        </div>
        {test.data?.sent && (
          <Alert tone="info">
            Sent. Check {to}; it looks like an alert email.
          </Alert>
        )}
        {test.data && !test.data.sent && (
          <Alert>The server refused it: {test.data.error}</Alert>
        )}
        {test.error && <Alert>{errText(test.error, "Could not send")}</Alert>}
      </form>
    </Panel>
  );
}
