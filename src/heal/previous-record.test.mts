/** Finding the previous record on disk: newest by timestamp, same spec, never throws. */
import { test } from "node:test";
import assert from "node:assert/strict";
import { mkdirSync, mkdtempSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { findPreviousRecord } from "./previous-record.mts";

function bundle(
  root: string,
  decisionId: string,
  specName: string,
  timestamp: string,
): void {
  mkdirSync(join(root, decisionId), { recursive: true });
  writeFileSync(
    join(root, decisionId, "initial.json"),
    JSON.stringify({ decisionId, specName, timestamp, steps: [] }),
  );
}

test("newest matching record wins; other specs, the current decision, junk dirs and a missing root are skipped", () => {
  const root = mkdtempSync(join(tmpdir(), "e2e-doctor-prev-"));
  try {
    bundle(root, "dec_old", "approve-an-order", "2026-09-01T10:00:00.000Z");
    bundle(root, "dec_new", "approve-an-order", "2026-09-02T10:00:00.000Z");
    bundle(root, "dec_now", "approve-an-order", "2026-09-02T12:00:00.000Z");
    bundle(root, "dec_other", "checkout", "2026-09-03T10:00:00.000Z");
    mkdirSync(join(root, "not-a-bundle"));
    writeFileSync(join(root, "stray.txt"), "x");
    mkdirSync(join(root, "dec_broken"));
    writeFileSync(join(root, "dec_broken", "initial.json"), "{not json");

    const previous = findPreviousRecord(root, "approve-an-order", {
      excludeDecisionId: "dec_now",
    });
    assert.equal(previous?.decisionId, "dec_new");
    assert.equal(
      findPreviousRecord(root, "approve-an-order")?.decisionId,
      "dec_now",
      "without an exclusion the newest is the current run",
    );
    assert.equal(findPreviousRecord(root, "nothing-here"), undefined);
    assert.equal(
      findPreviousRecord(join(root, "missing"), "approve-an-order"),
      undefined,
    );
  } finally {
    rmSync(root, { recursive: true, force: true });
  }
});
