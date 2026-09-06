/**
 * The printed Actions snippet: which secrets it names by profile kind, the honesty
 * note for agent profiles, and that it is valid YAML end to end (the comment block
 * included) — a reviewer should be able to paste it straight into a workflow file.
 */
import { test } from "node:test";
import assert from "node:assert/strict";
import { parse as parseYaml } from "yaml";
import type {
  AgentProfile,
  ApiProfile,
} from "../../heal/profiles/profiles.mts";
import { actionsSnippet } from "./actions-snippet.mts";

const API_PROFILE: ApiProfile = {
  kind: "api",
  preset: "custom",
  baseUrl: "http://127.0.0.1:1/v1",
  model: "m",
  apiKeyFrom: "env.TEST_KEY",
};

const AGENT_PROFILE: AgentProfile = {
  kind: "agent",
  agent: "claude",
};

test("an api profile names both secrets by exact name", () => {
  const snippet = actionsSnippet("t", API_PROFILE);
  assert.match(snippet, /SOLARI_API_KEY/);
  assert.match(snippet, /TEST_KEY/);
  assert.match(snippet, /secrets\.TEST_KEY/);
});

test("an agent profile names only SOLARI_API_KEY plus the honest CI note", () => {
  const snippet = actionsSnippet("c", AGENT_PROFILE);
  assert.match(snippet, /SOLARI_API_KEY/);
  assert.ok(
    !snippet.includes("secrets.TEST_KEY"),
    "an agent profile has no apiKeyFrom secret to name",
  );
  assert.match(snippet, /reported to run headless/);
});

test("never names a hive-controls hostname", () => {
  const snippet = actionsSnippet("t", API_PROFILE);
  assert.ok(!snippet.toLowerCase().includes("hive-controls"));
});

test("parses as YAML with a jobs key", () => {
  const snippet = actionsSnippet("t", API_PROFILE);
  const parsed = parseYaml(snippet) as { jobs?: unknown };
  assert.ok(parsed.jobs, "expected a jobs: key in the parsed workflow");
});

test("the local gate: E2E_DOCTOR_GATE local, no Solari secret, a browser install step; a LAN base URL gets a reachability warning", () => {
  const local = actionsSnippet(
    "l",
    {
      kind: "api",
      preset: "llamacpp",
      baseUrl: "http://models.internal:8080/v1",
      model: "m",
    },
    "local",
  );
  assert.match(local, /E2E_DOCTOR_GATE: local/);
  assert.ok(
    !local.includes("SOLARI_API_KEY"),
    "no cloud secret on the local gate",
  );
  assert.match(local, /npx playwright install --with-deps chromium/);
  assert.match(
    local,
    /none — the local gate and a keyless healer need no secret/,
  );
  assert.match(
    local,
    /models\.internal:8080\/v1 looks like a server on your own network/,
  );
  const cloud = actionsSnippet("t", API_PROFILE);
  assert.match(cloud, /E2E_DOCTOR_GATE: solari/);
  assert.ok(!cloud.includes("playwright install"));
});
