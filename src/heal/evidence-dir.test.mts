/** Where a repair PR commits its evidence bundle: derived from the spec, or overridden. */
import { test } from "node:test";
import assert from "node:assert/strict";
import { evidenceDirFor } from "./evidence-dir.mts";

test("a spec under a `specs/` directory puts evidence beside it (the layout this repo uses)", () => {
  assert.equal(
    evidenceDirFor(
      "usecases/self-healing-e2e/specs/approve-an-order.yaml",
      "dec_1",
    ),
    "usecases/self-healing-e2e/evidence/dec_1",
  );
  assert.equal(
    evidenceDirFor("fixtures/specs/approve-an-order.yaml", "dec_1"),
    "fixtures/evidence/dec_1",
  );
});

test("a spec anywhere else gets an evidence/ directory next to it — never above the repo root", () => {
  assert.equal(evidenceDirFor("e2e/login.yaml", "dec_2"), "e2e/evidence/dec_2");
  assert.equal(evidenceDirFor("login.yaml", "dec_3"), "evidence/dec_3");
  assert.equal(evidenceDirFor("specs/login.yaml", "dec_4"), "evidence/dec_4");
});

test("an explicit override wins and is normalized to a repo-relative posix path", () => {
  assert.equal(
    evidenceDirFor("specs/login.yaml", "dec_5", "artifacts/e2e-doctor/"),
    "artifacts/e2e-doctor/dec_5",
  );
  assert.equal(
    evidenceDirFor("specs/login.yaml", "dec_5", "./artifacts//e2e-doctor"),
    "artifacts/e2e-doctor/dec_5",
  );
  assert.equal(
    evidenceDirFor("specs/login.yaml", "dec_5", ""),
    "evidence/dec_5",
  );
});

test("an override that escapes the repo, or is absolute, is refused", () => {
  assert.throws(
    () => evidenceDirFor("specs/login.yaml", "dec_6", "../outside"),
    /inside the repository/,
  );
  assert.throws(
    () => evidenceDirFor("specs/login.yaml", "dec_6", "/tmp/evidence"),
    /inside the repository/,
  );
});

test("windows separators in the spec path are handled", () => {
  assert.equal(
    evidenceDirFor("usecases\\self-healing-e2e\\specs\\a.yaml", "dec_7"),
    "usecases/self-healing-e2e/evidence/dec_7",
  );
});
