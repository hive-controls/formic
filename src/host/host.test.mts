/**
 * The host router's rules: an explicit FORMIC_HOST always wins, a typo or an unready
 * explicit host refuses, and the default follows the gate directly — with no fallback
 * traversal between hosts (see host.mts's file header for why). Offline: hosts are
 * never opened, only selected.
 */
import { test } from "node:test";
import assert from "node:assert/strict";
import { describeHostSelection, HOST_VAR, HOSTS, selectHost } from "./host.mts";
import { GATE_VAR, selectGate } from "../fleet/fleet.mts";
import type { GateSelection } from "../fleet/fleet.mts";

const installed = { localBrowserInstalled: () => true };

function outsideGate(env: NodeJS.ProcessEnv): GateSelection {
  return selectGate({ ...env, [GATE_VAR]: "solari" }, installed);
}

function insideGate(): GateSelection {
  return selectGate({ [GATE_VAR]: "local" }, installed);
}

test("HOSTS lists solari-sandbox before local, mirroring the Fleet's GATES order", () => {
  assert.equal(HOSTS[0].name, "solari-sandbox");
  assert.equal(HOSTS[1].name, "local");
});

test("an explicit FORMIC_HOST=solari-sandbox wins when the key is present", () => {
  const gate = outsideGate({ SOLARI_API_KEY: "k" });
  const selection = selectHost(
    { SOLARI_API_KEY: "k", [HOST_VAR]: "solari-sandbox" },
    gate,
  );
  assert.equal(selection.host.name, "solari-sandbox");
  assert.equal(selection.how, "explicit");
  assert.equal(selection.reason, "FORMIC_HOST=solari-sandbox");
});

test("an explicit FORMIC_HOST is trimmed", () => {
  const gate = insideGate();
  const selection = selectHost({ [HOST_VAR]: " local " }, gate);
  assert.equal(selection.host.name, "local");
});

test("a typo FORMIC_HOST is refused and lists both real names — never silently a fallback", () => {
  const gate = insideGate();
  assert.throws(
    () => selectHost({ [HOST_VAR]: "solari" }, gate),
    /^Error: unsupported FORMIC_HOST "solari" \(expected "solari-sandbox" or "local"\)$/,
  );
});

test("an explicit solari-sandbox without a key refuses, naming the missing key", () => {
  const gate = insideGate();
  assert.throws(
    () => selectHost({ [HOST_VAR]: "solari-sandbox" }, gate),
    /^Error: FORMIC_HOST=solari-sandbox but SOLARI_API_KEY is not set$/,
  );
});

test("default follows an Outside gate to solari-sandbox", () => {
  const gate = outsideGate({ SOLARI_API_KEY: "k" });
  const selection = selectHost({ SOLARI_API_KEY: "k" }, gate);
  assert.equal(selection.host.name, "solari-sandbox");
  assert.equal(selection.how, "default");
  assert.equal(
    describeHostSelection(selection),
    "host: solari-sandbox (Outside) — default: follows the gate (solari, Outside)",
  );
});

test("default follows an Inside gate to local", () => {
  const gate = insideGate();
  const selection = selectHost({}, gate);
  assert.equal(selection.host.name, "local");
  assert.equal(selection.how, "default");
  assert.equal(
    describeHostSelection(selection),
    "host: local (Inside) — default: follows the gate (local, Inside)",
  );
});

test("mutation-proof: an Outside gate with no key throws on default — it never falls back to local", () => {
  // Constructed directly rather than via selectGate: selectGate itself would already
  // refuse an Outside gate with no key, so this proves selectHost's OWN refusal, not
  // that it merely inherited one.
  const fakeOutsideGate = {
    gate: { name: "solari", kind: "Outside" },
    how: "default",
    reason: "test fixture",
  } as GateSelection;
  assert.throws(
    () => selectHost({}, fakeOutsideGate),
    /^Error: FORMIC_HOST would default to solari-sandbox \(following the solari gate\) but SOLARI_API_KEY is not set$/,
  );
});
