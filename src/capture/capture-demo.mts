/**
 * Integration proof: capture the sample app's approve-order flow and
 * compile it to a spec.
 *
 * Runs on the LOCAL driver by default — no Solari key, no network, no cost. That is the
 * point: if this only worked against Solari, the driver abstraction would be decorative.
 *
 *   node --import tsx packages/harness/src/capture/capture-demo.mts [outfile]
 */
import { writeFileSync } from "node:fs";
import { LocalPlaywrightDriver } from "../driver/local-playwright.mts";
import { Recorder } from "./recorder.mts";
import { saveSpec } from "../spec/parse.mts";

const BASE = process.env.FORMIC_BASE_URL ?? "http://127.0.0.1:4173";
const outFile =
  process.argv[2] ?? "packages/harness/fixtures/specs/approve-an-order.yaml";

const driver = new LocalPlaywrightDriver();
const session = await driver.open();
const recorder = new Recorder(session, "approve-an-order", driver.name);

let captureSucceeded = false;
try {
  await recorder.goto(`${BASE}/`);
  await recorder.fill("#email", "ops@forgedepot.test", {
    selector: "#email",
    visible: true,
  });
  await recorder.fill("#password", "hunter2", {
    selector: "#password",
    visible: true,
  });
  await recorder.click("#signin-button", {
    testId: "current-user",
    hasText: "ops@forgedepot.test",
  });
  await recorder.click('[data-order-id="SO-4472"]', {
    testId: "detail-customer",
    hasText: "Contoso Rail",
  });
  await recorder.fill("#note", "verified against PO 88120", {
    selector: "#note",
    visible: true,
  });
  await recorder.click("#approve-button", {
    testId: "confirmation",
    hasText: "Order approved",
  });
  captureSucceeded = true;
} finally {
  const run = recorder.finish();

  // Recorder.record() appends failed steps too, so a capture that died midway still
  // produces a partial spec. Writing that over a known-good spec on every routine
  // failure would silently destroy the artifact — so persist only on success, and
  // always report what was captured either way.
  if (captureSucceeded) {
    writeFileSync(outFile, saveSpec(run.spec));
    console.log(`spec -> ${outFile}`);
  } else {
    console.error(
      `capture FAILED after ${run.steps.length} step(s) — ${outFile} left untouched`,
    );
  }

  console.log(`steps captured: ${run.steps.length}`);
  for (const step of run.steps) {
    console.log(
      `  ${step.index} ${step.action.padEnd(6)} ${(step.target ?? "").padEnd(28)} ${step.endedAt - step.startedAt}ms ${step.outcome}`,
    );
  }
  await session.close();
}
