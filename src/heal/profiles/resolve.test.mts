/**
 * `resolveHealer`'s precedence: flag > the recipe's healer variable > profiles.default >
 * the recipe's own default — and that a profile name always wins over the raw grammar
 * when both a saved profile and a grammar-shaped value could match. What comes back is
 * always configuration, never a healer.
 */
import { test } from "node:test";
import assert from "node:assert/strict";
import type { ProfilesFile } from "./profiles.mts";
import { describeHealerSelection, resolveHealer } from "./resolve.mts";

const PROFILES: ProfilesFile = {
  default: "local-api",
  profiles: {
    "claude-agent": { kind: "agent", agent: "claude" },
    "claude-agent-modeled": {
      kind: "agent",
      agent: "claude",
      model: "claude-sonnet-5",
    },
    "kimi-agent-modeled": { kind: "agent", agent: "kimi", model: "kimi-k2" },
    "local-api": {
      kind: "api",
      preset: "custom",
      baseUrl: "http://127.0.0.1:9/v1",
      model: "m",
    },
  },
};

test("a flag value naming a profile wins, with how/reason/kind from the profile", () => {
  const selection = resolveHealer({
    flagValue: "claude-agent",
    env: {},
    profiles: PROFILES,
  });
  assert.equal(selection.name, "claude-agent");
  assert.equal(selection.kind, "agent");
  assert.equal(selection.how, "flag");
  assert.equal(selection.reason, "--healer claude-agent");
  assert.equal(
    describeHealerSelection(selection),
    "healer: claude-agent (agent) — --healer claude-agent",
  );
  assert.equal(
    selection.config.values.E2E_DOCTOR_HEALER_AGENT_CMD?.startsWith("claude "),
    true,
  );
});

test("a model reaches an agent that has a selector, and never one that does not", () => {
  const passed = resolveHealer({
    flagValue: "claude-agent-modeled",
    env: {},
    profiles: PROFILES,
  });
  assert.match(
    passed.config.values.E2E_DOCTOR_HEALER_AGENT_CMD,
    /--model claude-sonnet-5$/,
  );

  // kimi exposes no model selector — a model on the profile (only reachable via a
  // hand-edited file; setup itself refuses to write this combination) must never be
  // passed as if the CLI accepted it.
  const dropped = resolveHealer({
    flagValue: "kimi-agent-modeled",
    env: {},
    profiles: PROFILES,
  });
  assert.equal(
    dropped.config.values.E2E_DOCTOR_HEALER_AGENT_CMD,
    "kimi -p {prompt}",
  );
});

test("a flag value that is not a profile falls back to the raw grammar", () => {
  const selection = resolveHealer({
    flagValue: "openai-compatible",
    env: {},
    profiles: PROFILES,
  });
  assert.equal(selection.name, "openai-compatible");
  assert.equal(selection.kind, "api");
  assert.equal(selection.how, "flag");
  assert.deepEqual(selection.config.values, {
    E2E_DOCTOR_HEALER: "openai-compatible",
  });
});

test("a named agent in the grammar resolves through the generic custom-command route", () => {
  const selection = resolveHealer({
    flagValue: "agent:codex",
    env: {},
    profiles: null,
  });
  assert.equal(selection.config.values.E2E_DOCTOR_HEALER, "agent:custom");
  assert.equal(
    selection.config.values.E2E_DOCTOR_HEALER_AGENT_CMD,
    "codex exec --skip-git-repo-check --sandbox workspace-write {prompt}",
  );
});

test("agent:custom leaves the command alone — the recipe's own variable already holds it", () => {
  const selection = resolveHealer({
    flagValue: "agent:custom",
    env: {},
    profiles: null,
  });
  assert.deepEqual(selection.config.values, {
    E2E_DOCTOR_HEALER: "agent:custom",
  });
});

test("E2E_DOCTOR_HEALER wins over profiles.default when no flag is given", () => {
  const selection = resolveHealer({
    env: { E2E_DOCTOR_HEALER: "claude-agent" },
    profiles: PROFILES,
  });
  assert.equal(selection.how, "env");
  assert.equal(selection.name, "claude-agent");
  assert.equal(selection.reason, "E2E_DOCTOR_HEALER=claude-agent");
});

test("profiles.default is used when no flag and no E2E_DOCTOR_HEALER", () => {
  const selection = resolveHealer({ env: {}, profiles: PROFILES });
  assert.equal(selection.how, "profile-default");
  assert.equal(selection.name, "local-api");
  assert.equal(
    selection.reason,
    'default: formic.profiles.yaml names "local-api"',
  );
});

test("no profiles file, no E2E_DOCTOR_HEALER: the recipe's own default, and nothing set", () => {
  const selection = resolveHealer({ env: {}, profiles: null });
  assert.equal(selection.how, "default");
  assert.equal(selection.name, "openai-compatible");
  assert.deepEqual(selection.config.values, {});
  assert.equal(
    selection.reason,
    "default: no profiles file, no E2E_DOCTOR_HEALER — the recipe's own default applies",
  );
});

test("a profiles file with no default falls back to the recipe's default, saying so", () => {
  const selection = resolveHealer({
    env: {},
    profiles: { profiles: { "local-api": PROFILES.profiles["local-api"] } },
  });
  assert.equal(selection.how, "default");
  assert.equal(
    selection.reason,
    "default: no E2E_DOCTOR_HEALER, no default in formic.profiles.yaml — the recipe's own default applies",
  );
});

test("an unknown value with a profiles file present lists both the grammar error and profile names", () => {
  assert.throws(
    () =>
      resolveHealer({ flagValue: "agent:cluade", env: {}, profiles: PROFILES }),
    /agent:cluade/,
  );
  assert.throws(
    () =>
      resolveHealer({ flagValue: "agent:cluade", env: {}, profiles: PROFILES }),
    /claude-agent/,
  );
});

test("an unknown value with no profiles file names the agents that do exist", () => {
  assert.throws(
    () => resolveHealer({ flagValue: "agent:cluade", env: {}, profiles: null }),
    /unsupported healer "agent:cluade" \(expected agent:claude \| agent:codex/,
  );
});
