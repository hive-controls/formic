/**
 * BrowserStack's wsEndpoint/capabilities construction, offline — the URL shape is the
 * part that must be correct with no live account (no BrowserStack credentials in this
 * environment as of 2026-09-06, so this is the whole offline-testable surface).
 */
import { test } from "node:test";
import assert from "node:assert/strict";
import {
  buildBrowserStackWsEndpoint,
  DEFAULT_CLIENT_PLAYWRIGHT_VERSION,
} from "./browserstack.mts";

function decodeCaps(wsEndpoint: string): Record<string, string> {
  const query = new URL(wsEndpoint.replace("wss://", "https://")).searchParams;
  return JSON.parse(query.get("caps") as string) as Record<string, string>;
}

test("builds a wss://cdp.browserstack.com/playwright endpoint with url-encoded caps", () => {
  const wsEndpoint = buildBrowserStackWsEndpoint({
    username: "u1",
    accessKey: "k1",
  });
  assert.match(
    wsEndpoint,
    /^wss:\/\/cdp\.browserstack\.com\/playwright\?caps=/,
  );
  const caps = decodeCaps(wsEndpoint);
  assert.equal(caps["browserstack.username"], "u1");
  assert.equal(caps["browserstack.accessKey"], "k1");
  assert.equal(caps.browser, "playwright-chromium");
  assert.equal(caps.build, "formic");
  assert.equal(caps.name, "formic run");
});

test("credentials and vendor options carry through untouched", () => {
  const wsEndpoint = buildBrowserStackWsEndpoint({
    username: "u2",
    accessKey: "k2",
    browser: "chrome",
    browserVersion: "latest",
    os: "Windows",
    osVersion: "11",
    buildName: "nightly",
    sessionName: "approve-an-order",
  });
  const caps = decodeCaps(wsEndpoint);
  assert.equal(caps.browser, "chrome");
  assert.equal(caps.browser_version, "latest");
  assert.equal(caps.os, "Windows");
  assert.equal(caps.os_version, "11");
  assert.equal(caps.build, "nightly");
  assert.equal(caps.name, "approve-an-order");
});

test("no browser_version key at all when unset — never an empty-string capability", () => {
  const wsEndpoint = buildBrowserStackWsEndpoint({
    username: "u",
    accessKey: "k",
  });
  const caps = decodeCaps(wsEndpoint);
  assert.equal("browser_version" in caps, false);
});

test("the client's Playwright version is always declared — the caller's when given", () => {
  const withoutVersion = decodeCaps(
    buildBrowserStackWsEndpoint({ username: "u", accessKey: "k" }),
  );
  assert.equal(
    withoutVersion["client.playwrightVersion"],
    DEFAULT_CLIENT_PLAYWRIGHT_VERSION,
  );
  const withVersion = decodeCaps(
    buildBrowserStackWsEndpoint({
      username: "u",
      accessKey: "k",
      playwrightVersion: "1.99.0",
    }),
  );
  assert.equal(withVersion["client.playwrightVersion"], "1.99.0");
});
