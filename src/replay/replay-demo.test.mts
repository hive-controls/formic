/**
 * Exit-code contract of the replay CLI. CI callers route on it, so a run that never
 * started must be distinguishable from a replay that failed.
 */
import { test } from "node:test";
import assert from "node:assert/strict";
import { spawn } from "node:child_process";
import { mkdtempSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import { saveSpec } from "../spec/parse.mts";
import { serveDirectory } from "./sample-app-server.mts";

const HERE = dirname(fileURLToPath(import.meta.url));
const DEMO = join(HERE, "replay-demo.mts");
const SAMPLE_APP = join(HERE, "..", "..", "fixtures", "sample-app");

/** Async on purpose: a spawnSync would block this process's event loop, and the
 *  in-process static server the third test relies on could never answer the browser. */
function runDemo(
  args: string[],
  env: Record<string, string> = {},
): Promise<{ status: number | null; stdout: string; stderr: string }> {
  return new Promise((resolve, reject) => {
    const child = spawn(process.execPath, ["--import", "tsx", DEMO, ...args], {
      // FORMIC_GATE=local: with a Solari key in the environment the Fleet would otherwise
      // default to Solari, and the suite must never spend cloud sessions.
      env: {
        ...process.env,
        FORMIC_GATE: "local",
        HIVEDECK_STATUS_FILE: "",
        ...env,
      },
    });
    let stdout = "";
    let stderr = "";
    child.stdout.on("data", (chunk: Buffer) => (stdout += chunk.toString()));
    child.stderr.on("data", (chunk: Buffer) => (stderr += chunk.toString()));
    child.on("error", reject);
    child.on("close", (status) => resolve({ status, stdout, stderr }));
  });
}

test("REVIEW REGRESSION (P2) — an unreadable spec exits 2, not 1", async () => {
  const result = await runDemo(["/nonexistent/spec.yaml"]);
  assert.equal(result.status, 2, result.stderr);
  assert.match(result.stderr, /could not start/);
});

test("the status-file opt-in covers a setup failure", async () => {
  const dir = mkdtempSync(join(tmpdir(), "formic-demo-status-"));
  try {
    const statusFile = join(dir, "status.ndjson");
    const result = await runDemo(["/nonexistent/spec.yaml"], {
      HIVEDECK_STATUS_FILE: statusFile,
    });

    assert.equal(result.status, 2, result.stderr);
    assert.deepEqual(
      readFileSync(statusFile, "utf8")
        .trim()
        .split("\n")
        .map((line) => JSON.parse(line) as unknown),
      [{ event: "started" }, { event: "failed" }],
    );
  } finally {
    rmSync(dir, { recursive: true, force: true });
  }
});

test("REVIEW REGRESSION (P2) — a mistyped FORMIC_GATE is refused, never silently local", async () => {
  // `FORMIC_GATE=solar` must not fall through to the local driver: a CI run meant to
  // produce a cloud recording would pass green with no recording at all. The spec must
  // be valid so the run gets as far as choosing the driver.
  const dir = mkdtempSync(join(tmpdir(), "formic-demo-"));
  try {
    const specFile = join(dir, "ok.yaml");
    writeFileSync(
      specFile,
      saveSpec({
        name: "ok",
        startUrl: "http://app.test/",
        steps: [
          { id: "st_1", index: 1, action: "goto", target: "http://app.test/" },
        ],
      }),
    );
    const result = await runDemo([specFile], { FORMIC_GATE: "solar" });
    assert.equal(result.status, 2, result.stderr);
    assert.match(result.stderr, /unsupported FORMIC_GATE "solar"/);
  } finally {
    rmSync(dir, { recursive: true, force: true });
  }
});

test("REVIEW REGRESSION (P2) — an invalid spec exits 2, not 1", async () => {
  const dir = mkdtempSync(join(tmpdir(), "formic-demo-"));
  try {
    const specFile = join(dir, "bad.yaml");
    writeFileSync(specFile, "name: broken\nsteps: []\n");
    const result = await runDemo([specFile]);
    assert.equal(result.status, 2, result.stderr);
    assert.match(result.stderr, /spec is invalid/);
  } finally {
    rmSync(dir, { recursive: true, force: true });
  }
});

test("REVIEW REGRESSION (P2) — a PASSED replay whose evidence cannot be written exits 2, not 1", async () => {
  // Exit 1 means the replay failed. An operational failure after a green run (replay
  // fetch, assembly, or the evidence write) must not be reported as a failed test.
  const app = await serveDirectory(SAMPLE_APP);
  const dir = mkdtempSync(join(tmpdir(), "formic-demo-"));
  try {
    const specFile = join(dir, "one-step.yaml");
    writeFileSync(
      specFile,
      saveSpec({
        name: "one-step",
        startUrl: `${app.baseUrl}/`,
        steps: [
          { id: "st_1", index: 1, action: "goto", target: `${app.baseUrl}/` },
        ],
      }),
    );
    const unwritable = join(dir, "no-such-dir", "evidence.json");
    const result = await runDemo([specFile, unwritable]);
    assert.equal(result.status, 2, result.stderr);
    assert.match(result.stderr, /evidence failed/);
    assert.match(result.stdout, /PASSED/, "the replay itself must have passed");
  } finally {
    rmSync(dir, { recursive: true, force: true });
    await app.close();
  }
});
