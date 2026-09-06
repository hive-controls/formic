/**
 * BrowserStack Automate backend, connected over its documented Playwright/CDP bridge.
 *
 * BrowserStack's own docs (https://www.browserstack.com/docs/automate/playwright,
 * "Connecting Playwright to an Existing Browser" —
 * https://www.browserstack.com/guide/playwright-connect-to-existing-browser) give one
 * connection shape: `chromium.connect({ wsEndpoint })` against
 * `wss://cdp.browserstack.com/playwright?caps=<url-encoded-JSON>`, the capabilities
 * (credentials, browser/os selection, build/session name) carried in the query string,
 * not headers. BrowserStack does not hand back an rrweb-shaped replay stream the way
 * Solari does — we own the page once connected, so recording is via the same injected
 * rrweb recorder the local backend uses (driver/rrweb-recorder.mts): rrweb injection
 * works the same as it does locally since the page is ours either way.
 *
 * UNVERIFIED — not exercised against a live BrowserStack account: no
 * BROWSERSTACK_USERNAME/BROWSERSTACK_ACCESS_KEY in this environment as of 2026-09-02.
 */
import { createRequire } from "node:module";
import { chromium, type Browser } from "playwright-core";
import type { Driver, DriverSession } from "./types.mts";
import { attachRrwebRecorder } from "./rrweb-recorder.mts";

const require = createRequire(import.meta.url);
const PLAYWRIGHT_VERSION = (
  require("playwright-core/package.json") as { version: string }
).version;

export interface BrowserStackDriverOptions {
  username: string;
  accessKey: string;
  /** Vendor "browser" capability; BrowserStack's own value for a Playwright-driven
   *  session (its docs use this literal string, not "chrome"). */
  browser?: string;
  browserVersion?: string;
  os?: string;
  osVersion?: string;
  buildName?: string;
  sessionName?: string;
  /** Test seam; defaults to Playwright's chromium.connect. */
  connect?: (wsEndpoint: string) => Promise<Browser>;
}

/** Pure URL builder — no I/O — so the capability shape is unit-testable without a
 *  live BrowserStack account. */
export function buildBrowserStackWsEndpoint(
  options: BrowserStackDriverOptions,
): string {
  const caps: Record<string, string> = {
    "browserstack.username": options.username,
    "browserstack.accessKey": options.accessKey,
    browser: options.browser ?? "playwright-chromium",
    os: options.os ?? "OS X",
    os_version: options.osVersion ?? "Sonoma",
    build: options.buildName ?? "formic-e2e-doctor",
    name: options.sessionName ?? "e2e-doctor run",
    "client.playwrightVersion": PLAYWRIGHT_VERSION,
  };
  if (options.browserVersion) caps.browser_version = options.browserVersion;
  return `wss://cdp.browserstack.com/playwright?caps=${encodeURIComponent(JSON.stringify(caps))}`;
}

const FLUSH_GRACE_MS = 50;

export class BrowserStackDriver implements Driver {
  readonly name = "browserstack";
  readonly canRecord = true;

  constructor(private readonly options: BrowserStackDriverOptions) {}

  async open(): Promise<DriverSession> {
    const connect =
      this.options.connect ??
      ((wsEndpoint: string) => chromium.connect({ wsEndpoint }));
    const wsEndpoint = buildBrowserStackWsEndpoint(this.options);
    const browser = await connect(wsEndpoint);
    // Before the first page exists, so the init script covers every document — same
    // ordering constraint as the local backend (driver/local-playwright.mts).
    const context = browser.contexts()[0] ?? (await browser.newContext());
    const recorder = await attachRrwebRecorder(context);
    const page = context.pages()[0] ?? (await context.newPage());

    return {
      sessionId: `browserstack-${Date.now()}`,
      page,
      async fetchReplay() {
        if (!page.isClosed()) {
          await page
            .evaluate(
              () => new Promise((resolve) => requestAnimationFrame(resolve)),
            )
            .catch(() => {});
          await new Promise((resolve) => setTimeout(resolve, FLUSH_GRACE_MS));
        }
        return recorder.events();
      },
      async close() {
        await browser.close();
      },
    };
  }
}
