import { readFileSync, readdirSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import { test } from "node:test";
import assert from "node:assert/strict";
import { validate } from "./index.mts";

const FIXTURES_DIR = join(
  dirname(fileURLToPath(import.meta.url)),
  "../fixtures/conformance",
);

const EXPECTED_INVALID_PATHS: Record<string, string> = {
  "missing-required.json": "",
  "wrong-type.json": "/launch/args",
  "unknown-property.json": "",
  "bad-env-name.json": "/env/0/name",
};

function loadFixture(subdir: "valid" | "invalid", name: string): unknown {
  const path = join(FIXTURES_DIR, subdir, name);
  return JSON.parse(readFileSync(path, "utf8"));
}

test("every valid fixture validates", () => {
  const files = readdirSync(join(FIXTURES_DIR, "valid"));
  assert.ok(files.length > 0, "expected at least one valid fixture");
  for (const file of files) {
    const manifest = loadFixture("valid", file);
    const result = validate(manifest);
    assert.equal(
      result.valid,
      true,
      `${file} should validate; errors: ${result.errors.join(", ")}`,
    );
  }
});

test("every invalid fixture is rejected with the expected error path", () => {
  const files = readdirSync(join(FIXTURES_DIR, "invalid"));
  assert.ok(files.length > 0, "expected at least one invalid fixture");
  for (const file of files) {
    const manifest = loadFixture("invalid", file);
    const result = validate(manifest);
    assert.equal(result.valid, false, `${file} should be rejected`);
    const expectedPath = EXPECTED_INVALID_PATHS[file];
    assert.ok(
      expectedPath !== undefined,
      `no expected path registered for fixture ${file}`,
    );
    const matchingError = result.errors.find((error) =>
      error.startsWith(expectedPath === "" ? "/" : expectedPath),
    );
    assert.ok(
      matchingError,
      `${file} expected an error at path "${expectedPath}"; got: ${result.errors.join(", ")}`,
    );
  }
});

test("an unknown-property error names the offending property", () => {
  const manifest = loadFixture("invalid", "unknown-property.json");
  const result = validate(manifest);
  assert.equal(result.valid, false);
  const matchingError = result.errors.find((error) =>
    error.includes("unknownField"),
  );
  assert.ok(
    matchingError,
    `expected an error naming the offending property; got: ${result.errors.join(", ")}`,
  );
});
