/**
 * The e2e-doctor CLI's `author`/`import codegen` wiring, end to end via spawn — additive
 * to the existing `replay`/`heal`/`setup` commands.
 */
import { test } from "node:test";
import assert from "node:assert/strict";
import { spawn } from "node:child_process";
import {
  existsSync,
  mkdtempSync,
  readFileSync,
  rmSync,
  writeFileSync,
} from "node:fs";
import { tmpdir } from "node:os";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import { loadSpec } from "../spec/parse.mts";

const HERE = dirname(fileURLToPath(import.meta.url));
const CLI = join(HERE, "e2e-doctor.mts");
// A file:// URL, not an OS path: passing the bare Windows path (as e2e-doctor.test.mts
// does via fileURLToPath) makes --import mistake the drive letter for a URL scheme
// (ERR_UNSUPPORTED_ESM_URL_SCHEME, "Received protocol 'c:'") — a pre-existing Windows-only
// failure in that file, not repeated here.
const TSX_LOADER = import.meta.resolve("tsx");

function runCli(
  args: string[],
): Promise<{ status: number | null; stdout: string; stderr: string }> {
  return new Promise((resolvePromise, reject) => {
    const child = spawn(process.execPath, [
      "--import",
      TSX_LOADER,
      CLI,
      ...args,
    ]);
    let stdout = "";
    let stderr = "";
    child.stdout.on("data", (chunk: Buffer) => (stdout += chunk.toString()));
    child.stderr.on("data", (chunk: Buffer) => (stderr += chunk.toString()));
    child.on("error", reject);
    child.on("close", (status) => resolvePromise({ status, stdout, stderr }));
  });
}

const DSL = `
name: cli-authored
startUrl: http://x/
steps:
  - goto: http://x/
  - click: "#a"
    assert:
      testId: b
      visible: true
`;

test("author writes a spec file that loadSpec accepts", async () => {
  const dir = mkdtempSync(join(tmpdir(), "e2e-doctor-author-"));
  try {
    const input = join(dir, "input.yaml");
    const out = join(dir, "spec.yaml");
    writeFileSync(input, DSL);
    const result = await runCli(["author", input, "--out", out]);
    assert.equal(result.status, 0, result.stdout + result.stderr);
    const parsed = loadSpec(readFileSync(out, "utf8"));
    assert.equal(parsed.steps.length, 2);
  } finally {
    rmSync(dir, { recursive: true, force: true });
  }
});

test("author with malformed DSL refuses with exit 2, naming the problem", async () => {
  const dir = mkdtempSync(join(tmpdir(), "e2e-doctor-author-bad-"));
  try {
    const input = join(dir, "input.yaml");
    writeFileSync(input, "steps:\n  - value: nope\n");
    const result = await runCli(["author", input]);
    assert.equal(result.status, 2);
    assert.match(result.stderr, /missing an action key/);
  } finally {
    rmSync(dir, { recursive: true, force: true });
  }
});

test("author --extend appends to an existing spec without touching prior steps", async () => {
  const dir = mkdtempSync(join(tmpdir(), "e2e-doctor-extend-"));
  try {
    const input = join(dir, "input.yaml");
    const spec = join(dir, "spec.yaml");
    const additions = join(dir, "additions.yaml");
    writeFileSync(input, DSL);
    const authored = await runCli(["author", input, "--out", spec]);
    assert.equal(authored.status, 0, authored.stdout + authored.stderr);
    const before = loadSpec(readFileSync(spec, "utf8"));

    writeFileSync(
      additions,
      'steps:\n  - click: "#c"\n    assert:\n      testId: d\n      visible: true\n',
    );
    const extended = await runCli([
      "author",
      "--extend",
      spec,
      additions,
      "--out",
      spec,
    ]);
    assert.equal(extended.status, 0, extended.stdout + extended.stderr);
    const after = loadSpec(readFileSync(spec, "utf8"));
    assert.equal(after.steps.length, 3);
    assert.deepEqual(
      after.steps.slice(0, 2).map((s: { id: string }) => s.id),
      before.steps.map((s: { id: string }) => s.id),
    );
  } finally {
    rmSync(dir, { recursive: true, force: true });
  }
});

test("import codegen writes a spec and reports unsupported lines on stderr", async () => {
  const dir = mkdtempSync(join(tmpdir(), "e2e-doctor-import-"));
  try {
    const source = join(dir, "codegen.spec.ts");
    const out = join(dir, "spec.yaml");
    writeFileSync(
      source,
      `test('t', async ({ page }) => {\n  await page.goto('http://x/');\n  await page.getByLabel('L').click();\n});\n`,
    );
    const result = await runCli(["import", "codegen", source, "--out", out]);
    assert.equal(result.status, 0, result.stdout + result.stderr);
    assert.match(result.stderr, /unsupported action locator/);
    const parsed = loadSpec(readFileSync(out, "utf8"));
    assert.equal(parsed.steps.length, 1);
  } finally {
    rmSync(dir, { recursive: true, force: true });
  }
});

test("import codegen writes nothing when the spec would not load", async () => {
  // The failure this pins: the command used to exit 0 having written YAML that the
  // product's own loadSpec then refused, with no line to go and fix.
  const dir = mkdtempSync(join(tmpdir(), "e2e-doctor-import-invalid-"));
  try {
    const source = join(dir, "codegen.spec.ts");
    const out = join(dir, "spec.yaml");
    writeFileSync(
      source,
      `test('t', async ({ page }) => {\n  await page.goto('http://x/');\n  await page.click('#go');\n});\n`,
    );
    const result = await runCli(["import", "codegen", source, "--out", out]);
    assert.equal(result.status, 2);
    assert.match(result.stderr, /"click" changes state and needs an assertion/);
    assert.match(result.stderr, /nothing written/);
    assert.equal(
      existsSync(out),
      false,
      "a refused import must leave no file behind",
    );
  } finally {
    rmSync(dir, { recursive: true, force: true });
  }
});

test("--out with no path is refused instead of printing to stdout", async () => {
  // `author input.yaml --out` used to print the spec and exit 0, so the file the user
  // asked for simply never appeared and nothing said so.
  const dir = mkdtempSync(join(tmpdir(), "e2e-doctor-author-flags-"));
  try {
    const input = join(dir, "input.yaml");
    writeFileSync(input, DSL);
    const missingValue = await runCli(["author", input, "--out"]);
    assert.equal(missingValue.status, 2);
    assert.match(missingValue.stderr, /--out requires a value/);

    const unknownFlag = await runCli(["author", input, "--ou", "x.yaml"]);
    assert.equal(unknownFlag.status, 2);
    assert.match(unknownFlag.stderr, /unknown flag "--ou"/);

    const extraPositional = await runCli(["author", input, "stray.yaml"]);
    assert.equal(extraPositional.status, 2);
    assert.match(
      extraPositional.stderr,
      /unexpected argument\(s\): stray.yaml/,
    );
  } finally {
    rmSync(dir, { recursive: true, force: true });
  }
});
