/**
 * runSpecAsTest tested directly: the `specSession` fixture wiring needs a real
 * Playwright Test runner to construct a testInfo and is proven for real by the
 * usecase's Playwright run (see the fixture lane's report). What's unit-testable
 * here is the pass/fail/attach contract runSpecAsTest owns, against a driver
 * session faked at the browser edge exactly like replay/cli.test.mts fakes it.
 * TestInfo is the runner's own value type, not an edge — only `page` is faked.
 */
import { test } from "node:test";
import assert from "node:assert/strict";
import { mkdirSync, mkdtempSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import type { TestInfo } from "@playwright/test";
import type { Page } from "playwright-core";
import type { DriverSession } from "../driver/types.mts";
import type { Spec } from "../spec/types.mts";
import { runSpecAsTest } from "./fixture.mts";

const SPEC: Spec = {
  name: "one-goto",
  startUrl: "http://app.test/",
  steps: [{ id: "st_1", index: 1, action: "goto", target: "http://app.test/" }],
};

/** The same flow with an assertion on the step — the shape a captured spec really
 *  has, and the one whose expectation must reach a failure message. */
const ASSERTING_SPEC: Spec = {
  name: "one-goto-asserted",
  startUrl: "http://app.test/",
  steps: [
    {
      id: "st_1",
      index: 1,
      action: "goto",
      target: "http://app.test/",
      assert: { testId: "order-list", hasText: "Orders" },
    },
  ],
};

interface FakeBehaviour {
  goto?: () => Promise<void>;
}

function fakeSession(behaviour: FakeBehaviour = {}): DriverSession {
  const page = {
    // Runs the callback here, the way a real page runs it there: the step log's clock
    // probe (`() => Date.now()`) must come back a NUMBER or every timestamp in the
    // evidence is NaN. An in-page-only API — the runner's two-animation-frame paint
    // wait after a goto — throws instead, which is the honest answer offline.
    evaluate: async (body: () => unknown) => {
      try {
        return await body();
      } catch {
        return undefined;
      }
    },
    goto: async () => {
      await behaviour.goto?.();
    },
  } as unknown as Page;
  return {
    sessionId: "fake-1",
    page,
    fetchReplay: async () => null,
    close: async () => {},
  };
}

interface FakeAttachment {
  name: string;
  body: string;
  path: string | undefined;
  contentType: string;
}

function fakeTestInfo(outputDir?: string): {
  attachments: FakeAttachment[];
  info: TestInfo;
} {
  const dir = outputDir ?? mkdtempSync(join(tmpdir(), "fixture-test-"));
  const attachments: FakeAttachment[] = [];
  const info = {
    outputPath: (...segments: string[]) => join(dir, ...segments),
    attach: async (
      name: string,
      options: { body?: string | Buffer; path?: string; contentType: string },
    ) => {
      attachments.push({
        name,
        body: options.body?.toString() ?? "",
        path: options.path,
        contentType: options.contentType,
      });
    },
  } as unknown as TestInfo;
  return { attachments, info };
}

test("a passed replay attaches the audit record and the rendered evidence page", async () => {
  const { attachments, info } = fakeTestInfo();
  const result = await runSpecAsTest(SPEC, fakeSession(), info);
  assert.equal(result.outcome, "passed");
  assert.deepEqual(
    attachments.map((a) => [a.name, a.contentType]),
    [
      ["one-goto-evidence", "application/json"],
      ["one-goto-evidence-page", "text/html"],
    ],
  );
  const evidence = JSON.parse(attachments[0].body) as { specName: string };
  assert.equal(evidence.specName, "one-goto");
  // The page is the real renderer's output, not a stub: it carries the spec name
  // and the run's outcome the way a reviewer opens it.
  assert.match(attachments[1].body, /^<!doctype html>/);
  assert.match(attachments[1].body, /one-goto/);
  assert.match(attachments[1].body, /class="outcome passed"/);
});

test("every frame already rendered into the test's output directory is attached, one per file, by its own path", async () => {
  const dir = mkdtempSync(join(tmpdir(), "fixture-frames-"));
  const framesDir = join(dir, "frames");
  mkdirSync(framesDir, { recursive: true });
  writeFileSync(join(framesDir, "step-1.png"), "not-really-a-png");
  writeFileSync(join(framesDir, "step-2.png"), "not-really-a-png-either");
  writeFileSync(join(framesDir, "notes.txt"), "ignored");
  const { attachments, info } = fakeTestInfo(dir);
  await runSpecAsTest(SPEC, fakeSession(), info);
  // The exact attachment set beyond the base evidence pair — filtering by content
  // type first would hide a regression that attached the ignored notes.txt (or
  // attached a frame under the wrong content type); comparing name, contentType,
  // and path together over the full remainder catches either.
  const frames = attachments.slice(2);
  assert.deepEqual(
    frames.map((a) => [a.name, a.contentType, a.path]),
    [
      ["one-goto-frame-step-1", "image/png", join(framesDir, "step-1.png")],
      ["one-goto-frame-step-2", "image/png", join(framesDir, "step-2.png")],
    ],
  );
});

test("a failed step throws with the step target and the step's assertion in the message, and still attaches evidence", async () => {
  const { attachments, info } = fakeTestInfo();
  await assert.rejects(
    runSpecAsTest(
      ASSERTING_SPEC,
      fakeSession({
        goto: async () => {
          throw new Error("net::ERR_CONNECTION_REFUSED");
        },
      }),
      info,
    ),
    (error: Error) => {
      assert.match(
        error.message,
        /step 1 "goto http:\/\/app\.test\/" failed in action phase/,
      );
      // The step's own expectation, serialised into the message — what a reader
      // needs to judge the failure without opening the spec.
      assert.match(
        error.message,
        /expected \{"testId":"order-list","hasText":"Orders"\}/,
      );
      assert.match(error.message, /ERR_CONNECTION_REFUSED/);
      return true;
    },
  );
  assert.equal(
    attachments.length,
    2,
    "the failed run's evidence record and page are still attached",
  );
});

/**
 * The Playwright-fixture sink. `runSpecAsTest` fetches the replay stream and assembles
 * evidence itself, so it is a separate door onto the same artifacts — and a test run's
 * attachments are exactly what a reviewer opens.
 */
test("SINK — a resolved value reaches no attachment this fixture writes", async () => {
  const canary = "canary-secret-value";
  process.env.FIXTURE_SINK_PASSWORD = canary;
  const spec: Spec = {
    name: "referenced-fill",
    startUrl: "http://app.test/",
    steps: [
      {
        id: "st_1",
        index: 1,
        action: "fill",
        target: "#password",
        valueFrom: "env.FIXTURE_SINK_PASSWORD",
        assert: { selector: "#password", visible: true },
      },
    ],
  };
  const session = fakeSession();
  // The page reports the value back in the only two ways it can: the error it throws
  // and the replay stream it emits.
  (
    session.page as unknown as { fill: (s: string, v: string) => Promise<void> }
  ).fill = async (_selector, value) => {
    throw new Error(`Timeout filling "#password" with "${value}"`);
  };
  // A renderable segment (Meta then FullSnapshot) whose incremental event carries what
  // was typed.
  (session as { fetchReplay: () => Promise<unknown> }).fetchReplay =
    async () => [
      { type: 4, timestamp: 1, data: { href: "http://app.test/" } },
      { type: 2, timestamp: 2, data: { node: {} } },
      { type: 3, timestamp: 3, data: { source: 5, text: canary, id: 7 } },
    ];
  const { attachments, info } = fakeTestInfo();
  try {
    await runSpecAsTest(spec, session, info);
  } catch {
    // A failed step fails the test — the attachments are what this asserts on.
  }
  delete process.env.FIXTURE_SINK_PASSWORD;
  assert.ok(attachments.length > 0, "nothing was attached to assert on");
  for (const attachment of attachments) {
    assert.equal(
      (attachment.body ?? "").includes(canary),
      false,
      `attachment ${attachment.name} carries the resolved value`,
    );
  }
});
