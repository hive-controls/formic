/**
 * The local driver must produce an evidence stream with no Solari key, or the driver
 * abstraction is decorative and Solari is load-bearing for evidence — the structural
 * dependence the driver seam forbids. This is the test that makes the seam real.
 */
import { test } from "node:test";
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import { LocalPlaywrightDriver } from "./local-playwright.mts";
import { isRenderable, sliceSegments } from "../evidence/segment.mts";
import { RRWEB_FULL_SNAPSHOT, RRWEB_META } from "../evidence/types.mts";
import { loadSpec } from "../spec/parse.mts";
import type { Spec } from "../spec/types.mts";
import { assembleEvidence } from "../replay/evidence.mts";
import { replaySpec } from "../replay/runner.mts";
import { serveDirectory } from "../replay/sample-app-server.mts";

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

test("the local driver records an rrweb stream that slices into one renderable segment per step", async () => {
  const driver = new LocalPlaywrightDriver();
  assert.equal(driver.canRecord, true, "the capability gap must be closed");
  const app = await serveDirectory(join(USECASE, "sample-app"));
  const session = await driver.open();
  try {
    const spec = rebase(
      loadSpec(
        readFileSync(join(USECASE, "specs", "approve-an-order.yaml"), "utf8"),
      ),
      app.baseUrl,
    );
    const result = await replaySpec(spec, session, { stepTimeoutMs: 2000 });
    assert.equal(result.outcome, "passed", JSON.stringify(result.failure));

    const events = await session.fetchReplay();
    assert.ok(events !== null, "a local run must yield an evidence stream");
    assert.ok(events.length > 0);
    // Same clock frame as the step anchors — the property that makes per-step
    // addressing exact (the recording-addressability probe measured it for Solari; local is one clock).
    assert.ok(events[0].timestamp >= result.steps[0].startedAt - 5);
    assert.ok(events.some((e) => e.type === RRWEB_META));
    assert.ok(events.some((e) => e.type === RRWEB_FULL_SNAPSHOT));

    const segments = sliceSegments(events, result.steps);
    assert.equal(segments.length, result.steps.length);
    for (const segment of segments) {
      assert.ok(
        isRenderable(segment),
        `step ${segment.stepIndex} not renderable`,
      );
    }
    // The goto's own window must carry the navigation's snapshot, not a preamble.
    assert.equal(segments[0].preambleCount, 0);

    const record = assembleEvidence(result, events, {
      driver: driver.name,
      sessionId: session.sessionId,
    });
    assert.equal(record.recording, "captured");
    assert.equal(record.segments.length, 7);
  } finally {
    await session.close();
    await app.close();
  }
});
