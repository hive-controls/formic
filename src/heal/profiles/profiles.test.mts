/**
 * Profile parsing/validation, and `healerFromProfile` turning a validated profile into
 * a live Healer (kept in this file per the Wave 1 brief — small enough not to split).
 */
import { test } from "node:test";
import assert from "node:assert/strict";
import { mkdtempSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import type { HealContext } from "../types.mts";
import { healerFromProfile } from "./healer-from-profile.mts";
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

const CONTEXT: HealContext = {
  spec: {
    name: "t",
    startUrl: "http://app.test/",
    steps: [
      { id: "st_1", index: 1, action: "goto", target: "http://app.test/" },
    ],
  },
  failure: {
    stepId: "st_1",
    index: 1,
    action: "goto",
    target: "http://app.test/",
    phase: "action",
    error: "boom",
  },
  failedStep: {
    id: "st_1",
    index: 1,
    action: "goto",
    target: "http://app.test/",
  },
  url: "http://app.test/",
  ariaSnapshot: "- document",
  attempt: 1,
  priorAttempts: [],
};

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

test("healerFromProfile (agent): a known agent resolves without spawning anything", () => {
  const profile: AgentProfile = { kind: "agent", agent: "claude" };
  const healer = healerFromProfile(profile, "claude-agent", {});
  assert.equal(healer.name, "agent:claude");
});

test("healerFromProfile (agent): an unlisted agent is refused, listing known agents", () => {
  const profile: AgentProfile = { kind: "agent", agent: "cluade" };
  assert.throws(
    () => healerFromProfile(profile, "t", {}),
    /unknown agent "cluade"/,
  );
  assert.throws(
    () => healerFromProfile(profile, "t", {}),
    /claude \| codex \| kimi \| grok \| custom/,
  );
});

test("healerFromProfile (api): a missing apiKeyFrom variable names the variable and the profile", () => {
  const profile: ApiProfile = {
    kind: "api",
    preset: "custom",
    baseUrl: "http://x/v1",
    model: "m",
    apiKeyFrom: "env.MISSING_KEY_XYZ",
  };
  assert.throws(
    () => healerFromProfile(profile, "prod", {}),
    /MISSING_KEY_XYZ/,
  );
  assert.throws(() => healerFromProfile(profile, "prod", {}), /profile "prod"/);
});

test("healerFromProfile (api): preset fills baseUrl+model; an explicit baseUrl overrides it", async () => {
  const calls: string[] = [];
  const originalFetch = globalThis.fetch;
  globalThis.fetch = (async (url: string | URL) => {
    calls.push(String(url));
    return new Response(
      JSON.stringify({
        choices: [
          { message: { content: '{"kind":"no-repair","reason":"ok"}' } },
        ],
      }),
      { status: 200, headers: { "content-type": "application/json" } },
    );
  }) as typeof fetch;
  try {
    const filled: ApiProfile = {
      kind: "api",
      preset: "ollama",
      model: "llama3",
    };
    await healerFromProfile(filled, "t", {}).propose(CONTEXT);
    assert.equal(calls[0], "http://127.0.0.1:11434/v1/chat/completions");

    const overridden: ApiProfile = {
      kind: "api",
      preset: "ollama",
      baseUrl: "http://127.0.0.1:9999/v1",
      model: "llama3",
    };
    await healerFromProfile(overridden, "t", {}).propose(CONTEXT);
    assert.equal(calls[1], "http://127.0.0.1:9999/v1/chat/completions");
  } finally {
    globalThis.fetch = originalFetch;
  }
});
