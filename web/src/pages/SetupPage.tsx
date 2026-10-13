import { useState, type FormEvent, type ReactNode } from "react";
import { useNavigate } from "@tanstack/react-router";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import {
  ArrowLeft,
  ArrowRight,
  Building2,
  Check,
  ClipboardCheck,
  KeyRound,
  Lock,
  ShieldCheck,
  UserRound,
} from "lucide-react";
import { api, ApiError, type User } from "@/lib/api";
import { statusQuery } from "@/lib/auth";
import {
  BrandMark,
  DEFAULT_BRANDING,
  LogoPicker,
  type Branding,
} from "@/lib/branding";
import { Alert, Button, Field, Input } from "@/ui/controls";
import { cn } from "@/ui/cn";

const MIN_PASSWORD = 12;
const DEFAULT_LOGO = "/logo-square.png";

type Form = {
  setupToken: string;
  recoveryKeySuffix: string;
  workspaceName: string;
  title: string;
  name: string;
  email: string;
  password: string;
  confirm: string;
};

const steps = [
  { title: "Verify", text: "Prove you run this server", icon: ShieldCheck },
  { title: "Workspace", text: "Name, tab title and logo", icon: Building2 },
  { title: "Root account", text: "The first administrator", icon: UserRound },
  { title: "Review", text: "Check and create", icon: ClipboardCheck },
] as const;

const emailOK = (s: string) => /^[^\s@]+@[^\s@]+\.[^\s@]+$/.test(s.trim());

/** 0–4: length, then mixing letters, digits and symbols. */
function strength(p: string) {
  if (p.length < MIN_PASSWORD) return p.length ? 1 : 0;
  const kinds = [/[a-z]/, /[A-Z]/, /\d/, /[^A-Za-z0-9]/].filter((r) =>
    r.test(p),
  ).length;
  return Math.min(4, 2 + (kinds >= 3 ? 1 : 0) + (p.length >= 20 ? 1 : 0));
}

/** First-run setup (§5.0, Phase 21): a wizard that creates the workspace and its root account. */
export function SetupPage() {
  const navigate = useNavigate();
  const qc = useQueryClient();
  const { data: status } = useQuery(statusQuery);
  const needSuffix = !!status?.recoveryConfirmRequired;
  const [step, setStep] = useState(0);
  const [form, setForm] = useState<Form>({
    setupToken: "",
    recoveryKeySuffix: "",
    workspaceName: "",
    title: "",
    name: "",
    email: "",
    password: "",
    confirm: "",
  });
  const [logo, setLogo] = useState<string | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const set = (k: keyof Form) => (e: { target: { value: string } }) =>
    setForm((f) => ({ ...f, [k]: e.target.value }));

  const problems: (string | null)[] = [
    !form.setupToken.trim()
      ? "Enter the setup token."
      : needSuffix && form.recoveryKeySuffix.replace(/[\s-]/g, "").length < 6
        ? "Enter the recovery key's last 6 characters."
        : null,
    !form.workspaceName.trim() ? "Give the workspace a name." : null,
    !form.name.trim()
      ? "Enter your name."
      : !emailOK(form.email)
        ? "Enter a valid email address."
        : form.password.length < MIN_PASSWORD
          ? `The password needs at least ${MIN_PASSWORD} characters.`
          : form.password !== form.confirm
            ? "The passwords don't match."
            : null,
    null,
  ];
  const done = (i: number) => i < step && !problems[i];

  const preview: Branding = {
    ...DEFAULT_BRANDING,
    name: form.workspaceName.trim() || DEFAULT_BRANDING.name,
    title:
      form.title.trim() || form.workspaceName.trim() || DEFAULT_BRANDING.title,
  };
  const icon = logo ?? DEFAULT_LOGO;

  const create = async () => {
    setBusy(true);
    try {
      const { confirm: _, ...body } = form;
      const user = await api<User>("POST", "/setup", {
        ...body,
        logo: logo ?? "",
      });
      qc.setQueryData(["auth", "me"], user);
      await qc.invalidateQueries({ queryKey: ["branding"] });
      await qc.invalidateQueries({ queryKey: ["system", "status"] });
      await navigate({ to: "/" });
    } catch (err) {
      const msg =
        err instanceof ApiError
          ? err.message
          : "Could not reach the controller.";
      setError(msg);
      // Send the user back to the step the problem belongs to.
      if (/setup token|recovery key/i.test(msg)) setStep(0);
      else if (/logo|name may|title may/i.test(msg)) setStep(1);
      else if (/password|email|name must/i.test(msg)) setStep(2);
    } finally {
      setBusy(false);
    }
  };

  const submit = (e: FormEvent) => {
    e.preventDefault();
    setError(null);
    if (problems[step]) return setError(problems[step] ?? null);
    if (step < steps.length - 1) return setStep(step + 1);
    const first = problems.findIndex(Boolean);
    if (first >= 0) {
      setStep(first);
      return setError(problems[first] ?? null);
    }
    void create();
  };
  const go = (i: number) => {
    setError(null);
    // Forward only over finished steps.
    if (i <= step || problems.slice(0, i).every((p) => !p)) setStep(i);
  };

  return (
    <div className="flex min-h-full items-center justify-center p-1.5 sm:p-4 lg:p-8">
      <div className="bg-surface border-line grid w-full max-w-5xl overflow-hidden rounded-md border lg:grid-cols-[19rem_1fr]">
        {/* Rail: brand, steps, live preview */}
        <aside className="bg-raised border-line flex flex-col gap-5 border-b p-3 sm:p-4 lg:border-r lg:border-b-0 lg:p-5">
          <BrandMark branding={preview} logo={icon} size="lg" />
          <div>
            <h1 className="text-lg font-semibold tracking-tight">
              Set up your workspace
            </h1>
            <p className="text-muted mt-1 text-xs leading-relaxed">
              A few details, then you&apos;re in. Everything except the root
              account can be changed later.
            </p>
          </div>
          <ol className="flex gap-1 overflow-x-auto lg:flex-col lg:gap-0.5">
            {steps.map((s, i) => {
              const active = i === step;
              const isDone = done(i);
              return (
                <li key={s.title} className="shrink-0">
                  <button
                    type="button"
                    onClick={() => go(i)}
                    aria-current={active ? "step" : undefined}
                    className={cn(
                      "flex w-full items-center gap-2.5 rounded-sm px-2 py-1.5 text-left",
                      active ? "bg-hover" : "hover:bg-hover/60",
                    )}
                  >
                    <span
                      className={cn(
                        "flex size-6 shrink-0 items-center justify-center rounded-full border text-[11px] font-semibold tabular-nums",
                        active && "border-line-accent text-accent",
                        isDone && "border-ok/40 text-ok",
                        !active && !isDone && "border-line-strong text-muted",
                      )}
                    >
                      {isDone ? <Check className="size-3.5" /> : i + 1}
                    </span>
                    <span className="hidden min-w-0 sm:block">
                      <span
                        className={cn(
                          "block text-xs font-medium",
                          !active && "text-muted",
                        )}
                      >
                        {s.title}
                      </span>
                      <span className="text-faint hidden truncate text-[11px] lg:block">
                        {s.text}
                      </span>
                    </span>
                  </button>
                </li>
              );
            })}
          </ol>
          <div className="mt-auto hidden flex-col gap-1.5 lg:flex">
            <span className="text-faint text-[11px] font-medium tracking-wide uppercase">
              Preview
            </span>
            <div className="border-line-strong overflow-hidden rounded-sm border">
              <div className="bg-bg flex items-center gap-1.5 px-2 py-1 text-[11px]">
                <img
                  src={icon}
                  alt=""
                  className="size-3.5 shrink-0 rounded-[2px]"
                />
                <span className="min-w-0 truncate">{preview.title}</span>
              </div>
              <div className="bg-surface border-line-strong flex h-9 items-center border-t px-2.5 text-xs">
                <BrandMark branding={preview} logo={icon} />
              </div>
            </div>
            <p className="text-faint flex items-center gap-1.5 text-[11px]">
              <Lock className="size-3" />
              {status?.version ? `SynCloud ${status.version}` : "SynCloud"} ·
              the setup token expires after an hour
            </p>
          </div>
        </aside>

        {/* The current step */}
        <form onSubmit={submit} className="flex min-h-[32rem] flex-col">
          <header className="border-line border-b px-3 py-3 sm:px-5 sm:py-4">
            <span className="text-faint text-[11px] font-medium tracking-wide uppercase tabular-nums">
              Step {step + 1} of {steps.length}
            </span>
            <h2 className="mt-0.5 text-base font-semibold">
              {stepTitle[step]}
            </h2>
            <p className="text-muted mt-0.5 text-xs">{stepIntro[step]}</p>
          </header>

          <div className="flex flex-1 flex-col gap-4 px-3 py-4 sm:px-5 sm:py-5">
            {step === 0 && (
              <div className="grid gap-4 md:grid-cols-2">
                <Field
                  label="Setup token"
                  hint="Printed in the controller's log when it started; valid for an hour."
                >
                  <Input
                    value={form.setupToken}
                    onChange={set("setupToken")}
                    placeholder="syn_setup_…"
                    className="font-mono text-xs"
                    autoComplete="off"
                    spellCheck={false}
                    autoFocus
                  />
                </Field>
                {needSuffix && (
                  <Field
                    label="Recovery key: last 6 characters"
                    hint="Dashes don't count."
                  >
                    <Input
                      value={form.recoveryKeySuffix}
                      onChange={set("recoveryKeySuffix")}
                      placeholder="2IRWKX"
                      className="font-mono text-xs uppercase"
                      autoComplete="off"
                      spellCheck={false}
                      maxLength={80}
                    />
                  </Field>
                )}
                {needSuffix && (
                  <Callout icon={KeyRound} className="md:col-span-2">
                    The recovery key (<span className="font-mono">SYNRK-…</span>
                    ) was printed with the setup token. It opens your backups,
                    so store it before you continue. For a key ending in{" "}
                    <span className="font-mono">
                      …-T5JV<span className="text-accent">2</span>-
                      <span className="text-accent">IRWKX</span>
                    </span>
                    , type <span className="text-accent font-mono">2IRWKX</span>
                    . The root user can open it later under Settings → Backups.
                  </Callout>
                )}
              </div>
            )}

            {step === 1 && (
              <>
                <div className="grid gap-4 md:grid-cols-2">
                  <Field
                    label="Workspace name"
                    hint="Header, sign-in page and alert emails."
                  >
                    <Input
                      value={form.workspaceName}
                      onChange={set("workspaceName")}
                      placeholder="Acme Cloud"
                      maxLength={40}
                      autoFocus
                    />
                  </Field>
                  <Field
                    label="Browser title"
                    hint="Optional: the tab; the workspace name when empty."
                  >
                    <Input
                      value={form.title}
                      onChange={set("title")}
                      placeholder={form.workspaceName || "Acme Cloud Console"}
                      maxLength={60}
                    />
                  </Field>
                </div>
                <Field
                  label="Logo"
                  hint="Optional. The browser icon is made from it."
                >
                  <LogoPicker
                    value={logo}
                    fallback={DEFAULT_LOGO}
                    onChange={setLogo}
                    onError={setError}
                  />
                </Field>
                <div className="lg:hidden">
                  <MiniPreview branding={preview} icon={icon} />
                </div>
              </>
            )}

            {step === 2 && (
              <>
                <div className="grid gap-4 md:grid-cols-2">
                  <Field label="Your name">
                    <Input
                      value={form.name}
                      onChange={set("name")}
                      autoComplete="name"
                      maxLength={100}
                      autoFocus
                    />
                  </Field>
                  <Field
                    label="Email"
                    hint="You sign in with it; built-in alerts go to it."
                  >
                    <Input
                      type="email"
                      value={form.email}
                      onChange={set("email")}
                      autoComplete="email"
                    />
                  </Field>
                  <Field label="Password">
                    <Input
                      type="password"
                      value={form.password}
                      onChange={set("password")}
                      autoComplete="new-password"
                    />
                  </Field>
                  <Field label="Confirm password">
                    <Input
                      type="password"
                      value={form.confirm}
                      onChange={set("confirm")}
                      autoComplete="new-password"
                    />
                  </Field>
                </div>
                <PasswordCheck
                  password={form.password}
                  confirm={form.confirm}
                />
                <Callout icon={ShieldCheck}>
                  The root account can do everything and bypasses permission
                  checks. Create normal users under IAM for daily work, and turn
                  on two-factor sign-in under IAM → My security.
                </Callout>
              </>
            )}

            {step === 3 && (
              <div className="grid gap-3 md:grid-cols-3">
                <Summary title="Verify" onEdit={() => go(0)}>
                  <Row
                    k="Setup token"
                    v={`${form.setupToken.slice(0, 14)}…`}
                    mono
                  />
                  {needSuffix && (
                    <Row
                      k="Recovery key"
                      v={`…${form.recoveryKeySuffix.toUpperCase()}`}
                      mono
                    />
                  )}
                </Summary>
                <Summary title="Workspace" onEdit={() => go(1)}>
                  <div className="flex items-center gap-2 py-1">
                    <img
                      src={icon}
                      alt=""
                      className="bg-raised size-8 rounded-sm object-contain"
                    />
                    <div className="min-w-0">
                      <div className="truncate text-xs font-medium">
                        {preview.name}
                      </div>
                      <div className="text-faint truncate text-[11px]">
                        Tab: {preview.title}
                      </div>
                    </div>
                  </div>
                  <Row k="Logo" v={logo ? "custom" : "default"} />
                </Summary>
                <Summary title="Root account" onEdit={() => go(2)}>
                  <Row k="Name" v={form.name} />
                  <Row k="Email" v={form.email} />
                  <Row
                    k="Password"
                    v={"•".repeat(Math.min(form.password.length, 16))}
                  />
                </Summary>
              </div>
            )}

            {step === 3 && (
              <Callout icon={ClipboardCheck}>
                After you create it: add an SMTP server under{" "}
                <span className="text-fg">Settings → Email</span> so the
                built-in alerts reach you, an S3 destination under{" "}
                <span className="text-fg">Settings → Backups</span>, and your
                other servers under{" "}
                <span className="text-fg">Compute → Nodes</span>.
              </Callout>
            )}
            {error && <Alert>{error}</Alert>}
          </div>

          <footer className="border-line flex items-center justify-between gap-2 border-t px-3 py-3 sm:px-5">
            {step > 0 ? (
              <Button
                type="button"
                variant="ghost"
                onClick={() => go(step - 1)}
                disabled={busy}
              >
                <ArrowLeft className="size-3.5" /> Back
              </Button>
            ) : (
              <span />
            )}
            <Button
              type="submit"
              variant="primary"
              disabled={busy}
              className="h-8 px-4"
            >
              {step < steps.length - 1 ? (
                <>
                  Continue <ArrowRight className="size-3.5" />
                </>
              ) : busy ? (
                "Creating workspace…"
              ) : (
                <>
                  <Check className="size-3.5" /> Create workspace
                </>
              )}
            </Button>
          </footer>
        </form>
      </div>
    </div>
  );
}

const stepTitle = [
  "Verify this server",
  "Your workspace",
  "Root account",
  "Review and create",
];
const stepIntro = [
  "Only someone with access to the controller's log can finish setup.",
  "How the dashboard, browser tab and alert emails present your workspace.",
  "The first administrator. More users come later, under IAM.",
  "Check the details. Everything except the account can be changed under Settings → General.",
];

function Callout({
  icon: Icon,
  children,
  className,
}: {
  icon: typeof Lock;
  children: ReactNode;
  className?: string;
}) {
  return (
    <div
      className={cn(
        "bg-raised border-line flex gap-2.5 rounded-sm border p-2.5 text-xs leading-relaxed",
        className,
      )}
    >
      <Icon className="text-accent mt-0.5 size-4 shrink-0" />
      <div className="text-muted min-w-0">{children}</div>
    </div>
  );
}

function PasswordCheck({
  password,
  confirm,
}: {
  password: string;
  confirm: string;
}) {
  const s = strength(password);
  const label = ["", "Too short", "Fair", "Strong", "Very strong"][s];
  const checks = [
    {
      ok: password.length >= MIN_PASSWORD,
      text: `${MIN_PASSWORD}+ characters`,
    },
    {
      ok: /[A-Za-z]/.test(password) && /[^A-Za-z]/.test(password),
      text: "letters and digits or symbols",
    },
    { ok: !!confirm && password === confirm, text: "passwords match" },
  ];
  return (
    <div className="flex flex-col gap-2">
      <div className="flex items-center gap-2">
        <div className="grid flex-1 grid-cols-4 gap-1" aria-hidden>
          {[1, 2, 3, 4].map((i) => (
            <span
              key={i}
              className={cn(
                "h-1 rounded-sm",
                i <= s
                  ? s <= 1
                    ? "bg-bad"
                    : s === 2
                      ? "bg-warn"
                      : "bg-ok"
                  : "bg-line-strong",
              )}
            />
          ))}
        </div>
        <span className="text-muted w-20 text-right text-[11px]">{label}</span>
      </div>
      <ul className="flex flex-wrap gap-x-4 gap-y-1 text-[11px]">
        {checks.map((c) => (
          <li
            key={c.text}
            className={cn(
              "flex items-center gap-1",
              c.ok ? "text-ok" : "text-faint",
            )}
          >
            <Check className={cn("size-3", !c.ok && "opacity-40")} /> {c.text}
          </li>
        ))}
      </ul>
    </div>
  );
}

function MiniPreview({ branding, icon }: { branding: Branding; icon: string }) {
  return (
    <div className="border-line-strong overflow-hidden rounded-sm border">
      <div className="bg-bg flex items-center gap-1.5 px-2 py-1 text-[11px]">
        <img src={icon} alt="" className="size-3.5 shrink-0 rounded-[2px]" />
        <span className="min-w-0 truncate">{branding.title}</span>
      </div>
      <div className="bg-surface border-line-strong flex h-9 items-center border-t px-2.5 text-xs">
        <BrandMark branding={branding} logo={icon} />
      </div>
    </div>
  );
}

function Summary({
  title,
  onEdit,
  children,
}: {
  title: string;
  onEdit: () => void;
  children: ReactNode;
}) {
  return (
    <section className="bg-raised border-line flex flex-col gap-1 rounded-sm border p-2.5">
      <div className="mb-1 flex items-center justify-between">
        <h3 className="text-faint text-[11px] font-medium tracking-wide uppercase">
          {title}
        </h3>
        <button
          type="button"
          onClick={onEdit}
          className="text-accent text-[11px] hover:underline"
        >
          Edit
        </button>
      </div>
      {children}
    </section>
  );
}

function Row({ k, v, mono }: { k: string; v: string; mono?: boolean }) {
  return (
    <div className="flex items-baseline justify-between gap-3 text-xs">
      <span className="text-muted shrink-0">{k}</span>
      <span
        className={cn("min-w-0 truncate text-right", mono && "font-mono")}
        title={v}
      >
        {v}
      </span>
    </div>
  );
}
