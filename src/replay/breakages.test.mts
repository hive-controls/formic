/** The breakage materialiser must never touch the pristine app it copies from. */
import { test } from "node:test";
import assert from "node:assert/strict";
import { spawnSync } from "node:child_process";
import { existsSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";

const FIXTURES = join(
  dirname(fileURLToPath(import.meta.url)),
  "..",
  "..",
  "fixtures",
);
const APPLY = join(FIXTURES, "breakages", "apply.mjs");
const APP = join(FIXTURES, "sample-app");

test("apply.mjs refuses the sample app itself, a directory inside it, and a parent of it — before deleting anything", () => {
  for (const destination of [APP, join(APP, "variant"), FIXTURES]) {
    const run = spawnSync(
      process.execPath,
      [APPLY, "renamed-selector", destination],
      {
        encoding: "utf8",
      },
    );
    assert.equal(run.status, 1, destination);
    assert.match(run.stderr, /refusing: destination/);
  }
  assert.ok(existsSync(join(APP, "index.html")), "the pristine app is intact");
  assert.ok(existsSync(join(FIXTURES, "specs", "approve-an-order.yaml")));
});
