// A person manages domains and certificates in a real browser (Phase 28):
// connects Cloudflare, registers domains (with a provider and without),
// points names at the cluster, requests wildcard certificates (DNS-01
// written by SynCloud, and by hand with the TXT record checked live),
// imports a provider's certificate, adds a certificate authority, chooses a
// domain and subdomain for a service, and meets what a teammate without the
// key-export permission sees. The outside world is domainsim: a Cloudflare,
// a DNS server and an ACME CA that agree with each other.
//
//   test/e2e/domains.sh                       # in the Docker lab
//   UI_URL=http://127.0.0.1:7071 SIM_URL=http://127.0.0.1:9102 ACME_DIRECTORY=http://127.0.0.1:9101/directory node domains.mjs
import { execFileSync } from "node:child_process";
import { mkdtempSync, readFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { BASE, env, launch, person, shot, step, until } from "./lib.mjs";

const EMAIL = env("UI_EMAIL", "e2e@example.com");
const PASSWORD = env("UI_PASSWORD", "e2e-password-123");
const SIM = env("SIM_URL", "http://127.0.0.1:9102");
const CF_TOKEN = "cf-test-token";
// The fake CA of domainsim (a second authority is added with this directory).
const ACME_DIRECTORY = env("ACME_DIRECTORY", "http://127.0.0.1:9101/directory");

const browser = await launch();
const admin = await person(browser, "admin");

// ── what a person's hands do ─────────────────────────────────────────────────

async function signIn(page, email, password) {
  await page.goto(`${BASE}/login`);
  await page.getByLabel("Email").fill(email);
  await page.getByLabel("Password").fill(password);
  await page.getByRole("button", { name: /sign in|log in/i }).click();
  await page.waitForURL((u) => !u.pathname.startsWith("/login"));
}

/** The API as the admin (for what a person would set up beforehand). */
let cookie = "";
async function adminApi(method, path, body) {
  if (!cookie) {
    const r = await fetch(`${BASE}/api/v1/auth/login`, { method: "POST", headers: { "content-type": "application/json", Origin: BASE }, body: JSON.stringify({ email: EMAIL, password: PASSWORD }) });
    cookie = (r.headers.getSetCookie?.() ?? []).map((c) => c.split(";")[0]).join("; ");
  }
  const r = await fetch(`${BASE}/api/v1${path}`, { method, headers: { "content-type": "application/json", Origin: BASE, cookie }, body: body === undefined ? undefined : JSON.stringify(body) });
  const text = await r.text();
  if (!r.ok) throw new Error(`${method} ${path}: ${r.status} ${text}`);
  return text ? JSON.parse(text) : null;
}

async function sim(method, path, body) {
  const r = await fetch(`${SIM}${path}`, { method, body: body === undefined ? undefined : JSON.stringify(body) });
  const text = await r.text();
  if (!r.ok) throw new Error(`sim ${method} ${path}: ${r.status} ${text}`);
  return text ? JSON.parse(text) : null;
}

const next = (page) => page.getByRole("button", { name: /^Next/ });

/** Picks a value from an image or tag picker that has nothing to list (no registry). */
async function pickTyped(page, label, searchPlaceholder, value) {
  await page.getByRole("combobox", { name: label, exact: true }).click();
  await page.getByPlaceholder(searchPlaceholder).fill(value);
  const option = page.locator(`[role=option][title="${value}"]`);
  if (await option.count()) await option.first().click();
  else await page.getByRole("button", { name: new RegExp(`Use ${value.replace(".", "\\.")} as it is`) }).click();
}
/** The domain picker lists domains alphabetically and starts on the first. */
async function chooseDomain(page, name) {
  await page.getByRole("combobox", { name: "Domain", exact: true }).click();
  await page.getByRole("option", { name, exact: true }).click();
}
const link = (page, name) => page.getByRole("link", { name, exact: true });
const dismissErrors = (page) => (page.errors.length = 0);

function openssl(...args) {
  return execFileSync("openssl", args, { encoding: "utf8", stdio: ["ignore", "pipe", "pipe"] });
}

/** A certificate "from a provider": a CA, an intermediate and a leaf for names. */
function providerCertificate(...names) {
  const d = mkdtempSync(join(tmpdir(), "syn-pki-"));
  const f = (n) => join(d, n);
  openssl("req", "-x509", "-newkey", "ec", "-pkeyopt", "ec_paramgen_curve:prime256v1", "-nodes", "-keyout", f("root.key"), "-out", f("root.crt"), "-subj", "/CN=Provider Root", "-days", "3650");
  openssl("req", "-newkey", "ec", "-pkeyopt", "ec_paramgen_curve:prime256v1", "-nodes", "-keyout", f("leaf.key"), "-out", f("leaf.csr"), "-subj", `/CN=${names[0]}`);
  const ext = `subjectAltName=${names.map((n) => `DNS:${n}`).join(",")}\nextendedKeyUsage=serverAuth\n`;
  execFileSync("sh", ["-c", `printf '%s' "${ext}" > ${f("ext.cnf")}`]);
  openssl("x509", "-req", "-in", f("leaf.csr"), "-CA", f("root.crt"), "-CAkey", f("root.key"), "-CAcreateserial", "-out", f("leaf.crt"), "-days", "365", "-extfile", f("ext.cnf"));
  return { chain: readFileSync(f("leaf.crt"), "utf8") + readFileSync(f("root.crt"), "utf8"), key: readFileSync(f("leaf.key"), "utf8") };
}

// ── the journey ──────────────────────────────────────────────────────────────

await step(admin, "an administrator signs in and opens Domains: nothing is registered yet", async () => {
  await signIn(admin, EMAIL, PASSWORD);
  dismissErrors(admin);
  await admin.goto(`${BASE}/domains`);
  await admin.getByText("No domains yet").waitFor();
  await shot(admin, "domains-empty");
});

await step(admin, "Cloudflare refuses a wrong token, accepts the right one and offers its zones", async () => {
  await admin.getByRole("main").getByRole("link", { name: /DNS providers/ }).click();
  await admin.getByText("No DNS provider").first().waitFor();
  await admin.getByRole("main").getByRole("link", { name: /Connect Cloudflare/ }).click();
  await admin.getByLabel("API token").fill("not-the-token");
  await admin.getByRole("button", { name: "Connect" }).click();
  await admin.getByText(/token was refused/i).waitFor();
  dismissErrors(admin); // the 400 was the right answer
  await admin.getByLabel("API token").fill(CF_TOKEN);
  await admin.getByRole("button", { name: "Connect" }).click();
  await admin.getByText("is connected").waitFor();
  await admin.getByText("example.test").first().waitFor();
  await shot(admin, "provider-zones");
});

await step(admin, "the domain is registered with the wizard: provider, zone, access, certificate policy", async () => {
  await admin.locator("tr", { hasText: "example.test" }).getByRole("link", { name: /Register/ }).click();
  await admin.getByRole("heading", { name: "Add a domain" }).waitFor();
  await admin.getByRole("radio", { name: /Cloudflare/ }).waitFor();
  await next(admin).click(); // Provider (chosen from the link)
  await admin.getByText("Zone at Cloudflare").waitFor();
  await next(admin).click(); // Domain
  await admin.getByText("All projects").first().waitFor();
  await shot(admin, "add-domain-access");
  await next(admin).click(); // Access
  await admin.getByText("Keep one wildcard certificate").waitFor();
  await next(admin).click(); // Certificates
  await admin.getByText("SynCloud writes the records").waitFor();
  await admin.getByRole("button", { name: "Add domain" }).click();
  await admin.waitForURL(/\/domains\/example\.test$/);
  await admin.getByText("Name servers").waitFor();
  await admin.getByText("ada.ns.cloudflare.test").waitFor();
  await shot(admin, "domain-overview");
});

await step(admin, "records already in the zone are listed; a name is pointed at the cluster without duplicates", async () => {
  // Someone made the same record twice, and another one for the name.
  for (const [type, name, content] of [["A", "app.example.test", "203.0.113.1"], ["A", "app.example.test", "203.0.113.1"], ["A", "app.example.test", "198.51.100.7"], ["MX", "example.test", "mail.example.test"]]) {
    await sim("POST", "/sim/records", { zone: "example.test", type, name, content });
  }
  await admin.getByRole("tab", { name: "Records" }).click();
  await admin.getByText("198.51.100.7").waitFor();
  await admin.getByRole("button", { name: /Point at the cluster/ }).click();
  await admin.getByRole("dialog").getByPlaceholder("@").fill("shop");
  await admin.getByText("What would change").waitFor();
  await admin.getByRole("dialog").getByText("shop.example.test").first().waitFor(); // the preview is for this name
  await shot(admin, "point-plan");
  await admin.getByRole("button", { name: /^Apply/ }).click();
  await admin.getByText(/Done: 1 created/).waitFor();
  await admin.keyboard.press("Escape");
  await admin.getByText("SynCloud").first().waitFor(); // the record it made is marked
});

await step(admin, "clean up finds the duplicate and removes only what is safe", async () => {
  await admin.getByRole("button", { name: /Clean up/ }).click();
  await admin.getByText("Duplicates (1)").waitFor();
  await admin.getByRole("button", { name: /^Remove 1$/ }).click();
  await admin.getByText("Removed 1.").waitFor();
  await admin.keyboard.press("Escape");
});

await step(admin, "a record can be added; the same record twice is refused with a reason", async () => {
  const dlg = admin.getByRole("dialog");
  await admin.getByRole("button", { name: /Add record/ }).click();
  await dlg.getByPlaceholder("@").fill("api");
  await dlg.getByLabel(/^Content/).fill("203.0.113.9");
  await dlg.getByRole("button", { name: "Save" }).click();
  await admin.getByText("203.0.113.9").waitFor();
  await admin.getByRole("button", { name: /Add record/ }).click();
  await dlg.getByPlaceholder("@").fill("api");
  await dlg.getByLabel(/^Content/).fill("203.0.113.9");
  await dlg.getByRole("button", { name: "Save" }).click();
  await admin.getByText(/same record already exists/).waitFor();
  dismissErrors(admin); // the 409 was the answer
  await admin.getByRole("button", { name: "Cancel" }).click();
});

await step(admin, "a wildcard certificate is requested: the TXT records are written and removed by SynCloud", async () => {
  await admin.goto(`${BASE}/domains/certificates/new?domain=example.test`);
  await admin.getByText("*.example.test").first().waitFor(); // the domain and its wildcard are preset
  await admin.getByRole("button", { name: "*.sub.example.test" }).count(); // quick add is for registered domains only
  await admin.getByLabel("Add a name").fill("*.sub.example.test");
  await admin.getByRole("button", { name: "Add", exact: true }).click();
  await next(admin).click(); // Names
  await admin.getByText("Let's Encrypt").first().waitFor();
  await next(admin).click(); // Authority
  await admin.getByText("_acme-challenge.example.test").waitFor();
  await admin.getByText("_acme-challenge.sub.example.test").waitFor(); // the nested wildcard has its own name
  await admin.getByText("written by SynCloud").first().waitFor();
  await shot(admin, "request-validation");
  await next(admin).click(); // Validation
  await next(admin).click(); // Key
  await admin.getByRole("button", { name: "Request certificate" }).click();
  await admin.waitForURL(/\/domains\/certificates\/cert_/);
  await until("the certificate to be valid", async () => {
    await admin.reload();
    // Wait for the page to render after the reload: counting at once races it.
    return admin.getByText("valid", { exact: true }).first().waitFor({ timeout: 3000 }).then(() => true, () => false);
  }, 120);
  await shot(admin, "certificate-valid");
  await admin.getByRole("tab", { name: "Details" }).click();
  await admin.getByText("SHA-256").waitFor();
  await admin.getByRole("tab", { name: "History" }).click();
  await admin.getByText("issued", { exact: true }).first().waitFor();
  // The challenge records are gone from the zone.
  const left = Object.values(await sim("GET", "/sim/records")).flat().filter((r) => r.name.startsWith("_acme-challenge"));
  if (left.length) throw new Error(`challenge records were left: ${JSON.stringify(left)}`);
});

await step(admin, "a manual domain: the TXT record is shown, checked live, and the certificate follows", async () => {
  await admin.goto(`${BASE}/domains/new`);
  await admin.getByRole("radio", { name: /Manual/ }).click();
  await next(admin).click();
  await admin.getByPlaceholder("example.com").fill("manual.test");
  await next(admin).click();
  await next(admin).click();
  await next(admin).click();
  await admin.getByRole("button", { name: "Add domain" }).click();
  await admin.waitForURL(/\/domains\/manual\.test$/);
  await admin.getByRole("tab", { name: "Records" }).click();
  await admin.getByText("Records to create at your DNS host").waitFor();
  await admin.getByRole("button", { name: "Show the records" }).click();
  await admin.getByText("A", { exact: true }).first().waitFor();

  await admin.goto(`${BASE}/domains/certificates/new`);
  await admin.getByLabel("Add a name").fill("*.sub.manual.test");
  await admin.getByRole("button", { name: "Add", exact: true }).click();
  for (let i = 0; i < 2; i++) await next(admin).click(); // Names, Authority
  await admin.getByText("you add it").waitFor(); // Validation: no provider on this domain
  for (let i = 0; i < 2; i++) await next(admin).click(); // Validation, Key
  await admin.getByRole("button", { name: "Request certificate" }).click();
  await admin.waitForURL(/\/domains\/certificates\/cert_/);
  await until("the order to wait for DNS", async () => {
    await admin.reload();
    return (await admin.getByText("waiting for DNS").count()) > 0;
  }, 120);
  await admin.getByRole("tab", { name: "Challenge" }).click();
  await admin.getByText("Add this TXT record at your DNS host").waitFor();
  await admin.getByText("_acme-challenge.sub.manual.test").first().waitFor();
  await shot(admin, "challenge-manual");
  // Not there yet: Verify says so.
  await admin.getByRole("button", { name: "Verify now" }).click();
  await admin.getByText("not seen yet").first().waitFor();
  // The person adds the record at their DNS host (the value is on the page).
  const values = await admin.locator("code").allInnerTexts();
  const value = values.find((v) => /^[A-Za-z0-9_-]{43}$/.test(v.trim()));
  if (!value) throw new Error(`no TXT value on the page: ${values}`);
  await sim("POST", "/sim/records", { zone: "manual.test", type: "TXT", name: "_acme-challenge.sub.manual.test", content: value.trim() });
  await admin.getByRole("button", { name: "Verify now" }).click();
  await until("the certificate to be issued", async () => {
    await admin.reload();
    // Wait for the page to render after the reload: counting at once races it.
    return admin.getByText("valid", { exact: true }).first().waitFor({ timeout: 3000 }).then(() => true, () => false);
  }, 60);
  await shot(admin, "manual-valid");
});

await step(admin, "a manual domain delegates _acme-challenge with one CNAME: the order waits for it, then everything is automatic", async () => {
  await admin.goto(`${BASE}/domains/new`);
  await admin.getByRole("radio", { name: /Manual/ }).click();
  await next(admin).click();
  await admin.getByPlaceholder("example.com").fill("deleg.test");
  await next(admin).click(); // Access
  await next(admin).click(); // Certificates
  await admin.getByRole("radio", { name: /Another domain you own/ }).click();
  await admin.getByRole("combobox", { name: "Validation domain" }).click();
  await admin.getByRole("option", { name: "example.test" }).click();
  await next(admin).click(); // Review
  await admin.getByText("Delegated by a CNAME to example.test").waitFor();
  await admin.getByRole("button", { name: "Add domain" }).click();
  await admin.waitForURL(/\/domains\/deleg\.test$/);
  await admin.getByText("via example.test").first().waitFor(); // the overview says how it is answered

  await admin.getByRole("tab", { name: "Challenge" }).click();
  await admin.getByText("Check a name").waitFor();
  await admin.getByText("not created yet").waitFor(); // a manual domain: the person makes the CNAME
  await admin.getByText(/This domain has no provider, so create this one record/).waitFor();
  const codes = await admin.locator("code").allInnerTexts();
  const target = codes.map((c) => c.trim()).find((c) => /^[0-9a-f]{12}\.acme\.example\.test$/.test(c));
  if (!target) throw new Error(`no CNAME target on the page: ${codes}`);
  await shot(admin, "challenge-delegated");

  // A wildcard is proved at the same name: asking about it shows the same CNAME.
  await admin.getByLabel("Host to check").fill("*.deleg.test");
  await admin.getByText("_acme-challenge.deleg.test").first().waitFor();
  await admin.getByLabel("Host to check").fill("*.shop.deleg.test"); // a nested wildcard has its own name and target
  await admin.getByText("_acme-challenge.shop.deleg.test").first().waitFor();
  await admin.getByLabel("Host to check").fill("");

  // The certificate is requested before the CNAME exists: the order waits, and says what to do.
  await admin.goto(`${BASE}/domains/certificates/new?domain=deleg.test`);
  await admin.getByText("*.deleg.test").first().waitFor();
  for (let i = 0; i < 2; i++) await next(admin).click(); // Names, Authority
  await admin.getByText("written by SynCloud via example.test").waitFor(); // Validation
  for (let i = 0; i < 2; i++) await next(admin).click(); // Validation, Key
  await admin.getByRole("button", { name: "Request certificate" }).click();
  await admin.waitForURL(/\/domains\/certificates\/cert_/);
  await until("the order to wait for the CNAME", async () => {
    await admin.reload();
    return (await admin.getByText("waiting for DNS").count()) > 0;
  }, 120);
  await admin.getByRole("tab", { name: "Challenge" }).click();
  await admin.getByText("Create this CNAME once at your DNS host.").waitFor();
  await admin.getByText(target).first().waitFor();
  await shot(admin, "challenge-delegated-order");

  // The person creates the CNAME at their DNS host, once. Nothing else.
  await sim("POST", "/sim/records", { zone: "deleg.test", type: "CNAME", name: "_acme-challenge.deleg.test", content: target });
  await admin.getByRole("button", { name: "Verify now" }).click();
  await until("the certificate to be issued through the CNAME", async () => {
    await admin.reload();
    // Wait for the page to render after the reload: counting at once races it.
    return admin.getByText("valid", { exact: true }).first().waitFor({ timeout: 3000 }).then(() => true, () => false);
  }, 90);
  // SynCloud wrote its TXT in the validation domain and removed it again.
  const recs = Object.values(await sim("GET", "/sim/records")).flat();
  if (recs.some((r) => r.type === "TXT" && r.name.endsWith(".acme.example.test"))) throw new Error("the delegated TXT record was left behind");
  if (!recs.some((r) => r.type === "CNAME" && r.name === "_acme-challenge.deleg.test")) throw new Error("the CNAME is gone");

  // Renewing needs nobody: the order finishes by itself.
  await admin.getByRole("button", { name: /Renew now/ }).click();
  await until("the renewal to finish by itself", async () => {
    await admin.reload();
    await admin.getByRole("tab", { name: "History" }).click();
    return admin.getByText("renewed", { exact: true }).first().waitFor({ timeout: 4000 }).then(() => true, () => false);
  }, 90);
  await admin.goto(`${BASE}/domains/deleg.test`);
  await admin.getByRole("tab", { name: "Challenge" }).click();
  await admin.getByText("in place").first().waitFor();
  await shot(admin, "challenge-delegated-ok");
});

await step(admin, "a certificate from a provider is imported: checked, listed, with its warning", async () => {
  const bad = providerCertificate("shop.example.test");
  const other = providerCertificate("x.example.test");
  await admin.goto(`${BASE}/domains/certificates/import`);
  await admin.getByPlaceholder("-----BEGIN CERTIFICATE-----").fill(bad.chain);
  await admin.getByPlaceholder("-----BEGIN PRIVATE KEY-----").fill(other.key); // the wrong key
  await admin.getByRole("button", { name: "Import certificate" }).click();
  await admin.getByText(/must be a pair|does not belong/).waitFor();
  dismissErrors(admin); // the 400 was the answer
  await admin.getByPlaceholder("-----BEGIN PRIVATE KEY-----").fill(bad.key);
  await admin.getByRole("button", { name: "Import certificate" }).click();
  await admin.getByText("does not renew by itself").waitFor();
  await admin.getByText(/chain is incomplete|root is not one this server trusts/).waitFor(); // a private root: said, not hidden
  await shot(admin, "imported");
  await admin.getByRole("link", { name: "Open the certificate" }).click();
  await admin.getByText("Imported").first().waitFor();
});

await step(admin, "the SSL list shows every certificate, filtered by source", async () => {
  await admin.goto(`${BASE}/domains/certificates`);
  await admin.getByText("*.sub.example.test").first().waitFor();
  await admin.getByText("*.sub.manual.test").first().waitFor();
  await admin.getByText("shop.example.test").first().waitFor();
  await admin.getByRole("combobox", { name: "Source" }).click();
  await admin.getByRole("option", { name: "Imported" }).click();
  await admin.getByText("shop.example.test").first().waitFor();
  if ((await admin.getByText("*.sub.manual.test").count()) !== 0) throw new Error("the filter did not apply");
  await shot(admin, "ssl-list");
});

await step(admin, "another authority is added with its binding, tested, and made the fallback", async () => {
  await admin.goto(`${BASE}/domains/authorities/new`);
  await admin.getByRole("radio", { name: /Other ACME authority/ }).click();
  await admin.getByLabel("Name", { exact: true }).fill("Provider CA");
  await admin.getByLabel("Directory URL").fill(ACME_DIRECTORY);
  await admin.getByText("asks for an external account binding").click();
  await admin.getByLabel(/^Key id/).fill("kid-123");
  await admin.getByLabel(/^HMAC key/).fill("c2VjcmV0LWhtYWM");
  await admin.getByRole("button", { name: "Add authority" }).click();
  await admin.waitForURL(/\/domains\/authorities$/);
  const row = admin.locator("tr", { hasText: "Provider CA" });
  await row.waitFor();
  await row.getByRole("button", { name: /Test/ }).click();
  await admin.getByText(/the account is registered/).waitFor();
  await row.getByRole("button", { name: "fallback" }).click();
  await admin.getByText(/Fallback:/).waitFor();
  await shot(admin, "authorities");
});

await step(admin, "the lookup page asks the DNS what the record answers", async () => {
  await admin.goto(`${BASE}/domains/lookup`);
  await admin.getByLabel("Name").fill("shop.example.test");
  await admin.getByRole("button", { name: "Look up" }).click();
  await admin.getByText("answered").first().waitFor();
  const ip = (await sim("GET", "/sim/records"))["example.test"].find((r) => r.name === "shop.example.test")?.content;
  if (!ip) throw new Error("the point step did not create shop.example.test");
  await admin.getByText(ip).first().waitFor();
});

await step(admin, "a service takes a subdomain and a wildcard from the picker; the wildcard certificate covers it", async () => {
  await adminApi("POST", "/projects", { name: "shop" });
  await adminApi("PUT", "/projects/shop/environments/production/services/web", {
    image: "busybox:1.37", ports: [{ name: "http", container: 8080 }], resources: { cpu: 0.05, memory: 16 }, desiredCount: 0,
  });
  await admin.goto(`${BASE}/projects/shop/production/services/web`);
  await admin.getByRole("tab", { name: "Networking" }).or(admin.getByText("Networking", { exact: true })).first().click();
  await admin.getByRole("heading", { name: "Custom domains" }).waitFor();
  await chooseDomain(admin, "example.test");
  await admin.getByLabel("Subdomain (empty = the domain itself)").fill("web");
  await admin.getByText(/covered by .*example\.test.*valid until/).waitFor();
  await shot(admin, "picker-exact");
  await admin.getByRole("button", { name: "Add domain" }).click();
  await admin.locator("tr", { hasText: "web.example.test" }).waitFor();
  // A wildcard below a subdomain: its certificate exists too.
  await admin.getByRole("switch", { name: /Wildcard/ }).click();
  await admin.getByLabel(/Subdomain \(the wildcard sits below it\)/).fill("sub");
  await admin.getByText(/matches one label/).waitFor();
  await admin.getByText(/covered by/).waitFor();
  await admin.getByRole("button", { name: "Add domain" }).click();
  await admin.locator("tr", { hasText: "*.sub.example.test" }).waitFor();
  await shot(admin, "picker-wildcard");
  // The wildcard is beaten by the exact host in Traefik's rules; here: both are listed.
  const items = (await adminApi("GET", "/projects/shop/environments/production/services/web/domains")).items;
  if (!items.some((d) => d.host === "*.sub.example.test" && d.wildcard && d.certificate.state === "covered")) throw new Error(JSON.stringify(items));
});

await step(admin, "the new-service wizard takes a domain from the picker and adds it right after the service", async () => {
  await admin.goto(`${BASE}/projects/shop/production/new-service`);
  await admin.getByText("Container image").first().waitFor();
  await pickTyped(admin, "Image", "Search images…", "busybox");
  await pickTyped(admin, "Tag", "Search tags…", "1.37");
  await next(admin).click(); // Basics
  await admin.getByPlaceholder("api").fill("storefront");
  await next(admin).click(); // Network
  await admin.getByText("Domain (optional)").waitFor();
  await admin.getByLabel("Container port").first().fill("8080");
  await chooseDomain(admin, "example.test");
  await admin.getByLabel("Subdomain (empty = the domain itself)").fill("store");
  await admin.getByText(/covered by .*example\.test.*valid until/).waitFor();
  await shot(admin, "wizard-domain");
  await next(admin).click(); // Resources
  await next(admin).click(); // Variables
  await next(admin).click(); // Deploy
  await next(admin).click(); // Review
  await admin.getByText("domain: https://store.example.test").waitFor();
  await admin.getByRole("button", { name: /Create and deploy/ }).click();
  await admin.waitForURL(/services\/storefront$/);
  const items = (await adminApi("GET", "/projects/shop/environments/production/services/storefront/domains")).items;
  if (!items.some((d) => d.host === "store.example.test" && d.certificate.state === "covered")) throw new Error(JSON.stringify(items));
});

await step(admin, "a name outside the domain's grants is refused with the reason", async () => {
  await adminApi("PUT", "/domains/example.test", { grants: [{ project: "blog", wildcards: false }] });
  const r = await fetch(`${BASE}/api/v1/projects/shop/environments/production/services/web/domains`, { method: "POST", headers: { "content-type": "application/json", Origin: BASE, cookie }, body: JSON.stringify({ host: "late.example.test" }) });
  if (r.status !== 403 || !/not granted/.test(await r.text())) throw new Error(`expected a refusal, got ${r.status}`);
  await adminApi("PUT", "/domains/example.test", { grants: [{ project: "*", wildcards: true }] });
});

await step(admin, "SynCloud's own challenge server: one click on (sslip.io delegates its zone, nothing to create), checked from the internet; a domain without a provider renews by itself after one CNAME", async () => {
  await admin.goto(`${BASE}/domains/challenge-server`);
  await admin.getByText("Turn it on").waitFor();
  await shot(admin, "challenge-server-off");
  // The dev controller's detected address is local: give the public one, as behind NAT.
  await admin.getByRole("button", { name: /Behind NAT/ }).click();
  await admin.getByLabel("Public address").fill("203.0.113.9");
  await admin.getByRole("button", { name: "Turn on", exact: true }).click();
  await admin.getByText("Check from the internet").waitFor();
  await admin.getByText("_acme-challenge.203-0-113-9.sslip.io").first().waitFor();
  await admin.getByText("delegated by sslip.io").waitFor();
  if ((await admin.getByText("Records to create").count()) > 0) throw new Error("there should be no records to create");
  await admin.getByRole("button", { name: "Check it works" }).click();
  await admin.getByText("Everything works.").waitFor();
  await shot(admin, "challenge-server-ok");

  // A domain without a provider: the server answers by default. One CNAME, once.
  await admin.goto(`${BASE}/domains/new`);
  await admin.getByRole("radio", { name: /Manual/ }).click();
  await next(admin).click();
  await admin.getByPlaceholder("example.com").fill("builtin.test");
  await next(admin).click(); // Access
  await next(admin).click(); // Certificates
  await admin.getByRole("radio", { name: /SynCloud's challenge server/ }).waitFor(); // the default for a domain without a provider
  await next(admin).click(); // Review
  await admin.getByText("SynCloud's challenge server (one CNAME)").waitFor();
  await admin.getByRole("button", { name: "Add domain" }).click();
  await admin.waitForURL(/\/domains\/builtin\.test$/);
  await admin.getByText("by SynCloud").first().waitFor();
  await admin.getByRole("tab", { name: "Challenge" }).click();
  await admin.getByText("not created yet").waitFor();
  const codes = await admin.locator("code").allInnerTexts();
  const target = codes.map((c) => c.trim()).find((c) => /^[0-9a-f]{12}\._acme-challenge\.203-0-113-9\.sslip\.io$/.test(c));
  if (!target) throw new Error(`no CNAME target on the page: ${codes}`);
  await shot(admin, "challenge-builtin");

  await admin.goto(`${BASE}/domains/certificates/new?domain=builtin.test`);
  await admin.getByText("*.builtin.test").first().waitFor();
  for (let i = 0; i < 2; i++) await next(admin).click(); // Names, Authority
  await admin.getByText("written by SynCloud via SynCloud's challenge server").waitFor(); // Validation
  for (let i = 0; i < 2; i++) await next(admin).click(); // Validation, Key
  await admin.getByRole("button", { name: "Request certificate" }).click();
  await admin.waitForURL(/\/domains\/certificates\/cert_/);
  await until("the order to wait for the CNAME", async () => {
    await admin.reload();
    return (await admin.getByText("waiting for DNS").count()) > 0;
  }, 120);
  await admin.getByRole("tab", { name: "Challenge" }).click();
  await admin.getByText("Create this CNAME once at your DNS host.").waitFor();
  await admin.getByText(target).first().waitFor();
  await sim("POST", "/sim/records", { zone: "builtin.test", type: "CNAME", name: "_acme-challenge.builtin.test", content: target });
  await admin.getByRole("button", { name: "Verify now" }).click();
  await until("the certificate to be issued by SynCloud's own server", async () => {
    await admin.reload();
    // Wait for the page to render after the reload: counting at once races it.
    return admin.getByText("valid", { exact: true }).first().waitFor({ timeout: 3000 }).then(() => true, () => false);
  }, 90);
  const held = (await adminApi("GET", "/domains/challenge-server")).records;
  if (held !== 0) throw new Error(`the server still holds ${held} challenge value(s)`);
  // Renewing needs nobody.
  await admin.getByRole("button", { name: /Renew now/ }).click();
  await until("the renewal to finish by itself", async () => {
    await admin.reload();
    await admin.getByRole("tab", { name: "History" }).click();
    return admin.getByText("renewed", { exact: true }).first().waitFor({ timeout: 4000 }).then(() => true, () => false);
  }, 90);
  await admin.goto(`${BASE}/domains/builtin.test`);
  await admin.getByRole("tab", { name: "Challenge" }).click();
  await admin.getByText("in place").first().waitFor();
  await shot(admin, "challenge-builtin-ok");
});

// A teammate who edits the domain's records and certificates, but cannot export keys.
const ed = await adminApi("POST", "/iam/users", { email: "ed@example.com", name: "Ed", password: "ed-password-12345" });
await adminApi("POST", "/iam/attachments", { principalType: "user", principalId: ed.id, policy: "DomainEditor:example.test" });
const teammate = await person(browser, "ed");
await step(teammate, "a teammate who edits one domain sees it, but not the key export or other domains", async () => {
  await signIn(teammate, "ed@example.com", "ed-password-12345");
  dismissErrors(teammate);
  await teammate.goto(`${BASE}/domains/example.test`);
  await teammate.getByText("Name servers").waitFor();
  await teammate.getByRole("tab", { name: "Certificates" }).click();
  await teammate.locator("tr", { hasText: "*.sub.example.test" }).getByRole("link").first().click();
  await teammate.getByRole("tab", { name: "Details" }).click();
  await teammate.getByRole("button", { name: /Export key and chain/ }).click();
  await teammate.getByRole("dialog").getByRole("button", { name: "Export" }).click();
  await teammate.getByText(/may not export private keys/).waitFor();
  dismissErrors(teammate); // the 403 was the answer
  await shot(teammate, "editor-no-export");
});

const errors = [...admin.errors, ...teammate.errors];
if (errors.length) {
  console.error(`FAIL: the browser reported errors:\n${errors.join("\n")}`);
  process.exit(1);
}
await browser.close();
console.log("PASS");
