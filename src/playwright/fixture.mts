/**
 * The Playwright Test fixture: extends `test` with a driver session that replays a
 * spec through the SAME replay function CI uses (replaySpec), on Playwright Test's
 * own browser/context lifecycle — recording via the same rrweb path the local driver
 * uses (driver/rrweb-recorder.mts), attached before the context's first page.
 *
 * `defineSpecTests` turns every spec file under a directory into one test() — the
 * "each spec becomes a test()" half of the ticket. The heal half is the reporter
 * (reporter.mts), which finds its way back to the spec file via the annotation this
 * file stamps on every generated test.
 */
import { existsSync, readFileSync, readdirSync } from "node:fs";
import { join, resolve } from "node:path";
import { test as base, type TestInfo } from "@playwright/test";
import { renderEvidencePage } from "../evidence/page.mts";
import { flushRecorder } from "../driver/recorder-flush.mts";
import { attachRrwebRecorder } from "../driver/rrweb-recorder.mts";
import type { DriverSession } from "../driver/types.mts";
import type { EvidenceRecord } from "../evidence/types.mts";
import { assembleEvidence } from "../replay/evidence.mts";
import { replaySpec, type ReplayResult } from "../replay/runner.mts";
import { loadSpec } from "../spec/parse.mts";
import type { Spec } from "../spec/types.mts";

/** Annotation type carrying the spec file's absolute path on a generated test — how
 *  the reporter finds the spec behind a failed test to hand to heal. */
export const SPEC_FILE_ANNOTATION = "formic-spec-file";

interface SpecFixtures {
  /** A DriverSession backed by Playwright Test's own context: same shape the CLI's
   *  local driver hands `replaySpec`, so the runner cannot tell the difference. */
  specSession: DriverSession;
}

export const test = base.extend<SpecFixtures>({
  specSession: async ({ context }, use, testInfo) => {
    // Before the first page, same requirement as the local driver: the init script
    // must cover every document this session navigates.
    const recorder = await attachRrwebRecorder(context);
    const page = await context.newPage();
    await use({
      sessionId: testInfo.testId,
      page,
      fetchReplay: async () => {
        // The same flush the local driver owes: rrweb's binding hop is asynchronous,
        // so the last action's events can still be in flight (driver/recorder-flush).
        await flushRecorder(page);
        return recorder.events();
      },
      close: async () => {},
    });
    await page.close();
  },
});

export { expect } from "@playwright/test";

/**
 * Attaches every artifact this replay produced for `spec`, one attachment per file:
 * the audit record as JSON, the reviewable evidence page rendered by the SAME
 * renderer the heal bundle uses (evidence/page.mts), and each frame PNG that a
 * frame renderer has already left in the test's own output directory. Frames are
 * attached when they exist and never rendered here — rendering drives a second
 * browser, which a test run must not pay for silently.
 */
async function attachEvidence(
  evidence: EvidenceRecord,
  spec: Spec,
  testInfo: TestInfo,
): Promise<void> {
  await testInfo.attach(`${spec.name}-evidence`, {
    body: JSON.stringify(evidence, null, 2),
    contentType: "application/json",
  });
  await testInfo.attach(`${spec.name}-evidence-page`, {
    body: renderEvidencePage({
      specName: evidence.specName,
      outcome: evidence.outcome,
      initial: evidence,
    }),
    contentType: "text/html",
  });
  const framesDir = testInfo.outputPath("frames");
  if (!existsSync(framesDir)) return;
  for (const frame of readdirSync(framesDir).sort()) {
    if (!frame.endsWith(".png")) continue;
    await testInfo.attach(`${spec.name}-frame-${frame.replace(/\.png$/, "")}`, {
      path: join(framesDir, frame),
      contentType: "image/png",
    });
  }
}

/**
 * Replays `spec` with `session`, attaches the evidence bundle as test attachments,
 * and fails the test on a failed step with the step name + assertion text.
 */
export async function runSpecAsTest(
  spec: Spec,
  session: DriverSession,
  testInfo: TestInfo,
): Promise<ReplayResult> {
  const result = await replaySpec(spec, session);
  const events = await session.fetchReplay();
  const evidence = assembleEvidence(result, events, {
    driver: "playwright-test",
    sessionId: session.sessionId,
    cdpConnect: session.cdpConnect,
    secrets: result.resolvedSecrets,
  });
  await attachEvidence(evidence, spec, testInfo);
  if (result.failure) {
    const step = spec.steps.find((s) => s.id === result.failure!.stepId);
    const assertion = step?.assert
      ? JSON.stringify(step.assert)
      : "(no assertion)";
    throw new Error(
      `step ${result.failure.index} "${step?.action ?? result.failure.action} ${step?.target ?? result.failure.target ?? ""}" ` +
        `failed in ${result.failure.phase} phase — expected ${assertion}: ${result.failure.error}`,
    );
  }
  return result;
}

/** Defines one test() per spec YAML file in `specsDir` (sorted for stable ordering). */
export function defineSpecTests(specsDir: string): void {
  const files = readdirSync(specsDir)
    .filter((name) => name.endsWith(".yaml") || name.endsWith(".yml"))
    .sort();
  for (const file of files) {
    const specPath = resolve(join(specsDir, file));
    const spec = loadSpec(readFileSync(specPath, "utf8"));
    test(spec.name, async ({ specSession }, testInfo) => {
      testInfo.annotations.push({
        type: SPEC_FILE_ANNOTATION,
        description: specPath,
      });
      await runSpecAsTest(spec, specSession, testInfo);
    });
  }
}
