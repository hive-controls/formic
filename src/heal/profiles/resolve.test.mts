/**
 * `resolveHealer`'s precedence: flag > FORMIC_HEALER > profiles.default > the legacy
 * grammar's own default — and that a profile name always wins over the legacy grammar
 * when both a saved profile and a `FORMIC_HEALER`-shaped value could match.
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
    "healer: claude-agent (agent:claude · model: agent default) — --healer claude-agent",
  );
});

test("the three healer-label model variants", () => {
  const noModel = resolveHealer({
    flagValue: "claude-agent",
    env: {},
    profiles: PROFILES,
  });
  assert.equal(
    describeHealerSelection(noModel),
    "healer: claude-agent (agent:claude · model: agent default) — --healer claude-agent",
  );

  const passed = resolveHealer({
    flagValue: "claude-agent-modeled",
    env: {},
    profiles: PROFILES,
  });
  assert.equal(
    describeHealerSelection(passed),
    "healer: claude-agent-modeled (agent:claude · claude-sonnet-5) — --healer claude-agent-modeled",
  );

  // kimi exposes no modelArgs/modelEnv — a model on the profile (only reachable via a
  // hand-edited file; setup itself refuses to write this combination) must never be
  // printed as if it ran.
  const refused = resolveHealer({
    flagValue: "kimi-agent-modeled",
    env: {},
    profiles: PROFILES,
  });
  assert.equal(
    describeHealerSelection(refused),
    "healer: kimi-agent-modeled (agent:kimi · model: kimi-k2 (not passed — this agent exposes no model selector)) — --healer kimi-agent-modeled",
  );
});

test("a flag value that is not a profile falls back to the legacy grammar", () => {
  const selection = resolveHealer({
    flagValue: "openai-compatible",
    env: {},
    profiles: PROFILES,
  });
  assert.equal(selection.name, "openai-compatible");
  assert.equal(selection.kind, "api");
  assert.equal(selection.how, "flag");
});

test("FORMIC_HEALER wins over profiles.default when no flag is given", () => {
  const selection = resolveHealer({
    env: { FORMIC_HEALER: "claude-agent" },
    profiles: PROFILES,
  });
  assert.equal(selection.how, "env");
  assert.equal(selection.name, "claude-agent");
  assert.equal(selection.reason, "FORMIC_HEALER=claude-agent");
});

test("profiles.default is used when no flag and no FORMIC_HEALER", () => {
  const selection = resolveHealer({ env: {}, profiles: PROFILES });
  assert.equal(selection.how, "profile-default");
  assert.equal(selection.name, "local-api");
  assert.equal(
    selection.reason,
    'default: formic.profiles.yaml names "local-api"',
  );
});

test("no profiles file, no FORMIC_HEALER: today's legacy default", () => {
  const selection = resolveHealer({ env: {}, profiles: null });
  assert.equal(selection.how, "default");
  assert.equal(selection.name, "openai-compatible");
  assert.equal(selection.reason, "default: no profiles file, no FORMIC_HEALER");
});

test("a profiles file with no default falls back to the legacy default, saying so", () => {
  const selection = resolveHealer({
    env: {},
    profiles: { profiles: { "local-api": PROFILES.profiles["local-api"] } },
  });
  assert.equal(selection.how, "default");
  assert.equal(
    selection.reason,
    "default: no FORMIC_HEALER, no default in formic.profiles.yaml",
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

test("an unknown value with no profiles file preserves the legacy typo message", () => {
  assert.throws(
    () => resolveHealer({ flagValue: "agent:cluade", env: {}, profiles: null }),
    /unsupported FORMIC_HEALER "agent:cluade"/,
  );
});
