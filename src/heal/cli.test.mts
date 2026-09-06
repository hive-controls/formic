/** The heal CLI's pure parts: exit-code mapping, the spec diff, healer selection. */
import { test } from "node:test";
import assert from "node:assert/strict";
import { mkdtempSync, readFileSync, rmSync } from "node:fs";
import { tmpdir } from "node:os";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import type { Page } from "playwright-core";
import type { Driver, DriverSession } from "../driver/types.mts";
import { loadSpec } from "../spec/parse.mts";
import type { Spec } from "../spec/types.mts";
import type { StatusEvent } from "../status/stream.mts";
import { exitCodeFor, renderSpecDiff, runHealCli } from "./cli.mts";
import type { HealResult } from "./loop.mts";
import { healerFromEnv } from "./healers/from-env.mts";
import { applyProposal } from "./proposal.mts";
import { scriptedHealer } from "./scripted.mts";

const spec = loadSpec(
  readFileSync(
    join(
      dirname(fileURLToPath(import.meta.url)),
      "..",
      "..",
      "fixtures",
      "specs",
      "approve-an-order.yaml",
    ),
    "utf8",
  ),
);

test("exit codes: 0 only when nothing is left for a human", () => {
  assert.equal(exitCodeFor("passed"), 0);
  assert.equal(exitCodeFor("healed"), 0);
  assert.equal(exitCodeFor("needs-human"), 1);
  assert.equal(exitCodeFor("unhealed"), 1);
  assert.equal(
    exitCodeFor("preview-unavailable"),
    3,
    "an environment fault is distinct from a heal verdict",
  );
});

test("a target rewrite renders as one removed and one added line", () => {
  const repaired = applyProposal(spec, {
    kind: "rewrite-target",
    stepId: spec.steps[3].id,
    target: "#submit-login",
    reason: "renamed",
  });
  assert.deepEqual(
    renderSpecDiff(spec, repaired),
    ['- target: "#signin-button"', '+ target: "#submit-login"'].map((l) =>
      l.replace(/^(.) /, "$1     "),
    ),
  );
});

test("an inserted step renders as added lines only, plus the index renumbering", () => {
  const repaired = applyProposal(
    spec,
    {
      kind: "insert-step",
      beforeStepId: spec.steps[6].id,
      step: {
        action: "click",
        target: "#approve-button",
        assert: { selector: "#confirm-banner", visible: true },
      },
      reason: "interstitial",
    },
    () => "st_new",
  );
  const diff = renderSpecDiff(spec, repaired);
  assert.ok(diff.some((l) => l.startsWith("+") && l.includes("st_new")));
  assert.ok(diff.some((l) => l.includes("#confirm-banner")));
  // The only removed line is the renumbered index of the step that moved down.
  assert.deepEqual(
    diff.filter((l) => l.startsWith("-")),
    ["-     index: 7"],
  );
});

test("a recorded assertion proposal renders as added lines only — never as a removed step", () => {
  // Measured on the first live class-4 heal: a greedy diff printed step 7 as deleted.
  const annotated = applyProposal(spec, {
    kind: "propose-assert-change",
    stepId: spec.steps[4].id,
    to: { testId: "detail-customer", hasText: "Fabrikam Metals" },
    reason: "data differs",
  });
  const diff = renderSpecDiff(spec, annotated);
  assert.ok(diff.length > 0);
  assert.deepEqual(
    diff.filter((l) => l.startsWith("-")),
    [],
    "nothing is removed by a proposal",
  );
  assert.ok(diff.some((l) => l.includes("proposedAssertChange")));
  assert.ok(diff.some((l) => l.includes("Fabrikam Metals")));
});

const HOST_ORIGIN = "https://sbx.example";
const ORIGINAL_ORIGIN = "http://app.test";

const rebasedSpec: Spec = {
  name: "rebase-test",
  startUrl: `${HOST_ORIGIN}/`,
  steps: [
    { id: "st_1", index: 1, action: "goto", target: `${HOST_ORIGIN}/` },
    // waitFor is exempt from the state-changing-action assert requirement, so this stays a
    // plain rewrite-target repair with no real-browser assertion evaluation needed.
    { id: "st_2", index: 2, action: "waitFor", target: "#old" },
  ],
};

/** waitFor fails for the pristine target, passes once the healer's proposal rewrites it. */
function fakeDriver(): Driver {
  return {
    name: "fake",
    canRecord: false,
    async open(): Promise<DriverSession> {
      const page = {
        evaluate: async () => undefined,
        goto: async () => undefined,
        url: () => "https://sbx.example/",
        locator: (target: string) => ({
          ariaSnapshot: async () => "(fake snapshot)",
          waitFor: async () => {
            if (target === "#old")
              throw new Error(
                `element not found: #old at ${HOST_ORIGIN}/?pt_token=SECRET.`,
              );
          },
        }),
      } as unknown as Page;
      return {
        sessionId: "fake-1",
        page,
        async fetchReplay() {
          return null;
        },
        close: async () => {},
      };
    },
  };
}

test("--app: the original origin is restored before writeSpec and in the diff base, and the result handed on (failure text included) carries no host URL or token", async () => {
  const written: string[] = [];
  const logs: string[] = [];
  let handedOn: HealResult | undefined;
  const code = await runHealCli({
    spec: rebasedSpec,
    driver: fakeDriver(),
    healer: scriptedHealer([
      {
        kind: "rewrite-target",
        stepId: "st_2",
        target: "#new",
        reason: "renamed",
      },
    ]),
    io: { log: (l) => logs.push(l), error: (l) => logs.push(l) },
    writeSpec: (yaml) => written.push(yaml),
    options: {
      host: {
        name: "solari-sandbox",
        kind: "Outside",
        baseUrl: `${HOST_ORIGIN}/?pt_token=<redacted>`,
      },
      // sbx.example is a fake non-loopback host: stub the network edge or the
      // default liveness probe would really fetch it.
      previewLiveness: async () => ({ alive: true, status: 200 }),
    },
    appHost: {
      baseUrl: `${HOST_ORIGIN}/?pt_token=SECRET`,
      originalOrigin: ORIGINAL_ORIGIN,
    },
    afterHeal: async (result) => {
      handedOn = result;
    },
  });
  assert.equal(code, 0, logs.join("\n"));
  assert.equal(
    written.length,
    1,
    "healed with a diff -> the spec is written back",
  );
  assert.match(written[0], /http:\/\/app\.test\//);
  assert.ok(
    !written[0].includes(HOST_ORIGIN),
    "the yaml handed to writeSpec never carries the host origin",
  );
  assert.ok(
    !logs.some((l) => l.includes(HOST_ORIGIN)),
    "no printed diff line names the host origin",
  );
  assert.equal(
    handedOn?.initial.evidence.host?.baseUrl,
    `${HOST_ORIGIN}/?pt_token=<redacted>`,
    "the audit's app-host row keeps its (already redacted) host URL — it says WHERE the app ran",
  );
  // Only the app-host rows may carry the (redacted) host URL; blank them before the check.
  const withoutHostRows = {
    ...handedOn!,
    initial: {
      ...handedOn!.initial,
      evidence: { ...handedOn!.initial.evidence, host: null },
    },
    attempts: handedOn!.attempts.map((attempt) => ({
      ...attempt,
      verification: attempt.verification && {
        ...attempt.verification,
        evidence: { ...attempt.verification.evidence, host: null },
      },
    })),
  };
  const serialized = JSON.stringify(withoutHostRows);
  assert.ok(
    !serialized.includes("SECRET"),
    "no token reaches afterHeal (the PR body reads this result)",
  );
  assert.ok(
    !serialized.includes(HOST_ORIGIN),
    "no host origin reaches afterHeal",
  );
  assert.equal(
    handedOn?.initial.result.failure?.error,
    `element not found: #old at ${ORIGINAL_ORIGIN}/.`,
    "the failure text is restored with its punctuation intact",
  );
});

test("healerFromEnv: defaults, agent adapters, and a clear error for a typo", () => {
  const api = healerFromEnv({});
  assert.equal(api.name, "openai-compatible");
  assert.equal(api.modelVersion, "claude-sonnet-5");

  const agent = healerFromEnv({ FORMIC_HEALER: "agent:claude" });
  assert.equal(agent.name, "agent:claude");

  const custom = healerFromEnv({
    FORMIC_HEALER: "agent:custom",
    FORMIC_HEALER_AGENT_CMD: "mycli --headless {prompt}",
  });
  assert.equal(custom.name, "agent:mycli");

  assert.throws(
    () => healerFromEnv({ FORMIC_HEALER: "agent:cluade" }),
    /unsupported FORMIC_HEALER "agent:cluade"/,
  );
  assert.throws(
    () => healerFromEnv({ FORMIC_HEALER: "openai" }),
    /unsupported FORMIC_HEALER "openai"/,
  );
});

test("the proposals hint is a function of the outcome: nothing to point at when --pr alone ran green", async () => {
  const logs: string[] = [];
  const hints: (string | undefined)[] = [];
  await runHealCli({
    spec: rebasedSpec,
    driver: fakeDriver(),
    healer: scriptedHealer([
      {
        kind: "rewrite-target",
        stepId: "st_2",
        target: "#new",
        reason: "renamed",
      },
    ]),
    io: { log: (l) => logs.push(l), error: (l) => logs.push(l) },
    proposalsHint: (result) => {
      const hint = result.outcome === "passed" ? undefined : "the repair PR";
      hints.push(hint);
      return hint;
    },
  });
  assert.equal(hints.length, 1, "the hint is asked once, with the result");
});

test("UNHEALED stdout never carries the pt_token value or the preview host; loopback stays visible", async () => {
  const logs: string[] = [];
  const code = await runHealCli({
    spec: rebasedSpec,
    driver: fakeDriver(),
    healer: scriptedHealer([
      {
        kind: "no-repair",
        reason:
          "the preview environment is not serving the application at all: " +
          "https://286e47f0b5635b69e3c5-4173.preview.getsolari.com/?pt_token=LIVE_TOKEN_VALUE " +
          "returned 404; compare http://127.0.0.1:8080/ locally",
      },
    ]),
    io: { log: (l) => logs.push(l), error: (l) => logs.push(l) },
    options: {
      previewLiveness: async () => ({ alive: true, status: 200 }),
    },
  });
  assert.equal(code, 1, logs.join("\n"));
  const printed = logs.join("\n");
  assert.ok(!printed.includes("LIVE_TOKEN_VALUE"), "token value never prints");
  assert.ok(
    !printed.includes("286e47f0b5635b69e3c5"),
    "sandbox id never prints",
  );
  assert.ok(
    !printed.includes("preview.getsolari.com"),
    "preview host never prints",
  );
  assert.ok(
    !printed.includes("SECRET"),
    "the initial failure's token never prints",
  );
  assert.ok(
    printed.includes("127.0.0.1:8080"),
    "a loopback URL identifies nothing and stays visible",
  );
  assert.match(printed, /UNHEALED/);
});

test("a dead preview before attempt 1 exits 3 as PREVIEW-UNAVAILABLE with zero healer calls and one redacted line", async () => {
  const logs: string[] = [];
  const healer = scriptedHealer([
    {
      kind: "rewrite-target",
      stepId: "st_2",
      target: "#new",
      reason: "renamed",
    },
  ]);
  let afterHealCalls = 0;
  const code = await runHealCli({
    spec: rebasedSpec,
    driver: fakeDriver(),
    healer,
    io: { log: (l) => logs.push(l), error: (l) => logs.push(l) },
    options: {
      previewLiveness: async () => ({ alive: false, status: 404 }),
    },
    afterHeal: async () => {
      afterHealCalls++;
    },
  });
  assert.equal(code, 3, logs.join("\n"));
  assert.equal(healer.calls.length, 0, "no healer attempt is spent");
  assert.equal(
    afterHealCalls,
    0,
    "an environment fault never runs afterHeal (no repair PR)",
  );
  assert.match(logs[0], /PREVIEW-UNAVAILABLE/);
  assert.equal(
    logs.length,
    2,
    "the headline plus one explanatory line — no reasoning dump",
  );
  const printed = logs.join("\n");
  assert.ok(!printed.includes("SECRET"), "the failure's token never prints");
  assert.ok(!printed.includes(HOST_ORIGIN), "the host origin never prints");
});

test("a healed/needs-human spec diff never prints a tokened preview URL", async () => {
  const logs: string[] = [];
  const code = await runHealCli({
    spec: rebasedSpec,
    driver: fakeDriver(),
    healer: scriptedHealer([
      {
        kind: "propose-assert-change",
        stepId: "st_2",
        to: {
          testId: "banner",
          hasText: `see ${HOST_ORIGIN}/?pt_token=ASSERT_SECRET`,
        },
        reason: "data differs",
      },
    ]),
    io: { log: (l) => logs.push(l), error: (l) => logs.push(l) },
    options: {
      previewLiveness: async () => ({ alive: true, status: 200 }),
    },
  });
  assert.equal(code, 1, logs.join("\n"));
  assert.ok(
    logs.some((l) => l.includes("spec diff")),
    "a diff was printed for the recorded proposal",
  );
  const printed = logs.join("\n");
  assert.ok(
    !printed.includes("ASSERT_SECRET"),
    "the diff's token never prints",
  );
  assert.ok(!printed.includes(HOST_ORIGIN), "the diff's host never prints");
});

test("needs-human status carries the written evidence directory", async () => {
  const evidenceDir = mkdtempSync(join(tmpdir(), "formic-heal-status-"));
  const events: StatusEvent[] = [];
  try {
    const code = await runHealCli({
      spec: rebasedSpec,
      driver: fakeDriver(),
      healer: scriptedHealer([
        {
          kind: "propose-assert-change",
          stepId: "st_2",
          to: { testId: "banner", hasText: "changed" },
          reason: "data differs",
        },
      ]),
      io: { log: () => {}, error: () => {} },
      options: {
        previewLiveness: async () => ({ alive: true, status: 200 }),
      },
      evidenceDir,
      status: { emit: (event) => events.push(event) },
    });

    assert.equal(code, 1);
    assert.deepEqual(events, [
      { event: "progress" },
      { event: "needs-human", artifact: evidenceDir },
    ]);
    const initial = JSON.parse(
      readFileSync(join(evidenceDir, "initial.json"), "utf8"),
    ) as { driver?: string };
    assert.equal(
      initial.driver,
      "fake",
      "the artifact directory the terminal event names must really hold evidence",
    );
    const proposals = JSON.parse(
      readFileSync(join(evidenceDir, "proposals.json"), "utf8"),
    ) as { untestedComponents?: unknown[] };
    assert.ok(Array.isArray(proposals.untestedComponents));
  } finally {
    rmSync(evidenceDir, { recursive: true, force: true });
  }
});

test("a cleanup failure after a healed run downgrades the exit code to 2 and the terminal event to failed", async () => {
  const events: StatusEvent[] = [];
  const logs: string[] = [];
  const code = await runHealCli({
    spec: rebasedSpec,
    driver: fakeDriver(),
    healer: scriptedHealer([
      {
        kind: "rewrite-target",
        stepId: "st_2",
        target: "#new",
        reason: "renamed",
      },
    ]),
    io: { log: (l) => logs.push(l), error: (l) => logs.push(l) },
    options: {
      previewLiveness: async () => ({ alive: true, status: 200 }),
    },
    status: { emit: (event) => events.push(event) },
    cleanup: async () => {
      throw new Error("host close failed");
    },
  });
  assert.equal(code, 2, logs.join("\n"));
  assert.match(logs.join("\n"), /cleanup failed: host close failed/);
  assert.deepEqual(events, [{ event: "progress" }, { event: "failed" }]);
});

test("a writeSpec failure after evidence was written still carries the evidence artifact on the failed event", async () => {
  const evidenceDir = mkdtempSync(join(tmpdir(), "formic-heal-evidence-"));
  const events: StatusEvent[] = [];
  try {
    const code = await runHealCli({
      spec: rebasedSpec,
      driver: fakeDriver(),
      healer: scriptedHealer([
        {
          kind: "rewrite-target",
          stepId: "st_2",
          target: "#new",
          reason: "renamed",
        },
      ]),
      io: { log: () => {}, error: () => {} },
      options: {
        previewLiveness: async () => ({ alive: true, status: 200 }),
      },
      evidenceDir,
      writeSpec: () => {
        throw new Error("disk full");
      },
      status: { emit: (event) => events.push(event) },
    });
    assert.equal(code, 2);
    assert.deepEqual(events, [
      { event: "progress" },
      { event: "failed", artifact: evidenceDir },
    ]);
  } finally {
    rmSync(evidenceDir, { recursive: true, force: true });
  }
});

test("an afterHeal failure still carries an artifact recorded via the side channel before it threw", async () => {
  const events: StatusEvent[] = [];
  const code = await runHealCli({
    spec: rebasedSpec,
    driver: fakeDriver(),
    healer: scriptedHealer([
      {
        kind: "rewrite-target",
        stepId: "st_2",
        target: "#new",
        reason: "renamed",
      },
    ]),
    io: { log: () => {}, error: () => {} },
    options: {
      previewLiveness: async () => ({ alive: true, status: 200 }),
    },
    status: { emit: (event) => events.push(event) },
    afterHeal: async (_result, _io, recordArtifact) => {
      recordArtifact("bundle/index.html");
      throw new Error("pr open failed");
    },
  });
  assert.equal(code, 2);
  assert.deepEqual(events, [
    { event: "progress" },
    { event: "failed", artifact: "bundle/index.html" },
  ]);
});

test("an afterHeal failure carries the LATEST recorded artifact when recordArtifact is called more than once — the --bundle-then-PR shape", async () => {
  // Mirrors e2e-doctor.mts's real afterHeal exactly: a durable --bundle write records
  // its artifact first, then a second, PR-only bundle write records over it before the
  // PR-open call that fails — the terminal event must carry that second (freshest)
  // bundle, not the first.
  const events: StatusEvent[] = [];
  const code = await runHealCli({
    spec: rebasedSpec,
    driver: fakeDriver(),
    healer: scriptedHealer([
      {
        kind: "rewrite-target",
        stepId: "st_2",
        target: "#new",
        reason: "renamed",
      },
    ]),
    io: { log: () => {}, error: () => {} },
    options: {
      previewLiveness: async () => ({ alive: true, status: 200 }),
    },
    status: { emit: (event) => events.push(event) },
    afterHeal: async (_result, _io, recordArtifact) => {
      recordArtifact("bundle-dir/index.html");
      recordArtifact("pr-work-dir/index.html");
      throw new Error("pr open failed");
    },
  });
  assert.equal(code, 2);
  assert.deepEqual(events, [
    { event: "progress" },
    { event: "failed", artifact: "pr-work-dir/index.html" },
  ]);
});

test("a healer exception never prints a tokened URL to stderr", async () => {
  const logs: string[] = [];
  const code = await runHealCli({
    spec: rebasedSpec,
    driver: fakeDriver(),
    healer: scriptedHealer(() => {
      throw new Error(
        `agent died reading ${HOST_ORIGIN}/?pt_token=THROWN_SECRET`,
      );
    }),
    io: { log: (l) => logs.push(l), error: (l) => logs.push(l) },
    options: {
      previewLiveness: async () => ({ alive: true, status: 200 }),
    },
  });
  assert.equal(code, 2, logs.join("\n"));
  assert.match(logs.join("\n"), /heal failed:/);
  const printed = logs.join("\n");
  assert.ok(!printed.includes("THROWN_SECRET"), "the token never prints");
  assert.ok(!printed.includes(HOST_ORIGIN), "the host never prints");
});
