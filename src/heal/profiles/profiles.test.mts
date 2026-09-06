/**
 * Profile parsing/validation, and `envFromProfile` turning a validated profile into the
 * environment a recipe reads (kept in this file per the Wave 1 brief — small enough not
 * to split).
 */
import { test } from "node:test";
import assert from "node:assert/strict";
import { mkdtempSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { envFromProfile } from "./env-from-profile.mts";
import {
  PROFILES_FILE,
  ProfileValidationError,
  loadProfiles,
  parseProfiles,
  saveProfiles,
  validateProfiles,
  type AgentProfile,
  type ApiProfile,
  type ProfilesFile,
} from "./profiles.mts";

const PREFIX = "E2E_DOCTOR_";

const VALID: ProfilesFile = {
  default: "local-api",
  profiles: {
    "claude-agent": { kind: "agent", agent: "claude" },
    "local-api": {
      kind: "api",
      preset: "custom",
      baseUrl: "http://127.0.0.1:9/v1",
      model: "m",
      apiKeyFrom: "env.MY_KEY",
    },
  },
};

test("an agent profile and an api profile both parse", () => {
  const parsed = parseProfiles(saveProfiles(VALID));
  assert.deepEqual(parsed, VALID);
});

test("a committed profile never carries a secret", () => {
  assert.throws(
    () =>
      validateProfiles({
        profiles: {
          p: {
            kind: "api",
            preset: "custom",
            baseUrl: "http://x/v1",
            model: "m",
            apiKey: "sk-leaked",
          },
        },
      }),
    (error: unknown) =>
      error instanceof ProfileValidationError &&
      /the file is committed; use apiKeyFrom: env.NAME/.test(error.message),
  );
});

test("apiKeyFrom must match env.<NAME>", () => {
  assert.throws(
    () =>
      validateProfiles({
        profiles: {
          p: {
            kind: "api",
            preset: "custom",
            baseUrl: "http://x/v1",
            model: "m",
            apiKeyFrom: "MY_KEY",
          },
        },
      }),
    /apiKeyFrom must match env\.<NAME>/,
  );
});

test("preset: custom requires a baseUrl", () => {
  assert.throws(
    () =>
      validateProfiles({
        profiles: { p: { kind: "api", preset: "custom", model: "m" } },
      }),
    /preset: custom requires a baseUrl/,
  );
});

test("an unknown preset lists all eight", () => {
  assert.throws(
    () =>
      validateProfiles({
        profiles: { p: { kind: "api", preset: "nope", model: "m" } },
      }),
    /anthropic \| openrouter \| openai \| ollama \| lmstudio \| llamacpp \| vllm \| custom/,
  );
});

test("an unknown agent lists the adapters and custom", () => {
  assert.throws(
    () =>
      validateProfiles({ profiles: { p: { kind: "agent", agent: "cluade" } } }),
    /claude \| codex \| kimi \| grok \| custom/,
  );
});

test("agent: custom requires a command", () => {
  assert.throws(
    () =>
      validateProfiles({ profiles: { p: { kind: "agent", agent: "custom" } } }),
    /agent: custom requires a command/,
  );
});

test("default naming an absent profile is refused", () => {
  assert.throws(
    () => validateProfiles({ default: "ghost", profiles: {} }),
    /default names "ghost" but no such profile exists/,
  );
});

test("an agent profile with model parses and validates", () => {
  const withModel: ProfilesFile = {
    profiles: {
      p: { kind: "agent", agent: "claude", model: "claude-sonnet-5" },
    },
  };
  const parsed = parseProfiles(saveProfiles(withModel));
  assert.deepEqual(parsed, withModel);
});

test("an empty-string model is refused (shape only, never a model list)", () => {
  assert.throws(
    () =>
      validateProfiles({
        profiles: { p: { kind: "agent", agent: "claude", model: "" } },
      }),
    /model must be a non-empty string/,
  );
});

test("a non-positive timeoutMs is refused", () => {
  assert.throws(
    () =>
      validateProfiles({
        profiles: { p: { kind: "agent", agent: "claude", timeoutMs: 0 } },
      }),
    /timeoutMs must be a positive number/,
  );
});

test("loadProfiles: a missing file is null, a malformed one throws", () => {
  const dir = mkdtempSync(join(tmpdir(), "e2e-doctor-profiles-"));
  try {
    assert.equal(loadProfiles(dir), null);
    writeFileSync(
      join(dir, PROFILES_FILE),
      "profiles:\n  p:\n    kind: agent\n",
    );
    assert.throws(() => loadProfiles(dir), ProfileValidationError);
  } finally {
    rmSync(dir, { recursive: true, force: true });
  }
});

test("envFromProfile (agent): a named preset resolves to the generic custom-command route", () => {
  const profile: AgentProfile = { kind: "agent", agent: "kimi" };
  const resolved = envFromProfile(profile, "kimi-agent", {}, PREFIX);
  assert.deepEqual(resolved.values, {
    E2E_DOCTOR_HEALER: "agent:custom",
    E2E_DOCTOR_HEALER_AGENT_CMD: "kimi -p {prompt}",
  });
  assert.deepEqual(resolved.secretKeys, []);
});

test("envFromProfile (agent): a model is baked into the argv, never left to a variable the recipe ignores", () => {
  const profile: AgentProfile = {
    kind: "agent",
    agent: "claude",
    model: "opus",
  };
  const resolved = envFromProfile(profile, "t", {}, PREFIX);
  assert.match(
    resolved.values.E2E_DOCTOR_HEALER_AGENT_CMD,
    /claude -p \{prompt\}.*--model opus$/,
  );
});

test("envFromProfile (agent): an unlisted agent is refused, listing known agents", () => {
  const profile: AgentProfile = { kind: "agent", agent: "cluade" };
  assert.throws(
    () => envFromProfile(profile, "t", {}, PREFIX),
    /unknown agent "cluade"/,
  );
  assert.throws(
    () => envFromProfile(profile, "t", {}, PREFIX),
    /claude \| codex \| kimi \| grok \| custom/,
  );
});

test("envFromProfile (api): a missing apiKeyFrom variable names the variable and the profile", () => {
  const profile: ApiProfile = {
    kind: "api",
    preset: "custom",
    baseUrl: "http://x/v1",
    model: "m",
    apiKeyFrom: "env.MISSING_KEY_XYZ",
  };
  assert.throws(
    () => envFromProfile(profile, "prod", {}, PREFIX),
    /MISSING_KEY_XYZ/,
  );
  assert.throws(
    () => envFromProfile(profile, "prod", {}, PREFIX),
    /profile "prod"/,
  );
});

test("envFromProfile (api): the key is read by name from the environment and marked secret", () => {
  const profile: ApiProfile = {
    kind: "api",
    preset: "custom",
    baseUrl: "http://x/v1",
    model: "m",
    apiKeyFrom: "env.SOME_KEY",
  };
  const resolved = envFromProfile(
    profile,
    "prod",
    { SOME_KEY: "s3cret" },
    PREFIX,
  );
  assert.equal(resolved.values.E2E_DOCTOR_HEALER_API_KEY, "s3cret");
  assert.deepEqual(resolved.secretKeys, ["E2E_DOCTOR_HEALER_API_KEY"]);
});

test("envFromProfile (api): preset fills baseUrl; an explicit baseUrl overrides it", () => {
  const filled: ApiProfile = { kind: "api", preset: "ollama", model: "llama3" };
  assert.equal(
    envFromProfile(filled, "t", {}, PREFIX).values.E2E_DOCTOR_HEALER_BASE_URL,
    "http://127.0.0.1:11434/v1",
  );
  const overridden: ApiProfile = {
    kind: "api",
    preset: "ollama",
    baseUrl: "http://127.0.0.1:9999/v1",
    model: "llama3",
  };
  assert.equal(
    envFromProfile(overridden, "t", {}, PREFIX).values
      .E2E_DOCTOR_HEALER_BASE_URL,
    "http://127.0.0.1:9999/v1",
  );
});
