/**
 * The heal loop against the breakage corpus, in a real browser, with a scripted healer.
 *
 * The healer is faked; everything else is real — the breakage, the replay, the
 * verification, the evidence. What these prove is the LOOP: a proposal only counts
 * after the whole spec replays green, an assertion proposal is never applied, and a
 * healer that keeps being wrong runs out of attempts.
 */
import { test, before, after } from "node:test";
import assert from "node:assert/strict";
import { execFileSync } from "node:child_process";
import { mkdtempSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import { LocalPlaywrightDriver } from "../driver/local-playwright.mts";
import { isRenderable } from "../evidence/segment.mts";
import { loadSpec, saveSpec } from "../spec/parse.mts";
import type { Spec } from "../spec/types.mts";
import {
  serveDirectory,
  type StaticApp,
} from "../replay/sample-app-server.mts";
import {
  adapterFromCommand,
  agentCliHealer,
  quoteCommandToken,
  type AgentCliAdapter,
} from "./healers/agent-cli.mts";
import { buildBrief } from "./brief.mts";
import { heal } from "./loop.mts";
import { scriptedHealer } from "./scripted.mts";

import type { RepairProposal } from "./types.mts";
const USECASE = join(
  dirname(fileURLToPath(import.meta.url)),
  "..",
  "..",
  "fixtures",
);
const SPEC_FILE = join(USECASE, "specs", "approve-an-order.yaml");

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
  scratch = mkdtempSync(join(tmpdir(), "formic-heal-"));
});
after(() => {
  rmSync(scratch, { recursive: true, force: true });
});

function breakage(name: string): string {
  const outDir = join(scratch, name);
  execFileSync(
    process.execPath,
    [join(USECASE, "breakages", "apply.mjs"), name, outDir],
    { stdio: "pipe" },
  );
  return outDir;
}

async function withApp<T>(
  dir: string,
  run: (spec: Spec, app: StaticApp) => Promise<T>,
): Promise<T> {
  const app = await serveDirectory(dir);
  try {
    return await run(
      rebase(loadSpec(readFileSync(SPEC_FILE, "utf8")), app.baseUrl),
      app,
    );
  } finally {
    await app.close();
  }
}

const driver = new LocalPlaywrightDriver();
const OPTIONS = { stepTimeoutMs: 2000 };
const SIGN_IN = "st_6c4760ff";
const ORDER_ROW = "st_3bfe602b";
const APPROVE = "st_28efd87b";

test("pristine: passes without ever asking the healer", async () => {
  const healer = scriptedHealer(() => {
    throw new Error("must not be called");
  });
  const result = await withApp(join(USECASE, "sample-app"), (spec) =>
    heal(spec, driver, healer, OPTIONS),
  );
  assert.equal(result.outcome, "passed");
  assert.equal(healer.calls.length, 0);
  assert.equal(result.attempts.length, 0);
  assert.equal(result.initial.evidence.modelVersion, null, "token-free path");
});

test("class 1 (renamed-selector): a target rewrite is verified by a full green replay, with before/after evidence", async () => {
  const healer = scriptedHealer([
    {
      kind: "rewrite-target",
      stepId: SIGN_IN,
      target: "#submit-login",
      reason: "renamed",
    },
  ]);
  const { result, original } = await withApp(
    breakage("renamed-selector"),
    async (spec) => ({
      original: spec,
      result: await heal(spec, driver, healer, OPTIONS),
    }),
  );
  assert.equal(result.outcome, "healed");
  assert.equal(result.attempts.length, 1);

  // The healer saw the failure, not a guess: phase, live URL, and a page snapshot.
  const context = healer.calls[0];
  assert.equal(context.failure.stepId, SIGN_IN);
  assert.equal(context.failure.phase, "action");
  assert.match(context.url, /^http:\/\/127\.0\.0\.1/);
  assert.match(context.ariaSnapshot, /Sign in/);

  // The spec to commit is a one-line diff of the original.
  const beforeLines = saveSpec(original).split("\n");
  const afterLines = saveSpec(result.spec).split("\n");
  assert.equal(beforeLines.filter((l, i) => l !== afterLines[i]).length, 1);

  // Evidence: before = the step as it failed; after = the same step id, repaired.
  const [attempt] = result.attempts;
  assert.ok(attempt.before && isRenderable(attempt.before), "before segment");
  assert.ok(attempt.after && isRenderable(attempt.after), "after segment");
  assert.equal(attempt.before.stepId, SIGN_IN);
  assert.equal(attempt.after.stepId, SIGN_IN);
  assert.equal(attempt.verification?.evidence.modelVersion, "scripted");
  assert.equal(
    attempt.verification?.result.steps.find((s) => s.id === SIGN_IN)?.outcome,
    "healed",
    "the audit record names the healed step",
  );
});

test("an agent-backed healer: modelVersion is null on the initial evidence and the resolved model on the verification evidence", async () => {
  const agentScratch = mkdtempSync(join(scratch, "agent-"));
  const fakeAgentPath = join(agentScratch, "fake-loop-agent.mjs");
  writeFileSync(
    fakeAgentPath,
    `import { writeFileSync } from "node:fs";
if (process.argv.includes("--version")) { console.log("fake-loop-agent 1.0.0"); process.exit(0); }
writeFileSync("PROPOSAL.yaml", "kind: rewrite-target\\nstepId: ${SIGN_IN}\\ntarget: \\"#submit-login\\"\\nreason: renamed\\n");
`,
  );
  const adapter: AgentCliAdapter = {
    ...adapterFromCommand(
      `${quoteCommandToken(process.execPath)} ${quoteCommandToken(fakeAgentPath)} {prompt}`,
    ),
    modelArgs: (m: string) => ["--model", m],
  };
  const healer = agentCliHealer({
    adapter,
    workspaceRoot: agentScratch,
    timeoutMs: 20_000,
    model: "claude-sonnet-5",
  });
  const result = await withApp(breakage("renamed-selector"), (spec) =>
    heal(spec, driver, healer, OPTIONS),
  );
  assert.equal(result.outcome, "healed");
  assert.equal(
    result.initial.evidence.modelVersion,
    null,
    "no healer acted yet on the initial run",
  );
  assert.equal(
    result.attempts[0].verification?.evidence.modelVersion,
    "claude-sonnet-5",
  );
});

test("class 3 (changed-flow): an inserted step heals a flow change; the new step carries its own evidence", async () => {
  const healer = scriptedHealer([
    {
      kind: "insert-step",
      beforeStepId: APPROVE,
      step: {
        action: "click",
        target: "#approve-button",
        assert: { selector: "#confirm-banner", visible: true },
      },
      reason: "approval now requires confirming an interstitial",
    },
  ]);
  const result = await withApp(breakage("changed-flow"), (spec) =>
    heal(spec, driver, healer, OPTIONS),
  );
  assert.equal(
    result.outcome,
    "healed",
    JSON.stringify(result.attempts[0]?.verification?.result.failure),
  );
  assert.equal(result.spec.steps.length, 8);
  const inserted = result.spec.steps[6];
  assert.equal(result.spec.steps[7].id, APPROVE);
  assert.equal(result.attempts[0].after?.stepId, inserted.id);
  assert.ok(result.attempts[0].after && isRenderable(result.attempts[0].after));
});

test("class 4 (swapped-data): an assertion proposal is recorded for a human and NEVER applied", async () => {
  const healer = scriptedHealer([
    {
      kind: "propose-assert-change",
      stepId: ORDER_ROW,
      to: { testId: "detail-customer", hasText: "Fabrikam Metals" },
      reason: "the customer on screen differs from the expectation",
    },
  ]);
  const result = await withApp(breakage("swapped-data"), (spec) =>
    heal(spec, driver, healer, OPTIONS),
  );
  assert.equal(result.outcome, "needs-human");
  const step = result.spec.steps.find((s) => s.id === ORDER_ROW)!;
  assert.deepEqual(step.assert, {
    testId: "detail-customer",
    hasText: "Contoso Rail",
  });
  assert.equal(step.proposedAssertChange?.to.hasText, "Fabrikam Metals");
  assert.equal(healer.calls.length, 1, "no second attempt after a needs-human");
  assert.equal(result.attempts[0].verification, undefined, "never replayed");
});

test("a healer that keeps being wrong runs out of attempts; each wrong proposal is fed back", async () => {
  const healer = scriptedHealer([
    {
      kind: "rewrite-target",
      stepId: SIGN_IN,
      target: "#still-wrong",
      reason: "guess 1",
    },
    {
      kind: "rewrite-target",
      stepId: SIGN_IN,
      target: "#also-wrong",
      reason: "guess 2",
    },
  ]);
  const result = await withApp(breakage("renamed-selector"), (spec) =>
    heal(spec, driver, healer, { ...OPTIONS, maxAttempts: 2 }),
  );
  assert.equal(result.outcome, "unhealed");
  assert.equal(result.attempts.length, 2);
  assert.equal(
    result.spec.steps[3].target,
    "#signin-button",
    "the original spec is returned",
  );
  const second = healer.calls[1];
  assert.equal(second.attempt, 2);
  assert.equal(second.priorAttempts.length, 1);
  assert.match(second.priorAttempts[0].result, /#still-wrong/);
});

test("a dead preview FAILS FAST as preview-unavailable: zero healer calls, no attempt spent", async () => {
  const healer = scriptedHealer([
    {
      kind: "rewrite-target",
      stepId: SIGN_IN,
      target: "#submit-login",
      reason: "renamed",
    },
  ]);
  const result = await withApp(breakage("renamed-selector"), (spec) =>
    heal(spec, driver, healer, {
      ...OPTIONS,
      previewLiveness: async () => ({ alive: false, status: 404 }),
    }),
  );
  assert.equal(result.outcome, "preview-unavailable");
  assert.equal(result.attempts.length, 0, "no attempt is recorded");
  assert.equal(healer.calls.length, 0, "the healer is never asked");
  assert.equal(result.spec.steps[3].target, "#signin-button");
});

test("a preview that dies DURING an attempt (verification fails, then dead) ends as preview-unavailable after exactly one propose", async () => {
  const healer = scriptedHealer([
    {
      kind: "rewrite-target",
      stepId: SIGN_IN,
      target: "#still-wrong",
      reason: "guess 1",
    },
  ]);
  let probe = 0;
  const result = await withApp(breakage("renamed-selector"), (spec) =>
    heal(spec, driver, healer, {
      ...OPTIONS,
      // One attempt only: with a larger budget the PRE-attempt probe would catch the
      // dead preview at attempt 2 and this test could not tell the post-verification
      // probe from it.
      maxAttempts: 1,
      previewLiveness: async () =>
        ++probe === 1
          ? { alive: true, status: 200 }
          : { alive: false, status: 404 },
    }),
  );
  assert.equal(result.outcome, "preview-unavailable");
  assert.equal(healer.calls.length, 1, "exactly one attempt was spent");
  assert.equal(result.attempts.length, 1, "the spent attempt is recorded");
  assert.equal(
    probe,
    2,
    "probed before the attempt and after its verification",
  );
});

test("an INVALID proposal is a declined attempt, not a crash: the run returns with its evidence, the rejection is fed back, and a later valid proposal can still heal", async () => {
  const healer = scriptedHealer([
    // A field the assert grammar does not have (role/name/text ARE valid locator
    // fields — see spec/types.mts — so this uses one that never will be). Must not
    // throw out of heal().
    {
      kind: "insert-step",
      beforeStepId: "st_whatever",
      step: {
        action: "click",
        target: "#approve-button",
        assert: { label: "paragraph", hasText: "confirm" },
      },
      reason: "confirmation appeared",
    } as unknown as RepairProposal,
    {
      kind: "rewrite-target",
      stepId: SIGN_IN,
      target: "#submit-login",
      reason: "renamed",
    },
  ]);
  const result = await withApp(breakage("renamed-selector"), (spec) =>
    heal(spec, driver, healer, { ...OPTIONS, maxAttempts: 2 }),
  );
  assert.equal(result.outcome, "healed");
  assert.equal(result.attempts.length, 2);
  assert.equal(result.attempts[0].proposal.kind, "no-repair");
  assert.match(
    result.attempts[0].proposal.reason,
    /invalid and was not applied — proposal is invalid:[\s\S]*label is not an assertion field/,
  );
  assert.equal(result.attempts[0].after, null);
  assert.match(
    healer.calls[1].priorAttempts[0].result,
    /label is not an assertion field/,
  );
});

/**
 * A resolved value must not reach the healer, or come back out of it.
 *
 * Two doors, both real and both outside the replay result the runner scrubs:
 *
 *  - the loop takes a FRESH url + accessibility snapshot at the moment of failure,
 *    after the replay has returned, and hands it straight to the brief and the agent
 *    workspace. An application that echoes what was typed puts the value on that
 *    screen;
 *  - a healer reads that screen and can quote what it saw into its own reasoning,
 *    which is printed by the CLI, written into the bundle and shown in the PR body.
 *
 * Real browser, real breakage, scripted healer — the only faked part is the model.
 */
test("a resolved value reaches neither the healer's snapshot nor its reasoning back out", async () => {
  const canary = "canary-secret-value";
  process.env.HEAL_SNAPSHOT_PASSWORD = canary;
  // The sample app echoes the signed-in identity onto the page, so filling it from a
  // reference puts the resolved value into the very snapshot the healer is shown.
  const referenced = (spec: Spec): Spec => ({
    ...spec,
    steps: spec.steps.map((step) =>
      step.target === "#email"
        ? {
            ...step,
            value: undefined,
            valueFrom: "env.HEAL_SNAPSHOT_PASSWORD",
          }
        : step,
    ),
  });
  try {
    await withApp(breakage("renamed-selector"), async (spec) => {
      const healer = scriptedHealer((context) => {
        // What the healer was SHOWN — the first door.
        assert.equal(
          context.ariaSnapshot.includes(canary),
          false,
          "the accessibility snapshot handed to the healer carries the resolved value",
        );
        assert.equal(JSON.stringify(context).includes(canary), false);
        // The brief and the agent workspace are both projections of that context, so
        // the text an agent-backed healer actually reads is asserted directly.
        assert.equal(
          buildBrief(context).includes(canary),
          false,
          "the brief carries the resolved value",
        );
        // What it says BACK — the second door.
        return {
          kind: "no-repair",
          reason: `the field already reads ${canary}`,
        };
      });
      const result = await heal(referenced(spec), driver, healer, OPTIONS);
      assert.equal(healer.calls.length > 0, true, "the healer was never asked");
      assert.equal(
        JSON.stringify(result.attempts).includes(canary),
        false,
        "the healer's own reasoning carried the resolved value back out",
      );
    });
  } finally {
    delete process.env.HEAL_SNAPSHOT_PASSWORD;
  }
});

test("a short resolved value never survives in a proposed step, however short it is", async () => {
  // The free-text floor lets a four-digit PIN through prose, on purpose. A proposed
  // step's `value` is not prose: it is a field that holds nothing but a typed value, so
  // the floor has no reason to apply and the whole field is withheld. Without this the
  // PIN survived the proposal, went into applyProposal, and came back out in the spec
  // and every artifact written from the run.
  const pin = "1234";
  process.env.HEAL_SHORT_PIN = pin;
  try {
    await withApp(breakage("renamed-selector"), async (spec) => {
      const referenced: Spec = {
        ...spec,
        steps: spec.steps.map((step) =>
          step.target === "#password"
            ? { ...step, value: undefined, valueFrom: "env.HEAL_SHORT_PIN" }
            : step,
        ),
      };
      const healer = scriptedHealer((context) => ({
        kind: "insert-step",
        beforeStepId: context.failure.stepId,
        step: {
          action: "fill",
          target: "#password",
          value: pin,
          assert: { selector: "#password", visible: true },
        },
        reason: "re-typing what I saw",
      }));
      const result = await heal(referenced, driver, healer, OPTIONS);
      for (const attempt of result.attempts) {
        assert.equal(
          JSON.stringify(attempt.proposal),
          JSON.stringify(attempt.proposal).replace(`"value":"${pin}"`, "!"),
          "a proposed step carried the resolved value",
        );
        assert.match(
          JSON.stringify(attempt.proposal),
          /redacted:env\.HEAL_SHORT_PIN/,
        );
      }
      // And it cannot arrive in the spec the run returns either.
      assert.equal(
        result.spec.steps.some((step) => step.value === pin),
        false,
        "the returned spec carried the resolved value",
      );
    });
  } finally {
    delete process.env.HEAL_SHORT_PIN;
  }
});

test("an error ESCAPING the loop is scrubbed — message and stack alike", async () => {
  // Everything the healer RETURNS is scrubbed. What it THROWS was not: a backend that
  // fails while parsing a model reply raises a SyntaxError quoting the text it choked
  // on — which is the page the healer was reading — and that error left the loop
  // untouched, straight to the terminal the CLI prints. An exception is an artifact.
  const canary = "canary-secret-value";
  process.env.HEAL_THROWN_PASSWORD = canary;
  try {
    await withApp(breakage("renamed-selector"), async (spec) => {
      const referenced: Spec = {
        ...spec,
        steps: spec.steps.map((step) =>
          step.target === "#password"
            ? {
                ...step,
                value: undefined,
                valueFrom: "env.HEAL_THROWN_PASSWORD",
              }
            : step,
        ),
      };
      const healer = scriptedHealer(() => {
        throw new SyntaxError(`Unexpected token in {"typed": "${canary}"}`);
      });
      await assert.rejects(
        () => heal(referenced, driver, healer, OPTIONS),
        (error: unknown) => {
          const thrown = error as Error;
          assert.equal(
            thrown.message.includes(canary),
            false,
            "the escaping error's message carries the resolved value",
          );
          assert.equal(
            (thrown.stack ?? "").includes(canary),
            false,
            "the escaping error's stack carries the resolved value",
          );
          // Still recognisable: a redaction that erases the diagnosis is its own bug.
          assert.match(thrown.message, /Unexpected token/);
          assert.match(thrown.message, /redacted:env\.HEAL_THROWN_PASSWORD/);
          assert.equal(thrown.name, "SyntaxError");
          return true;
        },
      );
    });
  } finally {
    delete process.env.HEAL_THROWN_PASSWORD;
  }
});
