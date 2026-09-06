/**
 * The pre-roll rule: a segment's preamble must reconstruct the DOM as it was when the
 * step BEGAN, not as it was at the last navigation.
 *
 * Found by looking at the first dogfood PR's frames: the approval step's BEFORE and
 * AFTER both showed the sign-in form, because the sample app is a single-page app and
 * every step after the goto mutates the DOM without navigating. The recording probe never
 * exposed this — every probe step navigated. Two tests: a synthetic stream that pins
 * the rule, and a real recording of the sample app rendered in a real browser, read
 * back from the player's own document.
 */
import { test } from "node:test";
import assert from "node:assert/strict";
import { execFileSync } from "node:child_process";
import { mkdtempSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { dirname, join } from "node:path";
import { fileURLToPath, pathToFileURL } from "node:url";
import { chromium } from "playwright-core";
import { LocalPlaywrightDriver } from "../driver/local-playwright.mts";
import { renderEvidencePage } from "./page.mts";
import { preambleFor, sliceSegments } from "./segment.mts";
import {
  type ReplayEvent,
  type StepRecord,
  RRWEB_FULL_SNAPSHOT,
  RRWEB_META,
} from "./types.mts";
import { loadSpec } from "../spec/parse.mts";
import type { Spec } from "../spec/types.mts";
import { replaySpec } from "../replay/runner.mts";
import { assembleEvidence } from "../replay/evidence.mts";
import { serveDirectory } from "../replay/sample-app-server.mts";
import { heal } from "../heal/loop.mts";
import { scriptedHealer } from "../heal/scripted.mts";

test("RULE — the preamble carries every event from the last snapshot up to the window start", () => {
  const meta: ReplayEvent = { type: RRWEB_META, timestamp: 100 };
  const full: ReplayEvent = { type: RRWEB_FULL_SNAPSHOT, timestamp: 110 };
  const m1: ReplayEvent = { type: 3, timestamp: 200, data: { n: 1 } };
  const m2: ReplayEvent = { type: 3, timestamp: 300, data: { n: 2 } };
  const m3: ReplayEvent = { type: 3, timestamp: 400, data: { n: 3 } };
  const events = [meta, full, m1, m2, m3];
  // A step that begins at 350: its window is [m3]; its DOM at 350 is full + m1 + m2.
  assert.deepEqual(preambleFor(events, 350, [m3]), [meta, full, m1, m2]);
  // A window opening with its own navigation needs nothing before it.
  assert.deepEqual(preambleFor(events, 100, [meta, full, m1]), []);
  // The pair binds a snapshot to the Meta before it; a later unpaired Meta (a
  // navigation whose snapshot has not arrived) rides in the pre-roll after the old page.
  const newMeta: ReplayEvent = { type: RRWEB_META, timestamp: 420 };
  const newFull: ReplayEvent = { type: RRWEB_FULL_SNAPSHOT, timestamp: 440 };
  assert.deepEqual(preambleFor([...events, newMeta, newFull], 430, [newFull]), [
    meta,
    full,
    m1,
    m2,
    m3,
    newMeta,
  ]);
});

const USECASE = join(
  dirname(fileURLToPath(import.meta.url)),
  "..",
  "..",
  "fixtures",
);

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

/** What the player's own document shows at the END of each segment on the page. */
async function renderedTextPerFrame(
  pageFile: string,
): Promise<Record<string, string>> {
  const browser = await chromium.launch({ headless: true });
  try {
    const page = await browser.newPage({
      viewport: { width: 1400, height: 1000 },
    });
    await page.route("**/*", (route) =>
      route.request().url().startsWith("file:")
        ? route.continue()
        : route.abort(),
    );
    await page.goto(pathToFileURL(pageFile).href, {
      waitUntil: "domcontentloaded",
    });
    await page.waitForFunction(
      () =>
        (window as unknown as { __e2edocReady?: boolean }).__e2edocReady ===
        true,
    );
    await page.waitForTimeout(300);
    // `return await`, not `return`: inside try/finally a bare `return promise` runs the
    // finally (browser.close()) before the evaluation settles.
    return await page.evaluate(() => {
      const out: Record<string, string> = {};
      for (const figure of Array.from(
        document.querySelectorAll("figure.player"),
      )) {
        const iframe = figure.querySelector(
          "iframe",
        ) as HTMLIFrameElement | null;
        out[figure.getAttribute("data-frame") ?? "?"] =
          iframe?.contentDocument?.body?.innerText ?? "";
      }
      return out;
    });
  } finally {
    await browser.close();
  }
}

test("a real SPA recording: each step's segment renders the page as it was AFTER that step", async () => {
  const app = await serveDirectory(join(USECASE, "sample-app"));
  const driver = new LocalPlaywrightDriver();
  const session = await driver.open();
  const dir = mkdtempSync(join(tmpdir(), "e2e-doctor-preroll-"));
  try {
    const spec = rebase(
      loadSpec(
        readFileSync(join(USECASE, "specs", "approve-an-order.yaml"), "utf8"),
      ),
      app.baseUrl,
    );
    const result = await replaySpec(spec, session, { stepTimeoutMs: 2000 });
    assert.equal(result.outcome, "passed", JSON.stringify(result.failure));
    const events = (await session.fetchReplay()) ?? [];
    const record = assembleEvidence(result, events, { driver: driver.name });
    const pageFile = join(dir, "index.html");
    writeFileSync(
      pageFile,
      renderEvidencePage({
        specName: spec.name,
        outcome: "passed",
        initial: record,
      }),
    );

    const text = await renderedTextPerFrame(pageFile);
    // Step 4 is the sign-in click: its segment ends on the orders list.
    assert.match(
      text["step-4"],
      /Open orders/,
      `step 4 rendered: ${text["step-4"].slice(0, 80)}`,
    );
    // Step 5 opens an order: the detail view with the customer.
    assert.match(text["step-5"], /Contoso Rail/);
    // Step 7 approves: the confirmation. Before the pre-roll rule this showed "Sign in".
    assert.match(
      text["step-7"],
      /Order approved/,
      `step 7 rendered: ${text["step-7"].slice(0, 80)}`,
    );
    assert.doesNotMatch(text["step-7"], /Sign in\s*Email/);
  } finally {
    await session.close();
    await app.close();
    rmSync(dir, { recursive: true, force: true });
  }
});

test("a frame shows a segment's LAST event: the inserted confirming click's AFTER frame shows the interstitial", async () => {
  // Measured on dogfood PR #11: the banner-revealing mutation was the final event of
  // the inserted step's 39 ms window, and the player's seek to the last event's
  // offset did not apply it — the AFTER frame showed the page before the click.
  const scratch = mkdtempSync(join(tmpdir(), "e2e-doctor-lastevent-"));
  const variant = join(scratch, "changed-flow");
  execFileSync(
    process.execPath,
    [join(USECASE, "breakages", "apply.mjs"), "changed-flow", variant],
    { stdio: "pipe" },
  );
  const app = await serveDirectory(variant);
  const driver = new LocalPlaywrightDriver();
  try {
    const spec = rebase(
      loadSpec(
        readFileSync(join(USECASE, "specs", "approve-an-order.yaml"), "utf8"),
      ),
      app.baseUrl,
    );
    const approve = spec.steps[6].id;
    const healer = scriptedHealer([
      {
        kind: "insert-step",
        beforeStepId: approve,
        step: {
          action: "click",
          target: "#approve-button",
          assert: { selector: "#confirm-banner", visible: true },
        },
        reason: "interstitial",
      },
    ]);
    const result = await heal(spec, driver, healer, { stepTimeoutMs: 2000 });
    assert.equal(result.outcome, "healed");
    const [attempt] = result.attempts;
    assert.ok(attempt.after, "the inserted step has an AFTER segment");

    const pageFile = join(scratch, "index.html");
    writeFileSync(
      pageFile,
      renderEvidencePage({
        specName: spec.name,
        outcome: "healed",
        initial: result.initial.evidence,
        attempts: [
          {
            attempt: 1,
            proposal: attempt.proposal,
            before: attempt.before,
            after: attempt.after,
            verification: attempt.verification?.evidence ?? null,
          },
        ],
      }),
    );
    const text = await renderedTextPerFrame(pageFile);
    assert.match(
      text["attempt-1-after"],
      /click Approve again to confirm/,
      `AFTER rendered: ${text["attempt-1-after"].slice(0, 120)}`,
    );
    assert.match(text["attempt-1-before"], /click Approve again to confirm/);
  } finally {
    await app.close();
    rmSync(scratch, { recursive: true, force: true });
  }
});

test("the slicer's segments stay renderable under the pre-roll rule", () => {
  const steps: StepRecord[] = [
    {
      id: "a",
      index: 1,
      action: "goto",
      startedAt: 100,
      endedAt: 105,
      outcome: "ok",
    },
    {
      id: "b",
      index: 2,
      action: "click",
      startedAt: 350,
      endedAt: 355,
      outcome: "ok",
    },
  ];
  const events: ReplayEvent[] = [
    { type: RRWEB_META, timestamp: 100 },
    { type: RRWEB_FULL_SNAPSHOT, timestamp: 110 },
    { type: 3, timestamp: 200 },
    { type: 3, timestamp: 400 },
  ];
  const [first, second] = sliceSegments(events, steps);
  assert.equal(first.preambleCount, 0);
  assert.equal(
    second.preambleCount,
    3,
    "Meta + FullSnapshot + the mutation before the window",
  );
  assert.equal(second.events.length, 4);
});
