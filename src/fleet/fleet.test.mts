/**
 * The Fleet's rules, each pinned: Solari-first when its key resolves, local as the
 * fallback, an explicit FORMIC_GATE always wins, a typo or an unready gate refuses, and
 * every choice comes back as CONFIGURATION — never a driver, never a connection.
 */
import { test } from "node:test";
import assert from "node:assert/strict";
import { describeSelection, GATE_VAR, selectGate } from "./fleet.mts";

test("default is Solari when SOLARI_API_KEY resolves", () => {
  const selection = selectGate({ SOLARI_API_KEY: "slr_test" });
  assert.equal(selection.gate.name, "solari");
  assert.equal(selection.gate.kind, "Outside");
  assert.deepEqual(selection.config, { E2E_DOCTOR_GATE: "solari" });
  assert.equal(selection.how, "default");
  assert.equal(
    describeSelection(selection),
    "gate: solari (Outside) — default: SOLARI_API_KEY resolves",
  );
});

test("default falls to local when no key resolves, and says why Solari was skipped", () => {
  const selection = selectGate({});
  assert.equal(selection.gate.name, "local");
  assert.equal(selection.gate.kind, "Inside");
  assert.deepEqual(selection.config, { E2E_DOCTOR_GATE: "local" });
  assert.equal(
    describeSelection(selection),
    "gate: local (Inside) — default: runs on this machine (solari: SOLARI_API_KEY is not set)",
  );
});

test("an explicit FORMIC_GATE=local wins over a present Solari key", () => {
  const selection = selectGate({
    SOLARI_API_KEY: "slr_test",
    [GATE_VAR]: " local ",
  });
  assert.equal(selection.gate.name, "local");
  assert.equal(selection.how, "explicit");
  assert.equal(
    describeSelection(selection),
    "gate: local (Inside) — FORMIC_GATE=local",
  );
});

test("an explicit Solari gate without a key refuses, naming the missing key", () => {
  assert.throws(
    () => selectGate({ [GATE_VAR]: "solari" }),
    /^Error: FORMIC_GATE=solari but SOLARI_API_KEY is not set$/,
  );
});

test("a typo is refused and lists the real names — never silently local", () => {
  assert.throws(
    () => selectGate({ [GATE_VAR]: "solaris" }),
    /unsupported FORMIC_GATE "solaris" \(expected "solari" or "local" or "browserstack" or "saucelabs"\)/,
  );
});

test("a trial vendor is never chosen by default, however complete its credentials", () => {
  const selection = selectGate({
    BROWSERSTACK_USERNAME: "u",
    BROWSERSTACK_ACCESS_KEY: "k",
    SAUCE_USERNAME: "u",
    SAUCE_ACCESS_KEY: "k",
  });
  assert.equal(selection.gate.name, "local");
});

test("browserstack resolves to the recipe's generic cdp gate plus an endpoint", () => {
  const selection = selectGate({
    [GATE_VAR]: "browserstack",
    BROWSERSTACK_USERNAME: "u",
    BROWSERSTACK_ACCESS_KEY: "k",
  });
  assert.equal(selection.config.E2E_DOCTOR_GATE, "cdp");
  assert.match(
    selection.config.E2E_DOCTOR_CDP_URL,
    /^wss:\/\/cdp\.browserstack\.com\/playwright\?caps=/,
  );
  assert.equal(
    describeSelection(selection),
    "gate: browserstack (Outside) — FORMIC_GATE=browserstack",
  );
});

test("browserstack with only one credential refuses, naming the missing one", () => {
  assert.throws(
    () =>
      selectGate({ [GATE_VAR]: "browserstack", BROWSERSTACK_USERNAME: "u" }),
    /FORMIC_GATE=browserstack but BROWSERSTACK_ACCESS_KEY not set/,
  );
});

test("saucelabs resolves to the local gate routed through Playwright's Selenium Grid", () => {
  const selection = selectGate({
    [GATE_VAR]: "saucelabs",
    SAUCE_USERNAME: "u",
    SAUCE_ACCESS_KEY: "k",
  });
  assert.equal(selection.config.E2E_DOCTOR_GATE, "local");
  assert.equal(
    selection.config.SELENIUM_REMOTE_URL,
    "https://ondemand.us-west-1.saucelabs.com:443/wd/hub",
  );
  assert.match(selection.config.SELENIUM_REMOTE_CAPABILITIES, /sauce:options/);
});

test("a recipe with its own prefix gets its own keys — nothing here is hard-coded to one recipe", () => {
  const selection = selectGate({ SOLARI_API_KEY: "slr_test" }, "OTHER_TOOL_");
  assert.deepEqual(selection.config, { OTHER_TOOL_GATE: "solari" });
});

test("the recipe's own gate variable selects a gate when FORMIC_GATE is unset", () => {
  const selection = selectGate({
    SOLARI_API_KEY: "slr_test",
    E2E_DOCTOR_GATE: "local",
  });
  assert.equal(selection.gate.name, "local");
  assert.equal(selection.how, "explicit");
});
