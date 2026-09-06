/**
 * Sauce Labs backend.
 *
 * ✅ VERIFIED by reading Sauce Labs' own docs (2026-09-02): unlike BrowserStack, Sauce
 * Labs exposes no direct `chromium.connect(wsEndpoint)` CDP bridge for Playwright. Its
 * documented Playwright route
 * (https://docs.saucelabs.com/web-apps/automated-testing/playwright/selenium-grid/) is
 * Playwright's own experimental Selenium Grid support
 * (https://playwright.dev/docs/selenium-grid): set `SELENIUM_REMOTE_URL` and
 * `SELENIUM_REMOTE_CAPABILITIES` and call `browserType.launch()` as usual — Playwright
 * detects the env vars at launch time and routes the session through the grid. There is
 * no `launch()`-time option for this; the env vars are the only documented interface,
 * so `open()` sets them for the duration of one `chromium.launch()` call and restores
 * whatever was there before, in a `finally`. This is a real constraint, not a style
 * choice: it makes concurrent `open()` calls on this driver (or a second Selenium-Grid
 * driver) within one process racy on the shared env — acceptable for the Fleet's
 * one-gate-per-run model (fleet.mts), not safe to reuse for parallel sessions.
 *
 * Sauce also does not hand back an rrweb-shaped stream, so recording is via the same
 * injected rrweb recorder the local backend uses (driver/rrweb-recorder.mts) — we own
 * the page once connected, so rrweb injection works the same as it does locally.
 *
 * UNVERIFIED — not exercised against a live Sauce Labs account: no
 * SAUCE_USERNAME/SAUCE_ACCESS_KEY in this environment as of 2026-09-02.
 */
import { chromium, type Browser } from "playwright-core";
import type { Driver, DriverSession } from "./types.mts";
import { attachRrwebRecorder } from "./rrweb-recorder.mts";

export interface SauceLabsDriverOptions {
  username: string;
  accessKey: string;
  region?: "us-west-1" | "eu-central-1";
  platformName?: string;
  buildName?: string;
  sessionName?: string;
  /** Test seam; defaults to Playwright's chromium.launch. */
  launch?: () => Promise<Browser>;
}

export interface SauceRemoteGrid {
  url: string;
  capabilities: string;
}

/** Pure builder — no I/O, no env mutation — so the grid URL and capabilities JSON are
 *  unit-testable without a live Sauce Labs account. */
export function buildSauceRemoteGrid(
  options: SauceLabsDriverOptions,
): SauceRemoteGrid {
  const region = options.region ?? "us-west-1";
  const capabilities = {
    platformName: options.platformName ?? "Windows 11",
    browserName: "chrome",
    "sauce:options": {
      devTools: true,
      username: options.username,
      accessKey: options.accessKey,
      build: options.buildName ?? "formic-e2e-doctor",
      name: options.sessionName ?? "e2e-doctor run",
    },
  };
  return {
    url: `https://ondemand.${region}.saucelabs.com:443/wd/hub`,
    capabilities: JSON.stringify(capabilities),
  };
}

const FLUSH_GRACE_MS = 50;

export class SauceLabsDriver implements Driver {
  readonly name = "saucelabs";
  readonly canRecord = true;

  constructor(private readonly options: SauceLabsDriverOptions) {}

  async open(): Promise<DriverSession> {
    const grid = buildSauceRemoteGrid(this.options);
    const launch =
      this.options.launch ?? (() => chromium.launch({ headless: true }));

    const previousUrl = process.env.SELENIUM_REMOTE_URL;
    const previousCaps = process.env.SELENIUM_REMOTE_CAPABILITIES;
    process.env.SELENIUM_REMOTE_URL = grid.url;
    process.env.SELENIUM_REMOTE_CAPABILITIES = grid.capabilities;
    let browser: Browser;
    try {
      browser = await launch();
    } finally {
      if (previousUrl === undefined) delete process.env.SELENIUM_REMOTE_URL;
      else process.env.SELENIUM_REMOTE_URL = previousUrl;
      if (previousCaps === undefined)
        delete process.env.SELENIUM_REMOTE_CAPABILITIES;
      else process.env.SELENIUM_REMOTE_CAPABILITIES = previousCaps;
    }

    const context = browser.contexts()[0] ?? (await browser.newContext());
    const recorder = await attachRrwebRecorder(context);
    const page = context.pages()[0] ?? (await context.newPage());

    return {
      sessionId: `saucelabs-${Date.now()}`,
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
