/**
 * Slicer tests, run against the REAL replay captured by the recording-addressability probe rather than a
 * hand-written fixture — a synthetic stream would encode the same assumptions the
 * production code makes, and would not have caught either invariant.
 *
 * No network, no Solari, no key required.
 */
import { test } from "node:test";
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import {
  parseReplay,
  sliceSegments,
  windowFor,
  preambleFor,
  isRenderable,
} from "./segment.mts";
import { type StepRecord, RRWEB_FULL_SNAPSHOT } from "./types.mts";

const FIXTURES = join(
  dirname(fileURLToPath(import.meta.url)),
  "..",
  "..",
  "fixtures",
);

const events = parseReplay(
  readFileSync(join(FIXTURES, "probe-2b-replay.raw"), "utf8"),
);
const actionLog = JSON.parse(
  readFileSync(join(FIXTURES, "probe-2b-action-log.json"), "utf8"),
);

const steps: StepRecord[] = actionLog.actions.map(
  (a: {
    step: number;
    action: string;
    startedAt: number;
    endedAt: number;
  }) => ({
    index: a.step,
    action: a.action,
    startedAt: a.startedAt,
    endedAt: a.endedAt,
    outcome: "ok" as const,
  }),
);

test("the fixture is the real capture", () => {
  assert.equal(events.length, 25);
  assert.equal(steps.length, 4);
});

test("INVARIANT 1 — the naive [startedAt, endedAt] window is broken (this is why the rule exists)", () => {
  // Guard against the regression of "simplifying" windowFor back to the step's own end.
  const naive = steps.map((s) =>
    events.filter(
      (e) => e.timestamp >= s.startedAt && e.timestamp <= s.endedAt,
    ),
  );
  assert.equal(
    naive[3].length,
    0,
    "step 4 must be EMPTY under the naive window — the bug this invariant prevents",
  );
  assert.ok(
    !naive[1].some((e) => e.type === RRWEB_FULL_SNAPSHOT),
    "step 2 must MISS the navigation it caused under the naive window",
  );
});

test("INVARIANT 1 — every step gets a non-empty half-open window", () => {
  const segments = sliceSegments(events, steps);
  for (const segment of segments) {
    assert.ok(
      segment.events.length > 0,
      `step ${segment.stepIndex} produced an empty segment`,
    );
  }
  // The last step runs to end of stream, not to a bounded end.
  assert.equal(windowFor(steps, steps.length - 1).to, Number.POSITIVE_INFINITY);
});

test("INVARIANT 1 — a step's segment captures the consequence of its action", () => {
  // The click in step 2 triggers a navigation that lands AFTER the click returns.
  const segments = sliceSegments(events, steps);
  const click = segments[1];
  assert.equal(click.action, "click:more-information");
  assert.ok(
    click.events.some((e) => e.type === RRWEB_FULL_SNAPSHOT),
    "step 2 must include the page load its click caused",
  );
});

test("INVARIANT 2 — every segment is renderable (carries a FullSnapshot)", () => {
  for (const segment of sliceSegments(events, steps)) {
    assert.ok(
      isRenderable(segment),
      `step ${segment.stepIndex} cannot be rendered by a player`,
    );
  }
});

test("INVARIANT 2 — a window that OPENS with its own Meta+FullSnapshot carries no preamble; any other window carries the pre-roll up to its start", () => {
  // The earlier form of this rule ("no preamble whenever the window holds a snapshot
  // somewhere") was insufficient: a window's own snapshot arrives after the action, and
  // the events before it — and, in a single-page app, every mutation since the LAST
  // snapshot — are needed to show the page as it was when the step began. See
  // preroll.test.mts for the case that exposed it.
  const segments = sliceSegments(events, steps);
  for (const segment of segments) {
    const own = segment.events.slice(segment.preambleCount);
    const opensWithPair =
      own[0]?.type === 4 && own[1]?.type === RRWEB_FULL_SNAPSHOT;
    if (opensWithPair) {
      assert.equal(segment.preambleCount, 0);
    } else {
      assert.ok(segment.preambleCount > 0, `step ${segment.stepIndex}`);
      const preamble = segment.events.slice(0, segment.preambleCount);
      assert.equal(preamble[0].type, 4, "pre-roll starts at a Meta");
      assert.equal(preamble[1].type, RRWEB_FULL_SNAPSHOT);
      assert.ok(
        preamble.every((e) => e.timestamp < segment.fromTimestamp),
        "pre-roll ends before the window start",
      );
    }
  }
});

test("INVARIANT 2 — a window with no snapshot of its own is renderable ONLY via the preamble", () => {
  // Every step in the probe happened to navigate, so each window carries its own
  // FullSnapshot and the preamble path is never exercised by the natural boundaries.
  // Derive a finer boundary that isolates the click's Incremental burst — the case a
  // real spec hits whenever a step does not navigate.
  const incrementalOnly = events.filter((e) => e.type === 3);
  assert.ok(
    incrementalOnly.length > 0,
    "fixture must contain incremental events",
  );
  const burstStart = incrementalOnly[1].timestamp;
  const nextStructural = events.find(
    (e) => e.timestamp > burstStart && e.type !== 3,
  );
  assert.ok(
    nextStructural,
    "fixture must have a structural event after the burst",
  );

  const finer: StepRecord[] = [
    {
      id: "st_click",
      index: 1,
      action: "click:no-navigation",
      startedAt: burstStart,
      endedAt: burstStart + 1,
      outcome: "ok",
    },
    {
      id: "st_next",
      index: 2,
      action: "next",
      startedAt: nextStructural!.timestamp,
      endedAt: nextStructural!.timestamp + 1,
      outcome: "ok",
    },
  ];

  const [segment] = sliceSegments(events, finer);
  const ownEvents = segment.events.slice(segment.preambleCount);
  assert.ok(
    !ownEvents.some((e) => e.type === RRWEB_FULL_SNAPSHOT),
    "this window must contain no snapshot of its own — otherwise the test proves nothing",
  );
  const preamble = segment.events.slice(0, segment.preambleCount);
  assert.deepEqual(
    preamble.slice(0, 2).map((e) => e.type),
    [4, RRWEB_FULL_SNAPSHOT],
    "the pre-roll starts with Meta + FullSnapshot",
  );
  assert.ok(
    preamble.every((e) => e.timestamp < burstStart),
    "and carries everything up to the window start, nothing from inside it",
  );
  assert.ok(
    isRenderable(segment),
    "the segment is renderable only because of the preamble",
  );
});

test("preambleFor starts with Meta before FullSnapshot (player ordering)", () => {
  const preamble = preambleFor(events, steps[2].startedAt);
  assert.ok(preamble.length >= 2);
  assert.deepEqual(
    preamble.slice(0, 2).map((e) => e.type),
    [4, 2],
    "Meta (4) must precede FullSnapshot (2)",
  );
});

test("REGRESSION — a step insertion must not repoint another step's evidence", () => {
  // The bug this fixes: evidence was keyed on `index`, which equals array position. A
  // flow repair (breakage class 3, changed flow) inserts a step, renumbering everything after it — so a
  // segment captured as "step 3" would silently start describing a different step. That
  // corrupts the audit trail precisely on the repair class we most differentiate on.
  const before = sliceSegments(events, steps);
  const targetId = steps[2].id;
  const segmentBefore = before.find((s) => s.stepId === targetId);
  assert.ok(segmentBefore, "the step must have a segment before the insertion");

  // Simulate the repair: insert a step at the front and renumber, ids untouched.
  const inserted: StepRecord = {
    id: "st_inserted",
    index: 1,
    action: "click:confirm-interstitial",
    startedAt: steps[0].startedAt - 5,
    endedAt: steps[0].startedAt - 4,
    outcome: "healed",
  };
  const after = [inserted, ...steps].map((step, position) => ({
    ...step,
    index: position + 1,
  }));

  const segmentAfter = sliceSegments(events, after).find(
    (s) => s.stepId === targetId,
  );
  assert.ok(segmentAfter, "the step must still be findable by its stable id");
  assert.equal(
    segmentAfter.action,
    segmentBefore.action,
    "id must still resolve to the same step",
  );
  assert.equal(
    segmentAfter.fromTimestamp,
    segmentBefore.fromTimestamp,
    "its window must be unchanged",
  );

  // And the positional key must have moved — proving index is unsafe to correlate on.
  assert.notEqual(
    segmentAfter.stepIndex,
    segmentBefore.stepIndex,
    "index must renumber (that is why it cannot be the key)",
  );
});

test("REVIEW REGRESSION (P1) — a boundary between Meta and FullSnapshot must still carry the Meta", () => {
  // rrweb emits Meta and FullSnapshot 12-28ms apart on navigation (all five
  // navigations in this capture). The old self-containment check asked only "does the
  // window hold a FullSnapshot?", so a boundary landing inside that gap suppressed the
  // preamble and produced a segment with a DOM but no viewport — silently unrenderable.
  const metaThenFull = events.flatMap((event, i) => {
    const next = events[i + 1];
    return event.type === 4 && next?.type === RRWEB_FULL_SNAPSHOT
      ? [{ meta: event, full: next }]
      : [];
  });
  assert.ok(
    metaThenFull.length > 0,
    "fixture must contain a Meta->FullSnapshot pair",
  );

  for (const { meta, full } of metaThenFull) {
    // Start the step strictly after the Meta and at/before the FullSnapshot.
    const boundary = meta.timestamp + 1;
    assert.ok(boundary <= full.timestamp, "boundary must land inside the gap");

    const finer: StepRecord[] = [
      {
        id: "st_gap",
        index: 1,
        action: "step-in-gap",
        startedAt: boundary,
        endedAt: boundary + 1,
        outcome: "ok",
      },
      {
        id: "st_end",
        index: 2,
        action: "later",
        startedAt: full.timestamp + 1,
        endedAt: full.timestamp + 2,
        outcome: "ok",
      },
    ];
    const [segment] = sliceSegments(events, finer);

    const ownEvents = segment.events.slice(segment.preambleCount);
    assert.ok(
      ownEvents.some((e) => e.type === RRWEB_FULL_SNAPSHOT),
      "the window itself must hold the FullSnapshot — otherwise this test proves nothing",
    );
    assert.ok(
      !ownEvents.some((e) => e.type === 4),
      "and must NOT hold its own Meta — that is the gap being exercised",
    );
    assert.ok(
      isRenderable(segment),
      `segment at boundary ${boundary} must be renderable`,
    );
    // Under the pre-roll rule the Meta arrives as the LAST preamble event, right
    // before the window's FullSnapshot — the pair the player needs, in order.
    const preamble = segment.events.slice(0, segment.preambleCount);
    assert.equal(preamble[preamble.length - 1], meta);
  }
});

test("REVIEW REGRESSION (P1) — isRenderable rejects a FullSnapshot with no preceding Meta", () => {
  // Checking only for a FullSnapshot is exactly what let a Meta-less segment pass.
  assert.equal(
    isRenderable({
      stepId: "x",
      stepIndex: 1,
      action: "a",
      fromTimestamp: 0,
      toTimestamp: 1,
      events: [{ type: RRWEB_FULL_SNAPSHOT, timestamp: 0 }],
      preambleCount: 0,
    }),
    false,
  );
  assert.equal(
    isRenderable({
      stepId: "x",
      stepIndex: 1,
      action: "a",
      fromTimestamp: 0,
      toTimestamp: 1,
      events: [
        { type: 4, timestamp: 0 },
        { type: RRWEB_FULL_SNAPSHOT, timestamp: 1 },
      ],
      preambleCount: 0,
    }),
    true,
  );
});

test("REVIEW REGRESSION — the final segment survives JSON serialisation", () => {
  // toTimestamp was Number.POSITIVE_INFINITY, which JSON.stringify writes as null —
  // silently voiding a field in the persisted audit record.
  const segments = sliceSegments(events, steps);
  const last = segments[segments.length - 1];
  assert.ok(
    Number.isFinite(last.toTimestamp),
    "final segment must have a finite bound",
  );

  const roundTripped = JSON.parse(JSON.stringify(segments));
  for (const segment of roundTripped) {
    assert.equal(
      typeof segment.toTimestamp,
      "number",
      "no bound may serialise to null",
    );
    assert.ok(Number.isFinite(segment.toTimestamp));
  }
  // The final bound must still include the last event.
  assert.ok(last.toTimestamp > events[events.length - 1].timestamp);
});

test("REVIEW REGRESSION (re-review P1) — a window inside the Meta->FullSnapshot gap must carry a COMPATIBLE pair", () => {
  // The preamble used to pick the latest Meta and the latest FullSnapshot at or before
  // the window start independently. A step that starts after a navigation's Meta and
  // ends before its FullSnapshot then got the NEW page's Meta stapled to the OLD page's
  // DOM: ordered, accepted by isRenderable, and showing the wrong page under the wrong
  // href. The carried pair must be the latest COMPLETED Meta->FullSnapshot pair.
  const pairs = events.flatMap((event, i) => {
    const next = events[i + 1];
    return event.type === 4 && next?.type === RRWEB_FULL_SNAPSHOT
      ? [{ meta: event, full: next, at: i }]
      : [];
  });
  assert.ok(pairs.length >= 2, "fixture must contain two navigations");

  for (let p = 1; p < pairs.length; p++) {
    const previous = pairs[p - 1];
    const current = pairs[p];
    if (current.full.timestamp - current.meta.timestamp < 2) continue;
    const inGap: StepRecord[] = [
      {
        id: "st_in_gap",
        index: 1,
        action: "inside-gap",
        startedAt: current.meta.timestamp + 1,
        endedAt: current.meta.timestamp + 1,
        outcome: "ok",
      },
      {
        id: "st_after",
        index: 2,
        action: "after",
        startedAt: current.full.timestamp, // exclusive bound: window has neither
        endedAt: current.full.timestamp + 1,
        outcome: "ok",
      },
    ];
    const [segment] = sliceSegments(events, inGap);
    const firstMeta = segment.events.find((e) => e.type === 4);
    const firstFull = segment.events.find(
      (e) => e.type === RRWEB_FULL_SNAPSHOT,
    );
    assert.ok(firstMeta && firstFull, "segment needs a preamble");
    assert.equal(
      firstFull.timestamp,
      previous.full.timestamp,
      "the DOM shown must be the previous page (the new one has not been snapshotted yet)",
    );
    assert.equal(
      firstMeta.timestamp,
      previous.meta.timestamp,
      "the Meta must be the one that belongs to that DOM, not the new navigation's",
    );
  }
});

test("REVIEW REGRESSION (re-review P2) — the final bound stays after the step start when the last step outlives the stream", () => {
  // A final step that emits no rrweb event (a waitFor, say) starts after the last
  // event; the finite fallback of lastEvent+1 was then BELOW the window start, writing
  // an inverted interval. Same for an empty stream.
  const lastEventAt = events[events.length - 1].timestamp;
  const outlives: StepRecord[] = [
    { ...steps[0] },
    {
      id: "st_late",
      index: 2,
      action: "waitFor",
      startedAt: lastEventAt + 500,
      endedAt: lastEventAt + 900,
      outcome: "ok",
    },
  ];
  const late = sliceSegments(events, outlives)[1];
  assert.ok(
    late.toTimestamp > late.fromTimestamp,
    `inverted interval: [${late.fromTimestamp}, ${late.toTimestamp})`,
  );
  const [empty] = sliceSegments([], [outlives[1]]);
  assert.ok(empty.toTimestamp > empty.fromTimestamp, "empty stream inverted");
});

test("REVIEW REGRESSION (P2) — a FullSnapshot with no Meta of its own keeps the previous Meta", () => {
  // An rrweb checkout / manual snapshot (or a trimmed stream) can carry a FullSnapshot
  // with no new Meta in front of it. The completed-pair fix (re-review P1) then stored
  // {meta: undefined, full}, so a later incremental-only window got a preamble of one
  // bare FullSnapshot — and isRenderable refused a perfectly good recording. The page
  // has not navigated, so the previous Meta still describes it: keep it.
  const lastPairIndex = events.findLastIndex(
    (e, i) => e.type === 4 && events[i + 1]?.type === RRWEB_FULL_SNAPSHOT,
  );
  assert.ok(
    lastPairIndex >= 0,
    "fixture must contain a Meta->FullSnapshot pair",
  );
  const meta = events[lastPairIndex];
  const full = events[lastPairIndex + 1];
  const standalone = { ...full, timestamp: full.timestamp + 5 };
  const withCheckout = [...events, standalone].sort(
    (a, b) => a.timestamp - b.timestamp,
  );
  assert.ok(
    !withCheckout.some(
      (e) =>
        e.timestamp > full.timestamp &&
        e.timestamp <= standalone.timestamp &&
        e.type === 4,
    ),
    "no Meta may precede the standalone snapshot — otherwise the test proves nothing",
  );

  const afterCheckout: StepRecord[] = [
    {
      id: "st_after_checkout",
      index: 1,
      action: "incremental-only",
      startedAt: standalone.timestamp + 1,
      endedAt: standalone.timestamp + 1,
      outcome: "ok",
    },
    {
      id: "st_bound",
      index: 2,
      action: "bound",
      startedAt: standalone.timestamp + 2,
      endedAt: standalone.timestamp + 2,
      outcome: "ok",
    },
  ];
  const [segment] = sliceSegments(withCheckout, afterCheckout);
  // The pre-roll starts at the completed pair before the checkout and carries the
  // checkout snapshot itself, so the player re-snapshots into the same viewport.
  assert.equal(
    segment.events.find((e) => e.type === 4)?.timestamp,
    meta.timestamp,
    "its viewport is the previous Meta, which still describes the page",
  );
  assert.ok(
    segment.events.some(
      (e) =>
        e.type === RRWEB_FULL_SNAPSHOT && e.timestamp === standalone.timestamp,
    ),
    "the checkout snapshot is carried",
  );
  assert.ok(isRenderable(segment), "a checkout snapshot must stay reviewable");
});

test("MUTATION PROOF — perturbing the anchor moves the cut", () => {
  // If the slicer ignored timestamps, this would produce identical output. It must not.
  const shifted = steps.map((s) => ({ ...s, startedAt: s.startedAt + 2500 }));
  const before = sliceSegments(events, steps);
  const after = sliceSegments(events, shifted);
  assert.notDeepEqual(
    before.map((s) => s.events.length),
    after.map((s) => s.events.length),
    "a 2.5s anchor shift must change the segmentation — otherwise the slicer is not reading timestamps",
  );
});

test("parseReplay sorts by timestamp and tolerates a trailing newline", () => {
  const parsed = parseReplay(
    '{"type":3,"timestamp":200}\n{"type":3,"timestamp":100}\n',
  );
  assert.deepEqual(
    parsed.map((e) => e.timestamp),
    [100, 200],
  );
});
