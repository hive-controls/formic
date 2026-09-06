/**
 * The deterministic slicer: step log + replay stream → one self-contained replay
 * segment per step.
 *
 * There is no LLM in this path and there never will be. It is pure data manipulation
 * over timestamps, which is what makes per-step evidence free to produce on every run
 * rather than something we spend tokens on.
 *
 * Two invariants, both discovered by measurement in the recording-addressability probe
 * (fixtures/probe-2b-replay.raw is that capture).
 * Both failure modes are SILENT — they produce a plausible-looking artifact showing
 * the wrong thing, which is the evidence-layer version of the wrong-element heal that
 * the evidence contract forbids. Hence the tests.
 */
import {
  type ReplayEvent,
  type ReplaySegment,
  type StepRecord,
  RRWEB_FULL_SNAPSHOT,
  RRWEB_META,
} from "./types.mts";

/**
 * INVARIANT 1 — the window is half-open: [step.startedAt, nextStep.startedAt).
 *
 * A step's own `endedAt` is the wrong upper bound. Playwright actions return before
 * their consequences settle, so bounding by `endedAt` drops the navigation the action
 * caused. Measured: it produced an EMPTY segment for one step and hid another step's
 * resulting page load entirely.
 */
export function windowFor(
  steps: StepRecord[],
  index: number,
): { from: number; to: number } {
  const step = steps[index];
  const next = steps[index + 1];
  return {
    from: step.startedAt,
    to: next ? next.startedAt : Number.POSITIVE_INFINITY,
  };
}

/**
 * INVARIANT 2 — a mid-stream segment needs a replay preamble.
 *
 * rrweb cannot render from an IncrementalSnapshot alone; it needs a Meta (viewport +
 * href) and a FullSnapshot to build the initial DOM. We carry forward the most recent
 * of each at or before the window start. Navigations emit fresh ones, so this is cheap.
 */
export function preambleFor(
  events: ReplayEvent[],
  from: number,
  windowEvents: ReplayEvent[] = [],
): ReplayEvent[] {
  // A window that OPENS with its own Meta->FullSnapshot pair establishes its DOM from
  // its first event; nothing before it is needed.
  if (
    windowEvents[0]?.type === RRWEB_META &&
    windowEvents[1]?.type === RRWEB_FULL_SNAPSHOT
  ) {
    return [];
  }

  // Otherwise the preamble is the PRE-ROLL: the latest completed Meta->FullSnapshot pair
  // at or before `from`, and EVERY event between that snapshot and `from`.
  //
  // Carrying only the pair (the original rule) reconstructs the page as it was at the
  // last navigation, not as it was when the step began. In a single-page app every step
  // after the first mutates the DOM without navigating, so a late step's segment
  // rendered the page-load DOM plus its own mutations — the first dogfood PR's BEFORE
  // and AFTER frames for the approval step both showed the sign-in form. The probe
  // probe never exposed this because every probe step navigated. The player seeks to
  // the window start (or end), so the pre-roll costs bytes, not review time.
  //
  // Pairing: a FullSnapshot binds to the Meta that precedes it; a snapshot with no Meta
  // of its own (an rrweb checkout, a trimmed stream) keeps the previous Meta (the page
  // has not navigated). Selecting the latest Meta and the latest FullSnapshot
  // independently stapled a new navigation's Meta onto the previous page's DOM when a
  // window sat inside the 12-28ms gap between them (re-review P1).
  let pendingMetaIndex = -1;
  let pairStart = -1;
  let windowStart = events.length;
  for (let i = 0; i < events.length; i++) {
    const event = events[i];
    if (event.timestamp >= from) {
      windowStart = i;
      break;
    }
    if (event.type === RRWEB_META) pendingMetaIndex = i;
    if (event.type === RRWEB_FULL_SNAPSHOT) {
      pairStart =
        pendingMetaIndex >= 0
          ? pendingMetaIndex
          : pairStart >= 0
            ? pairStart
            : i;
      pendingMetaIndex = -1;
    }
  }
  // Before the first snapshot has arrived there is no completed pair, but a pending
  // Meta (the first navigation's, with the window inside its 12-28 ms gap) is still
  // the viewport the window's own FullSnapshot needs.
  const start = pairStart >= 0 ? pairStart : pendingMetaIndex;
  if (start < 0) return [];
  return events.slice(start, windowStart);
}

/** Slice one segment per step. Events must be sorted by timestamp ascending. */
export function sliceSegments(
  events: ReplayEvent[],
  steps: StepRecord[],
): ReplaySegment[] {
  // A finite stand-in for "to the end of the stream". Exclusive, so +1 keeps the last
  // event inside the final window.
  const streamEnd =
    events.length > 0 ? events[events.length - 1].timestamp + 1 : 0;

  return steps.map((step, index) => {
    const { from, to } = windowFor(steps, index);
    const windowEvents = events.filter(
      (e) => e.timestamp >= from && e.timestamp < to,
    );

    // Only prepend context the window doesn't already establish for itself.
    // Self-containment is an ORDERED Meta-then-FullSnapshot pair, not merely the
    // presence of a FullSnapshot: rrweb emits Meta and FullSnapshot 12-28ms apart on
    // navigation (measured across all five navigations in the probe capture), so a
    // step boundary landing in that gap yields a window holding the FullSnapshot but
    // NOT its Meta. Treating that as self-contained drops the viewport/href the player
    // needs and produces a segment that silently fails to render.
    const preamble = preambleFor(events, from, windowEvents);

    return {
      stepId: step.id,
      stepIndex: step.index,
      action: step.action,
      fromTimestamp: from,
      // Never Infinity here: this lands in a persisted audit record, and
      // JSON.stringify turns Infinity into null — silently voiding the field. And never
      // below `from`: a final step that emits no event (a waitFor) starts AFTER the last
      // event, and lastEvent+1 alone wrote an inverted interval (re-review P2).
      toTimestamp: Number.isFinite(to) ? to : Math.max(streamEnd, from + 1),
      events: [...preamble, ...windowEvents],
      preambleCount: preamble.length,
    };
  });
}

/** Parse Solari's NDJSON replay payload into sorted events, skipping blank lines. */
export function parseReplay(ndjson: string): ReplayEvent[] {
  return ndjson
    .split("\n")
    .filter((line) => line.trim() !== "")
    .map((line) => JSON.parse(line) as ReplayEvent)
    .sort((a, b) => a.timestamp - b.timestamp);
}

/**
 * A segment that cannot render is worse than no segment: it looks like evidence and
 * shows nothing. Callers should assert this before publishing a repair PR.
 */
export function isRenderable(segment: ReplaySegment): boolean {
  // The invariant is an ordered Meta-then-FullSnapshot pair. Checking only for a
  // FullSnapshot is what let a Meta-less segment pass as evidence.
  const firstFullIndex = segment.events.findIndex(
    (e) => e.type === RRWEB_FULL_SNAPSHOT,
  );
  if (firstFullIndex < 0) return false;
  return segment.events
    .slice(0, firstFullIndex)
    .some((e) => e.type === RRWEB_META);
}
