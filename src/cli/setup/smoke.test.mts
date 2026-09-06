/**
 * `smokeHeal`'s adjudication against fake healers: ok is "a proposal parseProposal
 * accepts within the timeout", not "the proposal was no-repair" — see smoke.mts's
 * header for why. No real network egress and no real agent spawn anywhere here.
 */
import { test } from "node:test";
import assert from "node:assert/strict";
import { scriptedHealer } from "../../heal/scripted.mts";
import type { Healer } from "../../heal/types.mts";
import { smokeContext, smokeHeal } from "./smoke.mts";

test("smokeContext pins the exact synthetic failure the wizard smoke-tests against", () => {
  const context = smokeContext();
  assert.equal(context.url, "http://127.0.0.1:1/smoke");
  assert.equal(context.ariaSnapshot, '- document "Smoke check"');
  assert.equal(context.failure.error, "element not found");
  assert.equal(context.failedStep.action, "click");
  assert.equal(context.failedStep.target, "#approve");
  assert.equal(context.attempt, 1);
  assert.deepEqual(context.priorAttempts, []);
});

test("a no-repair proposal is ok, with its kind reported", async () => {
  const healer = scriptedHealer([
    { kind: "no-repair", reason: "nothing to fix" },
  ]);
  const result = await smokeHeal(healer, { timeoutMs: 5_000 });
  assert.equal(result.ok, true);
  assert.equal(result.kind, "no-repair");
  assert.equal(result.usage, null);
});

test("a non no-repair proposal is still ok — the kind is reported, not gated on", async () => {
  const healer = scriptedHealer([
    {
      kind: "propose-assert-change",
      stepId: "st_2",
      to: { selector: "#approve" },
      reason: "smoke context looks stale",
    },
  ]);
  const result = await smokeHeal(healer, { timeoutMs: 5_000 });
  assert.equal(result.ok, true);
  assert.equal(result.kind, "propose-assert-change");
});

test("a healer that throws is not ok, and the error message is reported", async () => {
  const healer = scriptedHealer(() => {
    throw new Error("endpoint refused the request");
  });
  const result = await smokeHeal(healer, { timeoutMs: 5_000 });
  assert.equal(result.ok, false);
  assert.match(result.error ?? "", /endpoint refused the request/);
});

test("a healer that never resolves times out", async () => {
  const healer: Healer = {
    name: "stuck",
    modelVersion: "stuck",
    propose: () => new Promise(() => {}),
  };
  const result = await smokeHeal(healer, { timeoutMs: 20 });
  assert.equal(result.ok, false);
  assert.match(result.error ?? "", /timed out/);
});

test("usage rides on the healer object and is passed through when present", async () => {
  const healer = {
    name: "fake",
    modelVersion: "fake",
    lastUsage: { inputTokens: 3, outputTokens: 2 },
    async propose() {
      return { kind: "no-repair" as const, reason: "ok" };
    },
  };
  const result = await smokeHeal(healer, { timeoutMs: 5_000 });
  assert.equal(result.ok, true);
  assert.deepEqual(result.usage, { inputTokens: 3, outputTokens: 2 });
});
