/**
 * The e2e-doctor CLI's `--app` wiring, end to end via spawn: gate + host
 * announcement, exit codes, and the evidence file's `host` block.
 */
import { test } from "node:test";
import assert from "node:assert/strict";
import { spawn } from "node:child_process";
import { createServer } from "node:http";
import type { AddressInfo } from "node:net";
import {
  copyFileSync,
  existsSync,
  mkdtempSync,
  readFileSync,
  rmSync,
  statSync,
  writeFileSync,
} from "node:fs";
import { tmpdir } from "node:os";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import { parse as parseYaml } from "yaml";

const HERE = dirname(fileURLToPath(import.meta.url));
const CLI = join(HERE, "e2e-doctor.mts");
const FIXTURES = join(HERE, "..", "..", "fixtures");
const SAMPLE_APP = join(FIXTURES, "sample-app");
const SAMPLE_SPEC = join(FIXTURES, "specs", "approve-an-order.yaml");
// Resolved once, absolutely, from THIS file's own location — never the bare "tsx"
// specifier passed straight to --import, which Node resolves relative to the CHILD
// process's cwd: a spawn into a scratch tmp dir (the `cwd` option below) has no
// node_modules chain back to the repo, so the bare form fails there with
// ERR_MODULE_NOT_FOUND even though it works when the child's cwd stays inside the repo.
// Kept as the file:// URL string itself (not converted to an OS path): Node's --import
// flag requires a file:// URL for an absolute path on Windows, rejecting a raw
// drive-letter path with ERR_UNSUPPORTED_ESM_URL_SCHEME.
const TSX_LOADER = import.meta.resolve("tsx");

/** Async on purpose: a spawnSync would block this process's event loop while the
 *  in-process local host waits for a browser to connect to it. */
function runCli(
  args: string[],
  env: Record<string, string> = {},
  options: { cwd?: string } = {},
): Promise<{ status: number | null; stdout: string; stderr: string }> {
  return new Promise((resolvePromise, reject) => {
    const child = spawn(
      process.execPath,
      ["--import", TSX_LOADER, CLI, ...args],
      {
        cwd: options.cwd,
        // FORMIC_GATE=local, SOLARI_API_KEY="": the suite must never spend cloud
        // sessions or sandboxes, even when a real key is present in the environment.
        // FORMIC_HEALER*: blanked so a real shell export never leaks into a spawned
        // test — every healer-selecting test below is explicit about its own flags.
        env: {
          ...process.env,
          FORMIC_GATE: "local",
          SOLARI_API_KEY: "",
          FORMIC_HEALER: "",
          FORMIC_HEALER_BASE_URL: "",
          FORMIC_HEALER_MODEL: "",
          FORMIC_HEALER_API_KEY: "",
          FORMIC_HEALER_AGENT_CMD: "",
          FORMIC_HEALER_TIMEOUT_MS: "",
          HIVEDECK_STATUS_FILE: "",
          ...env,
        },
      },
    );
    let stdout = "";
    let stderr = "";
    child.stdout.on("data", (chunk: Buffer) => (stdout += chunk.toString()));
    child.stderr.on("data", (chunk: Buffer) => (stderr += chunk.toString()));
    child.on("error", reject);
    child.on("close", (status) => resolvePromise({ status, stdout, stderr }));
  });
}

/** Lifted from openai-compatible.test.mts's own fakeEndpoint: a real HTTP server on
 *  loopback answering one canned chat-completion reply, for the CLI spawn tests below
 *  to point a real setup/heal run's OpenAI-compatible healer at. */
function fakeHealerEndpoint(
  status: number,
  body: unknown,
): Promise<{ baseUrl: string; close: () => Promise<void> }> {
  return new Promise((resolveEndpoint) => {
    const server = createServer((req, res) => {
      req.on("data", () => {});
      req.on("end", () => {
        res.writeHead(status, { "content-type": "application/json" });
        res.end(JSON.stringify(body));
      });
    });
    server.listen(0, "127.0.0.1", () => {
      const { port } = server.address() as AddressInfo;
      resolveEndpoint({
        baseUrl: `http://127.0.0.1:${port}/v1`,
        close: () => new Promise<void>((r) => server.close(() => r())),
      });
    });
  });
}

test("--app hosts the sample app locally: gate and host both announced, exit 0", async () => {
  const result = await runCli(["replay", SAMPLE_SPEC, "--app", SAMPLE_APP]);
  assert.equal(result.status, 0, result.stdout + result.stderr);
  assert.match(result.stdout, /^gate: local \(Inside\)/m);
  assert.match(result.stdout, /^host: local \(Inside\)/m);
});

test("the status-file opt-in writes NDJSON to the file only — stderr stays human-readable", async () => {
  const dir = mkdtempSync(join(tmpdir(), "formic-cli-status-"));
  try {
    const evidenceFile = join(dir, "evidence.json");
    const statusFile = join(dir, "status.ndjson");
    const result = await runCli(
      ["replay", SAMPLE_SPEC, evidenceFile, "--app", SAMPLE_APP],
      { HIVEDECK_STATUS_FILE: statusFile },
    );

    assert.equal(result.status, 0, result.stdout + result.stderr);
    assert.ok(
      !result.stderr.includes('"event"'),
      `stderr must carry no status JSON, got: ${result.stderr}`,
    );
    assert.ok(
      !result.stdout.includes('"event"'),
      `stdout must carry no status JSON, got: ${result.stdout}`,
    );
    const events = readFileSync(statusFile, "utf8")
      .trim()
      .split("\n")
      .map((line) => JSON.parse(line) as unknown);
    assert.deepEqual(events, [
      { event: "started" },
      { event: "progress" },
      { event: "ok", artifact: evidenceFile },
    ]);
  } finally {
    rmSync(dir, { recursive: true, force: true });
  }
});

test("the opt-in stays silent everywhere when HIVEDECK_STATUS_FILE is unset", async () => {
  const result = await runCli(["replay", SAMPLE_SPEC, "--app", SAMPLE_APP]);
  assert.equal(result.status, 0, result.stdout + result.stderr);
  assert.ok(
    !result.stderr.includes('"event"'),
    `stderr must carry no status JSON, got: ${result.stderr}`,
  );
  assert.ok(
    !result.stdout.includes('"event"'),
    `stdout must carry no status JSON, got: ${result.stdout}`,
  );
});

test("SIGINT while a hosted replay is running emits exactly one failed terminal event before exit 130", async () => {
  const dir = mkdtempSync(join(tmpdir(), "formic-cli-sigint-"));
  try {
    const statusFile = join(dir, "status.ndjson");
    const result = await new Promise<{
      status: number | null;
      signal: NodeJS.Signals | null;
      stdout: string;
      stderr: string;
    }>((resolvePromise, reject) => {
      const child = spawn(
        process.execPath,
        [
          "--import",
          TSX_LOADER,
          CLI,
          "replay",
          SAMPLE_SPEC,
          "--app",
          SAMPLE_APP,
        ],
        {
          env: {
            ...process.env,
            FORMIC_GATE: "local",
            SOLARI_API_KEY: "",
            HIVEDECK_STATUS_FILE: statusFile,
          },
        },
      );
      let stdout = "";
      let stderr = "";
      let signaled = false;
      // Anchored on the status FILE, not stdout: watchForSignal registers the
      // SIGINT/SIGTERM handlers synchronously, immediately before runReplayCli's
      // first statement (which emits "progress"). A "gate"/"host" stdout anchor
      // races ahead of that registration (measured: the default SIGINT
      // disposition killed the child before the handler was armed) — the
      // "progress" line landing in the file is proof the handler is already up.
      const poll = setInterval(() => {
        if (signaled) return;
        if (
          existsSync(statusFile) &&
          readFileSync(statusFile, "utf8").includes('"progress"')
        ) {
          signaled = true;
          clearInterval(poll);
          child.kill("SIGINT");
        }
      }, 2);
      child.stdout.on("data", (chunk: Buffer) => (stdout += chunk.toString()));
      child.stderr.on("data", (chunk: Buffer) => (stderr += chunk.toString()));
      child.on("error", (error) => {
        clearInterval(poll);
        reject(error);
      });
      child.on("close", (status, signal) => {
        clearInterval(poll);
        resolvePromise({ status, signal, stdout, stderr });
      });
    });

    assert.equal(result.status, 130, result.stdout + result.stderr);
    assert.ok(
      !result.stderr.includes('"event"'),
      `stderr must carry no status JSON, got: ${result.stderr}`,
    );
    const events = readFileSync(statusFile, "utf8")
      .trim()
      .split("\n")
      .map((line) => JSON.parse(line) as unknown);
    assert.deepEqual(events, [
      { event: "started" },
      { event: "progress" },
      { event: "failed" },
    ]);
  } finally {
    rmSync(dir, { recursive: true, force: true });
  }
});

test("a mistyped FORMIC_HOST is refused before a session opens, naming both host ids", async () => {
  const result = await runCli(["replay", SAMPLE_SPEC, "--app", SAMPLE_APP], {
    FORMIC_HOST: "solari-sandbx",
  });
  assert.equal(result.status, 2, result.stdout + result.stderr);
  assert.match(result.stderr, /solari-sandbox/);
  assert.match(result.stderr, /"local"/);
});

test("--healer agent:cluade is refused before a session opens, naming the known agents", async () => {
  const result = await runCli([
    "heal",
    SAMPLE_SPEC,
    "--healer",
    "agent:cluade",
  ]);
  assert.equal(result.status, 2, result.stdout + result.stderr);
  assert.match(result.stderr, /agent:cluade/);
  assert.match(result.stderr, /agent:claude/);
});

test("--healer agent:cluade with --app: the hosted app is still cleaned up, and a successful cleanup stays silent", async () => {
  // A later setup step (--healer resolution) fails AFTER --app already opened a real
  // host — exercising the setup-failure catch's own cleanup call for real, not just
  // the case where nothing was ever opened.
  const result = await runCli([
    "heal",
    SAMPLE_SPEC,
    "--app",
    SAMPLE_APP,
    "--healer",
    "agent:cluade",
  ]);
  assert.equal(result.status, 2, result.stdout + result.stderr);
  assert.match(result.stderr, /could not start:.*agent:cluade/);
  assert.ok(
    !result.stderr.includes("cleanup failed"),
    `a successful cleanup must not print anything, got: ${result.stderr}`,
  );
});

test("REVIEW REGRESSION (P2) — without --app: no host line, and today's exit code (2) for the dead-port sample spec", () => {
  return runCli(["replay", SAMPLE_SPEC]).then((result) => {
    assert.ok(!/^host: /m.test(result.stdout), result.stdout);
    // Measured on master before this change: exit 2 (the local driver captures a
    // non-renderable segment for the failed goto, so evidence assembly itself fails —
    // never 1, which would misreport a harness failure as a failed test).
    assert.equal(result.status, 2, result.stdout + result.stderr);
  });
});

test("--app with an evidence file: the written record carries a host block, and the app dir is untouched", async () => {
  const dir = mkdtempSync(join(tmpdir(), "e2e-doctor-"));
  try {
    const outFile = join(dir, "out.json");
    // --app comes BEFORE the evidence positional on purpose: if --app ever dropped out
    // of positionals()'s skip list, its directory argument would slide into the
    // evidenceFile slot instead of out.json — this ordering is what catches that.
    const result = await runCli([
      "replay",
      SAMPLE_SPEC,
      "--app",
      SAMPLE_APP,
      outFile,
    ]);
    assert.equal(result.status, 0, result.stdout + result.stderr);
    const record = JSON.parse(readFileSync(outFile, "utf8")) as {
      host?: { name: string; kind: string; baseUrl: string } | null;
    };
    assert.equal(record.host?.name, "local");
    assert.equal(record.host?.kind, "Inside");
    assert.ok(
      statSync(SAMPLE_APP).isDirectory(),
      "--app's directory argument must never be overwritten as if it were the evidence file",
    );
  } finally {
    rmSync(dir, { recursive: true, force: true });
  }
});

test("heal --app --bundle: the bundle diffs against the spec as written, not the rebased copy", async () => {
  // The sample spec passes cleanly against the sample app once --app hosts it (proven
  // by the first test above), so the healer is never even asked for a proposal — the
  // bundle's spec diff must come out EMPTY. Before the fix, writeEvidenceBundle's
  // "original" argument was the run's rebased spec while its healed-result argument was
  // already unrebased (heal/cli.mts's own unrebase step) — comparing the two showed the
  // host's origin as a spurious change, even on a run that repaired nothing.
  const dir = mkdtempSync(join(tmpdir(), "e2e-doctor-bundle-"));
  try {
    const specCopy = join(dir, "approve-an-order.yaml");
    copyFileSync(SAMPLE_SPEC, specCopy);
    const bundleDir = join(dir, "bundle");
    const result = await runCli([
      "heal",
      specCopy,
      "--app",
      SAMPLE_APP,
      "--bundle",
      bundleDir,
    ]);
    assert.equal(result.status, 0, result.stdout + result.stderr);
    assert.match(result.stdout, /PASSED/, result.stdout);

    const record = JSON.parse(
      readFileSync(join(bundleDir, "initial.json"), "utf8"),
    ) as { host?: { baseUrl: string } | null };
    // Sanity check that this run really did go through the local host (its own
    // ephemeral loopback origin, not the spec's captured :4173) — otherwise an empty
    // diff would prove nothing about the fix.
    assert.match(record.host?.baseUrl ?? "", /^http:\/\/127\.0\.0\.1:\d+\/$/);

    const html = readFileSync(join(bundleDir, "index.html"), "utf8");
    assert.ok(
      !html.includes("Spec diff"),
      "a passing run with nothing repaired must render no spec-diff section at all",
    );
  } finally {
    rmSync(dir, { recursive: true, force: true });
  }
});

test("setup --non-interactive writes a Cocoon after a passing smoke heal; heal --healer resolves it", async () => {
  const dir = mkdtempSync(join(tmpdir(), "e2e-doctor-setup-cli-"));
  const endpoint = await fakeHealerEndpoint(200, {
    choices: [
      {
        message: {
          content: '{"kind":"no-repair","reason":"nothing to repair"}',
        },
      },
    ],
    usage: { prompt_tokens: 11, completion_tokens: 7 },
  });
  try {
    writeFileSync(join(dir, ".gitignore"), "node_modules/\n.env\n");
    const setupResult = await runCli(
      [
        "setup",
        "--non-interactive",
        "--healer-kind",
        "api",
        "--preset",
        "custom",
        "--base-url",
        endpoint.baseUrl,
        "--model",
        "m",
        "--api-key-env",
        "TEST_KEY",
        "--profile-name",
        "t",
        "--make-default",
      ],
      { TEST_KEY: "x" },
      { cwd: dir },
    );
    assert.equal(
      setupResult.status,
      0,
      setupResult.stdout + setupResult.stderr,
    );
    const profilesText = readFileSync(
      join(dir, "formic.profiles.yaml"),
      "utf8",
    );
    const parsed = parseYaml(profilesText) as {
      default?: string;
      profiles: Record<string, { apiKeyFrom?: string }>;
    };
    assert.equal(parsed.default, "t");
    assert.equal(parsed.profiles.t.apiKeyFrom, "env.TEST_KEY");
    assert.ok(!profilesText.includes("x="), "no key value in the file");
    assert.ok(
      !profilesText.includes("apiKey:"),
      "no literal apiKey field in a committed profile",
    );
    assert.match(setupResult.stdout, /TEST_KEY/);
    // The suite runs on the local gate, so the printed CI snippet follows it: no cloud
    // secret is named (the earlier expectation of SOLARI_API_KEY was the bug).
    assert.match(setupResult.stdout, /FORMIC_GATE: local/);
    assert.ok(!setupResult.stdout.includes("secrets.SOLARI_API_KEY"));
    assert.match(setupResult.stdout, /smoke heal: ok in \d+(\.\d+)? s/);
    assert.match(setupResult.stdout, /11 in \/ 7 out/);

    const healResult = await runCli(
      ["heal", SAMPLE_SPEC, "--app", SAMPLE_APP, "--healer", "t"],
      { TEST_KEY: "x" },
      { cwd: dir },
    );
    assert.match(
      healResult.stdout,
      /healer: t \(api\) — --healer t/,
      healResult.stdout + healResult.stderr,
    );
  } finally {
    await endpoint.close();
    rmSync(dir, { recursive: true, force: true });
  }
});

test("setup --non-interactive against a 401 endpoint: non-zero exit, nothing written", async () => {
  const dir = mkdtempSync(join(tmpdir(), "e2e-doctor-setup-401-"));
  const endpoint = await fakeHealerEndpoint(401, {
    error: { message: "bad key" },
  });
  try {
    writeFileSync(join(dir, ".gitignore"), "node_modules/\n.env\n");
    const result = await runCli(
      [
        "setup",
        "--non-interactive",
        "--healer-kind",
        "api",
        "--preset",
        "custom",
        "--base-url",
        endpoint.baseUrl,
        "--model",
        "m",
        "--api-key-env",
        "TEST_KEY",
        "--profile-name",
        "t",
      ],
      { TEST_KEY: "x" },
      { cwd: dir },
    );
    assert.notEqual(result.status, 0, result.stdout + result.stderr);
    assert.match(result.stderr, /HTTP 401/);
    assert.equal(existsSync(join(dir, "formic.profiles.yaml")), false);
    assert.equal(existsSync(join(dir, ".env")), false);
  } finally {
    await endpoint.close();
    rmSync(dir, { recursive: true, force: true });
  }
});

test("record refuses a malformed argv instead of acting on a guess", async () => {
  // `record` read its argv by hand while every other subcommand went through the strict
  // checker: `--out` with no value silently defaulted, `--out --yes` wrote a file called
  // "--yes", and an unknown flag was ignored — a typo that still exits 0, having done
  // something other than what was asked.
  for (const [argv, expected] of [
    [
      ["record", "flow", "--url", "http://x.test/", "--out"],
      /--out requires a value/,
    ],
    [
      ["record", "flow", "--url", "http://x.test/", "--out", "--yes"],
      /--out requires a value/,
    ],
    [
      ["record", "flow", "--url", "http://x.test/", "--ou", "x.yaml"],
      /unknown flag/,
    ],
    [
      ["record", "flow", "--url", "http://x.test/", "--yes", "--yes"],
      /--yes given more than once/,
    ],
    [
      ["record", "flow", "extra", "--url", "http://x.test/"],
      /unexpected argument/,
    ],
  ] as [string[], RegExp][]) {
    const result = await runCli(argv);
    assert.equal(result.status, 2, `${argv.join(" ")} exited ${result.status}`);
    assert.match(result.stderr, expected, `for ${argv.join(" ")}`);
  }
});

test("--help exits 0 and prints usage to stdout", async () => {
  const result = await runCli(["--help"]);
  assert.equal(result.status, 0, result.stdout + result.stderr);
  assert.match(result.stdout, /^usage:/m);
});

test("-h exits 0 and prints usage to stdout", async () => {
  const result = await runCli(["-h"]);
  assert.equal(result.status, 0, result.stdout + result.stderr);
  assert.match(result.stdout, /^usage:/m);
});

test("setup --help exits 0 and prints usage to stdout", async () => {
  const result = await runCli(["setup", "--help"]);
  assert.equal(result.status, 0, result.stdout + result.stderr);
  assert.match(result.stdout, /^usage:/m);
});

test("a bare invocation with no command is still a usage error, exit 2", async () => {
  const result = await runCli([]);
  assert.equal(result.status, 2, result.stdout + result.stderr);
  assert.match(result.stderr, /^usage:/m);
});

test("an unknown command is still a usage error, exit 2", async () => {
  const result = await runCli(["bogus"]);
  assert.equal(result.status, 2, result.stdout + result.stderr);
  assert.match(result.stderr, /^usage:/m);
});
