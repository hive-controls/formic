/**
 * Capture-seam clock domain tests — the clock-domain defect's capture-path variant.
 *
 * `recorder.mts` anchors the step log with `runTimedStep`'s default host clock while the
 * events it addresses are stamped by the recorder inside the browser. A capture driven
 * against a cloud browser (Solari, BrowserStack, Sauce) from a machine whose clock
 * differs carries the identical defect the replay runner already fixed (commit
 * 45f9075): a skew larger than a step window empties the segment (integrity throw); a
 * skew the size of one step silently hands a segment its neighbour's events.
 *
 * Mirrors replay/runner.test.mts's "clock domains" tests, driving through `Recorder`
 * instead of `replaySpec`, against a fake page whose own `Date.now()` runs skewed.
 */
import { test } from "node:test";
import assert from "node:assert/strict";
import { Recorder } from "./recorder.mts";
import { sliceSegments, isRenderable } from "../evidence/segment.mts";
import {
  RRWEB_META,
  RRWEB_FULL_SNAPSHOT,
  type ReplayEvent,
} from "../evidence/types.mts";

const STEP_MS = 150;

function sleep(ms: number): Promise<void> {
  return new Promise((resolve) => setTimeout(resolve, ms));
}

/** A page whose own `Date.now()` runs `skewMs` ahead of this process's; each action
 *  (goto/click/fill) runs `onAction`, exactly as skewedPage() in replay/runner.test.mts
 *  routes its actions. */
function skewedPage(
  skewMs: number,
  onAction: () => Promise<void>,
): import("playwright-core").Page {
  return {
    evaluate: async (fn: unknown) => {
      const source = String(fn);
      if (/Date\.now\(\)/.test(source) && source.length < 60) {
        return Date.now() + skewMs;
      }
      throw new Error("no metrics collector in this fake");
    },
    goto: async () => await onAction(),
    click: async () => await onAction(),
    fill: async () => await onAction(),
  } as unknown as import("playwright-core").Page;
}

/** Three recorded steps, each ~STEP_MS long, each emitting one marked event at its
 *  midpoint — stamped in the PAGE's clock, exactly as a real recorder would. */
async function captureUnderSkew(skewMs: number) {
  const events: ReplayEvent[] = [];
  let step = 0;
  const onAction = async () => {
    await sleep(STEP_MS / 2);
    const at = Date.now() + skewMs;
    step += 1;
    if (step === 1) {
      // The goto's navigation pair, without which nothing renders.
      events.push({ type: RRWEB_META, timestamp: at - 2, data: {} });
      events.push({ type: RRWEB_FULL_SNAPSHOT, timestamp: at - 1, data: {} });
    }
    events.push({ type: 3, timestamp: at, data: { step } } as ReplayEvent);
    await sleep(STEP_MS / 2);
  };
  const session = {
    sessionId: "fake",
    page: skewedPage(skewMs, onAction),
    fetchReplay: async () => events,
    close: async () => {},
  };
  const recorder = new Recorder(session, "clock-domains", "fake-driver");
  await recorder.goto("http://app.test/");
  await recorder.click("#a", {});
  await recorder.click("#b", {});
  const { steps } = recorder.finish();
  return { segments: sliceSegments(events, steps), events, steps };
}

/** Which step emitted the marked events inside a segment's own WINDOW. The preamble is
 *  skipped deliberately — see the matching helper in replay/runner.test.mts. */
function stepsIn(segment: {
  events: ReplayEvent[];
  preambleCount: number;
}): number[] {
  return segment.events
    .slice(segment.preambleCount)
    .filter((e) => e.type === 3)
    .map((e) => (e.data as { step: number }).step);
}

test("capture clock domains (i) — a skew LARGER than the run must not empty the early segments", async () => {
  const { segments } = await captureUnderSkew(10_000);
  assert.equal(segments.length, 3);
  assert.ok(
    isRenderable(segments[0]),
    "step 1's segment must carry the navigation pair the recorder emitted during it",
  );
  assert.deepEqual(
    segments.map(stepsIn),
    [[1], [2], [3]],
    "each step's segment must carry that step's own events",
  );
});

test("capture clock domains (ii) — a skew the size of ONE STEP must not hand a segment its neighbour's events", async () => {
  const { segments } = await captureUnderSkew(STEP_MS);
  assert.equal(segments.length, 3);
  assert.deepEqual(
    segments.map(stepsIn),
    [[1], [2], [3]],
    "a segment must never carry the neighbouring step's events",
  );
});

test("capture clock domains (iii) — concurrent first steps must share ONE offset measurement", async () => {
  // A caller that fires two actions without awaiting the first is what makes a plain
  // `if (offset === null)` cache unsafe: both see null, both measure, and the two steps
  // land in two different clocks — the very defect, re-instantiated.
  let evaluations = 0;
  const page = {
    evaluate: async (fn: unknown) => {
      const source = String(fn);
      if (/Date\.now\(\)/.test(source) && source.length < 60) {
        evaluations += 1;
        await sleep(5);
        return Date.now();
      }
      throw new Error("no metrics collector in this fake");
    },
    goto: async () => await sleep(20),
    click: async () => await sleep(20),
    fill: async () => await sleep(20),
  } as unknown as import("playwright-core").Page;
  const session = {
    sessionId: "fake",
    page,
    fetchReplay: async () => [] as ReplayEvent[],
    close: async () => {},
  };
  const recorder = new Recorder(session, "clock-domains", "fake-driver");
  await Promise.all([
    recorder.goto("http://app.test/"),
    recorder.click("#a", {}),
  ]);
  assert.equal(
    evaluations,
    3,
    "recorderClockOffset takes three samples, and must be entered exactly once per run",
  );
});
