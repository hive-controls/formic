/**
 * BrowserStack Automate — the credential → endpoint builder, and nothing else.
 *
 * BrowserStack's own docs (https://www.browserstack.com/docs/automate/playwright,
 * "Connecting Playwright to an Existing Browser" —
 * https://www.browserstack.com/guide/playwright-connect-to-existing-browser) give one
 * connection shape: connect to
 * `wss://cdp.browserstack.com/playwright?caps=<url-encoded-JSON>`, the capabilities
 * (credentials, browser/os selection, build/session name) carried in the query string,
 * not headers.
 *
 * The platform builds that URL and stops. Nothing here opens a browser: a
 * BrowserStack choice resolves to the recipe's generic remote gate
 * (`…_GATE=cdp` + `…_CDP_URL=<this URL>`) and the recipe connects. That is the whole
 * reason this file has no Playwright import — a configurator that constructed drivers
 * would need the recipe's driver seam, and the platform depends on no recipe.
 *
 * UNVERIFIED — not exercised against a live BrowserStack account: no
 * BROWSERSTACK_USERNAME/BROWSERSTACK_ACCESS_KEY in this environment as of 2026-09-06.
 */

/**
 * BrowserStack pins the client's Playwright version in the capabilities. The platform
 * no longer depends on Playwright, so it cannot read that version from a package it
 * does not install: a caller that knows its own recipe's version passes it, and
 * otherwise this is the version the workshop's recipe pins today. Wrong-but-declared
 * beats absent — BrowserStack rejects a session whose client version it was never told.
 */
export const DEFAULT_CLIENT_PLAYWRIGHT_VERSION = "1.62.1";

export interface BrowserStackOptions {
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
  /** The connecting client's Playwright version, when the caller knows it. */
  playwrightVersion?: string;
}

/** Pure URL builder — no I/O — so the capability shape is unit-testable without a
 *  live BrowserStack account. */
export function buildBrowserStackWsEndpoint(
  options: BrowserStackOptions,
): string {
  const caps: Record<string, string> = {
    "browserstack.username": options.username,
    "browserstack.accessKey": options.accessKey,
    browser: options.browser ?? "playwright-chromium",
    os: options.os ?? "OS X",
    os_version: options.osVersion ?? "Sonoma",
    build: options.buildName ?? "formic",
    name: options.sessionName ?? "formic run",
    "client.playwrightVersion":
      options.playwrightVersion ?? DEFAULT_CLIENT_PLAYWRIGHT_VERSION,
  };
  if (options.browserVersion) caps.browser_version = options.browserVersion;
  return `wss://cdp.browserstack.com/playwright?caps=${encodeURIComponent(JSON.stringify(caps))}`;
}
