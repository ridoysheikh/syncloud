import { useRef, useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { FileKey, Trash2, Upload } from "lucide-react";
import { api } from "@/lib/api";
import { PageHeader } from "@/ui/PageHeader";
import { Panel } from "@/ui/Panel";
import { Tabs } from "@/ui/Tabs";
import { EmptyState } from "@/ui/EmptyState";
import { CopyBox } from "@/ui/CopyBox";
import { Alert, Button, Field, IconButton, Input } from "@/ui/controls";
import { Segmented } from "@/ui/choice";
import { Select } from "@/ui/select";
import { confirmAction } from "@/ui/dialogs";
import { since } from "@/lib/nodes";
import { cn, gap } from "@/ui/cn";
import { CertNames } from "./CertBits";
import { NamesEditor, nameProblem } from "./NamesEditor";
import { TextArea } from "./Wizard";
import {
  errText,
  invalidateAll,
  useCertificates,
  useDomains,
  type Certificate,
} from "./lib";

type Tab = "Paste" | "File (PKCS#12)" | "Make a request (CSR)";

interface Imported {
  certificate: Certificate;
  warnings: string[];
}

interface CertRequest {
  id: string;
  names: string[];
  keyType: string;
  csr: string;
  createdAt: string;
}

const b64 = (buf: ArrayBuffer) => {
  let s = "";
  const a = new Uint8Array(buf);
  for (let i = 0; i < a.length; i += 0x8000)
    s += String.fromCharCode(...a.subarray(i, i + 0x8000));
  return btoa(s);
};

/** The result of an import: the certificate, and what to know about it. */
function Done({ r }: { r: Imported }) {
  return (
    <Panel title="Imported">
      <div className="flex flex-col gap-2">
        <p className="text-xs">
          <CertNames names={r.certificate.names} max={20} /> is stored and
          serves the hosts it covers. It does not renew by itself: an alert
          warns {r.certificate.alertDays[0] ?? 30} days before it expires.
        </p>
        {r.warnings.map((w) => (
          <Alert key={w} tone="warn">
            {w}
          </Alert>
        ))}
        <div>
          <Link to={`/domains/certificates/${r.certificate.id}` as string}>
            <Button variant="primary">Open the certificate</Button>
          </Link>
        </div>
      </div>
    </Panel>
  );
}

function Paste({ replace }: { replace: string }) {
  const qc = useQueryClient();
  const [chain, setChain] = useState("");
  const [key, setKey] = useState("");
  const [pass, setPass] = useState("");
  const [rep, setRep] = useState(replace);
  const { data: certs = [] } = useCertificates();
  const imp = useMutation({
    mutationFn: () =>
      api<Imported>("POST", "/certificates/import", {
        chain,
        key,
        passphrase: pass || undefined,
        replace: rep || undefined,
      }),
    onSuccess: () => invalidateAll(qc),
  });
  const files =
    (set: (v: string) => void) =>
    async (e: React.ChangeEvent<HTMLInputElement>) => {
      const f = e.target.files?.[0];
      if (f) set(await f.text());
    };
  if (imp.data) return <Done r={imp.data} />;
  return (
    <Panel title="Certificate and private key">
      <div className="flex flex-col gap-3">
        <Field
          label="Certificate chain (PEM)"
          hint="Your certificate, then the provider's intermediate certificates. Any order is fine; a missing intermediate is fetched when the certificate says where."
        >
          <div className="flex flex-col gap-1">
            <TextArea
              value={chain}
              onChange={(e) => setChain(e.target.value)}
              rows={7}
              placeholder="-----BEGIN CERTIFICATE-----"
              spellCheck={false}
            />
            <label className="text-faint hover:text-fg inline-flex w-fit cursor-pointer items-center gap-1 text-xs">
              <Upload className="size-3" /> from a file…
              <input
                type="file"
                className="sr-only"
                accept=".pem,.crt,.cer,.txt"
                onChange={files(setChain)}
              />
            </label>
          </div>
        </Field>
        <Field
          label="Private key (PEM)"
          hint="It must belong to the certificate. It is sealed in the database and never shown again."
        >
          <div className="flex flex-col gap-1">
            <TextArea
              value={key}
              onChange={(e) => setKey(e.target.value)}
              rows={5}
              placeholder="-----BEGIN PRIVATE KEY-----"
              spellCheck={false}
              autoComplete="off"
            />
            <label className="text-faint hover:text-fg inline-flex w-fit cursor-pointer items-center gap-1 text-xs">
              <Upload className="size-3" /> from a file…
              <input
                type="file"
                className="sr-only"
                accept=".pem,.key,.txt"
                onChange={files(setKey)}
              />
            </label>
          </div>
        </Field>
        {key.includes("ENCRYPTED") && (
          <Field
            label="Key passphrase"
            hint="Used once to open the key; not stored."
          >
            <Input
              type="password"
              value={pass}
              onChange={(e) => setPass(e.target.value)}
              autoComplete="off"
              className="max-w-xs"
            />
          </Field>
        )}
        <Field
          label="Replace"
          hint="Keep the place of an earlier imported certificate: its pinned hosts and history stay."
        >
          <Select value={rep} onChange={setRep}>
            <option value="">A new certificate</option>
            {certs
              .filter((c) => c.source === "imported")
              .map((c) => (
                <option key={c.id} value={c.id}>
                  {c.names.join(", ")}
                </option>
              ))}
          </Select>
        </Field>
        {imp.error && <Alert>{errText(imp.error, "Could not import")}</Alert>}
        <div>
          <Button
            variant="primary"
            disabled={!chain.trim() || !key.trim() || imp.isPending}
            onClick={() => imp.mutate()}
          >
            {imp.isPending ? "Checking…" : "Import certificate"}
          </Button>
        </div>
      </div>
    </Panel>
  );
}

function Pkcs12() {
  const qc = useQueryClient();
  const file = useRef<HTMLInputElement>(null);
  const [name, setName] = useState("");
  const [data, setData] = useState("");
  const [password, setPassword] = useState("");
  const imp = useMutation({
    mutationFn: () =>
      api<Imported>("POST", "/certificates/import", { pkcs12: data, password }),
    onSuccess: () => invalidateAll(qc),
  });
  if (imp.data) return <Done r={imp.data} />;
  return (
    <Panel title="A .p12 / .pfx file">
      <div className="flex max-w-lg flex-col gap-3">
        <Field
          label="File"
          hint="Holds the certificate, its chain and its key, protected by a password."
        >
          <div className="flex items-center gap-2">
            <Button type="button" onClick={() => file.current?.click()}>
              <FileKey className="size-3.5" /> Choose a file
            </Button>
            <span className="text-muted text-xs">{name || "none chosen"}</span>
            <input
              ref={file}
              type="file"
              accept=".p12,.pfx"
              className="sr-only"
              onChange={async (e) => {
                const f = e.target.files?.[0];
                if (!f) return;
                setName(f.name);
                setData(b64(await f.arrayBuffer()));
              }}
            />
          </div>
        </Field>
        <Field label="Password">
          <Input
            type="password"
            value={password}
            onChange={(e) => setPassword(e.target.value)}
            autoComplete="off"
          />
        </Field>
        {imp.error && <Alert>{errText(imp.error, "Could not import")}</Alert>}
        <div>
          <Button
            variant="primary"
            disabled={!data || imp.isPending}
            onClick={() => imp.mutate()}
          >
            {imp.isPending ? "Opening…" : "Import certificate"}
          </Button>
        </div>
      </div>
    </Panel>
  );
}

/** A request for a provider to sign: the key is made here and never leaves. */
function Csr({ first }: { first: string }) {
  const qc = useQueryClient();
  const { data: domains = [] } = useDomains();
  const [names, setNames] = useState<string[]>(first ? [first] : []);
  const [keyType, setKeyType] = useState("ecdsa-p256");
  const [signed, setSigned] = useState<Record<string, string>>({});
  const [done, setDone] = useState<Imported | null>(null);
  const key = ["dm", "cert-requests"];
  const { data: pending = [], isLoading } = useQuery({
    queryKey: key,
    queryFn: async () =>
      (await api<{ items: CertRequest[] }>("GET", "/certificate-requests"))
        .items,
  });
  const create = useMutation({
    mutationFn: () =>
      api<CertRequest>("POST", "/certificate-requests", { names, keyType }),
    onSuccess: () => {
      setNames([]);
      void qc.invalidateQueries({ queryKey: key });
    },
  });
  const complete = useMutation({
    mutationFn: (id: string) =>
      api<Imported>("POST", `/certificate-requests/${id}/complete`, {
        chain: signed[id],
      }),
    onSuccess: (r) => {
      setDone(r);
      invalidateAll(qc);
      void qc.invalidateQueries({ queryKey: key });
    },
  });
  const discard = useMutation({
    mutationFn: (id: string) => api("DELETE", `/certificate-requests/${id}`),
    onSuccess: () => qc.invalidateQueries({ queryKey: key }),
  });
  return (
    <div className="flex flex-col gap-2">
      {done && <Done r={done} />}
      <Panel title="New request">
        <div className="flex flex-col gap-3">
          <p className="text-muted text-xs">
            Make a key and a certificate request (CSR) here, give the CSR to
            your provider, then upload what they sign. The private key never
            leaves SynCloud.
          </p>
          <NamesEditor value={names} onChange={setNames} domains={domains} />
          <Field label="Key type">
            <Segmented
              label="Key type"
              value={keyType}
              onChange={setKeyType}
              options={[
                { value: "ecdsa-p256", label: "ECDSA P-256" },
                { value: "rsa-2048", label: "RSA 2048" },
                { value: "rsa-3072", label: "RSA 3072" },
                { value: "rsa-4096", label: "RSA 4096" },
              ]}
            />
          </Field>
          {create.error && (
            <Alert>{errText(create.error, "Could not make the request")}</Alert>
          )}
          <div>
            <Button
              variant="primary"
              disabled={
                names.length === 0 ||
                names.some((n) => nameProblem(n)) ||
                create.isPending
              }
              onClick={() => create.mutate()}
            >
              Make the request
            </Button>
          </div>
        </div>
      </Panel>
      <Panel title="Waiting for a signed certificate" flush>
        {pending.length === 0 ? (
          <EmptyState icon={FileKey} title="No requests">
            {isLoading
              ? "Loading…"
              : "Requests you made appear here until their certificate is uploaded."}
          </EmptyState>
        ) : (
          <div className="divide-line divide-y">
            {pending.map((r) => (
              <div key={r.id} className="flex flex-col gap-2 p-3">
                <div className="flex flex-wrap items-center justify-between gap-2">
                  <span className="flex items-center gap-2 text-xs">
                    <CertNames names={r.names} max={4} />
                    <span className="text-faint">
                      {r.keyType} · {since(r.createdAt)}
                    </span>
                  </span>
                  <IconButton
                    label="Discard the request and its key"
                    onClick={async () =>
                      (await confirmAction(
                        "Discard this request? Its key is deleted.",
                      )) && discard.mutate(r.id)
                    }
                  >
                    <Trash2 className="size-3.5" />
                  </IconButton>
                </div>
                <Field label="CSR to give to your provider">
                  <CopyBox value={r.csr} wrap />
                </Field>
                <Field label="Signed certificate from your provider (PEM, with its chain)">
                  <TextArea
                    value={signed[r.id] ?? ""}
                    onChange={(e) =>
                      setSigned({ ...signed, [r.id]: e.target.value })
                    }
                    rows={4}
                    placeholder="-----BEGIN CERTIFICATE-----"
                    spellCheck={false}
                  />
                </Field>
                <div>
                  <Button
                    variant="primary"
                    disabled={
                      !(signed[r.id] ?? "").trim() || complete.isPending
                    }
                    onClick={() => complete.mutate(r.id)}
                  >
                    Upload the signed certificate
                  </Button>
                </div>
              </div>
            ))}
          </div>
        )}
      </Panel>
      {(complete.error || discard.error) && (
        <Alert>
          {errText(complete.error ?? discard.error, "The change failed")}
        </Alert>
      )}
    </div>
  );
}

export function ImportCertificatePage() {
  const query = new URLSearchParams(window.location.search);
  const [tab, setTab] = useState<Tab>("Paste");
  return (
    <div className={cn("flex flex-col", gap)}>
      <PageHeader
        crumbs={["Domains", "SSL certificates"]}
        title="Import a certificate"
      />
      <p className="text-muted text-xs">
        A certificate from DigiCert, Sectigo, your hosting company or any other
        provider. SynCloud checks that the key matches, the chain is complete
        and the certificate is not expired, serves it for the hosts it covers,
        and alerts before it expires. It cannot renew it for you.
      </p>
      <Tabs
        tabs={["Paste", "File (PKCS#12)", "Make a request (CSR)"] as const}
        value={tab}
        onChange={setTab}
        capitalize={false}
      />
      {tab === "Paste" && <Paste replace={query.get("replace") ?? ""} />}
      {tab === "File (PKCS#12)" && <Pkcs12 />}
      {tab === "Make a request (CSR)" && (
        <Csr first={query.get("domain") ?? ""} />
      )}
    </div>
  );
}
