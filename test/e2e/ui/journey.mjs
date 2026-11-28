// A person uses the dashboard (Phase 26 and 27): signs in, makes a project,
// starts a web service and a public TCP service with the new-service wizard,
// meets a port that is taken, looks the port up, and deletes the service.
// A teammate with a developer role does the same checks. Only what a person
// can do in a browser; the lab is asked only to prove the services answer.
//
//   test/e2e/ui.sh                      # in the Docker lab (nodes, Traefik)
//   UI_LAB=0 node journey.mjs           # against a dev controller: no tasks run
import { BASE, E2E, env, launch, lab, person, rootApi, shot, step, until } from "./lib.mjs";

const EMAIL = env("UI_EMAIL", "e2e@example.com");
const PASSWORD = env("UI_PASSWORD", "e2e-password-123");
const LAB = env("UI_LAB", "1") !== "0";
const TCP_PORT = 2525;

const browser = await launch();
const you = await person(browser, "you");

// ── helpers a person's hands do ──────────────────────────────────────────────

async function signIn(page, email, password) {
  await page.goto(`${BASE}/login`);
  await page.getByLabel("Email").fill(email);
  await page.getByLabel("Password").fill(password);
  await page.getByRole("button", { name: /sign in|log in/i }).click();
}

/** Picks a value from an image or tag picker that has nothing to list (no registry). */
async function pickTyped(page, label, searchPlaceholder, value) {
  await page.getByRole("combobox", { name: label, exact: true }).click();
  await page.getByPlaceholder(searchPlaceholder).fill(value);
  // A listed value carries itself as its title; else the typed text is used as it is.
  const option = page.locator(`[role=option][title="${value}"]`);
  if (await option.count()) await option.first().click();
  else await page.getByRole("button", { name: new RegExp(`Use ${value.replace(".", "\\.")} as it is`) }).click();
}

const next = (page) => page.getByRole("button", { name: /^Next/ });

/** The wizard up to the Network step, with a busybox image. */
async function toNetwork(page, name) {
  await page.goto(`${BASE}/projects/shop/production/new-service`);
  await page.getByText("Container image").first().waitFor();
  await pickTyped(page, "Image", "Search images…", "busybox");
  await pickTyped(page, "Tag", "Search tags…", "1.37");
  await next(page).click();
  await page.getByPlaceholder("api").fill(name);
  await next(page).click();
  await page.getByText("Public TCP/UDP").first().waitFor();
}

/** Deploy step: busybox runs this line. */
async function shellCommand(page, line) {
  await page.getByRole("radio", { name: "Shell command" }).click();
  await page.getByLabel("Shell command").fill(line);
}

async function serviceRunning(page, name) {
  await page.goto(`${BASE}/projects/shop/production/services/${name}`);
  await until(`${name} running`, async () => {
    await page.reload();
    return (await page.getByText("1/1", { exact: true }).count()) > 0;
  }, 120);
}

const portRow = (page, port) => page.locator("tr", { hasText: String(port) });

// ── the journey ──────────────────────────────────────────────────────────────

await step(you, "an unknown visitor is sent to the sign-in page", async () => {
  await you.goto(`${BASE}/projects`);
  await you.waitForURL(/\/login/);
});

await step(you, "a wrong password is refused with a reason, the right one signs in", async () => {
  await signIn(you, EMAIL, "not-the-password");
  await you.getByText("email or password is incorrect").waitFor();
  await signIn(you, EMAIL, PASSWORD);
  await you.waitForURL((u) => !u.pathname.startsWith("/login"));
  you.errors.length = 0; // the 401s of the visitor and the wrong password were the right answers
});

await step(you, "a project is created from the dashboard", async () => {
  await you.goto(`${BASE}/projects/new`);
  await you.getByPlaceholder("shop").fill("shop");
  await you.getByRole("button", { name: "Create project" }).click();
  await you.waitForURL(/\/projects\/shop/);
  await shot(you, "project");
});

await step(you, "a web service is created with the wizard: shared resources ask for no amount", async () => {
  await toNetwork(you, "site");
  // Public HTTP is the first card and the default: a port 8080 row, no public port.
  await you.getByLabel("Container port").first().fill("8080");
  await you.getByRole("radio", { name: "HTTP", exact: true }).last().click();
  await shot(you, "wizard-network-http");
  await next(you).click(); // Resources
  await you.getByText("Resources per task").waitFor();
  await you.getByText("Shares the node's cores").waitFor();
  if ((await you.getByText("Cores reserved per task").count()) !== 0) throw new Error("shared CPU asks for an amount");
  if ((await you.getByText("MiB reserved per task").count()) !== 0) throw new Error("shared memory asks for an amount");
  await shot(you, "wizard-resources-shared");
  await next(you).click(); // Variables
  await next(you).click(); // Deploy
  await shellCommand(you, "mkdir -p /www && echo hello-from-the-wizard > /www/index.html && exec httpd -f -p 8080 -h /www");
  await next(you).click(); // Review
  await you.getByText(/site-production-shop\./).first().waitFor();
  await you.getByText("shared CPU, shared memory, no limit").waitFor();
  await shot(you, "wizard-review-http");
  await you.getByRole("button", { name: /Create and deploy/ }).click();
  await you.waitForURL(/services\/site$/);
});

if (LAB) {
  await step(you, "the web service starts and answers on its address", async () => {
    await serviceRunning(you, "site");
    await until("the page through Traefik", () =>
      lab("ctl", "curl", "-s", "-m", "3", "-H", "Host: site-production-shop.localhost", "http://127.0.0.1:8080/").includes("hello-from-the-wizard"));
  });
}

await step(you, "a public TCP service takes the port its clients expect", async () => {
  await toNetwork(you, "echo");
  await you.getByText("Public TCP/UDP").first().click();
  await you.getByLabel("Container port").first().fill("8081");
  await you.getByRole("radio", { name: "Choose" }).first().click();
  await you.getByLabel("Number").fill(String(TCP_PORT));
  await you.getByText("Free on the project's load balancer.").first().waitFor();
  await shot(you, "wizard-network-tcp");
  await next(you).click(); // Resources
  await next(you).click(); // Variables
  await next(you).click(); // Deploy
  await shellCommand(you, "mkdir -p /www && echo hello-from-the-port > /www/index.html && exec httpd -f -p 8081 -h /www");
  await next(you).click(); // Review
  await you.getByText(`:${TCP_PORT}`).first().waitFor();
  await shot(you, "wizard-review-tcp");
  await you.getByRole("button", { name: /Create and deploy/ }).click();
  await you.waitForURL(/services\/echo$/);
});

if (LAB) {
  await step(you, "clients reach the service on the chosen port", async () => {
    await serviceRunning(you, "echo");
    await until(`port ${TCP_PORT} through Traefik`, () =>
      lab("ctl", "curl", "-s", "-m", "3", `http://127.0.0.1:${TCP_PORT}/`).includes("hello-from-the-port"));
  });
}

await step(you, "the Networking tab shows the chosen port as a person would read it", async () => {
  await you.goto(`${BASE}/projects/shop/production/services/echo`);
  await you.getByRole("tab", { name: "Networking" }).or(you.getByText("Networking", { exact: true })).first().click();
  await you.getByText(`:${TCP_PORT}`).first().waitFor();
  await you.getByText("(chosen)").first().waitFor();
  await shot(you, "networking");
});

await step(you, "Network → Ports lists the port with its owner", async () => {
  await you.goto(`${BASE}/network/ports`);
  const row = portRow(you, TCP_PORT);
  await row.first().waitFor();
  const text = await row.first().innerText();
  for (const want of [/tcp/i, /shop\/production\/echo/, /chosen/i]) if (!want.test(text)) throw new Error(`row lacks ${want}: ${text}`);
  await you.getByText("Traefik (HTTPS)").waitFor(); // the reserved list is on the page
  await shot(you, "ports-page");
});

await step(you, "a second service asking for the same port is stopped before it is made", async () => {
  await toNetwork(you, "dup");
  await you.getByText("Public TCP/UDP").first().click();
  await you.getByLabel("Container port").first().fill("9000");
  await you.getByRole("radio", { name: "Choose" }).first().click();
  await you.getByLabel("Number").fill(String(TCP_PORT));
  await you.getByText(/taken by production\/echo/).first().waitFor();
  await shot(you, "wizard-port-taken");
  if (!(await next(you).isDisabled())) throw new Error("Next is enabled while the port is taken");
  await you.getByLabel("Number").fill("443");
  await you.getByText(/not available/i).first().waitFor();
  if (!(await next(you).isDisabled())) throw new Error("Next is enabled on a reserved port");
  await you.getByLabel("Number").fill("2600");
  await you.getByText("Free on the project's load balancer.").first().waitFor();
  await until("Next to open", async () => !(await next(you).isDisabled()), 10);
});

// A teammate with the developer role of this project does the same lookup.
let annOk = false;
try {
  const ann = rootApi("POST", "/iam/users", { email: "ann@example.com", name: "Ann", password: "ann-password-123" });
  rootApi("POST", "/iam/attachments", { principalType: "user", principalId: ann.id, policy: "Developer:shop" });
  annOk = true;
} catch (e) {
  if (LAB) throw e; // the dev controller has no root cookie jar
}
if (annOk) {
  const teammate = await person(browser, "ann");
  await step(teammate, "a project developer gets the live port check too", async () => {
    await signIn(teammate, "ann@example.com", "ann-password-123");
    await teammate.waitForURL((u) => !u.pathname.startsWith("/login"));
    await toNetwork(teammate, "dup");
    await teammate.getByText("Public TCP/UDP").first().click();
    await teammate.getByLabel("Container port").first().fill("9000");
    await teammate.getByRole("radio", { name: "Choose" }).first().click();
    await teammate.getByLabel("Number").fill(String(TCP_PORT));
    await teammate.getByText(/taken by production\/echo/).first().waitFor();
    await shot(teammate, "developer-port-taken");
  });
}

await step(you, "deleting the service frees its port", async () => {
  await you.goto(`${BASE}/projects/shop/production/services/echo`);
  await you.getByRole("button", { name: "Delete" }).click();
  await you.getByRole("dialog").getByRole("button", { name: "Delete", exact: true }).click();
  await you.waitForURL((u) => !u.pathname.includes("/services/echo"));
  if (LAB) {
    await until(`port ${TCP_PORT} to be free`, async () => rootApiSafe("GET", `/projects/shop/ports/check?protocol=tcp&port=${TCP_PORT}`)?.state === "free", 120);
  }
  await you.goto(`${BASE}/network/ports`);
  await until("the ports page to drop the row", async () => {
    await you.reload();
    return (await portRow(you, TCP_PORT).count()) === 0;
  }, 60);
  await shot(you, "ports-after-delete");
});

function rootApiSafe(method, path) {
  try {
    return rootApi(method, path);
  } catch {
    return null;
  }
}

const errors = [...you.errors];
if (errors.length) {
  console.error(`FAIL: the browser reported errors:\n${errors.join("\n")}`);
  process.exit(1);
}
await browser.close();
console.log("PASS");
