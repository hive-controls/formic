/**
 * The Fleet's rules, each pinned: Solari-first when its key resolves, local as the
 * fallback, an explicit FORMIC_GATE always wins, a typo or an unready gate refuses.
 * Offline: presence probes are injected; drivers are constructed, never opened.
 */
import { test } from "node:test";
import assert from "node:assert/strict";
import { describeSelection, GATE_VAR, selectGate } from "./fleet.mts";

const installed = { localBrowserInstalled: () => true };
const notInstalled = { localBrowserInstalled: () => false };

test("default is Solari when SOLARI_API_KEY resolves", () => {
  const selection = selectGate({ SOLARI_API_KEY: "slr_test" }, installed);
  assert.equal(selection.gate.name, "solari");
  assert.equal(selection.gate.kind, "Outside");
  assert.equal(selection.driver.name, "solari-browser");
  assert.equal(selection.how, "default");
  assert.equal(
    describeSelection(selection),
    "gate: solari (Outside) — default: SOLARI_API_KEY resolves",
  );
});

test("default falls to local when no key resolves, and says why Solari was skipped", () => {
  const selection = selectGate({}, installed);
  assert.equal(selection.gate.name, "local");
  assert.equal(selection.gate.kind, "Inside");
  assert.equal(selection.driver.name, "local-playwright");
  assert.equal(selection.how, "default");
  assert.equal(
    describeSelection(selection),
    "gate: local (Inside) — default: Chromium is installed (solari: SOLARI_API_KEY is not set)",
  );
});

test("an explicit FORMIC_GATE=local wins over a present Solari key", () => {
  const selection = selectGate(
    { SOLARI_API_KEY: "slr_test", [GATE_VAR]: " local " },
    installed,
  );
  assert.equal(selection.gate.name, "local");
  assert.equal(selection.how, "explicit");
  assert.equal(
    describeSelection(selection),
    "gate: local (Inside) — FORMIC_GATE=local",
  );
});

test("an explicit Solari gate without a key refuses, naming the missing key", () => {
  assert.throws(
    () => selectGate({ [GATE_VAR]: "solari" }, installed),
    /^Error: FORMIC_GATE=solari but SOLARI_API_KEY is not set$/,
  );
});

test("a typo is refused and lists the real names — never silently local", () => {
  assert.throws(
    () =>
      selectGate(
        { SOLARI_API_KEY: "slr_test", [GATE_VAR]: "solar" },
        installed,
      ),
    /^Error: unsupported FORMIC_GATE "solar" \(expected "solari" or "local" or "browserstack" or "saucelabs"\)$/,
  );
});

test("a local gate without Chromium refuses with the install command", () => {
  assert.throws(
    () => selectGate({ [GATE_VAR]: "local" }, notInstalled),
    /FORMIC_GATE=local but Chromium is not installed .*npx playwright install chromium/,
  );
});

test("no ready gate at all refuses with every reason", () => {
  assert.throws(
    () => selectGate({}, notInstalled),
    /^Error: no gate is ready — solari: SOLARI_API_KEY is not set; local: Chromium is not installed/,
  );
});

// ── browserstack and saucelabs — explicit-only trial cloud-grid gates ──

test("browserstack is a clean keyless skip: absent creds never crash, presence names what's missing", () => {
  assert.throws(
    () => selectGate({ [GATE_VAR]: "browserstack" }, installed),
    /^Error: FORMIC_GATE=browserstack but BROWSERSTACK_USERNAME and BROWSERSTACK_ACCESS_KEY not set$/,
  );
});

test("browserstack with only one of the two creds set still refuses, naming the missing one", () => {
  assert.throws(
    () =>
      selectGate(
        { [GATE_VAR]: "browserstack", BROWSERSTACK_USERNAME: "u" },
        installed,
      ),
    /^Error: FORMIC_GATE=browserstack but BROWSERSTACK_ACCESS_KEY not set$/,
  );
});

test("saucelabs is a clean keyless skip: absent creds never crash, presence names what's missing", () => {
  assert.throws(
    () => selectGate({ [GATE_VAR]: "saucelabs" }, installed),
    /^Error: FORMIC_GATE=saucelabs but SAUCE_USERNAME and SAUCE_ACCESS_KEY not set$/,
  );
});

test("an explicit browserstack gate with both creds present is ready, Outside, and named", () => {
  const selection = selectGate(
    {
      [GATE_VAR]: "browserstack",
      BROWSERSTACK_USERNAME: "u",
      BROWSERSTACK_ACCESS_KEY: "k",
    },
    installed,
  );
  assert.equal(selection.gate.name, "browserstack");
  assert.equal(selection.gate.kind, "Outside");
  assert.equal(selection.driver.name, "browserstack");
  assert.equal(selection.how, "explicit");
});

test("an explicit saucelabs gate with both creds present is ready, Outside, and named", () => {
  const selection = selectGate(
    { [GATE_VAR]: "saucelabs", SAUCE_USERNAME: "u", SAUCE_ACCESS_KEY: "k" },
    installed,
  );
  assert.equal(selection.gate.name, "saucelabs");
  assert.equal(selection.gate.kind, "Outside");
  assert.equal(selection.driver.name, "saucelabs");
  assert.equal(selection.how, "explicit");
});

test("browserstack and saucelabs are never picked by the default (unset FORMIC_GATE) search, even with valid creds present — a third-party trial vendor must never silently steer the fleet", () => {
  const selection = selectGate(
    {
      BROWSERSTACK_USERNAME: "u",
      BROWSERSTACK_ACCESS_KEY: "k",
      SAUCE_USERNAME: "u",
      SAUCE_ACCESS_KEY: "k",
    },
    installed,
  );
  assert.equal(selection.gate.name, "local");
  assert.equal(selection.how, "default");
});

// ── Preflight: a target an Outside gate can never reach is refused BEFORE a session opens ──
import { preflightSpec } from "./fleet.mts";
import type { Spec } from "../spec/types.mts";

function specAt(startUrl: string, gotoTargets: string[] = [startUrl]): Spec {
  return {
    name: "t",
    startUrl,
    steps: gotoTargets.map((target, i) => ({
      id: `st_${i}`,
      index: i + 1,
      action: "goto" as const,
      target,
    })),
  };
}

test("preflight refuses a loopback or private target on an Outside gate, naming the URL, the gate, and the three ways out", () => {
  const solari = selectGate({ SOLARI_API_KEY: "k" }, installed);
  for (const url of [
    "http://127.0.0.1:4173/",
    "http://localhost:3000/",
    "http://app.localhost/",
    "http://[::1]:8080/",
    "http://10.0.0.5/",
    "http://192.168.1.20:4173/",
    "http://172.16.0.9/",
    "http://169.254.1.1/",
  ]) {
    assert.throws(
      () => preflightSpec(specAt(url), solari),
      (err: Error) =>
        err.message.includes(url) &&
        /not reachable from the solari gate \(Outside\)/.test(err.message) &&
        /--app <dir>/.test(err.message) &&
        /previewUrl|tunnel/.test(err.message) &&
        /FORMIC_GATE=local/.test(err.message),
      `should refuse ${url}`,
    );
  }
});

test("preflight checks every goto target, not only startUrl", () => {
  const solari = selectGate({ SOLARI_API_KEY: "k" }, installed);
  assert.throws(
    () =>
      preflightSpec(
        specAt("https://example.com/", [
          "https://example.com/",
          "http://127.0.0.1:9/",
        ]),
        solari,
      ),
    /step 2 .*http:\/\/127\.0\.0\.1:9\//,
  );
});

test("preflight passes a public target on an Outside gate and any target on an Inside gate", () => {
  const solari = selectGate({ SOLARI_API_KEY: "k" }, installed);
  const local = selectGate({ [GATE_VAR]: "local" }, installed);
  assert.doesNotThrow(() =>
    preflightSpec(specAt("https://example.com/"), solari),
  );
  assert.doesNotThrow(() =>
    preflightSpec(specAt("http://127.0.0.1:4173/"), local),
  );
  assert.doesNotThrow(() =>
    preflightSpec(specAt("http://localhost:3000/"), local),
  );
});
