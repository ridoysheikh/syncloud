// Helpers for the browser tests: a real Chrome, readable steps, and the
// lab's containers (docker exec) for what only the lab can see.
import { chromium } from "playwright-core";
import { execFileSync } from "node:child_process";
import { existsSync, mkdirSync } from "node:fs";

export const env = (k, d) => process.env[k] ?? d;
export const E2E = env("E2E_PREFIX", "sc-ui");
export const BASE = env("UI_URL", "http://127.0.0.1:7070");
export const SHOTS = env("UI_SHOTS", "/tmp/syncloud-ui-shots");
mkdirSync(SHOTS, { recursive: true });

const chromePaths = [
  env("CHROME_PATH", ""),
  "/usr/bin/google-chrome",
  "/usr/bin/google-chrome-stable",
  "/usr/bin/chromium",
  "/usr/bin/chromium-browser",
].filter(Boolean);

export async function launch() {
  const executablePath = chromePaths.find((p) => existsSync(p));
  if (!executablePath) throw new Error("no Chrome found: set CHROME_PATH");
  return chromium.launch({ executablePath, args: ["--no-sandbox"] });
}

/** A new person: their own cookies, and every browser error they would see. */
export async function person(browser, name) {
  const ctx = await browser.newContext({ viewport: { width: 1280, height: 1000 } });
  const page = await ctx.newPage();
  page.setDefaultTimeout(20_000);
  page.errors = [];
  page.on("pageerror", (e) => page.errors.push(`${name}: ${e}`));
  page.on("console", (m) => {
    // A failed request is reported by the response listener below, with its address.
    if (m.type() === "error" && !/Failed to load resource/.test(m.text())) page.errors.push(`${name}: ${m.text()}`);
  });
  page.on("response", (r) => {
    if (r.status() < 400) return;
    const path = new URL(r.url()).pathname;
    // The lab has no private registry and no metrics, traffic or log backends:
    // the dashboard asks and is refused, as it would on a cluster without them.
    if ([502, 503].includes(r.status()) && /^\/api\/v1\/(registry|metrics|traffic|logs|health)\b|\/(metrics|traffic|logs|charts|history)$|\/autoscaling\/charts$/.test(path)) return;
    page.errors.push(`${name}: ${r.status()} ${r.request().method()} ${path}`);
  });
  page.person = name;
  return page;
}

let n = 0;
export async function shot(page, name) {
  await page.screenshot({ path: `${SHOTS}/${String(++n).padStart(2, "0")}-${name}.png`, fullPage: true });
}

/** One readable step: prints ✓, or fails the run with a screenshot. */
export async function step(page, title, fn) {
  try {
    await fn();
    console.log(`  ✓ ${title}`);
  } catch (e) {
    await page.screenshot({ path: `${SHOTS}/FAIL.png`, fullPage: true }).catch(() => {});
    console.error(`FAIL: ${title}\n${e.stack ?? e}`);
    console.error(`page: ${page.url()}  (screenshot: ${SHOTS}/FAIL.png)`);
    process.exit(1);
  }
}

/** Runs a command inside a lab node. */
export function lab(node, ...cmd) {
  return execFileSync("docker", ["exec", `${E2E}-${node}`, ...cmd], { encoding: "utf8", stdio: ["ignore", "pipe", "pipe"] });
}

/** The root session's API, for what a person would ask an admin for. */
export function rootApi(method, path, body) {
  const args = ["curl", "-fsS", "-X", method, "-b", "/tmp/jar", "-H", "content-type: application/json", "-H", "Origin: http://localhost:7070"];
  if (body !== undefined) args.push("-d", JSON.stringify(body));
  args.push(`localhost:7070/api/v1${path}`);
  const out = lab("ctl", ...args);
  return out ? JSON.parse(out) : null;
}

export const sleep = (ms) => new Promise((r) => setTimeout(r, ms));

/** Retries until it works or the time is up. */
export async function until(what, fn, seconds = 90) {
  const end = Date.now() + seconds * 1000;
  let last;
  while (Date.now() < end) {
    try {
      const v = await fn();
      if (v) return v;
    } catch (e) {
      last = e;
    }
    await sleep(1000);
  }
  throw new Error(`timed out waiting for ${what}${last ? `: ${last.message}` : ""}`);
}
