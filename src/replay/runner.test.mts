/**
 * Replay runner tests — against a REAL headless Chromium and the real sample app,
 * because the thing under test is whether a spec drives a browser and whether the
 * assertions bite. A stubbed Page would only prove the stub.
 *
 * The breakage variants are the breakage corpus, materialised by the same script the
 * baseline measurement used, so the runner is measured against exactly what
 * Playwright's Healer was.
 */
import { test, before, after } from "node:test";
import assert from "node:assert/strict";
import { execFileSync, spawnSync } from "node:child_process";
import { mkdtempSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import { LocalPlaywrightDriver } from "../driver/local-playwright.mts";
import { loadSpec } from "../spec/parse.mts";
import type { Spec } from "../spec/types.mts";
import {
  replaySpec,
  recorderClockOffset,
  type ReplayResult,
} from "./runner.mts";
import { assembleEvidence } from "./evidence.mts";
import { renderEvidencePage } from "../evidence/page.mts";
import { sliceSegments, isRenderable } from "../evidence/segment.mts";
import {
  RRWEB_META,
  RRWEB_FULL_SNAPSHOT,
  type ReplayEvent,
} from "../evidence/types.mts";
import { serveDirectory } from "./sample-app-server.mts";
import { compileSpec } from "../export/index.mts";

const HERE = dirname(fileURLToPath(import.meta.url));
const USECASE = join(HERE, "..", "..", "fixtures");
const SAMPLE_APP = join(USECASE, "sample-app");
const APPLY_BREAKAGE = join(USECASE, "breakages", "apply.mjs");
const SPEC_FILE = join(USECASE, "specs", "approve-an-order.yaml");

/** The committed spec was captured against :4173; point it at the ephemeral server. */
function rebase(spec: Spec, baseUrl: string): Spec {
  const captured = new URL(spec.startUrl).origin;
  const swap = (url: string | undefined) =>
    url?.startsWith(captured) ? baseUrl + url.slice(captured.length) : url;
  return {
    ...spec,
    startUrl: swap(spec.startUrl) ?? spec.startUrl,
    steps: spec.steps.map((step) =>
      step.action === "goto" ? { ...step, target: swap(step.target) } : step,
    ),
  };
}

let scratch: string;
before(() => {
  scratch = mkdtempSync(join(tmpdir(), "formic-replay-"));
});
after(() => {
  rmSync(scratch, { recursive: true, force: true });
});

function breakage(name: string): string {
  const outDir = join(scratch, name);
  execFileSync(process.execPath, [APPLY_BREAKAGE, name, outDir], {
    stdio: "pipe",
  });
  return outDir;
}

/** The corpus names itself: apply.mjs lists its classes when handed an unknown one, so
 *  a class added there is covered by the survivability guard without editing this file. */
function corpusClasses(): string[] {
  const run = spawnSync(
    process.execPath,
    [APPLY_BREAKAGE, "__no-such-class__", join(scratch, "unused")],
    { encoding: "utf8" },
  );
  const listed = /known: (.+)/.exec(run.stderr)?.[1];
  assert.ok(listed, `apply.mjs did not list its classes: ${run.stderr}`);
  return listed.split(", ").map((name) => name.trim());
}

async function replayAgainst(appDir: string): Promise<ReplayResult> {
  const app = await serveDirectory(appDir);
  const driver = new LocalPlaywrightDriver();
  const session = await driver.open();
  try {
    const spec = rebase(loadSpec(readFileSync(SPEC_FILE, "utf8")), app.baseUrl);
    return await replaySpec(spec, session, { stepTimeoutMs: 2000 });
  } finally {
    await session.close();
    await app.close();
  }
}

test("pristine app: the captured spec replays green, token-free", async () => {
  const result = await replayAgainst(SAMPLE_APP);
  assert.equal(result.outcome, "passed", JSON.stringify(result.failure));
  assert.equal(result.steps.length, 7);
  assert.ok(result.steps.every((step) => step.outcome === "ok"));

  // Ids come from the spec, not from position — the evidence key survives a repair.
  const spec = loadSpec(readFileSync(SPEC_FILE, "utf8"));
  assert.deepEqual(
    result.steps.map((step) => step.id),
    spec.steps.map((step) => step.id),
  );
  // Anchors must be monotonic or the half-open windows overlap.
  for (let i = 1; i < result.steps.length; i++) {
    assert.ok(result.steps[i].startedAt >= result.steps[i - 1].startedAt);
  }
  // Every green step carries the page's accessibility snapshot at its END state — the
  // deterministic material untested-component proposals and UI-drift diffs are built on.
  for (const step of result.steps) {
    assert.ok(
      typeof step.ariaSnapshot === "string" && step.ariaSnapshot.length > 0,
      `step ${step.index} has an end-state snapshot`,
    );
  }
  // The element each step acted on, as its aria line — the coverage key.
  assert.equal(result.steps[3].targetNode, '- button "Sign in"');
  assert.equal(
    result.steps[0].targetNode,
    undefined,
    "goto has no target node",
  );
  // The sign-in step ends on the orders list: its snapshot names the order rows.
  assert.match(result.steps[3].ariaSnapshot ?? "", /SO-4472/);
  // The final step ends on the confirmation: its snapshot shows it, not the list.
  assert.match(result.steps[6].ariaSnapshot ?? "", /[Aa]pproved|confirm/);
});

test("pristine app: every step carries per-step metrics — nav/paint/LCP on the goto step only, CLS/long-tasks tracked on every step", async () => {
  const result = await replayAgainst(SAMPLE_APP);
  assert.equal(result.outcome, "passed", JSON.stringify(result.failure));

  const gotoStep = result.steps[0];
  assert.equal(gotoStep.action, "goto");
  assert.ok(gotoStep.metrics, "goto step has a metrics object");
  assert.equal(typeof gotoStep.metrics?.ttfb, "number");
  assert.equal(typeof gotoStep.metrics?.domContentLoaded, "number");
  assert.equal(typeof gotoStep.metrics?.domComplete, "number");
  assert.equal(typeof gotoStep.metrics?.firstPaint, "number");
  assert.equal(typeof gotoStep.metrics?.firstContentfulPaint, "number");
  assert.equal(typeof gotoStep.metrics?.lcp, "number");
  // Track-only: CLS/long tasks are real zero measurements on a static sample app, and
  // ARE present (not absent) on the navigating step itself too.
  assert.equal(gotoStep.metrics?.cls, 0);
  assert.equal(gotoStep.metrics?.longTasksCount, 0);

  for (const step of result.steps.slice(1)) {
    assert.ok(step.metrics, `step ${step.index} has a metrics object`);
    // Non-goto steps never carry navigation/paint/LCP — those belong to the step that
    // caused the navigation, never reused across steps on the same document.
    assert.equal(step.metrics?.ttfb, null);
    assert.equal(step.metrics?.domContentLoaded, null);
    assert.equal(step.metrics?.lcp, null);
    // CLS/long tasks are tracked on every step — present, not absent.
    assert.equal(typeof step.metrics?.cls, "number");
    assert.equal(typeof step.metrics?.longTasksCount, "number");
  }
});

test("class 1 (renamed-selector): fails at the ACTION of step 4 and stops there", async () => {
  const result = await replayAgainst(breakage("renamed-selector"));
  assert.equal(result.outcome, "failed");
  assert.equal(result.failure?.index, 4);
  assert.equal(result.failure?.phase, "action");
  assert.match(result.failure?.error ?? "", /#signin-button/);

  // Stop-at-first-failure: nothing after the broken step is recorded, and the broken
  // step itself IS recorded (its anchor slices the "before" evidence).
  assert.equal(result.steps.length, 4);
  assert.equal(result.steps[3].outcome, "failed");
  assert.equal(result.steps[3].id, result.failure?.stepId);
});

test("class 2 (moved-element): the control moved out from under the identifier — the click lands on nothing", async () => {
  // The corpus defect this test exists to keep dead: the breakage used to relocate
  // `data-order-id` onto the <li> WRAPPING the row button, and the button filled the
  // wrapper, so the click still reached the handler and the whole replay stayed green.
  // A breakage no replay can see exercises no healer.
  const result = await replayAgainst(breakage("moved-element"));
  assert.equal(result.outcome, "failed", JSON.stringify(result.failure));
  assert.equal(result.failure?.index, 5);
  // The locator still resolves and the click still succeeds — it just no longer lands
  // on anything that opens the detail view. Only the assertion catches that.
  assert.equal(result.failure?.phase, "assert");
  assert.match(result.failure?.error ?? "", /toBeVisible/);
});

test("class 4 (swapped-data): every locator resolves; ONLY the assertion catches it", async () => {
  const result = await replayAgainst(breakage("swapped-data"));
  assert.equal(result.outcome, "failed");
  assert.equal(result.failure?.index, 5);
  assert.equal(result.failure?.phase, "assert");
  // Playwright's diagnostic names both sides, which is what a reviewer needs to see.
  assert.match(result.failure?.error ?? "", /Contoso Rail/);
  assert.match(result.failure?.error ?? "", /Fabrikam Metals/);
});

test("REVIEW REGRESSION (P2) — a programmatic fill with no value is a step failure, not an empty fill", async () => {
  // The validator catches this for YAML; a library caller handing replaySpec a
  // hand-built Spec bypasses it, and `value ?? ""` silently cleared the field.
  let filledWith: string | undefined;
  const session = {
    sessionId: "fake",
    page: {
      fill: async (_target: string, value: string) => {
        filledWith = value;
      },
    } as unknown as import("playwright-core").Page,
    fetchReplay: async () => null,
    close: async () => {},
  };
  const result = await replaySpec(
    {
      name: "no-value",
      startUrl: "http://app.test/",
      steps: [{ id: "st_1", index: 1, action: "fill", target: "#email" }],
    },
    session,
  );
  assert.equal(result.outcome, "failed");
  assert.equal(result.failure?.phase, "action");
  assert.match(result.failure?.error ?? "", /fill needs a value/);
  assert.equal(filledWith, undefined, "the page must not have been touched");
});

test("class 3 (changed-flow): a hasText on HIDDEN static text must not pass", async () => {
  // First observed run of this corpus PASSED here: the confirmation heading's text is
  // always in the DOM, just hidden, and toHaveText matches hidden elements. Visibility
  // is now implied by every assertion — this is the test that keeps it that way.
  const result = await replayAgainst(breakage("changed-flow"));
  assert.equal(result.outcome, "failed");
  assert.equal(result.failure?.index, 7);
  assert.equal(result.failure?.phase, "assert");
  assert.match(result.failure?.error ?? "", /toBeVisible/);
});

test("no breakage class is survivable: EVERY class in the corpus reds the pristine spec before any heal", async () => {
  // The healer verification matrix can only stamp a healer on a run that had something
  // to heal. A class the shipped spec replays green through reports `failed` for every
  // healer in the matrix and holds the publish gate shut — which is exactly how the
  // moved-element defect surfaced. This guard is per-class and self-extending.
  const survivors: string[] = [];
  for (const name of corpusClasses()) {
    const result = await replayAgainst(breakage(name));
    if (result.outcome !== "failed") survivors.push(name);
  }
  assert.deepEqual(
    survivors,
    [],
    "breakage classes the spec replays green through",
  );
});

test("role+name and text asserts resolve against the real sample app", async () => {
  // What three vendor agents reached for and were rejected 6/6: naming a heading with
  // no testId. Real headless Chromium, not a stub — the thing under test is whether
  // getByRole/getByText actually resolve, not whether the mapping compiles.
  const app = await serveDirectory(SAMPLE_APP);
  const driver = new LocalPlaywrightDriver();
  const session = await driver.open();
  try {
    const result = await replaySpec(
      {
        name: "role-and-text-asserts",
        startUrl: app.baseUrl,
        steps: [
          { id: "st_1", index: 1, action: "goto", target: app.baseUrl },
          {
            id: "st_2",
            index: 2,
            action: "fill",
            target: "#email",
            value: "ops@forgedepot.test",
          },
          {
            id: "st_3",
            index: 3,
            action: "fill",
            target: "#password",
            value: "hunter2",
          },
          {
            id: "st_4",
            index: 4,
            action: "click",
            target: "#signin-button",
            // role + accessible name — the getByRole locator.
            assert: { role: "heading", name: "Open orders" },
          },
          {
            id: "st_5",
            index: 5,
            action: "click",
            target: '[data-order-id="SO-4472"]',
            assert: { testId: "detail-customer", hasText: "Contoso Rail" },
          },
          {
            id: "st_6",
            index: 6,
            action: "click",
            target: "#approve-button",
            // text — the getByText locator, exact whole-string match.
            assert: { text: "Order approved", exact: true },
          },
        ],
      },
      session,
      { stepTimeoutMs: 2000 },
    );
    assert.equal(result.outcome, "passed", JSON.stringify(result.failure));
    assert.ok(result.steps.every((step) => step.outcome === "ok"));
  } finally {
    await session.close();
    await app.close();
  }
});

test("a url assertion (exact, prefix, pattern) each independently resolves against a real page URL", async () => {
  // The sample app never navigates, so its URL is static — exactly the case a
  // navigation proposal needs to assert on directly, instead of a destination heading.
  // One url key per step (the validator's exactly-one rule), each mode on its own.
  const app = await serveDirectory(SAMPLE_APP);
  const driver = new LocalPlaywrightDriver();
  const session = await driver.open();
  try {
    const result = await replaySpec(
      {
        name: "url-asserts",
        startUrl: app.baseUrl,
        steps: [
          {
            id: "st_1",
            index: 1,
            action: "goto",
            target: app.baseUrl,
            assert: { url: `${app.baseUrl}/` },
          },
          {
            id: "st_2",
            index: 2,
            action: "waitFor",
            target: "body",
            assert: { urlPrefix: app.baseUrl },
          },
          {
            id: "st_3",
            index: 3,
            action: "waitFor",
            target: "body",
            assert: { urlPattern: "^http://127\\.0\\.0\\.1:\\d+/$" },
          },
        ],
      },
      session,
      { stepTimeoutMs: 2000 },
    );
    assert.equal(result.outcome, "passed", JSON.stringify(result.failure));
    assert.equal(result.steps.length, 3);
    assert.ok(result.steps.every((step) => step.outcome === "ok"));
  } finally {
    await session.close();
    await app.close();
  }
});

test("a url assertion fails discriminately — exact, prefix, and pattern each catch their own mismatch", async () => {
  const app = await serveDirectory(SAMPLE_APP);
  const driver = new LocalPlaywrightDriver();
  const mismatches: Array<
    [string, { [K in "url" | "urlPrefix" | "urlPattern"]?: string }]
  > = [
    ["url", { url: "http://127.0.0.1:1/no-such-port" }],
    ["urlPrefix", { urlPrefix: "https://" }],
    ["urlPattern", { urlPattern: "^https://" }],
  ];
  try {
    for (const [field, assertion] of mismatches) {
      const session = await driver.open();
      try {
        const result = await replaySpec(
          {
            name: `url-mismatch-${field}`,
            startUrl: app.baseUrl,
            steps: [
              {
                id: "st_1",
                index: 1,
                action: "goto",
                target: app.baseUrl,
                assert: assertion,
              },
            ],
          },
          session,
          { stepTimeoutMs: 1000 },
        );
        assert.equal(
          result.outcome,
          "failed",
          `${field} should have failed against the real page URL`,
        );
        assert.equal(result.failure?.phase, "assert");
      } finally {
        await session.close();
      }
    }
  } finally {
    await app.close();
  }
});

test("a canonical url value passes in all four consumers — replay, playwright, cypress, and puppeteer", async () => {
  // The exact agreement the canonical-URL rule exists to guarantee: Playwright's
  // toHaveURL(string) normalizes its argument through new URL() before comparing,
  // while Cypress (cy.url().should("eq", ...)) and Puppeteer (raw ===) compare the
  // string as-is — a merely-absolute-but-non-canonical value could pass one and fail
  // the others. The validator now only ever lets a canonical value through, so all
  // four consumers see and agree on the identical string.
  const app = await serveDirectory(SAMPLE_APP);
  const canonicalUrl = `${app.baseUrl}/`;
  const driver = new LocalPlaywrightDriver();
  const session = await driver.open();
  try {
    const result = await replaySpec(
      {
        name: "canonical-url",
        startUrl: app.baseUrl,
        steps: [
          {
            id: "st_1",
            index: 1,
            action: "goto",
            target: app.baseUrl,
            assert: { url: canonicalUrl },
          },
        ],
      },
      session,
      { stepTimeoutMs: 2000 },
    );
    assert.equal(result.outcome, "passed", JSON.stringify(result.failure));
  } finally {
    await session.close();
    await app.close();
  }

  const compiledSpec: Spec = {
    name: "canonical-url",
    startUrl: canonicalUrl,
    steps: [
      {
        id: "st_1",
        index: 1,
        action: "goto",
        target: canonicalUrl,
        assert: { url: canonicalUrl },
      },
    ],
  };
  const quoted = JSON.stringify(canonicalUrl);
  assert.ok(
    compileSpec(compiledSpec, "playwright").source.includes(
      `await expect(page).toHaveURL(${quoted});`,
    ),
  );
  assert.ok(
    compileSpec(compiledSpec, "cypress").source.includes(
      `cy.url().should("eq", ${quoted});`,
    ),
  );
  assert.ok(
    compileSpec(compiledSpec, "puppeteer").source.includes(
      `await expectUrlExact(${quoted});`,
    ),
  );
});

test("a url assertion on one step, followed by an element assertion on the next, both replay clean", async () => {
  // An assert is EITHER page-level OR element-level, never both (the validator's
  // mutual-exclusivity rule) — this is the shape a navigation check now takes:
  // its own step, immediately followed by whatever element check still applies.
  const app = await serveDirectory(SAMPLE_APP);
  const driver = new LocalPlaywrightDriver();
  const session = await driver.open();
  try {
    const result = await replaySpec(
      {
        name: "url-then-element",
        startUrl: app.baseUrl,
        steps: [
          { id: "st_1", index: 1, action: "goto", target: app.baseUrl },
          {
            id: "st_2",
            index: 2,
            action: "fill",
            target: "#email",
            value: "ops@forgedepot.test",
          },
          {
            id: "st_3",
            index: 3,
            action: "fill",
            target: "#password",
            value: "hunter2",
          },
          {
            id: "st_4",
            index: 4,
            action: "click",
            target: "#signin-button",
            assert: { urlPrefix: app.baseUrl },
          },
          {
            id: "st_5",
            index: 5,
            action: "click",
            target: '[data-order-id="SO-4472"]',
            assert: { testId: "detail-customer", hasText: "Contoso Rail" },
          },
        ],
      },
      session,
      { stepTimeoutMs: 2000 },
    );
    assert.equal(result.outcome, "passed", JSON.stringify(result.failure));
    assert.equal(result.steps.length, 5);
    assert.ok(result.steps.every((step) => step.outcome === "ok"));
  } finally {
    await session.close();
    await app.close();
  }
});

/**
 * The step log and the replay stream must be timestamped on the SAME clock.
 *
 * Step anchors came from the harness process's `Date.now()` while event timestamps come
 * from the browser-side recorder. On a developer machine the two agree and everything
 * looks correct; inside a Solari guest they were measured ~0.9-1.0 s apart, and every
 * window then addressed the wrong part of the stream. Both variants are tested because
 * only the first one is loud: a skew LARGER than the run empties the early segments and
 * `assembleEvidence` refuses them, while a skew the size of ONE STEP silently hands each
 * segment its neighbour's events — evidence that renders, and shows the wrong thing.
 *
 * The clock/recorder edge is the only thing faked: a page whose JS clock runs ahead of
 * the harness, and a replay stream stamped in that same page clock.
 */
const STEP_MS = 150;

function sleep(ms: number): Promise<void> {
  return new Promise((resolve) => setTimeout(resolve, ms));
}

/** A page whose own `Date.now()` runs `skewMs` ahead of this process's. */
function skewedPage(
  skewMs: number,
  onAction: () => Promise<void>,
): import("playwright-core").Page {
  return {
    // The fake routes by the evaluated function's source: the runner's clock read, the
    // goto paint-wait, and the metrics drain are the three evaluates a step makes.
    evaluate: async (fn: unknown) => {
      const source = String(fn);
      if (source.includes("requestAnimationFrame")) return undefined;
      if (/Date\.now\(\)/.test(source) && source.length < 60) {
        return Date.now() + skewMs;
      }
      throw new Error("no metrics collector in this fake");
    },
    addInitScript: async () => {},
    goto: async () => await onAction(),
    click: async () => await onAction(),
    url: () => "http://app.test/",
    locator: () => {
      throw new Error("no locator in this fake");
    },
  } as unknown as import("playwright-core").Page;
}

/** Three steps, each ~STEP_MS long, each emitting one marked event at its midpoint —
 *  stamped in the PAGE's clock, exactly as a real recorder would. */
async function replayUnderSkew(skewMs: number) {
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
  const result = await replaySpec(
    {
      name: "clock-domains",
      startUrl: "http://app.test/",
      steps: [
        { id: "st_1", index: 1, action: "goto", target: "http://app.test/" },
        { id: "st_2", index: 2, action: "click", target: "#a" },
        { id: "st_3", index: 3, action: "click", target: "#b" },
      ],
    },
    session,
  );
  assert.equal(result.outcome, "passed", JSON.stringify(result.failure));
  return {
    segments: sliceSegments(events, result.steps),
    events,
    steps: result.steps,
  };
}

/**
 * Which step emitted the marked events inside a segment's own WINDOW. The preamble is
 * skipped deliberately: INVARIANT 2's pre-roll carries every event since the last
 * snapshot forward on purpose, so an earlier step's events appearing there is the
 * design working, not the misattribution under test.
 */
function stepsIn(segment: {
  events: ReplayEvent[];
  preambleCount: number;
}): number[] {
  return segment.events
    .slice(segment.preambleCount)
    .filter((e) => e.type === 3)
    .map((e) => (e.data as { step: number }).step);
}

test("clock domains (i) — a skew LARGER than the run must not empty the early segments", async () => {
  const { segments } = await replayUnderSkew(10_000);
  assert.equal(segments.length, 3);
  // The loud variant: step 1's segment held nothing at all, so the integrity check
  // refused the whole record with "segment for step 1 is not renderable".
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

test("clock domains (ii) — a skew the size of ONE STEP must not hand a segment its neighbour's events", async () => {
  const { segments } = await replayUnderSkew(STEP_MS);
  assert.equal(segments.length, 3);
  // The silent variant: every segment renders, and every one of them shows the
  // previous step. This is the "artifact shows the wrong thing" class segment.mts's
  // own header says the slicer exists to prevent.
  assert.deepEqual(
    segments.map(stepsIn),
    [[1], [2], [3]],
    "a segment must never carry the neighbouring step's events",
  );
});

/**
 * Clock-offset sampling: a sample that throws must cost only that sample.
 *
 * The first version bailed out of the whole loop on any failure and returned 0, which
 * reads as "no skew" — so one flaky evaluate against a genuinely skewed recorder put
 * every anchor back in the wrong clock, silently, which is the misattribution the
 * sampling exists to prevent. 0 is only correct when NOTHING could be measured.
 */
function pageAnsweringWith(
  answers: Array<{ offsetMs: number; delayMs?: number } | "throw">,
): import("playwright-core").Page {
  let call = 0;
  return {
    evaluate: async () => {
      const answer = answers[Math.min(call++, answers.length - 1)];
      if (answer === "throw") throw new Error("page is gone");
      if (answer.delayMs) await sleep(answer.delayMs);
      return Date.now() + answer.offsetMs;
    },
  } as unknown as import("playwright-core").Page;
}

/** The measurement carries a round trip, so it is never exact. */
function assertNear(actual: number, expected: number, tolerance = 60): void {
  assert.ok(
    Math.abs(actual - expected) <= tolerance,
    `expected ~${expected}, got ${actual}`,
  );
}

test("clock offset — a failed sample must not discard the offsets already measured", async () => {
  const offset = await recorderClockOffset(
    pageAnsweringWith([{ offsetMs: 4000 }, "throw", "throw"]),
  );
  assertNear(offset, 4000);
});

test("clock offset — a browser BEHIND the host keeps its sign", async () => {
  const offset = await recorderClockOffset(
    pageAnsweringWith([{ offsetMs: -2500 }]),
  );
  assertNear(offset, -2500);
});

test("clock offset — the narrowest round trip wins", async () => {
  // The slow samples' own offsets are off by half their round trip; the fast one is the
  // measurement worth keeping, which is the whole reason for sampling more than once.
  const offset = await recorderClockOffset(
    pageAnsweringWith([
      { offsetMs: 1000, delayMs: 150 },
      { offsetMs: 5000 },
      { offsetMs: 1000, delayMs: 150 },
    ]),
  );
  assertNear(offset, 5000);
});

test("clock offset — 0 only when NOTHING could be measured", async () => {
  const offset = await recorderClockOffset(
    pageAnsweringWith(["throw", "throw", "throw"]),
  );
  assert.equal(offset, 0);
});

test("clock offset — a non-finite probe reading is a lost sample, not a NaN offset", async () => {
  // A fake/broken page whose evaluate() resolves without throwing but returns
  // undefined — exactly what a torn-down JS context or a stubbed Page can produce.
  // Before the fix this sample won unconditionally (best started null) and NaN then
  // survived `?? 0` untouched, because NaN is not nullish.
  let call = 0;
  const brokenThenGood = {
    evaluate: async () => {
      call += 1;
      return call === 1 ? undefined : Date.now() + 2000;
    },
  } as unknown as import("playwright-core").Page;
  const offset = await recorderClockOffset(brokenThenGood);
  assert.ok(Number.isFinite(offset), `offset must never be NaN, got ${offset}`);
  assertNear(offset, 2000);
});

test("clock offset — 0 only when nothing could be measured, including an all-non-finite run", async () => {
  const alwaysBroken = {
    evaluate: async () => undefined,
  } as unknown as import("playwright-core").Page;
  const offset = await recorderClockOffset(alwaysBroken);
  assert.equal(offset, 0);
});

test("the step log is anchored in the recorder's clock; the record's own timestamp is not", async () => {
  // Two different clocks on purpose, and the audit must not blur them: step times
  // address the replay stream, the record's timestamp says when the record was made.
  // Durations are unaffected either way — a constant offset cancels in a subtraction.
  const skewMs = 3_600_000;
  const { steps } = await replayUnderSkew(skewMs);
  const record = assembleEvidence(
    { specName: "clock", outcome: "passed", steps },
    null,
    { driver: "fake" },
  );
  assertNear(steps[0].startedAt - Date.now(), skewMs, 500);
  assertNear(Date.parse(record.timestamp) - Date.now(), 0, 500);
  assert.ok(steps[0].endedAt - steps[0].startedAt >= 0, "durations stay sane");
});

/**
 * `valueFrom` — the value a spec must not carry.
 *
 * Against the real sample app and a real browser, because the whole claim is that the
 * credential reaches the FIELD and reaches nothing else. The spec below is the
 * committed one with both sign-in values turned into references; a pass proves the
 * resolution, and the greps prove the redaction on the same run.
 */
function referencedSignIn(spec: Spec): Spec {
  return {
    ...spec,
    steps: spec.steps.map((step) => {
      if (step.target === "#email")
        return {
          ...step,
          value: undefined,
          valueFrom: "env.APPROVE_AN_ORDER_EMAIL",
        };
      if (step.target === "#password")
        return {
          ...step,
          value: undefined,
          valueFrom: "env.APPROVE_AN_ORDER_PASSWORD",
        };
      return step;
    }),
  };
}

async function replayReferenced(
  environment: Record<string, string | undefined>,
): Promise<ReplayResult> {
  const app = await serveDirectory(SAMPLE_APP);
  const driver = new LocalPlaywrightDriver();
  const session = await driver.open();
  const restore: [string, string | undefined][] = Object.entries(environment);
  const previous = restore.map(
    ([name]) => [name, process.env[name]] as [string, string | undefined],
  );
  for (const [name, value] of restore) {
    if (value === undefined) delete process.env[name];
    else process.env[name] = value;
  }
  try {
    const spec = referencedSignIn(
      rebase(loadSpec(readFileSync(SPEC_FILE, "utf8")), app.baseUrl),
    );
    return await replaySpec(spec, session, { stepTimeoutMs: 2000 });
  } finally {
    for (const [name, value] of previous) {
      if (value === undefined) delete process.env[name];
      else process.env[name] = value;
    }
    await session.close();
    await app.close();
  }
}

const RECORDED_PASSWORD = "hunter2-from-the-environment";

test("valueFrom — replay resolves the value from the environment and passes", async () => {
  const result = await replayReferenced({
    APPROVE_AN_ORDER_EMAIL: "ops@forgedepot.test",
    APPROVE_AN_ORDER_PASSWORD: RECORDED_PASSWORD,
  });
  assert.equal(result.outcome, "passed", JSON.stringify(result.failure));
  // The audit "inputs" field records the SOURCE, never what it resolved to.
  const password = result.steps.find((step) => step.target === "#password");
  assert.deepEqual(password?.inputs, {
    valueFrom: "env.APPROVE_AN_ORDER_PASSWORD",
  });
});

test("valueFrom — the resolved value appears in NO string of the result", async () => {
  // Every writer downstream (step log, evidence record, PR body, terminal) reads this
  // object. One whole-object grep is the only check that covers all of them, including
  // the accessibility snapshots the runner captures after each step.
  const result = await replayReferenced({
    APPROVE_AN_ORDER_EMAIL: "ops@forgedepot.test",
    APPROVE_AN_ORDER_PASSWORD: RECORDED_PASSWORD,
  });
  // The list travels ON the result, deliberately and in ONE declared place, so evidence
  // assembly can apply it to the replay stream the runner never saw. Everything a writer
  // reads — the step log, the failure, the snapshots — must be clean of it.
  const { resolvedSecrets, ...reported } = result;
  assert.equal(
    JSON.stringify(reported).includes(RECORDED_PASSWORD),
    false,
    "the resolved value must not survive into anything a writer reads",
  );
  assert.deepEqual(resolvedSecrets, [
    {
      value: "ops@forgedepot.test",
      reference: "<redacted:env.APPROVE_AN_ORDER_EMAIL>",
    },
    {
      value: RECORDED_PASSWORD,
      reference: "<redacted:env.APPROVE_AN_ORDER_PASSWORD>",
    },
  ]);
});

test("valueFrom — a missing variable fails the step by NAME, and says nothing else", async () => {
  const result = await replayReferenced({
    APPROVE_AN_ORDER_EMAIL: "ops@forgedepot.test",
    APPROVE_AN_ORDER_PASSWORD: undefined,
  });
  assert.equal(result.outcome, "failed");
  assert.equal(result.failure?.target, "#password");
  assert.match(
    result.failure?.error ?? "",
    /the environment variable APPROVE_AN_ORDER_PASSWORD is not set/,
  );
  assert.match(result.failure?.error ?? "", /env\.APPROVE_AN_ORDER_PASSWORD/);
});

test("valueFrom — the value survives into NO artifact, forced onto every plane at once", async () => {
  // The test above passes a GREEN run and greps the result. Green is the easy case:
  // nothing had put the value anywhere. This forces it onto every plane a reviewer can
  // read, in one run, and then checks all of them:
  //
  //  - a FAILING assertion, so Playwright's own error quotes what it found on screen;
  //  - the post-step accessibility snapshots the runner captures;
  //  - the replay stream, fetched here rather than assumed;
  //  - the evidence record assembled from all of it, and the page rendered from that.
  const app = await serveDirectory(SAMPLE_APP);
  const driver = new LocalPlaywrightDriver();
  const session = await driver.open();
  const canary = "canary-secret-value";
  process.env.APPROVE_AN_ORDER_EMAIL = canary;
  process.env.APPROVE_AN_ORDER_PASSWORD = RECORDED_PASSWORD;
  try {
    const base = referencedSignIn(
      rebase(loadSpec(readFileSync(SPEC_FILE, "utf8")), app.baseUrl),
    );
    // The sample app echoes the signed-in identity back onto the page, so the value
    // typed from the environment IS on screen. Asserting something else about that
    // element makes Playwright report what it actually found — the value.
    const spec: Spec = {
      ...base,
      steps: base.steps.map((step) =>
        step.target === "#signin-button"
          ? { ...step, assert: { testId: "current-user", hasText: "nobody" } }
          : step,
      ),
    };
    const result = await replaySpec(spec, session, { stepTimeoutMs: 3000 });
    assert.equal(result.outcome, "failed", "the run must actually fail here");
    assert.equal(result.failure?.phase, "assert");
    // The control: this is where the value lands when nothing removes it.
    assert.match(
      result.failure?.error ?? "",
      /<redacted:env\.APPROVE_AN_ORDER_EMAIL>/,
    );
    const events = await session.fetchReplay();
    const evidence = assembleEvidence(result, events, {
      driver: driver.name,
      sessionId: session.sessionId,
      secrets: result.resolvedSecrets,
    });
    const { resolvedSecrets, ...reported } = result;
    const planes: [string, string][] = [
      ["the replay result", JSON.stringify(reported)],
      ["the evidence record", JSON.stringify(evidence)],
      [
        "the rendered evidence page",
        renderEvidencePage({
          specName: evidence.specName,
          outcome: evidence.outcome,
          initial: evidence,
        }),
      ],
    ];
    for (const [plane, text] of planes) {
      assert.equal(text.includes(canary), false, `${plane} carries the value`);
      assert.equal(
        text.includes(RECORDED_PASSWORD),
        false,
        `${plane} carries the password`,
      );
    }
    assert.equal(resolvedSecrets?.length, 2);
  } finally {
    delete process.env.APPROVE_AN_ORDER_EMAIL;
    delete process.env.APPROVE_AN_ORDER_PASSWORD;
    await session.close();
    await app.close();
  }
});

/** A page whose only content is what the test writes, served the same way the sample
 *  app is. Frames need documents of their own, which the sample app has none of. */
function pagesWith(files: Record<string, string>): string {
  const directory = mkdtempSync(join(tmpdir(), "formic-frames-"));
  for (const [name, html] of Object.entries(files)) {
    writeFileSync(
      join(directory, name),
      `<!doctype html><meta charset="utf-8">${html}`,
    );
  }
  return directory;
}

const FRAME_PAGES = {
  "index.html": `<h1>Host</h1>
    <iframe id="outer" name="outer-frame" src="outer.html"></iframe>`,
  "outer.html": `<h1>Outer</h1>
    <iframe id="inner" name="inner-frame" src="inner.html"></iframe>`,
  "inner.html": `<h1>Inner</h1>
    <input id="note" />
    <button id="go">Go</button>
    <p data-testid="receipt">waiting</p>
    <script>
      document.getElementById("go").addEventListener("click", function () {
        document.querySelector('[data-testid="receipt"]').textContent =
          "Done " + document.getElementById("note").value;
      });
    </script>`,
};

async function replayFrameSpec(steps: Spec["steps"]): Promise<ReplayResult> {
  const app = await serveDirectory(pagesWith(FRAME_PAGES));
  const driver = new LocalPlaywrightDriver();
  const session = await driver.open();
  try {
    const spec: Spec = {
      name: "frames",
      startUrl: `${app.baseUrl}/index.html`,
      steps: [
        {
          id: "st_f0",
          index: 1,
          action: "goto",
          target: `${app.baseUrl}/index.html`,
        },
        ...steps.map((step, position) => ({ ...step, index: position + 2 })),
      ],
    };
    return await replaySpec(spec, session, { stepTimeoutMs: 2000 });
  } finally {
    await session.close();
    await app.close();
  }
}

test("a step's frame chain is walked, and the locator resolves INSIDE the frame it names", async () => {
  // `#note` and `#go` exist only in the innermost document. Without the chain the same
  // step resolves nothing at all, which is the whole point of the field.
  const result = await replayFrameSpec([
    {
      id: "st_f1",
      index: 0,
      action: "fill",
      target: "#note",
      value: "42",
      frame: [{ selector: "#outer" }, { selector: "#inner" }],
      assert: {
        selector: "#note",
        visible: true,
        frame: [{ selector: "#outer" }, { selector: "#inner" }],
      },
    },
    {
      id: "st_f2",
      index: 0,
      action: "click",
      target: "#go",
      frame: [{ selector: "#outer" }, { name: "inner-frame" }],
      // No frame of its own: an assertion inherits the step's, which is the case that
      // is almost always meant.
      assert: { testId: "receipt", hasText: "Done 42" },
    },
  ]);
  assert.equal(result.outcome, "passed", JSON.stringify(result.failure));
});

test("a chain link that names no open frame fails the step in the ACTION phase, by link", async () => {
  // Not an assert failure and not a locator failure: the step could not be RUN at the
  // address it gave. The message names which link, so a reader knows which <iframe> to
  // go and look at rather than re-reading a selector that is fine.
  const result = await replayFrameSpec([
    {
      id: "st_f1",
      index: 0,
      action: "click",
      target: "#go",
      frame: [{ selector: "#outer" }, { name: "no-such-frame" }],
      assert: { testId: "receipt", visible: true },
    },
  ]);
  assert.equal(result.outcome, "failed");
  assert.equal(result.failure?.phase, "action");
  assert.match(result.failure?.error ?? "", /frame chain link 2/);
  assert.match(result.failure?.error ?? "", /no-such-frame/);
});

test("a frame chain is scoped to its parent — a name that matches elsewhere does not count", async () => {
  // `page.frame({ name })` searches the whole page, so "inner-frame inside inner-frame"
  // would have found the one real inner frame and passed. A chain whose links are not
  // actually nested is not the chain the spec wrote down.
  const result = await replayFrameSpec([
    {
      id: "st_f1",
      index: 0,
      action: "click",
      target: "#go",
      frame: [{ name: "inner-frame" }, { name: "inner-frame" }],
      assert: { testId: "receipt", visible: true },
    },
  ]);
  assert.equal(result.outcome, "failed");
  assert.match(result.failure?.error ?? "", /frame chain link 1/);
});

test("a frame link may name its frame by document URL prefix", async () => {
  const result = await replayFrameSpec([
    {
      id: "st_f1",
      index: 0,
      action: "waitFor",
      target: "#go",
      frame: [{ name: "outer-frame" }, { urlPrefix: "http://" }],
    },
  ]);
  assert.equal(result.outcome, "passed", JSON.stringify(result.failure));
});

test("a link matching more than one sibling frame is REFUSED, never resolved to the first", () => {
  // Two iframes of one embedded form share a name. Taking the first ran the step in a
  // document the spec did not name — silently, and with every locator inside it
  // resolving, so nothing downstream could tell.
  const twins = {
    "index.html": `<h1>Host</h1>
      <iframe name="twin" src="twin.html"></iframe>
      <iframe name="twin" src="twin.html"></iframe>`,
    "twin.html": '<button id="go">Go</button>',
  };
  return (async () => {
    const app = await serveDirectory(pagesWith(twins));
    const driver = new LocalPlaywrightDriver();
    const session = await driver.open();
    try {
      const result = await replaySpec(
        {
          name: "twins",
          startUrl: `${app.baseUrl}/index.html`,
          steps: [
            {
              id: "st_g",
              index: 1,
              action: "goto",
              target: `${app.baseUrl}/index.html`,
            },
            {
              id: "st_c",
              index: 2,
              action: "waitFor",
              target: "#go",
              frame: [{ name: "twin" }],
            },
          ],
        },
        session,
        { stepTimeoutMs: 2000 },
      );
      assert.equal(result.outcome, "failed");
      assert.equal(result.failure?.phase, "action");
      assert.match(result.failure?.error ?? "", /frame chain link 1/);
      assert.match(result.failure?.error ?? "", /ambiguous — 2 frames match/);
    } finally {
      await session.close();
      await app.close();
    }
  })();
});

test("a referenced value typed into a CHILD FRAME never reaches the replay stream", async () => {
  // rrweb records the top-level document and observes same-origin child documents through
  // it, so the masking callback that sees a child frame's input is the PARENT's. Replay
  // marks a referenced target in the element's OWN realm — the only realm it can evaluate
  // in — so the decision landed in the child's memo and the callback that actually masks
  // never saw it. An unlabelled <input type="text"> in an iframe then went into the
  // evidence stream in the clear, which is the one thing this path exists to prevent.
  const pages = pagesWith({
    "index.html": `<h1>Host</h1><iframe id="child" src="child.html"></iframe>`,
    "child.html":
      '<input id="anything" type="text" /><p data-testid="ok">ready</p>',
  });
  const app = await serveDirectory(pages);
  const driver = new LocalPlaywrightDriver();
  const session = await driver.open();
  const secret = "correct-horse-battery-staple";
  process.env.FRAME_MASK_PROBE = secret;
  try {
    const result = await replaySpec(
      {
        name: "framed-secret",
        startUrl: `${app.baseUrl}/index.html`,
        steps: [
          {
            id: "st_g",
            index: 1,
            action: "goto",
            target: `${app.baseUrl}/index.html`,
          },
          {
            id: "st_f",
            index: 2,
            action: "fill",
            target: "#anything",
            frame: [{ selector: "#child" }],
            valueFrom: "env.FRAME_MASK_PROBE",
            assert: {
              testId: "ok",
              visible: true,
              frame: [{ selector: "#child" }],
            },
          },
        ],
      },
      session,
      { stepTimeoutMs: 2000 },
    );
    assert.equal(result.outcome, "passed", JSON.stringify(result.failure));
    const stream = JSON.stringify(await session.fetchReplay());
    assert.ok(
      !stream.includes(secret),
      "the replay stream must not carry a referenced value typed into an iframe",
    );
    assert.ok(
      stream.includes("<secret:"),
      "and it must show the placeholder in its place, not simply nothing",
    );
  } finally {
    delete process.env.FRAME_MASK_PROBE;
    await session.close();
    await app.close();
  }
});

test("a name link waits for a frame that attaches late, to the step's own timeout", async () => {
  // A frame attaches when the page decides to. Asked once, a chain got a frame that was a
  // few hundred milliseconds from existing and failed the step with a message about a
  // frame the reader could see in the browser. A selector link inherits Playwright's own
  // waiting; a name/url link had none of its own.
  const pages = pagesWith({
    "index.html": `<h1>Host</h1>
      <script>
        setTimeout(function () {
          var f = document.createElement("iframe");
          f.name = "late-frame";
          f.src = "late.html";
          document.body.appendChild(f);
        }, 600);
      </script>`,
    "late.html": '<button id="ready">Ready</button>',
  });
  const app = await serveDirectory(pages);
  const driver = new LocalPlaywrightDriver();
  const session = await driver.open();
  try {
    const result = await replaySpec(
      {
        name: "late-frame",
        startUrl: `${app.baseUrl}/index.html`,
        steps: [
          {
            id: "st_g",
            index: 1,
            action: "goto",
            target: `${app.baseUrl}/index.html`,
          },
          {
            id: "st_w",
            index: 2,
            action: "waitFor",
            target: "#ready",
            frame: [{ name: "late-frame" }],
          },
        ],
      },
      session,
      { stepTimeoutMs: 3000 },
    );
    assert.equal(result.outcome, "passed", JSON.stringify(result.failure));
  } finally {
    await session.close();
    await app.close();
  }
});

test("a SELECTOR link matching more than one frame is refused too — the rule is the link's, not the form's", async () => {
  // A selector is no more allowed to stand for two frames than a name is. This one
  // matched both iframes and the step ran in whichever the engine met first, with every
  // locator inside it resolving — the same misattribution the name form is refused for,
  // wearing the form the recorder prefers.
  const pages = pagesWith({
    "index.html": `<h1>Host</h1>
      <iframe data-widget src="twin.html"></iframe>
      <iframe data-widget src="twin.html"></iframe>`,
    "twin.html": '<button id="go">Go</button>',
  });
  const app = await serveDirectory(pages);
  const driver = new LocalPlaywrightDriver();
  const session = await driver.open();
  try {
    const result = await replaySpec(
      {
        name: "twin-selectors",
        startUrl: `${app.baseUrl}/index.html`,
        steps: [
          {
            id: "st_g",
            index: 1,
            action: "goto",
            target: `${app.baseUrl}/index.html`,
          },
          {
            id: "st_w",
            index: 2,
            action: "waitFor",
            target: "#go",
            frame: [{ selector: "iframe[data-widget]" }],
          },
        ],
      },
      session,
      { stepTimeoutMs: 1500 },
    );
    assert.equal(result.outcome, "failed");
    assert.equal(result.failure?.phase, "action");
    assert.match(result.failure?.error ?? "", /frame chain link 1/);
    assert.match(result.failure?.error ?? "", /ambiguous — 2 frames match/);
  } finally {
    await session.close();
    await app.close();
  }
});
