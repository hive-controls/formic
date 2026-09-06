/**
 * The one place a step gets timestamped.
 *
 * Capture and replay both emit `StepRecord`s, and the evidence slicer addresses the
 * replay stream by `startedAt`. If the two paths timed a step differently, a segment
 * sliced from a replay run would be addressed on a subtly different anchor than one
 * sliced from a capture — so both go through this helper and neither touches
 * `Date.now()` on its own.
 *
 * WHICH clock is the caller's to choose, and it is not a free choice: an anchor is only
 * meaningful in the same clock as the stream it addresses. `Date.now()` is this
 * process's clock, and the recorder's events are stamped by the browser — the same
 * clock on a developer machine, measured ~0.9-1.0 s apart inside a Solari guest, where
 * every window then addressed the wrong part of the stream. A caller that
 * slices a recorded stream passes that recorder's clock; callers with no recorder keep
 * the default.
 */
import type { Page } from "playwright-core";
import type { StepRecord } from "./types.mts";

/** Milliseconds since the epoch, on the clock the step log is anchored in. */
export type StepClock = () => number;

/**
 * How far a browser's clock runs ahead of this process's, in milliseconds.
 *
 * The step log anchors the evidence slicer, and the events it addresses are stamped by
 * the recorder inside the browser — on capture AND on replay alike. Those are two
 * clocks. On a developer machine they agree and nothing shows; inside a Solari guest
 * they were measured ~0.9-1.0 s apart, which put every event outside the window of the
 * step that produced it — six of seven segments empty, and the integrity check
 * refusing the record for the first step.
 *
 * Sampled rather than read once: the reply carries a round trip, so a single read is
 * off by an unknown share of it. Three samples and the narrowest round trip bound the
 * error at half of the best one — the same reason NTP keeps the least-delayed exchange.
 *
 * A failed sample costs only that sample. Abandoning the whole measurement on the first
 * one to throw returns 0, and 0 does not mean "unknown" — it means "no skew", which is
 * exactly the wrong anchor on the skewed recorder this exists for. 0 is the answer only
 * when NOTHING could be measured (no JS engine, a closed page), where the caller's own
 * clock is the only one there is and is correct whenever the recorder shares this
 * machine.
 *
 * Measured once per run, not per step: a capture or replay is seconds to minutes, over
 * which OS clock drift stays orders of magnitude below a step window. A page that
 * redefines `Date.now()` would defeat this, and is out of scope — an app under test
 * that lies about the time can already lie about anything the evidence records.
 */
export async function recorderClockOffset(page: Page): Promise<number> {
  let best: { roundTrip: number; offset: number } | null = null;
  for (let sample = 0; sample < 3; sample++) {
    try {
      const before = Date.now();
      const remote = await page.evaluate(() => Date.now());
      const roundTrip = Date.now() - before;
      // The type says number; a fake or broken page can still resolve without
      // throwing and hand back undefined/NaN/anything else. Unguarded, that value
      // rides straight into `offset` — and NaN survives `?? 0` untouched below, since
      // NaN is not nullish, so every evidence timestamp downstream goes invalid.
      if (!Number.isFinite(remote)) {
        throw new Error(
          `recorder clock probe returned a non-finite value: ${String(remote)}`,
        );
      }
      if (best === null || roundTrip < best.roundTrip) {
        best = { roundTrip, offset: remote - (before + roundTrip / 2) };
      }
    } catch {
      // This sample is lost; the ones already taken are not.
    }
  }
  return best?.offset ?? 0;
}

export interface TimedStepInput {
  id: string;
  index: number;
  action: string;
  target?: string;
  value?: string;
  /** The reference a step's value came from, when it was not written down. The audit
   *  "inputs" field records the SOURCE, which is the only thing about that value the
   *  record may hold — see SpecStep.valueFrom. */
  valueFrom?: string;
  /**
   * An anchor ALREADY OBSERVED, in this log's own clock — for a step whose action the
   * caller did not perform and only learned about afterwards.
   *
   * A human's click is stamped inside the browser and reaches the host over a binding,
   * so "now" on arrival is later than the interaction by the width of that hop, and a
   * window opened there starts after the event it exists to contain. Callers that drive
   * the page themselves have no such gap and must leave this unset.
   */
  startedAt?: number;
}

/**
 * The audit "inputs" field: the value a step supplied, or — where the spec referenced it
 * instead of carrying it — the reference, never what it resolved to.
 *
 * The REFERENCE wins when both are somehow present. The validator refuses a step
 * carrying both, so this is the belt to that brace: a malformed step that reached the
 * log anyway must have written down what replay actually used, and replay resolves the
 * reference. Preferring the literal is how the audit would come to disagree with the
 * run it is a record of.
 */
function stepInputs(
  input: TimedStepInput,
): Record<string, unknown> | undefined {
  if (input.valueFrom !== undefined) return { valueFrom: input.valueFrom };
  if (input.value !== undefined) return { value: input.value };
  return undefined;
}

/**
 * Run one step, appending its record to `log` whether it succeeds or throws.
 *
 * Appending on failure is deliberate: a failed step is the most important line in an
 * audit record, and the heal loop needs its anchor to slice the "before" evidence.
 * The error is rethrown so the caller decides what stopping means.
 */
export async function runTimedStep(
  log: StepRecord[],
  input: TimedStepInput,
  run: () => Promise<void>,
  now: StepClock = Date.now,
): Promise<StepRecord> {
  // Anchors must be STRICTLY increasing and REAL. Two instant steps in one Date.now()
  // tick would share an anchor, and the slicer's half-open windows would give the
  // first step [t, t) — empty — while handing its events to the second (review P2).
  // Inventing `previous + 1` is not the answer either: that anchor lies in the future,
  // so an event this step emits at real time t lands in the PREVIOUS step's window
  // (review P2, round 7). The only anchor that is both real and distinct is the next
  // clock tick — wait for it. At most 1 ms, and only on a collision.
  const previous = log[log.length - 1];
  // An observed anchor is a REAL past moment, so it needs no wait — only the same
  // strictly-increasing guarantee, which for an out-of-order arrival means taking the
  // earliest anchor that is still distinct from the previous step's.
  if (input.startedAt === undefined) {
    while (previous !== undefined && now() <= previous.startedAt) {
      await new Promise((resolve) => setTimeout(resolve, 1));
    }
  }
  const startedAt =
    input.startedAt === undefined
      ? now()
      : previous === undefined
        ? input.startedAt
        : Math.max(input.startedAt, previous.startedAt + 1);
  let outcome: StepRecord["outcome"] = "ok";
  let error: string | undefined;
  try {
    await run();
  } catch (caught) {
    outcome = "failed";
    error = (caught as Error).message;
    throw caught;
  } finally {
    log.push({
      id: input.id,
      index: input.index,
      action: input.action,
      target: input.target,
      inputs: stepInputs(input),
      startedAt,
      // Duration reporting only. Never bounds a segment — see segment.mts INVARIANT 1.
      endedAt: now(),
      outcome,
      error,
    });
  }
  return log[log.length - 1];
}
