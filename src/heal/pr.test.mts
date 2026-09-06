/**
 * The repair PR, offline: every git/gh command it issues, in order, with the files it
 * writes into the worktree, and the body a reviewer would read.
 */
import { test } from "node:test";
import assert from "node:assert/strict";
import {
  existsSync,
  mkdtempSync,
  mkdirSync,
  readFileSync,
  rmSync,
  writeFileSync,
} from "node:fs";
import { tmpdir } from "node:os";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import { loadSpec } from "../spec/parse.mts";
import { applyProposal } from "./proposal.mts";
import { renderSpecDiff } from "./cli.mts";
import type { HealResult } from "./loop.mts";
import type { EvidenceBundle } from "./bundle.mts";
import { openRepairPr, prBody, prTitle, type CommandRunner } from "./pr.mts";
import type { EvidenceRecord } from "../evidence/types.mts";

const SPEC_FILE = join(
  dirname(fileURLToPath(import.meta.url)),
  "..",
  "..",
  "fixtures",
  "specs",
  "approve-an-order.yaml",
);
const original = loadSpec(readFileSync(SPEC_FILE, "utf8"));
const signIn = original.steps[3];

function record(
  outcome: "passed" | "failed",
  modelVersion: string | null,
): EvidenceRecord {
  return {
    decisionId: "dec_abc123",
    timestamp: "2026-09-01T21:00:00.000Z",
    systemVersion: "harness@0.1.0",
    modelVersion,
    specName: original.name,
    driver: "local-playwright",
    sessionId: "local-1",
    outcome,
    recording: "captured",
    steps: [],
    segments: [],
  };
}

const failure = {
  stepId: signIn.id,
  index: 4,
  action: "click" as const,
  target: "#signin-button",
  phase: "action" as const,
  error:
    "page.click: Timeout 2000ms exceeded.\nCall log:\n  - waiting for locator('#signin-button')",
};

const healed: HealResult = {
  outcome: "healed",
  spec: applyProposal(original, {
    kind: "rewrite-target",
    stepId: signIn.id,
    target: 'role=button[name="Sign in"]',
    reason: "the id is gone; the button is still there by role and name",
  }),
  initial: {
    result: { specName: original.name, outcome: "failed", steps: [], failure },
    evidence: record("failed", null),
  },
  attempts: [
    {
      attempt: 1,
      proposal: {
        kind: "rewrite-target",
        stepId: signIn.id,
        target: 'role=button[name="Sign in"]',
        reason: "the id is gone; the button is still there by role and name",
      },
      verification: {
        result: { specName: original.name, outcome: "passed", steps: [] },
        evidence: record("passed", "agent:claude@2.1.257"),
      },
      before: null,
      after: null,
    },
  ],
  healer: { name: "agent:claude", modelVersion: "agent:claude@2.1.257" },
};

function bundleFor(result: HealResult, dir: string): EvidenceBundle {
  mkdirSync(join(dir, "frames"), { recursive: true });
  writeFileSync(join(dir, "index.html"), "<html>evidence</html>");
  writeFileSync(join(dir, "frames", "attempt-1-before.png"), "png-before");
  writeFileSync(join(dir, "frames", "attempt-1-after.png"), "png-after");
  return {
    dir,
    pageFile: join(dir, "index.html"),
    frames: [
      {
        name: "attempt-1-before",
        file: join(dir, "frames", "attempt-1-before.png"),
      },
      {
        name: "attempt-1-after",
        file: join(dir, "frames", "attempt-1-after.png"),
      },
    ],
    files: [
      "index.html",
      "frames/attempt-1-before.png",
      "frames/attempt-1-after.png",
    ],
    diff: renderSpecDiff(original, result.spec),
    untested: [],
    drift: null,
  };
}

test("the PR body: outcome, the failure, the one-line diff, the healer's verbatim reason, frames, audit fields", () => {
  const body = prBody({
    result: healed,
    diff: renderSpecDiff(original, healed.spec),
    bundleDir: "usecases/self-healing-e2e/evidence/dec_abc123",
    rawBase:
      "https://github.com/o/r/blob/e2e-doctor/approve-an-order-dec_abc123",
    frames: ["attempt-1-before", "attempt-1-after"],
  });
  assert.equal(prTitle(healed), "E2E Doctor: heal approve-an-order");
  assert.match(
    body,
    /\*\*Outcome: healed\.\*\* Healer: `agent:claude` \(agent:claude@2\.1\.257\)/,
  );
  assert.match(body, /failed at step 4 \(action phase\)/);
  assert.match(
    body,
    /```diff\n-     target: "#signin-button"\n\+     target: role=button\[name="Sign in"\]\n```/,
  );
  assert.match(
    body,
    /> the id is gone; the button is still there by role and name/,
  );
  assert.match(
    body,
    /!\[before\]\(https:\/\/github\.com\/o\/r\/blob\/e2e-doctor\/approve-an-order-dec_abc123\/usecases\/self-healing-e2e\/evidence\/dec_abc123\/frames\/attempt-1-before\.png\?raw=true\)/,
  );
  assert.match(body, /\| decision id \| `dec_abc123` \|/);
  assert.match(body, /Nothing in this description was written by a model/);
  assert.ok(
    !body.includes("Untested components"),
    "no section when nothing is untested",
  );
});

test("the PR body renders whatever the evidence's OWN sessionId field holds, unmodified — assembleEvidence already scrubbed it upstream, and re-hashing here would desync it from the same record's initial.json/evidence page", () => {
  const body = prBody({
    result: {
      ...healed,
      initial: {
        ...healed.initial,
        evidence: {
          ...healed.initial.evidence,
          sessionId: "session_bbbbbbbbbbbbbbbb",
        },
      },
    },
    diff: [],
    bundleDir: "evidence/dec_abc123",
    rawBase: "https://github.com/o/r/blob/b",
    frames: [],
  });
  assert.match(body, /session_bbbbbbbbbbbbbbbb/);
});

test("untested components render as a proposed-not-applied section with a frame link per component", () => {
  const body = prBody({
    result: healed,
    diff: [],
    bundleDir: "usecases/self-healing-e2e/evidence/dec_abc123",
    rawBase: "https://github.com/o/r/blob/b",
    frames: ["step-5"],
    untested: [
      {
        role: "button",
        name: "Back to orders",
        firstSeenStepId: "st_5",
        firstSeenIndex: 5,
        suggestedTarget: 'role=button[name="Back to orders"]',
        suggestedAction: "click",
      },
    ],
  });
  assert.match(
    body,
    /## Untested components — 1 proposed step\(s\), not applied/,
  );
  assert.ok(
    !body.includes("## UI drift"),
    "no drift section without a previous record",
  );
  const withDrift = prBody({
    result: healed,
    diff: [],
    bundleDir: "b",
    rawBase: "https://x/y",
    frames: ["step-4"],
    drift: {
      previousDecisionId: "dec_prev",
      previousTimestamp: "2026-09-01T00:00:00.000Z",
      steps: [
        {
          stepId: "st_4",
          index: 4,
          added: [{ role: "button", name: "Export CSV" }],
          removed: [],
        },
      ],
    },
  });
  assert.match(
    withDrift,
    /## UI drift since `dec_prev` \(2026-09-01T00:00:00\.000Z\)/,
  );
  assert.match(
    withDrift,
    /\| \[step 4\]\(https:\/\/x\/y\/b\/frames\/step-4\.png\?raw=true\) \| `button` Export CSV \| — \|/,
  );
  assert.match(
    body,
    /\| `button` Back to orders \| \[step 5\]\(https:\/\/github\.com\/o\/r\/blob\/b\/usecases\/self-healing-e2e\/evidence\/dec_abc123\/frames\/step-5\.png\?raw=true\) \| `click` `role=button\[name="Back to orders"\]` \|/,
  );
});

test("needs-human: the body explains accept vs reject and the title says a decision is needed", () => {
  const annotated = applyProposal(original, {
    kind: "propose-assert-change",
    stepId: original.steps[4].id,
    to: { testId: "detail-customer", hasText: "Fabrikam Metals" },
    reason: "the customer on screen differs",
  });
  const result: HealResult = {
    ...healed,
    outcome: "needs-human",
    spec: annotated,
    attempts: [
      {
        attempt: 1,
        proposal: {
          kind: "propose-assert-change",
          stepId: original.steps[4].id,
          to: { testId: "detail-customer", hasText: "Fabrikam Metals" },
          reason: "the customer on screen differs",
        },
        before: null,
        after: null,
      },
    ],
  };
  assert.equal(
    prTitle(result),
    "E2E Doctor: approve-an-order needs a human decision",
  );
  const body = prBody({
    result,
    diff: renderSpecDiff(original, annotated),
    bundleDir: "b",
    rawBase: "r",
    frames: [],
  });
  assert.match(body, /not applied — awaiting your decision/);
  assert.match(body, /\*\*Accept\*\*/);
  assert.match(body, /\*\*Reject\*\*/);
});

test("openRepairPr: worktree, branch, spec + bundle written, commit, push, gh pr create, worktree removed", async () => {
  const root = mkdtempSync(join(tmpdir(), "e2e-doctor-pr-"));
  const bundle = bundleFor(healed, join(root, "bundle-src"));
  const calls: string[] = [];
  const run: CommandRunner = async (command, args, options) => {
    calls.push(`${command} ${args.join(" ")}`);
    if (command === "git" && args[0] === "rev-parse")
      return { stdout: "master\n" };
    if (command === "gh" && args[0] === "repo") return { stdout: "o/r\n" };
    if (command === "git" && args[0] === "worktree" && args[1] === "add") {
      mkdirSync(args[3], { recursive: true });
      return { stdout: "" };
    }
    if (command === "gh" && args[0] === "pr") {
      assert.equal(
        options.cwd,
        join(
          root,
          ".e2e-doctor-worktrees",
          "e2e-doctor__approve-an-order-dec_abc123",
        ),
      );
      return { stdout: "https://github.com/o/r/pull/99\n" };
    }
    return { stdout: "" };
  };
  try {
    const opened = await openRepairPr({
      repoRoot: root,
      specFile: "usecases/self-healing-e2e/specs/approve-an-order.yaml",
      bundleDir: "usecases/self-healing-e2e/evidence/dec_abc123",
      original,
      result: healed,
      bundle,
      run,
    });
    assert.equal(opened.branch, "e2e-doctor/approve-an-order-dec_abc123");
    assert.equal(opened.url, "https://github.com/o/r/pull/99");

    const worktree = join(
      root,
      ".e2e-doctor-worktrees",
      "e2e-doctor__approve-an-order-dec_abc123",
    );
    const writtenSpec = readFileSync(
      join(worktree, "usecases/self-healing-e2e/specs/approve-an-order.yaml"),
      "utf8",
    );
    assert.match(writtenSpec, /role=button\[name="Sign in"\]/);
    assert.ok(
      existsSync(
        join(
          worktree,
          "usecases/self-healing-e2e/evidence/dec_abc123/frames/attempt-1-after.png",
        ),
      ),
    );
    assert.ok(
      existsSync(
        join(
          worktree,
          "usecases/self-healing-e2e/evidence/dec_abc123/index.html",
        ),
      ),
    );

    const sequence = calls.map((c) => c.split(" ").slice(0, 2).join(" "));
    assert.deepEqual(sequence, [
      "git rev-parse",
      "gh repo",
      "git worktree",
      "git checkout",
      "git add",
      "git commit",
      "git push",
      "gh pr",
      "git worktree",
    ]);
    assert.ok(calls[calls.length - 1].startsWith("git worktree remove"));
    // The PR body is not part of the change and must not sit in the worktree, where a
    // repo's own pre-commit lint would see it (measured on the first dogfood run).
    assert.ok(
      !existsSync(join(worktree, ".e2e-doctor-pr-body.md")),
      "no body file in the worktree",
    );
    const prCreate = calls.find((c) => c.includes("pr create"))!;
    assert.match(prCreate, /--body-file \S*e2e-doctor-pr-\S*pr-body\.md/);
    // The PR must contain the repair and nothing else: branch from the BASE, never
    // from whatever the working checkout happens to have checked out.
    assert.ok(
      calls.some((c) => /^git worktree add --detach \S+ master$/.test(c)),
      "worktree starts from the base ref",
    );
    assert.ok(
      calls.some((c) =>
        c.startsWith(
          "git push -q -u origin e2e-doctor/approve-an-order-dec_abc123",
        ),
      ),
    );
    assert.ok(
      calls.some((c) =>
        c.includes(
          "pr create --base master --head e2e-doctor/approve-an-order-dec_abc123",
        ),
      ),
    );
  } finally {
    rmSync(root, { recursive: true, force: true });
  }
});

test("the PR body carries no terminal escape codes from a coloured runner error", () => {
  const coloured =
    "\u001b[2mexpect(\u001b[22m\u001b[31mlocator\u001b[39m\u001b[2m).\u001b[22mtoBeVisible\u001b[2m(\u001b[22m\u001b[2m)\u001b[22m failed\n" +
    "Locator:  getByTestId('confirmation')\n" +
    "Expected: visible";
  const coloured_result = {
    ...healed,
    initial: {
      ...healed.initial,
      result: {
        ...healed.initial.result,
        failure: { index: 7, phase: "assert", error: coloured },
      },
    },
  } as typeof healed;
  const body = prBody({
    result: coloured_result,
    diff: renderSpecDiff(original, healed.spec),
    bundleDir: "usecases/self-healing-e2e/evidence/dec_abc123",
    rawBase:
      "https://github.com/o/r/blob/e2e-doctor/approve-an-order-dec_abc123",
    frames: [],
  });
  assert.ok(
    !body.includes("\u001b["),
    "the PR body must not contain ANSI escape sequences",
  );
  assert.match(body, /expect\(locator\)\.toBeVisible\(\) failed/);
});

test("REGRESSION — PR body never emits a frames/step-N.png URL whose frame is not in the shipped set", () => {
  const frames = ["step-1", "step-2", "attempt-1-before"];
  const body = prBody({
    result: healed,
    diff: [],
    bundleDir: "evidence/dec_abc123",
    rawBase: "https://github.com/o/r/blob/b",
    frames,
    untested: [
      {
        role: "button",
        name: "Save",
        firstSeenStepId: "st_2",
        firstSeenIndex: 2,
        suggestedTarget: 'role=button[name="Save"]',
        suggestedAction: "click",
      },
      {
        role: "button",
        name: "Export",
        firstSeenStepId: "st_8",
        firstSeenIndex: 8,
        suggestedTarget: 'role=button[name="Export"]',
        suggestedAction: "click",
      },
    ],
    drift: {
      previousDecisionId: "dec_prev",
      previousTimestamp: "2026-09-01T00:00:00.000Z",
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
  });
  const shipped = new Set(frames.map((name) => `frames/${name}.png`));
  const emitted = [...body.matchAll(/frames\/step-\d+\.png/g)].map(
    (match) => match[0],
  );
  assert.ok(
    emitted.length > 0,
    "at least one in-bundle step frame URL is emitted",
  );
  for (const path of emitted) {
    assert.ok(
      shipped.has(path),
      `emitted ${path} must be in the shipped frame set (${[...shipped].join(", ")})`,
    );
  }
  assert.match(body, /frames\/step-1\.png/);
  assert.match(body, /frames\/step-2\.png/);
  assert.match(
    body,
    /no frame — first seen in the healed run, step 8; frames are captured from the pre-heal record/,
  );
});
