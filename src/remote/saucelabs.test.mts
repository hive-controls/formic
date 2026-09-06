/**
 * Sauce Labs' Selenium-Grid URL/capabilities construction, offline — no live Sauce Labs
 * credentials in this environment as of 2026-09-02, so this is the whole
 * offline-testable surface. See saucelabs.mts's header for why this is a Selenium Grid
 * URL, not a chromium.connect() wsEndpoint like BrowserStack's.
 */
import { test } from "node:test";
import assert from "node:assert/strict";
import { buildSauceRemoteGrid } from "./saucelabs.mts";

test("builds the us-west-1 ondemand grid URL by default", () => {
  const grid = buildSauceRemoteGrid({ username: "u1", accessKey: "k1" });
  assert.equal(grid.url, "https://ondemand.us-west-1.saucelabs.com:443/wd/hub");
});

test("region selects the grid host", () => {
  const grid = buildSauceRemoteGrid({
    username: "u1",
    accessKey: "k1",
    region: "eu-central-1",
  });
  assert.equal(
    grid.url,
    "https://ondemand.eu-central-1.saucelabs.com:443/wd/hub",
  );
});

test("capabilities carry credentials under sauce:options with devTools on, and a sane default platform/browser", () => {
  const grid = buildSauceRemoteGrid({ username: "u2", accessKey: "k2" });
  const caps = JSON.parse(grid.capabilities) as {
    platformName: string;
    browserName: string;
    "sauce:options": {
      devTools: boolean;
      username: string;
      accessKey: string;
      build: string;
      name: string;
    };
  };
  assert.equal(caps.browserName, "chrome");
  assert.equal(caps.platformName, "Windows 11");
  assert.equal(caps["sauce:options"].devTools, true);
  assert.equal(caps["sauce:options"].username, "u2");
  assert.equal(caps["sauce:options"].accessKey, "k2");
});

test("vendor options override the defaults", () => {
  const grid = buildSauceRemoteGrid({
    username: "u3",
    accessKey: "k3",
    platformName: "macOS 14",
    buildName: "nightly",
    sessionName: "approve-an-order",
  });
  const caps = JSON.parse(grid.capabilities) as {
    platformName: string;
    "sauce:options": { build: string; name: string };
  };
  assert.equal(caps.platformName, "macOS 14");
  assert.equal(caps["sauce:options"].build, "nightly");
  assert.equal(caps["sauce:options"].name, "approve-an-order");
});
