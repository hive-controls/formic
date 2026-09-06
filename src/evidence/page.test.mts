/**
 * The evidence page and its frames, against the real probe capture: the page is
 * self-contained, every player renders a document in a real browser, and a
 * non-renderable segment is refused rather than screenshotted as a blank.
 */
import { test } from "node:test";
import assert from "node:assert/strict";
import {
  mkdtempSync,
  readFileSync,
  rmSync,
  statSync,
  writeFileSync,
} from "node:fs";
import { tmpdir } from "node:os";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import { parseReplay, sliceSegments } from "./segment.mts";
import { renderEvidencePage, frameNames, PRODUCT_NAME } from "./page.mts";
import { renderFrames } from "./frames.mts";
import { assembleEvidence, redactPreviewUrl } from "../replay/evidence.mts";
import type { EvidenceRecord, ReplaySegment, StepRecord } from "./types.mts";

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
) as {
  actions: {
    step: number;
    action: string;
    startedAt: number;
    endedAt: number;
  }[];
};
const steps: StepRecord[] = actionLog.actions.map((a) => ({
  id: `st_probe_${a.step}`,
  index: a.step,
  action: a.action,
  startedAt: a.startedAt,
  endedAt: a.endedAt,
  outcome: "ok" as const,
}));
const segments = sliceSegments(events, steps);

const record: EvidenceRecord = {
  decisionId: "dec_test",
  timestamp: "2026-09-01T00:00:00.000Z",
  systemVersion: "harness@test",
  modelVersion: null,
  specName: "probe-2b",
  driver: "solari-browser",
  sessionId: "sess_test",
  outcome: "passed",
  recording: "captured",
  steps,
  segments,
};

test("the page is self-contained: player inlined, segments inlined, no external loads", () => {
  const html = renderEvidencePage({
    specName: "probe-2b",
    outcome: "passed",
    initial: record,
  });
  assert.match(html, /rrwebPlayer/, "player bundle inlined");
  assert.ok(
    !/<script[^>]+src=|<link[^>]+href=/.test(html),
    "no external resources",
  );
  assert.equal(
    (html.match(/<figure class="player"/g) ?? []).length,
    segments.length,
  );
  assert.match(html, new RegExp(PRODUCT_NAME));
  assert.match(html, /dec_test/);
  assert.ok(
    !html.includes("</script>}"),
    "embedded JSON must not terminate the script early",
  );
});

test("the app host audit row renders redacted, and never a raw token", () => {
  const html = renderEvidencePage({
    specName: "probe-2b",
    outcome: "passed",
    initial: {
      ...record,
      host: {
        name: "solari-sandbox",
        kind: "Outside",
        baseUrl:
          "https://sbx-1-8080.preview.getsolari.com/?pt_token=%3Credacted%3E",
      },
    },
  });
  assert.match(html, /app host/);
  assert.match(html, /solari-sandbox \(Outside\)/);
  assert.match(html, /pt_token=%3Credacted%3E/, "the redacted value is shown");
  assert.ok(
    !/pt_token=(?!%3Credacted%3E)[\w-]+/.test(html),
    "no live token value present",
  );
});

test("the app host audit row never prints the preview host or sandbox id in the clear", () => {
  const html = renderEvidencePage({
    specName: "probe-2b",
    outcome: "passed",
    initial: {
      ...record,
      host: {
        name: "solari-sandbox",
        kind: "Outside",
        baseUrl: redactPreviewUrl(
          "https://286e47f0b5635b69e3c5-4173.preview.getsolari.com/?pt_token=LIVE_TOKEN_VALUE",
        ),
      },
    },
  });
  assert.match(html, /app host/);
  assert.match(html, /solari-sandbox \(Outside\)/);
  assert.ok(
    !html.includes("286e47f0b5635b69e3c5"),
    "the sandbox id must not appear on the page",
  );
  assert.ok(
    !html.includes("preview.getsolari.com"),
    "the preview host must not appear on the page",
  );
  assert.match(html, /pt_token=%3Credacted%3E/, "the token stays redacted");
});

test("a run that did not host the app shows the placeholder, not a blank row", () => {
  const html = renderEvidencePage({
    specName: "probe-2b",
    outcome: "passed",
    initial: record,
  });
  assert.match(html, /not hosted by this run/);
});

test("the session row renders whatever the record's OWN sessionId field holds, unmodified — assembleEvidence already scrubbed it upstream, and re-hashing here would desync it from the same record's initial.json/PR body", () => {
  const html = renderEvidencePage({
    specName: "probe-2b",
    outcome: "passed",
    initial: { ...record, sessionId: "session_bbbbbbbbbbbbbbbb" },
  });
  assert.match(html, /session_bbbbbbbbbbbbbbbb/);
});

test("REGRESSION — sink 2: an empty session id renders as the 'none' placeholder, never a raw empty string or a reference minted for nothing", () => {
  const empty = assembleEvidence(
    { specName: "probe-2b", outcome: "passed", steps },
    events,
    { driver: "solari-browser", sessionId: "" },
  );
  assert.equal(empty.sessionId, null, "sink 1: the assembled record itself");
  const html = renderEvidencePage({
    specName: "probe-2b",
    outcome: "passed",
    initial: empty,
  });
  assert.match(html, /session reference<\/th><td>none/);
  assert.ok(
    !html.includes("session_"),
    "no reference was minted for an absent id",
  );
});

test("a heal page shows BEFORE and AFTER players for the attempt, then the initial run", () => {
  const html = renderEvidencePage({
    specName: "probe-2b",
    outcome: "healed",
    initial: record,
    healer: { name: "agent:claude", modelVersion: "agent:claude@2.1.257" },
    diff: ['-     target: "#a"', '+     target: "#b"'],
    attempts: [
      {
        attempt: 1,
        proposal: {
          kind: "rewrite-target",
          stepId: steps[1].id,
          target: "#b",
          reason: "renamed",
        },
        before: segments[1],
        after: segments[1],
        verification: { ...record, modelVersion: "agent:claude@2.1.257" },
      },
    ],
  });
  assert.deepEqual(
    frameNames({
      specName: "probe-2b",
      outcome: "healed",
      initial: record,
      attempts: [
        {
          attempt: 1,
          proposal: { kind: "no-repair", reason: "x" },
          before: segments[0],
          after: segments[0],
          verification: null,
        },
      ],
    }).slice(0, 2),
    ["attempt-1-before", "attempt-1-after"],
  );
  assert.match(html, /BEFORE — the step as it failed/);
  assert.match(html, /AFTER — the repaired step, verified/);
  assert.match(html, /class="add">\+ {5}target: &quot;#b&quot;/);
  assert.match(html, /agent:claude@2\.1\.257/);
});

test("every player renders a document in a real browser and yields a non-empty frame", async () => {
  const dir = mkdtempSync(join(tmpdir(), "e2e-doctor-page-"));
  try {
    const pageFile = join(dir, "index.html");
    writeFileSync(
      pageFile,
      renderEvidencePage({
        specName: "probe-2b",
        outcome: "passed",
        initial: record,
      }),
    );
    const frames = await renderFrames(pageFile, join(dir, "frames"));
    assert.equal(frames.length, segments.length);
    for (const frame of frames) {
      assert.ok(
        statSync(frame.file).size > 1000,
        `${frame.name} is a real PNG`,
      );
    }
    assert.deepEqual(
      frames.map((f) => f.name),
      segments.map((s) => `step-${s.stepIndex}`),
    );
  } finally {
    rmSync(dir, { recursive: true, force: true });
  }
});

test("GUARD — a segment with no DOM produces no frame; the renderer refuses", async () => {
  const dir = mkdtempSync(join(tmpdir(), "e2e-doctor-page-"));
  try {
    const empty = {
      ...segments[0],
      events: segments[0].events.filter((e) => e.type === 4),
    };
    const pageFile = join(dir, "index.html");
    writeFileSync(
      pageFile,
      renderEvidencePage({
        specName: "probe-2b",
        outcome: "passed",
        initial: { ...record, segments: [empty] },
      }),
    );
    await assert.rejects(
      renderFrames(pageFile, join(dir, "frames")),
      /rendered an empty document|__e2edocReady|Timeout/,
    );
  } finally {
    rmSync(dir, { recursive: true, force: true });
  }
});

test("a step's end-state accessibility snapshot renders on the page, escaped and collapsed", () => {
  const withSnapshot: EvidenceRecord = {
    ...record,
    steps: record.steps.map((step, i) =>
      i === 0
        ? {
            ...step,
            ariaSnapshot: '- button "Approve <order>"\n- textbox "Note"',
          }
        : step,
    ),
  };
  const html = renderEvidencePage({
    specName: "probe-2b",
    outcome: "passed",
    initial: withSnapshot,
  });
  assert.match(html, /<details class="snapshot">/, "collapsed by default");
  assert.match(
    html,
    /button &quot;Approve &lt;order&gt;&quot;/,
    "escaped, present",
  );
  assert.ok(!html.includes('"Approve <order>"'), "never raw");
});

test("a step's metrics render compactly, and a missing measurement shows an em dash", () => {
  const withMetrics: EvidenceRecord = {
    ...record,
    steps: record.steps.map((step, i) => {
      if (i === 0) {
        return {
          ...step,
          metrics: {
            ttfb: 12,
            domContentLoaded: 88,
            domComplete: 140,
            firstPaint: 30,
            firstContentfulPaint: 34,
            lcp: 812,
            cls: 0.01,
            longTasksCount: 1,
            longTasksMs: 60,
          },
        };
      }
      if (i === 1) {
        // A non-goto step: nav/paint/LCP absent (null), CLS/long tasks still tracked.
        return {
          ...step,
          metrics: {
            ttfb: null,
            domContentLoaded: null,
            domComplete: null,
            firstPaint: null,
            firstContentfulPaint: null,
            lcp: null,
            cls: 0,
            longTasksCount: 0,
            longTasksMs: 0,
          },
        };
      }
      return step; // no metrics collected at all for this step
    }),
  };
  const html = renderEvidencePage({
    specName: "probe-2b",
    outcome: "passed",
    initial: withMetrics,
  });
  assert.match(html, /<details class="metrics">/, "metrics cell present");
  assert.match(html, /LCP 812ms/, "goto step shows LCP");
  assert.match(html, /CLS 0\.010/, "goto step shows CLS to 3 decimals");
  // The non-goto step's summary carries no LCP figure at all (never a dash mid-summary).
  assert.match(html, /<summary>CLS 0\.000 · LT 0<\/summary>/);
  // A step with no metrics object at all renders a bare em dash, not an empty cell.
  const lastStepIndex = withMetrics.steps[withMetrics.steps.length - 1].index;
  assert.match(html, new RegExp(`<td>${lastStepIndex}</td>.*?<td>—</td>`, "s"));
});

function preHealRecord(stepIndices: number[]): EvidenceRecord {
  const segment = (index: number): ReplaySegment => ({
    stepId: `st_${index}`,
    stepIndex: index,
    action: "click",
    fromTimestamp: index,
    toTimestamp: index + 1,
    events: [],
    preambleCount: 0,
  });
  return {
    decisionId: "dec_pre",
    timestamp: "2026-09-01T00:00:00.000Z",
    systemVersion: "harness@test",
    modelVersion: null,
    specName: "heal-demo",
    driver: "solari-browser",
    sessionId: "sess_test",
    outcome: "failed",
    recording: "captured",
    steps: stepIndices.map((index) => ({
      id: `st_${index}`,
      index,
      action: "click",
      startedAt: index,
      endedAt: index + 1,
      outcome: "ok" as const,
    })),
    segments: stepIndices.map(segment),
  };
}

test("REGRESSION — evidence page never emits a frames/step-N.png href whose frame is not in the shipped set", () => {
  const input = {
    specName: "heal-demo",
    outcome: "healed",
    initial: preHealRecord([1, 2]),
    untested: [
      {
        role: "button",
        name: "Save",
        firstSeenStepId: "st_2",
        firstSeenIndex: 2,
        suggestedTarget: 'role=button[name="Save"]',
        suggestedAction: "click" as const,
      },
      {
        role: "button",
        name: "Export",
        firstSeenStepId: "st_8",
        firstSeenIndex: 8,
        suggestedTarget: 'role=button[name="Export"]',
        suggestedAction: "click" as const,
      },
    ],
    drift: {
      previousDecisionId: "dec_prev",
      previousTimestamp: "2026-08-01T00:00:00.000Z",
      steps: [
        {
          stepId: "st_1",
          index: 1,
          added: [{ role: "button", name: "Help" }],
          removed: [],
        },
        {
          stepId: "st_8",
          index: 8,
          added: [{ role: "link", name: "Docs" }],
          removed: [],
        },
      ],
    },
  };
  const html = renderEvidencePage(input);
  const shipped = new Set(
    frameNames(input).map((name) => `frames/${name}.png`),
  );
  const hrefs = [...html.matchAll(/\bhref="(frames\/step-\d+\.png)"/g)].map(
    (match) => match[1],
  );
  assert.ok(hrefs.length > 0, "at least one in-bundle frame link is emitted");
  for (const href of hrefs) {
    assert.ok(
      shipped.has(href),
      `emitted ${href} must be in the shipped frame set (${[...shipped].join(", ")})`,
    );
  }
  assert.match(html, /href="frames\/step-1\.png"/);
  assert.match(html, /href="frames\/step-2\.png"/);
  assert.match(
    html,
    /no frame — first seen in the healed run, step 8; frames are captured from the pre-heal record/,
  );
});
