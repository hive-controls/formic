/** The evidence bundle's file contract, on the real probe stream (frames included). */
import { test } from "node:test";
import assert from "node:assert/strict";
import {
  existsSync,
  mkdirSync,
  mkdtempSync,
  readFileSync,
  rmSync,
  writeFileSync,
} from "node:fs";
import { tmpdir } from "node:os";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import { parseReplay, sliceSegments } from "../evidence/segment.mts";
import type { EvidenceRecord, StepRecord } from "../evidence/types.mts";
import { assembleEvidence, sessionReference } from "../replay/evidence.mts";
import { loadSpec } from "../spec/parse.mts";
import { writeEvidenceBundle } from "./bundle.mts";
import { scrubHealResult } from "./cli.mts";
import type { HealResult } from "./loop.mts";
import { prBody } from "./pr.mts";
import { applyProposal } from "./proposal.mts";

const FIXTURES = join(
  dirname(fileURLToPath(import.meta.url)),
  "..",
  "..",
  "fixtures",
);
const events = parseReplay(
  readFileSync(join(FIXTURES, "probe-2b-replay.raw"), "utf8"),
);
const actions = (
  JSON.parse(
    readFileSync(join(FIXTURES, "probe-2b-action-log.json"), "utf8"),
  ) as {
    actions: {
      step: number;
      action: string;
      startedAt: number;
      endedAt: number;
    }[];
  }
).actions;
const spec = loadSpec(
  readFileSync(join(FIXTURES, "specs", "approve-an-order.yaml"), "utf8"),
);
// Address the probe's 4 segments by the spec's first 4 step ids so the bundle is coherent.
const steps: StepRecord[] = actions.map((a, i) => ({
  id: spec.steps[i].id,
  index: a.step,
  action: a.action,
  startedAt: a.startedAt,
  endedAt: a.endedAt,
  outcome: "ok" as const,
}));
const segments = sliceSegments(events, steps);
const evidence = (
  outcome: "passed" | "failed",
  modelVersion: string | null,
): EvidenceRecord => ({
  decisionId: "dec_bundle",
  timestamp: "2026-09-01T21:00:00.000Z",
  systemVersion: "harness@test",
  modelVersion,
  specName: spec.name,
  driver: "solari-browser",
  // Already the shape assembleEvidence would have produced — every heal run goes
  // through it before a bundle ever sees the record, so a fixture here represents
  // that, not a raw backend id.
  sessionId: "session_bbbbbbbbbbbbbbbb",
  outcome,
  recording: "captured",
  steps,
  segments,
});

test("a healed bundle: page, initial + attempt JSON, and a rendered BEFORE/AFTER frame pair", async () => {
  const proposal = {
    kind: "rewrite-target" as const,
    stepId: spec.steps[3].id,
    target: "#submit-login",
    reason: "renamed",
  };
  const result: HealResult = {
    outcome: "healed",
    spec: applyProposal(spec, proposal),
    initial: {
      result: {
        specName: spec.name,
        outcome: "failed",
        steps,
        failure: {
          stepId: spec.steps[3].id,
          index: 4,
          action: "click",
          phase: "action",
          error: "boom",
        },
      },
      evidence: evidence("failed", null),
    },
    attempts: [
      {
        attempt: 1,
        proposal,
        verification: {
          result: { specName: spec.name, outcome: "passed", steps },
          evidence: evidence("passed", "scripted"),
        },
        before: segments[3],
        after: segments[3],
      },
    ],
    healer: { name: "scripted", modelVersion: "scripted" },
  };
  const dir = mkdtempSync(join(tmpdir(), "e2e-doctor-bundle-"));
  try {
    const bundle = await writeEvidenceBundle(spec, result, dir);
    for (const file of [
      "index.html",
      "initial.json",
      "attempt-1.json",
      "proposals.json",
      "frames/attempt-1-before.png",
      "frames/attempt-1-after.png",
    ]) {
      assert.ok(existsSync(join(dir, file)), `${file} written`);
      assert.ok(bundle.files.includes(file), `${file} listed`);
    }
    assert.deepEqual(bundle.diff, [
      '-     target: "#signin-button"',
      '+     target: "#submit-login"',
    ]);
    assert.equal(
      bundle.frames.length,
      2 + segments.length,
      "attempt pair + one per initial step",
    );
    const attempt = JSON.parse(
      readFileSync(join(dir, "attempt-1.json"), "utf8"),
    ) as { verification: { modelVersion: string } };
    assert.equal(attempt.verification.modelVersion, "scripted");
    for (const file of ["initial.json", "attempt-1.json", "index.html"]) {
      const content = readFileSync(join(dir, file), "utf8");
      assert.match(content, /session_bbbbbbbbbbbbbbbb/);
    }
  } finally {
    rmSync(dir, { recursive: true, force: true });
  }
});

test("cross-sink equality: the SAME raw backend session id yields the SAME reference in initial.json, the rendered evidence page, and the PR body — computed once, by assembleEvidence, never re-hashed downstream", async () => {
  const rawSessionId = "backend.example.test:cross-sink-check";
  const expectedReference = sessionReference(rawSessionId);
  const initialEvidence = assembleEvidence(
    { specName: spec.name, outcome: "passed", steps },
    events,
    { driver: "solari-browser", sessionId: rawSessionId },
  );
  const result: HealResult = {
    outcome: "passed",
    spec,
    initial: {
      result: { specName: spec.name, outcome: "passed", steps },
      evidence: initialEvidence,
    },
    attempts: [],
    healer: { name: "scripted", modelVersion: "scripted" },
  };
  const dir = mkdtempSync(join(tmpdir(), "e2e-doctor-bundle-cross-sink-"));
  try {
    await writeEvidenceBundle(spec, result, dir, { renderFrames: false });
    const initialJson = JSON.parse(
      readFileSync(join(dir, "initial.json"), "utf8"),
    ) as EvidenceRecord;
    const html = readFileSync(join(dir, "index.html"), "utf8");
    const body = prBody({
      result,
      diff: [],
      bundleDir: "evidence/dec_cross_sink",
      rawBase: "https://github.com/o/r/blob/b",
      frames: [],
    });
    assert.equal(
      initialJson.sessionId,
      expectedReference,
      "bundle's initial.json",
    );
    assert.ok(html.includes(expectedReference!), "the rendered evidence page");
    assert.ok(body.includes(expectedReference!), "the PR body");
  } finally {
    rmSync(dir, { recursive: true, force: true });
  }
});

test("MEASURED — a frames directory left by an earlier write is cleared, so a failed render cannot leave stale frames beside fresh JSON", async () => {
  // Seen on a Solari run: renderFrames refused step 1, the writer threw, and the nine
  // frames from the previous run stayed in place next to the new index.html — a bundle
  // that lied. The frames directory is generated wholesale; it starts empty every write.
  const result: HealResult = {
    outcome: "passed",
    spec,
    initial: {
      result: { specName: spec.name, outcome: "passed", steps },
      evidence: evidence("passed", null),
    },
    attempts: [],
    healer: { name: "scripted", modelVersion: "scripted" },
  };
  const dir = mkdtempSync(join(tmpdir(), "e2e-doctor-bundle-stale-"));
  try {
    mkdirSync(join(dir, "frames"), { recursive: true });
    writeFileSync(join(dir, "frames", "step-9.png"), "stale");
    await writeEvidenceBundle(spec, result, dir, { renderFrames: false });
    assert.ok(
      !existsSync(join(dir, "frames", "step-9.png")),
      "the stale frame must be gone",
    );
  } finally {
    rmSync(dir, { recursive: true, force: true });
  }
});

test("a scrubbed result: the host's tokened URL inside segments, the failure text and the reason never reaches initial.json, attempt JSON or the page", async () => {
  // A Solari-hosted run replays against `https://<id>-<port>.preview.getsolari.com?pt_token=…`;
  // rrweb events carry that href verbatim. The bundle is what the repair PR commits.
  const base =
    "https://f3047429cdb29b8d522d-4173.preview.getsolari.com?pt_token=FAKE_TOKEN";
  const hostedHref = `${new URL(base).origin}/?pt_token=FAKE_TOKEN`;
  const tokened = segments.map((segment) => ({
    ...segment,
    events: segment.events.map((event) =>
      event.type === 4
        ? { ...event, data: { ...(event.data as object), href: hostedHref } }
        : event,
    ),
  }));
  const withHost = (record: EvidenceRecord): EvidenceRecord => ({
    ...record,
    segments: tokened,
  });
  const proposal = {
    kind: "rewrite-target" as const,
    stepId: spec.steps[3].id,
    target: "#submit-login",
    reason: `renamed; seen at ${hostedHref}`,
  };
  const result: HealResult = {
    outcome: "healed",
    spec: applyProposal(spec, proposal),
    initial: {
      result: {
        specName: spec.name,
        outcome: "failed",
        steps,
        failure: {
          stepId: spec.steps[3].id,
          index: 4,
          action: "click",
          phase: "action",
          error: `timeout at ${hostedHref}`,
        },
      },
      evidence: withHost(evidence("failed", null)),
    },
    attempts: [
      {
        attempt: 1,
        proposal,
        verification: {
          result: { specName: spec.name, outcome: "passed", steps },
          evidence: withHost(evidence("passed", "scripted")),
        },
        before: tokened[3],
        after: tokened[3],
      },
    ],
    healer: { name: "scripted", modelVersion: "scripted" },
  };
  const dir = mkdtempSync(join(tmpdir(), "e2e-doctor-bundle-scrub-"));
  try {
    const scrubbed = scrubHealResult(result, {
      baseUrl: base,
      originalOrigin: "http://127.0.0.1:4173",
    });
    await writeEvidenceBundle(spec, scrubbed, dir, { renderFrames: false });
    for (const file of ["initial.json", "attempt-1.json", "index.html"]) {
      const content = readFileSync(join(dir, file), "utf8");
      assert.equal(
        (content.match(/pt_token/g) ?? []).length,
        0,
        `${file} carries no token`,
      );
      assert.ok(
        !content.includes("preview.getsolari.com"),
        `${file} carries no preview host`,
      );
    }
    const initial = JSON.parse(
      readFileSync(join(dir, "initial.json"), "utf8"),
    ) as EvidenceRecord;
    const meta = initial.segments[0].events.find((e) => e.type === 4);
    assert.equal(
      (meta?.data as { href: string }).href,
      "http://127.0.0.1:4173/",
      "the meta href reads as the captured origin",
    );
  } finally {
    rmSync(dir, { recursive: true, force: true });
  }
});

test("a BARE hosted-preview hostname (no scheme, no token) planted in the failure text, a replay event, and the healer's own reason never reaches the bundle or the PR body", async () => {
  // unrebaseText (scrubHealResult's existing pass) only rewrites a FULL matching
  // URL; a bare mention of just the hostname — inside a Playwright timeout message,
  // a healer's own reasoning, or an rrweb href attribute — is the leak this proves
  // is closed, across every one of the writers named in the review.
  const hostname = "f3047429cdb29b8d522d-4173.preview.getsolari.com";
  const bareEvents = segments.map((segment) => ({
    ...segment,
    events: segment.events.map((event) =>
      event.type === 4
        ? {
            ...event,
            data: { ...(event.data as object), note: `served by ${hostname}` },
          }
        : event,
    ),
  }));
  const withBareHost = (record: EvidenceRecord): EvidenceRecord => ({
    ...record,
    segments: bareEvents,
  });
  const proposal = {
    kind: "rewrite-target" as const,
    stepId: spec.steps[3].id,
    target: "#submit-login",
    reason: `renamed; last seen serving from ${hostname}`,
  };
  const result: HealResult = {
    outcome: "healed",
    spec: applyProposal(spec, proposal),
    initial: {
      result: {
        specName: spec.name,
        outcome: "failed",
        steps,
        failure: {
          stepId: spec.steps[3].id,
          index: 4,
          action: "click",
          phase: "action",
          error: `dial tcp ${hostname}:443: connection reset`,
        },
      },
      evidence: withBareHost(evidence("failed", null)),
    },
    attempts: [
      {
        attempt: 1,
        proposal,
        verification: {
          result: { specName: spec.name, outcome: "passed", steps },
          evidence: withBareHost(evidence("passed", "scripted")),
        },
        before: bareEvents[3],
        after: bareEvents[3],
      },
    ],
    healer: { name: "scripted", modelVersion: "scripted" },
  };
  const dir = mkdtempSync(join(tmpdir(), "e2e-doctor-bundle-bare-host-"));
  try {
    const scrubbed = scrubHealResult(result, {
      baseUrl: `https://${hostname}/`,
      originalOrigin: "http://127.0.0.1:4173",
    });
    // Whole-tree, not per-field: the planted hostname is gone from failure text
    // (steps[].error's sibling), a replay event, AND the healer's own reason.
    const serialized = JSON.stringify(scrubbed);
    assert.ok(
      !serialized.includes(hostname),
      "no field of the scrubbed result carries the bare hostname",
    );
    await writeEvidenceBundle(spec, scrubbed, dir, { renderFrames: false });
    for (const file of ["initial.json", "attempt-1.json", "index.html"]) {
      const content = readFileSync(join(dir, file), "utf8");
      assert.ok(
        !content.includes(hostname),
        `${file} carries no bare hostname`,
      );
    }
    const body = prBody({
      result: scrubbed,
      diff: [],
      bundleDir: "evidence/dec_bundle",
      rawBase: "https://github.com/o/r/blob/b",
      frames: [],
    });
    assert.ok(!body.includes(hostname), "the PR body carries no bare hostname");
  } finally {
    rmSync(dir, { recursive: true, force: true });
  }
});

test("coverage proposals come from the GREEN record: the verified repair's replay, listed in proposals.json and on the page", async () => {
  const withCoverage = (record: EvidenceRecord): EvidenceRecord => ({
    ...record,
    steps: record.steps.map((s, i) => ({
      ...s,
      targetNode: i === 1 ? '- button "Sign in"' : undefined,
      ariaSnapshot:
        i === 1
          ? '- button "Sign in"\n- list:\n  - button "SO-4471 Northwind"\n  - link "Help"'
          : undefined,
    })),
  });
  const result: HealResult = {
    outcome: "healed",
    spec,
    initial: {
      result: {
        specName: spec.name,
        outcome: "failed",
        steps,
        failure: {
          stepId: spec.steps[3].id,
          index: 4,
          action: "click",
          phase: "action",
          error: "boom",
        },
      },
      evidence: evidence("failed", null),
    },
    attempts: [
      {
        attempt: 1,
        proposal: {
          kind: "rewrite-target",
          stepId: spec.steps[3].id,
          target: "#submit-login",
          reason: "renamed",
        },
        verification: {
          result: { specName: spec.name, outcome: "passed", steps },
          evidence: withCoverage(evidence("passed", "scripted")),
        },
        before: segments[3],
        after: segments[3],
      },
    ],
    healer: { name: "scripted", modelVersion: "scripted" },
  };
  const dir = mkdtempSync(join(tmpdir(), "e2e-doctor-bundle-coverage-"));
  try {
    const previous: EvidenceRecord = {
      ...withCoverage(evidence("passed", null)),
      decisionId: "dec_prev",
      timestamp: "2026-09-01T00:00:00.000Z",
    };
    previous.steps = previous.steps.map((s, i) =>
      i === 1
        ? {
            ...s,
            ariaSnapshot: '- button "Sign in"\n- link "Help"\n- link "Old"',
          }
        : s,
    );
    const bundle = await writeEvidenceBundle(spec, result, dir, {
      renderFrames: false,
      previous,
    });
    assert.deepEqual(
      bundle.drift?.steps.map((d) => [
        d.index,
        d.added.map((n) => n.name),
        d.removed.map((n) => n.name),
      ]),
      [[steps[1].index, ["SO-4471 Northwind"], ["Old"]]],
      "drift vs the previous record, per shared step id",
    );
    assert.deepEqual(
      bundle.untested.map((c) => `${c.role} ${c.name}`),
      ["button SO-4471 Northwind", "link Help"],
      "touched button subtracted; the initial (failed) record is not the source",
    );
    const proposals = JSON.parse(
      readFileSync(join(dir, "proposals.json"), "utf8"),
    ) as { untestedComponents: { suggestedTarget: string }[] };
    assert.equal(
      proposals.untestedComponents[0].suggestedTarget,
      'role=button[name="SO-4471 Northwind"]',
    );
    const html = readFileSync(join(dir, "index.html"), "utf8");
    assert.match(html, /UI drift <span class="verdict">since dec_prev/);
    assert.match(html, /Untested components/);
    const proposals2 = JSON.parse(
      readFileSync(join(dir, "proposals.json"), "utf8"),
    ) as { uiDrift: { previousDecisionId: string } };
    assert.equal(proposals2.uiDrift.previousDecisionId, "dec_prev");
    assert.match(html, /role=button\[name=&quot;SO-4471 Northwind&quot;\]/);
  } finally {
    rmSync(dir, { recursive: true, force: true });
  }
});

/**
 * The two sinks a resolved secret reaches through a HEAL run, canaried the same way the
 * session-id cross-sink test above canaries an identity: the bundle's own files and the
 * PR body a reviewer reads. Both are built from the record assembly produced, so both
 * are protected by the ONE list travelling on the replay result — or by neither.
 */
test("SINK — a resolved value reaches neither the bundle's files nor the PR body", async () => {
  const canary = "canary-secret-value";
  const secrets = [
    { value: canary, reference: "<redacted:env.SIGN_IN_PASSWORD>" },
  ];
  const failing: StepRecord[] = steps.map((step, index) =>
    index === 3
      ? {
          ...step,
          outcome: "failed" as const,
          error: `Timeout filling "#password" with "${canary}"`,
          ariaSnapshot: `- textbox "Password": ${canary}`,
        }
      : step,
  );
  const initialEvidence = assembleEvidence(
    {
      specName: spec.name,
      outcome: "failed",
      steps: failing,
      failure: {
        stepId: spec.steps[3].id,
        index: 4,
        action: "click",
        phase: "action",
        error: `Timeout filling "#password" with "${canary}"`,
      },
      resolvedSecrets: secrets,
    },
    events,
    { driver: "solari-browser", sessionId: "sess-canary", secrets },
  );
  // A healer that quotes the value back into its reasoning is a SECOND door onto the
  // same leak; it is closed where the reasoning is produced (the heal loop), and
  // canaried there. What this asserts is the evidence-derived half: every file the
  // bundle writes from the record, and the PR body built from the same result.
  const proposal = {
    kind: "no-repair" as const,
    reason: "the field would not accept the value",
  };
  const result: HealResult = {
    outcome: "unhealed",
    spec,
    initial: {
      result: {
        specName: spec.name,
        outcome: "failed",
        steps: failing,
        resolvedSecrets: secrets,
      },
      evidence: initialEvidence,
    },
    attempts: [{ attempt: 1, proposal, before: segments[3], after: null }],
    healer: { name: "scripted", modelVersion: "scripted" },
  };
  const dir = mkdtempSync(join(tmpdir(), "e2e-doctor-bundle-secret-"));
  try {
    await writeEvidenceBundle(spec, result, dir, { renderFrames: false });
    for (const file of ["initial.json", "attempt-1.json", "index.html"]) {
      assert.equal(
        readFileSync(join(dir, file), "utf8").includes(canary),
        false,
        `the bundle's ${file} carries the resolved value`,
      );
    }
    const body = prBody({
      result,
      diff: [],
      bundleDir: "evidence/dec_secret",
      rawBase: "https://github.com/o/r/blob/b",
      frames: [],
    });
    assert.equal(body.includes(canary), false, "the PR body");
  } finally {
    rmSync(dir, { recursive: true, force: true });
  }
});
