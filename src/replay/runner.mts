/**
 * The replay runner — the token-free happy path.
 *
 * Takes a ratified spec and an OPEN driver session, executes the steps in order, and
 * returns a step log in the same shape the Recorder emits, so the evidence slicer works
 * unchanged on a replay. No LLM is involved anywhere in this file; while the UI is
 * stable this is the entire cost of a CI run.
 *
 * Why a session and not a driver: on failure the caller keeps the live page. The heal
 * loop inspects the page at the exact point the step broke — closing it here
 * would throw that state away.
 *
 * Why it stops at the first failure: every step after a broken one runs against a
 * page in an unintended state. Continuing would produce records — and evidence
 * segments — that look like findings and are noise. The failed step's segment is the
 * "before" artifact; what comes after it is the heal loop's business.
 */
import type { Page } from "playwright-core";
import type { DriverSession } from "../driver/types.mts";
import type { StepRecord } from "../evidence/types.mts";
import { runTimedStep, recorderClockOffset } from "../evidence/step-log.mts";
import {
  closeStepMetrics,
  installMetricsCollector,
} from "../evidence/metrics.mts";
import {
  VALUE_FROM_FORM,
  VALUE_FROM_PATTERN,
  envVariableOf,
  type ActionKind,
  type Spec,
  type SpecStep,
} from "../spec/types.mts";
import { CLASSIFY_AS_HOOK } from "../capture/events.mts";
import { checkAssertion } from "./assertions.mts";
import { resolveFrameChain, type LocatorScope } from "./frames.mts";
import {
  scrubBackendIdentity,
  type BackendIdentity,
  type ResolvedSecret,
} from "./evidence.mts";

export interface ReplayOptions {
  /** Applies to each action AND each assertion individually. Never a spec field. */
  stepTimeoutMs?: number;
}

/** Best-effort, bounded, outside the timed window: a snapshot that cannot be taken in
 *  time is simply absent — it must never fail or delay a step. */
async function endStateSnapshot(
  page: Page,
  timeoutMs: number,
): Promise<string | undefined> {
  try {
    return await page.locator("body").ariaSnapshot({ timeout: timeoutMs });
  } catch {
    return undefined;
  }
}

/**
 * Which half of the step broke. The heal loop routes on this: an `action` failure is
 * a locator/flow problem a repair may rewrite; an `assert` failure is an expectation
 * problem that becomes a `proposedAssertChange` for a human, never an automatic edit.
 */
export type FailurePhase = "action" | "assert";

export interface ReplayFailure {
  stepId: string;
  index: number;
  action: ActionKind;
  target?: string;
  phase: FailurePhase;
  error: string;
}

export interface ReplayResult {
  specName: string;
  outcome: "passed" | "failed";
  steps: StepRecord[];
  failure?: ReplayFailure;
  /**
   * Every value this run resolved from a `valueFrom` reference — carried OUT so the
   * caller can hand it to evidence assembly.
   *
   * The runner scrubs what it returns, but it is not the last writer: the caller then
   * fetches the replay stream and assembles a record from it, and a list that died here
   * left the evidence JSON, the HTML page, the bundle, the PR body and the Playwright
   * attachments protected by session-id redaction alone. NEVER written into the step
   * log, and never copied into an `EvidenceRecord` — `assembleEvidence` builds that
   * field by field from the result.
   */
  resolvedSecrets?: ResolvedSecret[];
}

const DEFAULT_STEP_TIMEOUT_MS = 5000;

class PhasedError extends Error {
  constructor(
    readonly phase: FailurePhase,
    cause: Error,
  ) {
    super(cause.message);
    this.name = "PhasedError";
  }
}

/**
 * Resolves a step's value, and REMEMBERS what it resolved so no writer can publish it.
 *
 * A `valueFrom` step's value exists only for the length of the step: read from the
 * environment here, handed to Playwright, and then known to this process — which is
 * exactly the point at which the step log, the error text and the accessibility
 * snapshot are about to be written. So the resolver is also the scrub list: every
 * value it hands out is replaced, in every string of the result, by the reference
 * that NAMES it. That is the backend-identity pass (replay/evidence.mts) applied to
 * the one other class of string a run knows and must not write down.
 *
 * A missing variable fails the step by NAME. It cannot say more: there is no value to
 * describe, and describing one is what this whole path exists to avoid.
 */
class StepValues {
  private readonly resolved = new Map<string, string>();

  valueFor(step: SpecStep): string | undefined {
    if (step.valueFrom === undefined) return step.value;
    if (!VALUE_FROM_PATTERN.test(step.valueFrom)) {
      throw new Error(
        `valueFrom "${step.valueFrom}" is not a supported reference — expected ${VALUE_FROM_FORM}`,
      );
    }
    const variable = envVariableOf(step.valueFrom);
    const value = process.env[variable];
    if (value === undefined || value === "") {
      throw new Error(
        `the environment variable ${variable} is not set — this step reads its value from ${step.valueFrom}, so replay has nothing to supply`,
      );
    }
    this.resolved.set(value, `<redacted:${step.valueFrom}>`);
    return value;
  }

  /** What every writer must never see. */
  list(): ResolvedSecret[] {
    return [...this.resolved].map(([value, reference]) => ({
      value,
      reference,
    }));
  }

  /** The same list, in the shape the scrub pass takes. */
  scrubList(): BackendIdentity {
    return { secrets: this.list() };
  }
}

/**
 * Attaches the resolved-secret list AFTER the scrub, never before.
 *
 * The scrub walks every string of the object it is given, so a list carried INSIDE it
 * would have its own `value` fields rewritten into their references — the carrier
 * would arrive empty of exactly what it exists to carry, and every downstream sink
 * would silently go unprotected while looking correct.
 */
function carrying(result: ReplayResult, values: StepValues): ReplayResult {
  const resolved = values.list();
  return resolved.length === 0
    ? result
    : { ...result, resolvedSecrets: resolved };
}

async function performAction(
  page: Page,
  scope: LocatorScope,
  step: SpecStep,
  timeout: number,
  value: string | undefined,
): Promise<void> {
  const target = step.target;
  // The validator guarantees target/value presence per action; these guards keep the
  // runner honest if a spec bypasses it.
  if (target === undefined) throw new Error(`${step.action} needs a target`);
  switch (step.action) {
    case "goto":
      await page.goto(target, { waitUntil: "load", timeout });
      // Let the page PAINT before the step ends. Measured on the Solari recorder
      // (2026-09-01, fresh context): the navigated document's Meta + FullSnapshot land up
      // to 56 ms AFTER the load event, while two animation frames over CDP take ≥ 88 ms —
      // without this wait the loaded page fell into the NEXT step's half-open window and
      // the goto step rendered as an empty document. A paint signal, not a sleep.
      await page.evaluate(
        () =>
          new Promise<void>((resolve) =>
            requestAnimationFrame(() => requestAnimationFrame(() => resolve())),
          ),
      );
      return;
    case "click":
      await scope.click(target, { timeout });
      return;
    case "fill":
      if (value === undefined) throw new Error("fill needs a value");
      await scope.fill(target, value, { timeout });
      return;
    case "press":
      if (value === undefined) throw new Error("press needs a value");
      await scope.press(target, value, { timeout });
      return;
    case "select":
      if (value === undefined) throw new Error("select needs a value");
      await scope.selectOption(target, value, { timeout });
      return;
    case "waitFor":
      await scope.locator(target).waitFor({ state: "visible", timeout });
      return;
  }
}

/** The aria line of the element a step is about to act on — the coverage key. Bounded
 *  and best-effort like the end-state snapshot; never fails the step. */
async function targetNodeOf(
  scope: LocatorScope,
  step: SpecStep,
  timeoutMs: number,
): Promise<string | undefined> {
  if (step.action === "goto" || step.target === undefined) return undefined;
  try {
    const snapshot = await scope
      .locator(step.target)
      .first()
      .ariaSnapshot({ timeout: timeoutMs });
    return snapshot.split("\n")[0] || undefined;
  } catch {
    return undefined;
  }
}

/**
 * Tell the PAGE that this step's target holds something secret, before typing into it.
 *
 * The replay stream is a second channel out of the page and it carries typed text — and
 * it is the one plane the host-side scrub cannot cover reliably, because rrweb splits
 * typing across events, so an exact-token match never fires. The page-side classifier
 * masks a field it can recognise, but a `valueFrom` step's target need not look like
 * anything: an unlabelled `<input type="text">` reaches every route and matches none.
 *
 * The spec has already settled the question, so the answer is written into the
 * classifier's own write-once memo table rather than decided again. Best-effort and
 * bounded, like every other page read here: a page with no classifier on it (a backend
 * that records server-side) has no local stream to mask, and a locator that will not
 * resolve is about to fail the step anyway, with a better message than this could give.
 */
async function markReferencedTarget(
  scope: LocatorScope,
  step: SpecStep,
  timeoutMs: number,
): Promise<void> {
  if (step.valueFrom === undefined || step.target === undefined) return;
  try {
    await scope
      .locator(step.target)
      .first()
      .evaluate(
        (element, hook: string) => {
          const mark = (globalThis as unknown as Record<string, unknown>)[hook];
          if (typeof mark === "function") {
            (mark as (node: Element, category: string) => void)(
              element,
              "referenced",
            );
          }
        },
        CLASSIFY_AS_HOOK,
        { timeout: timeoutMs },
      );
  } catch {
    // Nothing to mark, or nothing listening. The step's own failure says more.
  }
}

async function executeStep(
  page: Page,
  step: SpecStep,
  timeout: number,
  onTargetNode: (node: string) => void,
  values: StepValues,
): Promise<void> {
  // WHERE the step's target is resolved, before anything is resolved there. A chain that
  // names no open frame is an ACTION-phase failure by name: the step could not be run at
  // the address it gave, which is a locator/flow problem a repair may rewrite — not a
  // stale expectation.
  let scope: LocatorScope;
  try {
    scope = await resolveFrameChain(page, step.frame, timeout);
  } catch (caught) {
    throw new PhasedError("action", caught as Error);
  }
  const targetNode = await targetNodeOf(scope, step, timeout);
  if (targetNode) onTargetNode(targetNode);
  await markReferencedTarget(scope, step, timeout);
  try {
    await performAction(page, scope, step, timeout, values.valueFor(step));
  } catch (caught) {
    throw new PhasedError("action", caught as Error);
  }
  if (step.assert === undefined) return;
  try {
    await checkAssertion(page, step.assert, timeout, scope);
  } catch (caught) {
    throw new PhasedError("assert", caught as Error);
  }
}

/**
 * Closes the PREVIOUS step's metrics window, if there is one. Called right before the
 * next step's action runs — the same half-open boundary segment.mts's INVARIANT 1 uses
 * for replay segments (the next step's start, not this step's own end), and the only
 * point before a possible navigation would wipe the page-side collector's buffers.
 */
async function closePreviousStepMetrics(
  page: Page,
  steps: StepRecord[],
): Promise<void> {
  const previous = steps[steps.length - 1];
  if (!previous) return;
  previous.metrics = await closeStepMetrics(page, previous.action);
}

// recorderClockOffset moved to evidence/step-log.mts — it is a clock concern shared by
// capture and replay, not a replay-only one. Re-exported here so existing importers of
// this module keep working.
export { recorderClockOffset };

export async function replaySpec(
  spec: Spec,
  session: DriverSession,
  options: ReplayOptions = {},
): Promise<ReplayResult> {
  const timeout = options.stepTimeoutMs ?? DEFAULT_STEP_TIMEOUT_MS;
  const steps: StepRecord[] = [];
  // Every `valueFrom` this run resolves, so the return below can scrub what it typed
  // out of the step log, the failure text and every snapshot it captured.
  const values = new StepValues();
  await installMetricsCollector(session.page);
  // Every anchor below is in the RECORDER's clock, because that is the clock the stream
  // it addresses is stamped in. Measured before the first step so no step is anchored
  // in one clock and sliced in another.
  const offset = await recorderClockOffset(session.page);
  const recorderNow = () => Date.now() + offset;

  for (const step of spec.steps) {
    await closePreviousStepMetrics(session.page, steps);
    // The step record is pushed when the timed window closes, so anything learned
    // during the step is attached AFTER runTimedStep returns (or throws).
    let targetNode: string | undefined;
    const attach = () => {
      const record = steps[steps.length - 1];
      if (record?.id === step.id && targetNode) record.targetNode = targetNode;
    };
    try {
      await runTimedStep(
        steps,
        {
          id: step.id,
          index: step.index,
          action: step.action,
          target: step.target,
          value: step.value,
          valueFrom: step.valueFrom,
        },
        () =>
          executeStep(
            session.page,
            step,
            timeout,
            (node) => {
              targetNode = node;
            },
            values,
          ),
        recorderNow,
      );
      attach();
      const snapshot = await endStateSnapshot(session.page, timeout);
      if (snapshot !== undefined)
        steps[steps.length - 1].ariaSnapshot = snapshot;
    } catch (caught) {
      attach();
      // Best-effort: the failed step's own window closes here too, so it carries
      // whatever CLS/long-task activity accumulated up to the failure.
      await closePreviousStepMetrics(session.page, steps);
      const phase =
        caught instanceof PhasedError ? caught.phase : ("action" as const);
      // Scrubbed HERE, before the result leaves this function: a Playwright timeout
      // message quotes what it was asked to type, and the failure path is the one that
      // reaches a healer brief, a terminal and a PR body.
      return carrying(
        scrubBackendIdentity(
          {
            specName: spec.name,
            outcome: "failed" as const,
            steps,
            failure: {
              stepId: step.id,
              index: step.index,
              action: step.action,
              target: step.target,
              phase,
              error: (caught as Error).message,
            },
          },
          values.scrubList(),
        ),
        values,
      );
    }
  }

  await closePreviousStepMetrics(session.page, steps);
  return carrying(
    scrubBackendIdentity(
      { specName: spec.name, outcome: "passed" as const, steps },
      values.scrubList(),
    ),
    values,
  );
}
