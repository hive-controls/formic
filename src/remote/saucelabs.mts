/**
 * Sauce Labs — the credential → grid builder, and nothing else.
 *
 * ✅ VERIFIED by reading Sauce Labs' own docs (2026-09-02, re-read 2026-09-06): unlike
 * BrowserStack, Sauce Labs exposes no `wsEndpoint` CDP bridge for Playwright. Its
 * documented Playwright route
 * (https://docs.saucelabs.com/web-apps/automated-testing/playwright/selenium-grid/) is
 * Playwright's own experimental Selenium Grid support
 * (https://playwright.dev/docs/selenium-grid): set `SELENIUM_REMOTE_URL` and
 * `SELENIUM_REMOTE_CAPABILITIES`, then launch a browser as usual — Playwright detects
 * the two variables at launch time and routes the session through the grid.
 *
 * That is exactly the shape a configurator can deliver: two environment variables, set
 * around an ordinary local launch. So a Sauce choice resolves to the recipe's `local`
 * gate plus this pair — never to a `cdp` URL, which Sauce does not offer.
 *
 * UNVERIFIED — not exercised against a live Sauce Labs account: no
 * SAUCE_USERNAME/SAUCE_ACCESS_KEY in this environment as of 2026-09-06.
 */

export interface SauceLabsOptions {
  username: string;
  accessKey: string;
  region?: "us-west-1" | "eu-central-1";
  platformName?: string;
  buildName?: string;
  sessionName?: string;
}

export interface SauceRemoteGrid {
  url: string;
  capabilities: string;
}

/** Pure builder — no I/O, no env mutation — so the grid URL and capabilities JSON are
 *  unit-testable without a live Sauce Labs account. */
export function buildSauceRemoteGrid(
  options: SauceLabsOptions,
): SauceRemoteGrid {
  const region = options.region ?? "us-west-1";
  const capabilities = {
    platformName: options.platformName ?? "Windows 11",
    browserName: "chrome",
    "sauce:options": {
      devTools: true,
      username: options.username,
      accessKey: options.accessKey,
      build: options.buildName ?? "formic",
      name: options.sessionName ?? "formic run",
    },
  };
  return {
    url: `https://ondemand.${region}.saucelabs.com:443/wd/hub`,
    capabilities: JSON.stringify(capabilities),
  };
}
