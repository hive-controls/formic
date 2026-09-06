/**
 * `runSetup` end to end with every seam injected — no real agent spawn, no real
 * network egress (the one live `fetch` attempt in the interactive test targets an
 * unreachable loopback port, never a mock), no real smoke heal.
 */
import { test, before, after } from "node:test";
import assert from "node:assert/strict";
import {
  existsSync,
  mkdtempSync,
  readFileSync,
  rmSync,
  statSync,
  writeFileSync,
} from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { PassThrough } from "node:stream";
import { parse as parseYaml } from "yaml";
import type { DetectSeams } from "./detect.mts";
import type { SmokeHealFn } from "./smoke.mts";
import { runSetup, type SetupDeps, snippetGateFor } from "./setup.mts";

let scratch: string;
before(() => {
  scratch = mkdtempSync(join(tmpdir(), "e2e-doctor-setup-"));
});
after(() => {
  rmSync(scratch, { recursive: true, force: true });
});

function offlineDetectSeams(onPathNames: string[] = []): DetectSeams {
  return {
    which: async (command) =>
      onPathNames.includes(command) ? `/usr/bin/${command}` : null,
    run: async () => ({ code: 0, stdout: "1.0.0\n", stderr: "" }),
  };
}

const OK_SMOKE: SmokeHealFn = async () => ({
  ok: true,
  kind: "no-repair",
  latencyMs: 12,
  usage: { inputTokens: 10, outputTokens: 5 },
});
const FAIL_SMOKE: SmokeHealFn = async () => ({
  ok: false,
  latencyMs: 8,
  error: "HTTP 401 — bad key",
});

function makeDeps(cwd: string, overrides: Partial<SetupDeps> = {}): SetupDeps {
  return {
    cwd,
    env: {},
    io: { input: new PassThrough(), output: new PassThrough(), isTty: false },
    log: () => {},
    error: () => {},
    detect: offlineDetectSeams(),
    smoke: OK_SMOKE,
    ...overrides,
  };
}

test("non-interactive api, key already in env: exit 0, profile written, .env untouched", async () => {
  const dir = mkdtempSync(join(scratch, "a-"));
  const deps = makeDeps(dir, { env: { TEST_KEY: "already-set" } });
  const code = await runSetup(
    [
      "--non-interactive",
      "--healer-kind",
      "api",
      "--preset",
      "custom",
      "--base-url",
      "http://x/v1",
      "--model",
      "m",
      "--api-key-env",
      "TEST_KEY",
      "--profile-name",
      "t",
    ],
    deps,
  );
  assert.equal(code, 0);
  const written = readFileSync(join(dir, "formic.profiles.yaml"), "utf8");
  const parsed = parseYaml(written) as {
    default?: string;
    profiles: Record<string, { apiKeyFrom?: string }>;
  };
  assert.equal(parsed.profiles.t.apiKeyFrom, "env.TEST_KEY");
  assert.ok(!written.includes("already-set"), "no key value in the file");
  assert.equal(
    existsSync(join(dir, ".env")),
    false,
    ".env must not exist — the key was already set",
  );
});

test("non-interactive keyless local preset: no --api-key-env needed, profile has no apiKeyFrom", async () => {
  const dir = mkdtempSync(join(scratch, "k-"));
  const deps = makeDeps(dir, {});
  const code = await runSetup(
    [
      "--non-interactive",
      "--healer-kind",
      "api",
      "--preset",
      "llamacpp",
      "--base-url",
      "http://models.internal:8080/v1",
      "--model",
      "m",
      "--profile-name",
      "local",
    ],
    deps,
  );
  assert.equal(code, 0);
  const written = readFileSync(join(dir, "formic.profiles.yaml"), "utf8");
  const parsed = parseYaml(written) as {
    profiles: Record<string, { apiKeyFrom?: string; baseUrl?: string }>;
  };
  assert.equal(parsed.profiles.local.apiKeyFrom, undefined);
  assert.equal(parsed.profiles.local.baseUrl, "http://models.internal:8080/v1");
  assert.equal(existsSync(join(dir, ".env")), false);
});

test("smoke failure: exit 1, nothing written", async () => {
  const dir = mkdtempSync(join(scratch, "b-"));
  const deps = makeDeps(dir, {
    env: { TEST_KEY: "already-set" },
    smoke: FAIL_SMOKE,
  });
  const code = await runSetup(
    [
      "--non-interactive",
      "--healer-kind",
      "api",
      "--preset",
      "custom",
      "--base-url",
      "http://x/v1",
      "--model",
      "m",
      "--api-key-env",
      "TEST_KEY",
      "--profile-name",
      "t",
    ],
    deps,
  );
  assert.equal(code, 1);
  assert.throws(() => readFileSync(join(dir, "formic.profiles.yaml"), "utf8"));
  assert.throws(() => readFileSync(join(dir, ".env"), "utf8"));
});

test("non-TTY without --non-interactive: exit 2, error names --non-interactive", async () => {
  const dir = mkdtempSync(join(scratch, "c-"));
  const errors: string[] = [];
  const deps = makeDeps(dir, { error: (line) => errors.push(line) });
  const code = await runSetup([], deps);
  assert.equal(code, 2);
  assert.ok(errors.some((line) => line.includes("--non-interactive")));
});

test("non-interactive agent kind, detected on PATH: exit 0, agent profile written", async () => {
  const dir = mkdtempSync(join(scratch, "d-"));
  const deps = makeDeps(dir, { detect: offlineDetectSeams(["claude"]) });
  const code = await runSetup(
    [
      "--non-interactive",
      "--healer-kind",
      "agent",
      "--agent",
      "claude",
      "--profile-name",
      "c",
    ],
    deps,
  );
  assert.equal(code, 0);
  const parsed = parseYaml(
    readFileSync(join(dir, "formic.profiles.yaml"), "utf8"),
  ) as {
    profiles: Record<
      string,
      { kind: string; agent: string; timeoutMs: number }
    >;
  };
  assert.deepEqual(parsed.profiles.c, {
    kind: "agent",
    agent: "claude",
    timeoutMs: 180_000,
  });
});

test("non-interactive agent kind with --model, adapter has modelArgs: written with model", async () => {
  const dir = mkdtempSync(join(scratch, "d2-"));
  const deps = makeDeps(dir, { detect: offlineDetectSeams(["claude"]) });
  const code = await runSetup(
    [
      "--non-interactive",
      "--healer-kind",
      "agent",
      "--agent",
      "claude",
      "--model",
      "claude-sonnet-5",
      "--profile-name",
      "c",
    ],
    deps,
  );
  assert.equal(code, 0);
  const parsed = parseYaml(
    readFileSync(join(dir, "formic.profiles.yaml"), "utf8"),
  ) as { profiles: Record<string, { model?: string }> };
  assert.equal(parsed.profiles.c.model, "claude-sonnet-5");
});

test("--model against an agent with no modelArgs/modelEnv is refused, nothing written", async () => {
  const dir = mkdtempSync(join(scratch, "d3-"));
  const errors: string[] = [];
  const deps = makeDeps(dir, {
    detect: offlineDetectSeams(["kimi"]),
    error: (line) => errors.push(line),
  });
  const code = await runSetup(
    [
      "--non-interactive",
      "--healer-kind",
      "agent",
      "--agent",
      "kimi",
      "--model",
      "kimi-k2",
      "--profile-name",
      "k",
    ],
    deps,
  );
  assert.equal(code, 2);
  assert.ok(errors.some((line) => line.includes("no model selector")));
  assert.equal(existsSync(join(dir, "formic.profiles.yaml")), false);
});

test("a --agent-cmd naming {model} with no --model is refused, nothing written", async () => {
  const dir = mkdtempSync(join(scratch, "d4-"));
  const errors: string[] = [];
  const deps = makeDeps(dir, { error: (line) => errors.push(line) });
  const code = await runSetup(
    [
      "--non-interactive",
      "--healer-kind",
      "agent",
      "--agent-cmd",
      "mycli {prompt} --model {model}",
      "--profile-name",
      "m",
    ],
    deps,
  );
  assert.equal(code, 2);
  assert.ok(errors.some((line) => line.includes("names {model}")));
  assert.equal(existsSync(join(dir, "formic.profiles.yaml")), false);
});

/** Writes each answer only once its cue text has actually reached `output` — every
 *  prompt closes its own readline.Interface on answer, so an answer written before
 *  the NEXT prompt's Interface exists is silently lost (verified live). A real
 *  terminal never produces that ordering (each Enter is its own read event), so
 *  driving answers off the prompt text reproduces realistic pacing, not a race. */
function driveAnswers(
  output: PassThrough,
  input: PassThrough,
  steps: { when: RegExp; answer: string }[],
): void {
  let buffer = "";
  let next = 0;
  output.on("data", (chunk: string) => {
    buffer += chunk;
    if (next < steps.length && steps[next].when.test(buffer)) {
      input.write(`${steps[next].answer}\n`);
      buffer = "";
      next++;
    }
  });
}

test("interactive api flow: masked secret, .env created 0600, secret never on output", async () => {
  const dir = mkdtempSync(join(scratch, "e-"));
  writeFileSync(join(dir, ".gitignore"), "node_modules/\n.env\n");
  const input = new PassThrough();
  const output = new PassThrough();
  output.setEncoding("utf8");
  let observed = "";
  output.on("data", (chunk: string) => (observed += chunk));
  const deps = makeDeps(dir, {
    io: { input, output, isTty: true },
  });

  // Sequence: choose "api: custom" (8th option — the last preset — no agents on PATH), base URL (an
  // unreachable loopback port — no mock fetch needed, no real network egress),
  // "where should the key live" (1 = env), env var name, the secret itself, model
  // (the /models GET against the unreachable port fails, falling back to a free
  // text ask).
  driveAnswers(output, input, [
    { when: /Choose a healer:/, answer: "8" },
    { when: /Base URL: /, answer: "http://127.0.0.1:1/v1" },
    { when: /Where should the key live\?/, answer: "1" },
    { when: /Env var name for the key: /, answer: "TEST_KEY" },
    { when: /Paste the API key/, answer: "sk-interactive-secret" },
    { when: /Model: /, answer: "m" },
  ]);

  const code = await runSetup(["--profile-name", "t2"], deps);
  assert.equal(code, 0);
  const envContent = readFileSync(join(dir, ".env"), "utf8");
  assert.equal(envContent, "TEST_KEY=sk-interactive-secret\n");
  // NTFS has no POSIX mode bits — chmodSync(0o600) is a no-op there, so this
  // assertion only holds on a POSIX filesystem.
  if (process.platform !== "win32") {
    assert.equal(statSync(join(dir, ".env")).mode & 0o777, 0o600);
  }
  assert.ok(!observed.includes("sk-interactive-secret"));
});

test("the CI snippet's gate is the Fleet's choice for the environment: local without a Solari key, solari with one, explicit wins", () => {
  assert.equal(snippetGateFor({}), "local");
  assert.equal(snippetGateFor({ SOLARI_API_KEY: "slr_live_x" }), "solari");
  assert.equal(
    snippetGateFor({ SOLARI_API_KEY: "slr_live_x", FORMIC_GATE: "local" }),
    "local",
  );
  assert.equal(snippetGateFor({ FORMIC_GATE: "typo" }), "solari");
});
