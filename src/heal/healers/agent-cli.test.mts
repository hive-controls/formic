/**
 * The agent-CLI healer with a fake agent: proves the workspace contract (what the agent
 * is given, what is read back) without any real agent. The real adapters are
 * verified by live runs, reported on the PR, never claimed by this test.
 */
import { test, before, after } from "node:test";
import assert from "node:assert/strict";
import {
  existsSync,
  mkdtempSync,
  readFileSync,
  rmSync,
  writeFileSync,
} from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import dotenv from "dotenv";
import type { HealContext } from "../types.mts";
import {
  adapterFromCommand,
  agentCliHealer,
  quoteCommandToken,
  AGENT_PROMPT,
  type AgentCliAdapter,
} from "./agent-cli.mts";

const CONTEXT: HealContext = {
  spec: {
    name: "t",
    startUrl: "http://app.test/",
    steps: [
      { id: "st_1", index: 1, action: "goto", target: "http://app.test/" },
      {
        id: "st_2",
        index: 2,
        action: "click",
        target: "#signin-button",
        assert: { testId: "current-user", hasText: "ops@forgedepot.test" },
      },
    ],
  },
  failure: {
    stepId: "st_2",
    index: 2,
    action: "click",
    target: "#signin-button",
    phase: "action",
    error: "page.click: Timeout 2000ms exceeded.",
  },
  failedStep: {
    id: "st_2",
    index: 2,
    action: "click",
    target: "#signin-button",
    assert: { testId: "current-user", hasText: "ops@forgedepot.test" },
  },
  url: "http://app.test/",
  ariaSnapshot: '- button "Sign in"',
  attempt: 1,
  priorAttempts: [],
};

let scratch: string;
let fakeAgent: string;
before(() => {
  scratch = mkdtempSync(join(tmpdir(), "formic-agent-cli-"));
  // A "coding agent" that reads the brief, checks it was told the right things, and
  // writes a proposal — exactly the contract a real one is held to.
  fakeAgent = join(scratch, "fake-agent.mjs");
  writeFileSync(
    fakeAgent,
    `import { readFileSync, writeFileSync } from "node:fs";
const prompt = process.argv[2];
if (process.argv.includes("--version")) {
  writeFileSync(${JSON.stringify(join(scratch, "version-argv.txt"))}, process.argv.slice(2).join(" "));
  console.log("fake-agent 9.9.9");
  process.exit(0);
}
writeFileSync("argv.txt", process.argv.slice(2).join(" "));
writeFileSync("env.txt", process.env.FAKE_MODEL_ENV ?? "");
const heal = readFileSync("HEAL.md", "utf8");
const claude = readFileSync("CLAUDE.md", "utf8");
const agents = readFileSync("AGENTS.md", "utf8"); // export-denylist: ok
if (!heal.includes("st_2") || !heal.includes("Sign in")) { console.error("brief missing context"); process.exit(3); }
if (!claude.includes("PROPOSAL.yaml") || claude !== agents) { console.error("pointer files wrong"); process.exit(3); }
if (!prompt.includes("PROPOSAL.yaml")) { console.error("prompt wrong"); process.exit(3); }
const mode = process.env.FAKE_AGENT_MODE ?? "propose";
if (mode === "silent") process.exit(0);
if (mode === "hang") setTimeout(() => {}, 60_000);
else if (mode === "no-reason") { console.log("I rewrote the target to the role locator because the id is gone."); writeFileSync("PROPOSAL.yaml", "rewrite-target:\\n  stepId: st_2\\n  target: \\"#submit-login\\"\\n"); }
else writeFileSync("PROPOSAL.yaml", "kind: rewrite-target\\nstepId: st_2\\ntarget: \\"#submit-login\\"\\nreason: renamed\\n");
`,
  );
});
after(() => {
  rmSync(scratch, { recursive: true, force: true });
});

function fakeAdapter() {
  return adapterFromCommand(
    `${quoteCommandToken(process.execPath)} ${quoteCommandToken(fakeAgent)} {prompt}`,
  );
}

function fakeAdapterWithModelToken() {
  return adapterFromCommand(
    `${quoteCommandToken(process.execPath)} ${quoteCommandToken(fakeAgent)} {prompt} --model {model}`,
  );
}

test("materializes the workspace, spawns the agent with the fixed prompt, reads PROPOSAL.yaml back", async () => {
  const healer = agentCliHealer({
    adapter: fakeAdapter(),
    workspaceRoot: scratch,
    timeoutMs: 20_000,
  });
  const proposal = await healer.propose(CONTEXT);
  assert.deepEqual(proposal, {
    kind: "rewrite-target",
    stepId: "st_2",
    target: "#submit-login",
    reason: "renamed",
  });
  assert.match(healer.modelVersion, /^agent:.*@fake-agent 9\.9\.9$/);
  // The workspace is kept: it is audit material.
  assert.ok(
    healer.lastWorkspace && existsSync(join(healer.lastWorkspace, "HEAL.md")),
  );
  assert.ok(existsSync(join(healer.lastWorkspace, "context", "spec.yaml")));
  assert.equal(
    readFileSync(
      join(healer.lastWorkspace, "context", "aria-snapshot.txt"),
      "utf8",
    ),
    '- button "Sign in"',
  );
  assert.match(AGENT_PROMPT, /PROPOSAL\.yaml/);
});

test("a modelArgs adapter passes --model in argv; modelVersion is the resolved model", async () => {
  const adapter: AgentCliAdapter = {
    ...fakeAdapter(),
    modelArgs: (model: string) => ["--model", model],
  };
  const healer = agentCliHealer({
    adapter,
    workspaceRoot: scratch,
    timeoutMs: 20_000,
    model: "claude-sonnet-5",
  });
  await healer.propose(CONTEXT);
  const argv = readFileSync(join(healer.lastWorkspace!, "argv.txt"), "utf8");
  assert.match(argv, /--model claude-sonnet-5/);
  assert.equal(healer.modelVersion, "claude-sonnet-5");
  assert.equal(healer.modelRequested, "claude-sonnet-5");
  assert.equal(healer.modelPassed, true);
});

test("a modelEnv adapter sets the env var; modelVersion is the resolved model", async () => {
  const adapter: AgentCliAdapter = {
    ...fakeAdapter(),
    modelEnv: "FAKE_MODEL_ENV",
  };
  const healer = agentCliHealer({
    adapter,
    workspaceRoot: scratch,
    timeoutMs: 20_000,
    model: "claude-sonnet-5",
  });
  await healer.propose(CONTEXT);
  const env = readFileSync(join(healer.lastWorkspace!, "env.txt"), "utf8");
  assert.equal(env, "claude-sonnet-5");
  const argv = readFileSync(join(healer.lastWorkspace!, "argv.txt"), "utf8");
  assert.ok(!argv.includes("claude-sonnet-5"), "not also passed in argv");
  assert.equal(healer.modelVersion, "claude-sonnet-5");
  assert.equal(healer.modelPassed, true);
});

test("an adapter with neither route never passes the model, and modelVersion falls back to the CLI version", async () => {
  const healer = agentCliHealer({
    adapter: fakeAdapter(),
    workspaceRoot: scratch,
    timeoutMs: 20_000,
    model: "claude-sonnet-5",
  });
  await healer.propose(CONTEXT);
  const argv = readFileSync(join(healer.lastWorkspace!, "argv.txt"), "utf8");
  assert.ok(!argv.includes("claude-sonnet-5"));
  assert.equal(healer.modelRequested, "claude-sonnet-5");
  assert.equal(healer.modelPassed, false);
  assert.match(healer.modelVersion, /^agent:.*@fake-agent 9\.9\.9$/);
});

test("a {model}-token adapter substitutes the model in place, in both argv and versionArgs", async () => {
  const healer = agentCliHealer({
    adapter: fakeAdapterWithModelToken(),
    workspaceRoot: scratch,
    timeoutMs: 20_000,
    model: "claude-sonnet-5",
  });
  await healer.propose(CONTEXT);
  const argv = readFileSync(join(healer.lastWorkspace!, "argv.txt"), "utf8");
  assert.match(argv, /--model claude-sonnet-5$/);
  const versionArgv = readFileSync(join(scratch, "version-argv.txt"), "utf8");
  assert.match(versionArgv, /^--model claude-sonnet-5 --version$/);
  assert.equal(healer.modelVersion, "claude-sonnet-5");
});

test("adapterFromCommand: a double-quoted command containing a space stays one token", () => {
  const adapter = adapterFromCommand(
    '"C:\\Program Files\\nodejs\\node.exe" script.mjs {prompt}',
  );
  assert.equal(adapter.command, "C:\\Program Files\\nodejs\\node.exe");
  assert.deepEqual(adapter.args, ["script.mjs", "{prompt}"]);
});

test("adapterFromCommand: a single-quoted command containing a space stays one token", () => {
  const adapter = adapterFromCommand(
    "'/opt/my agents/bin/mycli' --headless {prompt}",
  );
  assert.equal(adapter.command, "/opt/my agents/bin/mycli");
  assert.deepEqual(adapter.args, ["--headless", "{prompt}"]);
});

test("adapterFromCommand: an ordinary unquoted command is unaffected (the old behaviour, still)", () => {
  const adapter = adapterFromCommand("mycli --headless {prompt}");
  assert.equal(adapter.command, "mycli");
  assert.deepEqual(adapter.args, ["--headless", "{prompt}"]);
});

test("RULE — tokenizing is purely syntactic: an UNQUOTED path with a space splits, and no filesystem probe rescues it", () => {
  // The resolution must not depend on what happens to exist on the machine or in the
  // caller's cwd, so this is deterministic on every OS: quoting is the only fix.
  const adapter = adapterFromCommand(
    "C:\\Program Files\\nodejs\\node.exe script.mjs {prompt}",
  );
  assert.equal(adapter.command, "C:\\Program");
  assert.deepEqual(adapter.args, [
    "Files\\nodejs\\node.exe",
    "script.mjs",
    "{prompt}",
  ]);
});

test("quoteCommandToken round-trips any path with whitespace back out of the tokenizer unchanged", () => {
  for (const path of [
    "C:\\Program Files\\nodejs\\node.exe",
    "\\\\server\\share path\\agent.exe",
    "/opt/my agents/bin/mycli",
    'weird"quote',
    // A quoter is only correct if it round-trips its OWN grammar, so the adversarial
    // set belongs here, not a hand-picked three: a directory path ends in a backslash
    // (double quotes cannot express it — the closing quote would read as an escape),
    // and a token may carry either quote character.
    "C:\\space dir\\",
    "C:\\Program Files\\a dir\\",
    "/opt/it's here/mycli",
    "has \"both\" 'quotes'",
  ]) {
    const adapter = adapterFromCommand(
      `${quoteCommandToken(path)} --flag {prompt}`,
    );
    assert.equal(adapter.command, path);
    assert.deepEqual(adapter.args, ["--flag", "{prompt}"]);
  }
});

test("REVIEW REGRESSION (P2) — quoteCommandToken refuses by name the one token neither quote form can express", () => {
  // Ends in a backslash (rules out double quotes) AND carries a single quote (rules
  // out single quotes). Emitting a broken string here would surface as a confusing
  // unbalanced-quote error somewhere else entirely.
  assert.throws(() => quoteCommandToken("C:\\it's dir\\"), {
    message: /cannot be quoted for FORMIC_HEALER_AGENT_CMD/,
  });
});

test("RULE — inside double quotes a backslash is literal, so a UNC path needs no doubling", () => {
  const adapter = adapterFromCommand('"\\\\server\\share\\node.exe" {prompt}');
  assert.equal(adapter.command, "\\\\server\\share\\node.exe");
  assert.deepEqual(adapter.args, ["{prompt}"]);
});

test('RULE — the one escape inside double quotes is \\" ; single quotes have none', () => {
  const escaped = adapterFromCommand('mycli "say \\"hi\\" now" {prompt}');
  assert.deepEqual(escaped.args, ['say "hi" now', "{prompt}"]);
  const literal = adapterFromCommand("mycli 'a\\\"b' {prompt}");
  assert.deepEqual(literal.args, ['a\\"b', "{prompt}"]);
});

test("a trailing backslash is kept, quoted or not (Windows paths end in one)", () => {
  assert.deepEqual(adapterFromCommand("mycli C:\\tmp\\ {prompt}").args, [
    "C:\\tmp\\",
    "{prompt}",
  ]);
  assert.deepEqual(adapterFromCommand("mycli 'C:\\tmp\\' {prompt}").args, [
    "C:\\tmp\\",
    "{prompt}",
  ]);
});

test("a quoted empty argument survives as an empty token, not as nothing", () => {
  assert.deepEqual(adapterFromCommand("mycli \"\" '' {prompt}").args, [
    "",
    "",
    "{prompt}",
  ]);
});

test("REVIEW REGRESSION (P2) — an unbalanced quote is refused by name, never silently closed at EOF", () => {
  assert.throws(() => adapterFromCommand('mycli "unterminated'), {
    message: /unbalanced double quote/,
  });
  assert.throws(() => adapterFromCommand("mycli 'unterminated"), {
    message: /unbalanced single quote/,
  });
});

test("REVIEW REGRESSION (P2) — an empty, whitespace-only, or empty-quoted command is refused, not spawned", () => {
  // The last two are the sibling the first version of this guard missed: a QUOTED
  // whitespace-only executable is a real token, so `!command` was false and it passed.
  for (const template of [
    "",
    "   ",
    '""',
    "'' --flag",
    '"   " --flag',
    "'\t'",
  ]) {
    assert.throws(() => adapterFromCommand(template), {
      message: /FORMIC_HEALER_AGENT_CMD is empty/,
    });
  }
});

test("REVIEW REGRESSION (P2) — a parse error never echoes the template, which can carry a credential", () => {
  // The CLI prints this message; a healer command may legitimately carry an inline
  // token, so the error may name only the error kind and where it happened.
  const marker = "s3cr3t-marker-value";
  assert.throws(
    () => adapterFromCommand(`mycli --token ${marker} "unterminated`),
    (error: Error) => {
      assert.ok(
        !error.message.includes(marker),
        `error message leaked the template: ${error.message.replace(marker, "<redacted>")}`,
      );
      assert.match(
        error.message,
        /unbalanced double quote opened at character \d+/,
      );
      return true;
    },
  );
});

test("REVIEW REGRESSION (P2) — the README's Windows .env line survives this repo's own dotenv parsing", () => {
  // The doc is only true if the value dotenv hands us is the value the tokenizer
  // wants: dotenv keeps a single-quoted value literal, so the inner double quotes
  // reach us intact. A `"\"...\""` form does NOT survive it — that is the bug this pins.
  const line =
    String.raw`FORMIC_HEALER_AGENT_CMD='"C:\Program Files\mycli\mycli.exe" --flag {prompt}'` +
    "\n";
  const value = dotenv.parse(Buffer.from(line)).FORMIC_HEALER_AGENT_CMD;
  const adapter = adapterFromCommand(value!);
  assert.equal(adapter.command, "C:\\Program Files\\mycli\\mycli.exe");
  assert.deepEqual(adapter.args, ["--flag", "{prompt}"]);
});

test("a {model}-token adapter with no model set refuses at construction, never spawns with a dangling flag", () => {
  assert.throws(
    () =>
      agentCliHealer({
        adapter: fakeAdapterWithModelToken(),
        workspaceRoot: scratch,
        timeoutMs: 20_000,
      }),
    /\{model\}.*no model was set/,
  );
});

test("a nested proposal with no reason takes its reason from the agent's own output, marked", async () => {
  // codex-cli 0.151.0, live: `rewrite-target:` nested and no `reason`, twice.
  const healer = agentCliHealer({
    adapter: fakeAdapter(),
    workspaceRoot: scratch,
    timeoutMs: 20_000,
    env: { ...process.env, FAKE_AGENT_MODE: "no-reason" },
  });
  const proposal = await healer.propose(CONTEXT);
  assert.equal(proposal.kind, "rewrite-target");
  assert.match(proposal.reason, /^\[from agent output\] I rewrote the target/);
});

test("an agent that writes nothing is a failure to propose, not a proposal", async () => {
  const healer = agentCliHealer({
    adapter: fakeAdapter(),
    workspaceRoot: scratch,
    timeoutMs: 20_000,
    env: { ...process.env, FAKE_AGENT_MODE: "silent" },
  });
  await assert.rejects(healer.propose(CONTEXT), /produced no proposal/);
});

test("an agent that hangs is killed at the timeout, naming the workspace", async () => {
  const healer = agentCliHealer({
    adapter: fakeAdapter(),
    workspaceRoot: scratch,
    timeoutMs: 1500,
    env: { ...process.env, FAKE_AGENT_MODE: "hang" },
  });
  await assert.rejects(
    healer.propose(CONTEXT),
    /did not finish within 1500 ms.*workspace/,
  );
});
