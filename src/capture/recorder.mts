/**
 * Trajectory recorder: drive a session, and emit BOTH artifacts at once —
 * the spec (what to replay) and the step log (how to address the evidence).
 *
 * The step log's timestamps are the product. They are what makes per-step evidence a
 * scripted slice rather than an agent's guess (measured in the recording-addressability probe), so every action goes
 * through `record()` and nothing touches the page directly.
 */
import { randomBytes } from "node:crypto";
import type { Page } from "playwright-core";
import type { DriverSession } from "../driver/types.mts";
import type { StepRecord } from "../evidence/types.mts";
import { runTimedStep, recorderClockOffset } from "../evidence/step-log.mts";
import type {
  ActionKind,
  Assertion,
  FrameChain,
  Spec,
  SpecStep,
} from "../spec/types.mts";

export interface RecordedRun {
  spec: Spec;
  steps: StepRecord[];
}

/** A step a human performed, as the recorder learned about it. `value` may still change
 *  while the step is in flight — see `Recorder.observe`. */
export interface ObservedStep {
  action: ActionKind;
  target: string;
  /** The nested browsing context `target` is addressed in, outermost first. Absent for
   *  an interaction on the top-level page, which is nearly all of them. */
  frame?: FrameChain;
  value?: string;
  /** Set instead of `value` when the field held something the spec must not carry —
   *  the reference replay reads it back from (spec/types.mts, SpecStep.valueFrom). */
  valueFrom?: string;
}

/** Short, opaque, non-positional. Opaque on purpose: an id derived from the action or
 *  target would change when a repair rewrites the target, defeating the whole point. */
export function newStepId(): string {
  return `st_${randomBytes(4).toString("hex")}`;
}

export class Recorder {
  private readonly steps: StepRecord[] = [];
  private readonly specSteps: SpecStep[] = [];
  private startUrl = "";
  /**
   * Measured once per run, on the first recorded step — see recorderClockOffset().
   * The in-flight promise is what is cached, not the resolved number: steps that start
   * before the first measurement returns must share it, or two concurrent first steps
   * would measure separately and anchor the log in two different clocks.
   */
  private clockOffset: Promise<number> | null = null;

  constructor(
    private readonly session: DriverSession,
    private readonly specName: string,
    private readonly driverName: string,
    /** Injectable so tests can assert on stable ids. */
    private readonly generateId: () => string = newStepId,
  ) {}

  private get page(): Page {
    return this.session.page;
  }

  /** How many steps have been recorded so far — the index of the one just observed. */
  get stepCount(): number {
    return this.steps.length;
  }

  /**
   * The step log must be anchored in the same clock as the events the evidence slicer
   * addresses — the recorder's, stamped inside the browser — not this process's. See
   * evidence/step-log.mts's recorderClockOffset() for why (the clock-domain defect the
   * replay runner already fixed; capture carries the identical clock split).
   */
  private async recorderNow(): Promise<() => number> {
    this.clockOffset ??= recorderClockOffset(this.page);
    const offset = await this.clockOffset;
    return () => Date.now() + offset;
  }

  /**
   * Run one action, timestamping it on both sides.
   *
   * Timing lives in evidence/step-log.mts, shared with the replay runner, so capture and
   * replay can never anchor evidence differently.
   */
  private async record(
    action: ActionKind,
    target: string | undefined,
    value: string | undefined,
    assertion: Assertion | undefined,
    run: () => Promise<void>,
  ): Promise<void> {
    const index = this.steps.length + 1;
    const id = this.generateId();
    const now = await this.recorderNow();
    try {
      await runTimedStep(
        this.steps,
        { id, index, action, target, value },
        run,
        now,
      );
    } finally {
      // The spec step is appended even when the action failed, so a capture that died
      // midway still yields a partial spec (capture-demo decides whether to persist it).
      this.specSteps.push({
        id,
        index,
        action,
        target,
        value,
        assert: assertion,
      });
    }
  }

  async goto(url: string): Promise<void> {
    if (this.startUrl === "") this.startUrl = url;
    await this.record("goto", url, undefined, undefined, async () => {
      await this.page.goto(url, { waitUntil: "load" });
    });
  }

  async click(target: string, assertion: Assertion): Promise<void> {
    await this.record("click", target, undefined, assertion, async () => {
      await this.page.click(target);
    });
  }

  async fill(
    target: string,
    value: string,
    assertion: Assertion,
  ): Promise<void> {
    await this.record("fill", target, value, assertion, async () => {
      await this.page.fill(target, value);
    });
  }

  /**
   * Record a step the HUMAN already performed in the browser.
   *
   * Nothing is driven here — the action has happened. `settle` holds the step's window
   * open until the next thing does, which is exactly the half-open boundary the evidence
   * slicer uses, so a human step's segment carries the consequences of its action the
   * same way a scripted step's does. Everything else — the id, the clock, the spec step
   * — is the scripted path's, unchanged: capture must not grow a second producer.
   *
   * `draft` is read when the window CLOSES, not when it opens: keystrokes coalesce into
   * one fill, so the value is not known at the start of the step.
   *
   * `observedAt` is the moment the PAGE stamped the event, in the recorder's own clock —
   * the same clock `recorderNow` produces, and the clock the rrweb stream is stamped in.
   * Anchoring on it rather than on "now" is what keeps the interaction inside its own
   * evidence window: the binding hop from the page to this process takes milliseconds,
   * and a window that opens after the hop starts after the click it exists to hold.
   *
   * No assertion is written. A recording proposes expectations and a human adopts them
   * (capture/proposals.mts) — the rule `proposedAssertChange` already states for repairs,
   * applied at birth.
   */
  async observe(
    draft: ObservedStep,
    settle: () => Promise<void>,
    observedAt?: number,
  ): Promise<string> {
    const index = this.steps.length + 1;
    const id = this.generateId();
    const now = await this.recorderNow();
    const input = {
      id,
      index,
      action: draft.action,
      target: draft.target,
      value: draft.value,
      valueFrom: draft.valueFrom,
      startedAt: observedAt,
    };
    try {
      await runTimedStep(
        this.steps,
        input,
        async () => {
          try {
            await settle();
          } finally {
            input.value = draft.value;
          }
        },
        now,
      );
    } finally {
      this.specSteps.push({
        id,
        index,
        action: draft.action,
        target: draft.target,
        // Spread, not an always-present key: a spec whose steps are all top-level must
        // serialise byte-for-byte as it did before the field existed.
        ...(draft.frame === undefined ? {} : { frame: draft.frame }),
        value: draft.value,
        valueFrom: draft.valueFrom,
      });
    }
    return id;
  }

  /** The compiled spec + the step log that addresses its evidence. */
  finish(): RecordedRun {
    return {
      spec: {
        name: this.specName,
        startUrl: this.startUrl,
        capturedBy: this.driverName,
        capturedAt: new Date().toISOString(),
        steps: this.specSteps,
      },
      steps: this.steps,
    };
  }
}
