/**
 * Evidence types — the audit record every replay, heal, and action emits.
 *
 * The field list is adopted verbatim from the EU AI Act Art. 12 / financial-services
 * audit norms the platform adopts (EU AI Act Art. 12 / financial-services record-keeping): timestamp, decision id, system/model version,
 * inputs, action taken. Do not rename these to something more convenient; the point is
 * that they line up with what an auditor already expects to see.
 */

/** A single rrweb event as Solari emits it (NDJSON, one per line). */
export interface ReplayEvent {
  /** rrweb event type: 0 DomContentLoaded, 1 Load, 2 FullSnapshot, 3 Incremental, 4 Meta. */
  type: number;
  /** Absolute epoch milliseconds. Verified (recording-addressability probe) to share a clock frame
   *  with the client's own `Date.now()`, which is what makes step addressing exact. */
  timestamp: number;
  data?: Record<string, unknown>;
}

export const RRWEB_FULL_SNAPSHOT = 2;
export const RRWEB_META = 4;

/**
 * One captured or replayed step. `startedAt` is the anchor that addresses the replay
 * stream; `endedAt` is recorded for duration reporting only and MUST NOT be used to
 * bound a segment — see sliceSegments().
 */
export interface StepRecord {
  /** The spec step's stable id — the key evidence is addressed by. See SpecStep.id. */
  id: string;
  /** Display position at the time of the run. Renumbers on insertion; not an identity. */
  index: number;
  /** What the harness did, e.g. "click" / "fill" / "goto". */
  action: string;
  /** The step's target as written in the spec (a locator, a URL). */
  target?: string;
  /** Values supplied to the action — the audit "inputs" field. */
  inputs?: Record<string, unknown>;
  startedAt: number;
  endedAt: number;
  outcome: "ok" | "failed" | "healed";
  /** Present when outcome is "failed" or "healed". */
  error?: string;
  /**
   * The page's accessibility snapshot (Playwright `ariaSnapshot()` of the body) at the
   * step's END state, captured AFTER the timed window closes so evidence anchors never
   * move. Deterministic for a given DOM — the material untested-component proposals and
   * UI-drift diffs are built on. Absent when the step failed or the capture timed out.
   */
  ariaSnapshot?: string;
  /**
   * The aria line of the element the step acted on (`- button "Sign in"`), taken from
   * the resolved target just before the action. What `untestedComponents` subtracts:
   * a component a step touched is covered. Absent for goto, on failure, or on timeout.
   */
  targetNode?: string;
  /**
   * Lab performance metrics for this step's window (see evidence/metrics.mts):
   * navigation/paint/LCP on goto steps, CLS and long tasks on every step. Track and
   * trend only — no budget, no assertion, no failure caused by a metric value.
   * Individual fields are `null` when not measured; absent entirely when the runner
   * could not collect at all (e.g. a hand-built session with no evaluate).
   */
  metrics?: StepMetrics;
}

/** See evidence/metrics.mts for the collection method and window semantics. */
export interface StepMetrics {
  ttfb: number | null;
  domContentLoaded: number | null;
  domComplete: number | null;
  firstPaint: number | null;
  firstContentfulPaint: number | null;
  lcp: number | null;
  cls: number | null;
  longTasksCount: number | null;
  longTasksMs: number | null;
}

/** The audit record for one run. Field names are the audit-norm field list. */
export interface EvidenceRecord {
  /** Stable id for this decision/run — what a reviewer cites. */
  decisionId: string;
  /** When the record was made, on the HARNESS clock. Deliberately not the same clock as
   *  each step's `startedAt`/`endedAt`, which are anchored in the RECORDER's so they can
   *  address the replay stream — see step-log.mts. Step durations are unaffected either
   *  way: a constant offset cancels in a subtraction. */
  timestamp: string;
  /** System + model version, so a repair can be attributed to a specific agent build. */
  systemVersion: string;
  modelVersion: string | null;
  specName: string;
  /** Which backend ran it (Driver.name) — a run must be attributable to its substrate. */
  driver: string;
  /** Stable hash reference derived from DriverSession.sessionId, so records can be
   *  correlated without publishing a backend or machine identifier. `null` when the
   *  assembling caller had no session. */
  sessionId: string | null;
  /** Where the app under test was served from, when `--app` hosted it.
   *  `baseUrl` is redacted at the CLI edge before assembly — never a live token.
   *  `null` when the run did not host the app itself (no `--app`, or a driver whose
   *  spec already targets a reachable host). */
  host?: { name: string; kind: "Outside" | "Inside"; baseUrl: string } | null;
  outcome: "passed" | "failed";
  /**
   * Whether `segments` is empty because nothing was recorded or because the driver
   * cannot record. An empty list must never read as "nothing happened".
   */
  recording: "captured" | "unavailable";
  steps: StepRecord[];
  segments: ReplaySegment[];
  /** Mirrors DriverSession.cdpConnect — present only for a backend that connects over
   *  CDP (Solari). `retried` is true when the first connect attempt failed and a fresh
   *  session's connect succeeded, so the flake rate is measurable from the audit trail. */
  cdpConnect?: { latencyMs: number; retried: boolean };
}

/** A per-step slice of the replay stream, self-contained enough to render. */
export interface ReplaySegment {
  /** Stable id of the step this segment shows. Survives repairs and insertions. */
  stepId: string;
  /** Display position at slice time. Provided for rendering; never for correlation. */
  stepIndex: number;
  action: string;
  /** Inclusive lower bound — the step's own start. */
  fromTimestamp: number;
  /** EXCLUSIVE upper bound — the next step's start, or Infinity for the last step. */
  toTimestamp: number;
  /** Preamble (Meta + FullSnapshot carried forward) followed by the window's events.
   *  Ready to hand to an rrweb player as-is. */
  events: ReplayEvent[];
  /** How many leading events are preamble rather than events from this step's window.
   *  Kept explicit so a reviewer can tell inherited context from new activity. */
  preambleCount: number;
}
