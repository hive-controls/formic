/**
 * The step anchor is what addresses the evidence stream, so its one non-negotiable
 * property is tested here: anchors are strictly increasing, or the slicer's half-open
 * windows collapse.
 */
import { test } from "node:test";
import assert from "node:assert/strict";
import type { StepRecord } from "./types.mts";
import { runTimedStep } from "./step-log.mts";

test("REVIEW REGRESSION (P2) — consecutive anchors are strictly increasing even within one millisecond", async () => {
  // Two instantly-satisfied steps (an already-visible waitFor, say) start in the same
  // Date.now() tick. With equal anchors the slicer gives the first step the empty
  // window [t, t) and hands every event at t to the second — plausible evidence,
  // wrongly attributed. Fifty no-op steps back to back make the collision certain.
  const log: StepRecord[] = [];
  for (let i = 0; i < 50; i++) {
    await runTimedStep(
      log,
      { id: `st_${i}`, index: i + 1, action: "waitFor", target: "#x" },
      async () => {},
    );
  }
  for (let i = 1; i < log.length; i++) {
    assert.ok(
      log[i].startedAt > log[i - 1].startedAt,
      `step ${i + 1} anchor ${log[i].startedAt} does not exceed step ${i}'s ${log[i - 1].startedAt}`,
    );
  }
  // And an anchor never precedes its own step's end.
  for (const step of log) assert.ok(step.endedAt >= step.startedAt);
  // REVIEW REGRESSION (P2, round 7) — anchors are REAL wall-clock times, never
  // invented. A +1 bump per collision pushed the 50th anchor ~49 ms into the future,
  // so an event the step emitted at real time t fell into the PREVIOUS step's window.
  const now = Date.now();
  assert.ok(
    log[log.length - 1].startedAt <= now,
    `last anchor ${log[log.length - 1].startedAt} is ${log[log.length - 1].startedAt - now} ms in the future`,
  );
});

test("a failed step is still logged, with its error, and the failure propagates", async () => {
  const log: StepRecord[] = [];
  await assert.rejects(
    runTimedStep(log, { id: "st_f", index: 1, action: "click" }, async () => {
      throw new Error("no such element");
    }),
    /no such element/,
  );
  assert.equal(log.length, 1);
  assert.equal(log[0].outcome, "failed");
  assert.equal(log[0].error, "no such element");
});

test("the audit inputs record the REFERENCE, never a literal that should not be there", () => {
  // The validator refuses a step carrying both, so this is the belt to that brace: if a
  // malformed step ever reaches the log, what gets written down is the thing replay
  // actually used — the reference — not the literal that came along beside it.
  const log: StepRecord[] = [];
  return runTimedStep(
    log,
    {
      id: "st_1",
      index: 1,
      action: "fill",
      target: "#password",
      value: "hunter2",
      valueFrom: "env.SIGN_IN_PASSWORD",
    },
    async () => {},
  ).then(() => {
    assert.deepEqual(log[0].inputs, { valueFrom: "env.SIGN_IN_PASSWORD" });
  });
});
