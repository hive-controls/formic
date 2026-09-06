/**
 * The heal rules, as tests. These encode the platform rule "healing must pair with
 * assertions and produce a diff" — the rules are structural, so a backend cannot
 * break them by being clever.
 */
import { test } from "node:test";
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import { loadSpec, saveSpec } from "../spec/parse.mts";
import type { Spec } from "../spec/types.mts";
import {
  applyProposal,
  demandReferencesKept,
  parseProposal,
  ProposalValidationError,
} from "./proposal.mts";
import { buildBrief, extractJsonObject, HEALER_RULES } from "./brief.mts";

const SPEC_FILE = join(
  dirname(fileURLToPath(import.meta.url)),
  "..",
  "..",
  "fixtures",
  "specs",
  "approve-an-order.yaml",
);
const spec = loadSpec(readFileSync(SPEC_FILE, "utf8"));
const signIn = spec.steps[3]; // click #signin-button
const approve = spec.steps[6]; // click #approve-button

test("rewrite-target changes exactly one line and keeps id and assert", () => {
  const repaired = applyProposal(
    spec,
    parseProposal({
      kind: "rewrite-target",
      stepId: signIn.id,
      target: "#submit-login",
      reason: "the sign-in button was renamed",
    }),
  );
  const step = repaired.steps[3];
  assert.equal(step.id, signIn.id);
  assert.equal(step.target, "#submit-login");
  assert.deepEqual(step.assert, signIn.assert);

  const before = saveSpec(spec).split("\n");
  const after = saveSpec(repaired).split("\n");
  assert.equal(before.length, after.length);
  const changed = before.filter((line, i) => line !== after[i]);
  assert.equal(
    changed.length,
    1,
    `expected a one-line diff, got ${changed.length}`,
  );
  assert.ok(
    spec.steps[3].target === "#signin-button",
    "input spec is never mutated",
  );
});

test("insert-step gets a fresh id, renumbers index, and leaves every existing id alone", () => {
  const repaired = applyProposal(
    spec,
    parseProposal({
      kind: "insert-step",
      beforeStepId: approve.id,
      step: {
        action: "click",
        target: "#approve-button",
        assert: { selector: "#confirm-banner", visible: true },
      },
      reason: "approval now shows an interstitial that needs a second click",
    }),
    () => "st_inserted",
  );
  assert.equal(repaired.steps.length, spec.steps.length + 1);
  assert.equal(repaired.steps[6].id, "st_inserted");
  assert.equal(
    repaired.steps[7].id,
    approve.id,
    "the original step follows it",
  );
  assert.deepEqual(
    repaired.steps.map((s) => s.index),
    repaired.steps.map((_, i) => i + 1),
  );
  assert.deepEqual(
    repaired.steps.filter((s) => s.id !== "st_inserted").map((s) => s.id),
    spec.steps.map((s) => s.id),
  );
});

test("RULE — propose-assert-change never touches assert; it records a proposal for a human", () => {
  const detail = spec.steps[4]; // click order row, asserts customer name
  const repaired = applyProposal(
    spec,
    parseProposal({
      kind: "propose-assert-change",
      stepId: detail.id,
      to: { testId: "detail-customer", hasText: "Fabrikam Metals" },
      reason: "the customer shown differs from the expectation",
    }),
  );
  const step = repaired.steps[4];
  assert.deepEqual(step.assert, detail.assert, "assert is untouched");
  assert.deepEqual(step.proposedAssertChange?.to, {
    testId: "detail-customer",
    hasText: "Fabrikam Metals",
  });
  assert.deepEqual(step.proposedAssertChange?.from, detail.assert);
  // Seen on the first live class-4 heal: `from` shared the `assert` object and the
  // YAML came out as `assert: &a1 … from: *a1` — correct, and unreadable in review.
  const yaml = saveSpec(repaired);
  assert.doesNotMatch(
    yaml,
    /&a\d|\*a\d/,
    "no YAML anchors in a reviewable spec",
  );
});

test("RULE — a repair may propose role+name and text asserts (the forms three vendor agents reached for and were rejected 6/6)", () => {
  const detail = spec.steps[4]; // click order row, asserts customer name
  const byRole = applyProposal(
    spec,
    parseProposal({
      kind: "propose-assert-change",
      stepId: detail.id,
      to: { role: "heading", name: "Fabrikam Metals — 31 items" },
      reason: "the detail heading now carries the customer, not a testId field",
    }),
  );
  assert.deepEqual(byRole.steps[4].proposedAssertChange?.to, {
    role: "heading",
    name: "Fabrikam Metals — 31 items",
  });

  const byText = applyProposal(
    spec,
    parseProposal({
      kind: "insert-step",
      beforeStepId: spec.steps[6].id, // click #approve-button
      step: {
        action: "click",
        target: "#confirm-dialog button",
        assert: { text: "Confirmed", exact: true },
      },
      reason: "approval now shows a confirm dialog first",
    }),
    () => "st_inserted",
  );
  assert.deepEqual(byText.steps[6].assert, { text: "Confirmed", exact: true });
});

test("RULE — a repair may propose a url-only assertion; a url+element mix is rejected", () => {
  const byUrl = applyProposal(
    spec,
    parseProposal({
      kind: "propose-assert-change",
      stepId: approve.id,
      to: { url: "http://127.0.0.1:4173/orders/SO-4471/confirmation" },
      reason: "the approval now navigates to a confirmation page",
    }),
  );
  assert.deepEqual(byUrl.steps[6].proposedAssertChange?.to, {
    url: "http://127.0.0.1:4173/orders/SO-4471/confirmation",
  });

  assert.throws(
    () =>
      parseProposal({
        kind: "propose-assert-change",
        stepId: approve.id,
        to: { url: "http://127.0.0.1:4173/orders", testId: "confirmation" },
        reason: "x",
      }),
    (err: unknown) =>
      err instanceof ProposalValidationError &&
      err.problems.some((p) =>
        p.includes(
          "cannot combine a url/urlPrefix/urlPattern predicate with an element assertion field",
        ),
      ),
  );
});

test("RULE — assert.name without assert.role is rejected on a proposal, same as on a spec", () => {
  assert.throws(
    () =>
      parseProposal({
        kind: "propose-assert-change",
        stepId: spec.steps[4].id,
        to: { name: "Fabrikam Metals" },
        reason: "x",
      }),
    (err: unknown) =>
      err instanceof ProposalValidationError &&
      err.problems.some((p) => p.includes("to.name requires to.role")),
  );
});

test("RULE — a rewrite-target smuggling an assert is rejected", () => {
  assert.throws(
    () =>
      parseProposal({
        kind: "rewrite-target",
        stepId: signIn.id,
        target: "#submit-login",
        assert: { selector: "#anything", visible: true },
        reason: "x",
      }),
    (err: unknown) =>
      err instanceof ProposalValidationError &&
      err.problems.some((p) => p.includes("assert is not allowed")),
  );
});

test("RULE — an inserted state-changing step without an assert is refused (class 4 holds for healed steps)", () => {
  assert.throws(
    () =>
      applyProposal(
        spec,
        parseProposal({
          kind: "insert-step",
          beforeStepId: approve.id,
          step: { action: "click", target: "#approve-button" },
          reason: "x",
        }),
      ),
    (err: unknown) => /must carry an assert/.test((err as Error).message),
  );
});

test("unknown kinds, unknown step ids, and index-based locators-by-shape are rejected", () => {
  assert.throws(
    () => parseProposal({ kind: "rewrite-everything", reason: "x" }),
    ProposalValidationError,
  );
  assert.throws(
    () =>
      applyProposal(
        spec,
        parseProposal({
          kind: "rewrite-target",
          stepId: "st_nope",
          target: "#x",
          reason: "x",
        }),
      ),
    (err: unknown) =>
      err instanceof ProposalValidationError &&
      err.problems.some((p) => p.includes('no step with id "st_nope"')),
  );
  assert.throws(() => parseProposal(null), ProposalValidationError);
  assert.throws(
    () => parseProposal({ kind: "no-repair" }),
    (err: unknown) =>
      err instanceof ProposalValidationError &&
      err.problems.some((p) => p.includes("reason is required")),
  );
});

test("the kind-as-key YAML shape is normalised; reason is still required", () => {
  // codex-cli 0.151.0 wrote `rewrite-target:\n  stepId: …\n  target: …` on its first
  // live run — unambiguous, so accepted. It also omitted `reason`, which is not.
  assert.deepEqual(
    parseProposal({
      "rewrite-target": { stepId: signIn.id, target: "#submit-login" },
      reason: "renamed",
    }),
    {
      kind: "rewrite-target",
      stepId: signIn.id,
      target: "#submit-login",
      reason: "renamed",
    },
  );
  assert.throws(
    () =>
      parseProposal({
        "rewrite-target": { stepId: signIn.id, target: "#submit-login" },
      }),
    (err: unknown) =>
      err instanceof ProposalValidationError &&
      err.problems.some((p) => p.includes("reason is required")),
  );
});

test("extractJsonObject tolerates prose and code fences around the object", () => {
  const wrapped =
    'Sure! Here is the proposal:\n```json\n{"kind":"no-repair","reason":"a \\"quoted\\" } brace"}\n```\nDone.';
  assert.deepEqual(extractJsonObject(wrapped), {
    kind: "no-repair",
    reason: 'a "quoted" } brace',
  });
  assert.throws(() => extractJsonObject("no object here"), /no JSON object/);
});

test("a no-repair that names a stepId is accepted with the stepId dropped — it touches nothing, so it is not a smuggled change", () => {
  const parsed = parseProposal({
    kind: "no-repair",
    reason: "the element is gone and nothing matches",
    stepId: "st_1",
  });
  assert.deepEqual(parsed, {
    kind: "no-repair",
    reason: "the element is gone and nothing matches",
  });
});

test('"type" is accepted as the spelling of "kind" — the one alias models reach for', () => {
  const parsed = parseProposal({
    type: "rewrite-target",
    stepId: "st_1",
    target: "#new",
    reason: "renamed",
  });
  assert.equal(parsed.kind, "rewrite-target");
  assert.ok(!("type" in parsed));
});

/**
 * A referenced value stays referenced. The healer is the one actor that rewrites a
 * committed spec, so it is also the one that could quietly put a credential back into
 * it — by proposing a literal for a step that had a reference, or by inserting a step
 * carrying the placeholder a redacted recording left behind.
 */
const referenced = {
  ...spec,
  steps: spec.steps.map((step) =>
    step.target === "#password"
      ? {
          ...step,
          value: undefined,
          valueFrom: "env.APPROVE_AN_ORDER_PASSWORD",
        }
      : step,
  ),
};

test("RULE — a proposal may not turn a valueFrom into a literal value", () => {
  // Checked on the OUTCOME, not on any one proposal kind: the four kinds that exist
  // today cannot reach an existing step's value, and this is what keeps that true of
  // the fifth. Applied by `applyProposal` on every path.
  const literal = {
    ...referenced,
    steps: referenced.steps.map((step) =>
      step.target === "#password"
        ? { ...step, valueFrom: undefined, value: "hunter2" }
        : step,
    ),
  };
  assert.throws(
    () => demandReferencesKept(referenced, literal),
    (err: unknown) =>
      err instanceof ProposalValidationError &&
      err.problems.some((p) => p.includes("its value must stay referenced")),
  );
  // Renumbering, inserting and rewriting a target are all untouched by the rule.
  assert.doesNotThrow(() => demandReferencesKept(referenced, referenced));
});

test("RULE — an inserted step may reference a value, but may not carry a placeholder", () => {
  const inserted = applyProposal(
    referenced,
    parseProposal({
      kind: "insert-step",
      beforeStepId: referenced.steps[3].id,
      step: {
        action: "fill",
        target: "#otp",
        valueFrom: "env.APPROVE_AN_ORDER_OTP",
        assert: { selector: "#otp", visible: true },
      },
      reason: "a one-time code step appeared",
    }),
    () => "st_inserted",
  );
  assert.equal(inserted.steps[3].valueFrom, "env.APPROVE_AN_ORDER_OTP");

  assert.throws(
    () =>
      parseProposal({
        kind: "insert-step",
        beforeStepId: referenced.steps[3].id,
        step: {
          action: "fill",
          target: "#otp",
          value: "<secret:password>",
          assert: { selector: "#otp", visible: true },
        },
        reason: "quoting the placeholder it read in the spec",
      }),
    (err: unknown) =>
      err instanceof ProposalValidationError &&
      err.problems.some((p) =>
        p.includes("step.value is a redaction placeholder"),
      ),
  );

  assert.throws(
    () =>
      parseProposal({
        kind: "insert-step",
        beforeStepId: referenced.steps[3].id,
        step: {
          action: "fill",
          target: "#otp",
          value: "123456",
          valueFrom: "env.APPROVE_AN_ORDER_OTP",
          assert: { selector: "#otp", visible: true },
        },
        reason: "both at once",
      }),
    (err: unknown) =>
      err instanceof ProposalValidationError &&
      err.problems.some((p) =>
        p.includes("step carries both value and valueFrom"),
      ),
  );
});

test("the brief carries the reference verbatim, and no value beside it", () => {
  // The healer sees the WHOLE spec, so this is also the check that a referenced value
  // survives serialisation into the brief as a reference rather than as a blank.
  const brief = buildBrief({
    spec: referenced,
    failure: {
      stepId: referenced.steps[2].id,
      index: 3,
      action: "fill",
      target: "#password",
      phase: "action",
      error: "locator resolved to hidden element",
    },
    failedStep: referenced.steps[2],
    url: "http://127.0.0.1:4173/",
    ariaSnapshot: "- textbox",
    attempt: 1,
    priorAttempts: [],
  });
  assert.match(brief, /- valueFrom: env\.APPROVE_AN_ORDER_PASSWORD/);
  assert.match(brief, /valueFrom: env\.APPROVE_AN_ORDER_PASSWORD/);
  assert.doesNotMatch(brief, /- value:/);
  // And the rules tell the healer what to do with it.
  assert.match(HEALER_RULES, /never replace it with a literal `value`/);
});

test("INTEGRATION — applyProposal itself refuses a repair that literalises a reference", () => {
  // The guard tested above is called directly; this drives the ONE door a healer's
  // output goes through, so removing the call from `applyProposal` cannot escape it.
  //
  // The reachable shape: an inserted step that takes over a referenced step's id and
  // carries a literal. It is a duplicate id too, and the validator would say so — but
  // the guard runs FIRST on purpose, because "a credential came back into the spec" is
  // the finding, and "two steps share an id" is a symptom of how.
  const referencedStep = referenced.steps[2];
  assert.throws(
    () =>
      applyProposal(
        referenced,
        parseProposal({
          kind: "insert-step",
          beforeStepId: referencedStep.id,
          step: {
            action: "fill",
            target: "#password",
            value: "hunter2",
            assert: { selector: "#password", visible: true },
          },
          reason: "putting the value back",
        }),
        () => referencedStep.id,
      ),
    (err: unknown) =>
      err instanceof ProposalValidationError &&
      err.problems.some((p) => p.includes("its value must stay referenced")),
  );
});

test("RULE — a proposed assertion is held to the SPEC's url contract, at proposal time", () => {
  // The proposal parser used to know only the mutual-exclusivity and name/role rules,
  // so each of these was accepted here and refused later — by validateSpec, from inside
  // applyProposal, where the reader sees a spec error rather than the bad proposal that
  // caused it. `propose-assert-change` never even reaches that validator: its `to` lands
  // in `proposedAssertChange`, which validateSpec does not walk, so a malformed url
  // reached a human's review as something to accept.
  const cases: [Record<string, unknown>, string][] = [
    [
      {
        url: "http://127.0.0.1:4173/a",
        urlPrefix: "http://127.0.0.1:4173/",
      },
      "may carry only one of url, urlPrefix, urlPattern",
    ],
    [{ url: "/orders/SO-4471" }, "must be an absolute http(s) URL"],
    [{ url: "HTTP://127.0.0.1:4173/orders" }, "must be a canonical URL"],
    [{ urlPrefix: "http://127.0.0.1:4173/a/../b" }, "must be a canonical URL"],
    [{ urlPattern: "(unclosed" }, "must be a valid regular expression"],
    [{ testId: "   " }, "must be a non-empty string"],
  ];
  for (const [to, expected] of cases) {
    assert.throws(
      () =>
        parseProposal({
          kind: "propose-assert-change",
          stepId: approve.id,
          to,
          reason: "x",
        }),
      (err: unknown) =>
        err instanceof ProposalValidationError &&
        err.problems.some((p) => p.includes(expected)),
      `${JSON.stringify(to)} must be refused at proposal time with "${expected}"`,
    );
  }
});

test("a canonical url proposal still applies — the rule refuses malformed urls, not url assertions", () => {
  const applied = applyProposal(
    spec,
    parseProposal({
      kind: "propose-assert-change",
      stepId: approve.id,
      to: { urlPrefix: "http://127.0.0.1:4173/orders/" },
      reason: "the approval now navigates",
    }),
  );
  assert.deepEqual(applied.steps[6].proposedAssertChange?.to, {
    urlPrefix: "http://127.0.0.1:4173/orders/",
  });
});

const FRAMED_SPEC: Spec = {
  name: "framed",
  startUrl: "http://127.0.0.1:4173/",
  steps: [
    {
      id: "st_g",
      index: 1,
      action: "goto",
      target: "http://127.0.0.1:4173/",
    },
    {
      id: "st_pay",
      index: 2,
      action: "click",
      target: "#pay",
      frame: [{ selector: "#checkout" }],
      assert: { testId: "receipt", visible: true },
    },
  ],
};

test("RULE — a rewrite-target is the ONE proposal that may move a step into another frame", () => {
  const moved = applyProposal(
    FRAMED_SPEC,
    parseProposal({
      kind: "rewrite-target",
      stepId: "st_pay",
      target: "#pay-now",
      frame: [{ selector: "#checkout" }, { name: "payment-frame" }],
      reason: "the pay button moved into a nested payment frame",
    }),
  );
  assert.deepEqual(moved.steps[1].frame, [
    { selector: "#checkout" },
    { name: "payment-frame" },
  ]);
  assert.equal(moved.steps[1].target, "#pay-now");

  // Omitted, the chain is left alone — which is what almost every repair means.
  const kept = applyProposal(
    FRAMED_SPEC,
    parseProposal({
      kind: "rewrite-target",
      stepId: "st_pay",
      target: "#pay-now",
      reason: "renamed",
    }),
  );
  assert.deepEqual(kept.steps[1].frame, [{ selector: "#checkout" }]);
});

test("RULE — no other proposal kind may carry a frame: a chain is part of a step's ADDRESS", () => {
  for (const proposal of [
    {
      kind: "propose-assert-change",
      stepId: "st_pay",
      to: { testId: "receipt" },
      frame: [{ selector: "#checkout" }],
      reason: "x",
    },
    {
      kind: "no-repair",
      frame: [{ selector: "#checkout" }],
      reason: "x",
    },
  ]) {
    assert.throws(
      () => parseProposal(proposal),
      (err: unknown) =>
        err instanceof ProposalValidationError &&
        err.problems.some((p) =>
          p.includes(`frame is not allowed on a ${proposal.kind} proposal`),
        ),
      `${proposal.kind} must not carry a frame`,
    );
  }
});

test("RULE — a proposed frame chain is validated, not taken on trust", () => {
  for (const [frame, expected] of [
    [[], "is empty — omit it entirely"],
    [[{ selector: "#a", name: "b" }], "may name a frame only one way"],
    [[{ selctor: "#a" }], "is not a frame field"],
    [["#a"], "must be a mapping naming one frame"],
  ] as [unknown, string][]) {
    assert.throws(
      () =>
        parseProposal({
          kind: "rewrite-target",
          stepId: "st_pay",
          target: "#pay",
          frame,
          reason: "x",
        }),
      (err: unknown) =>
        err instanceof ProposalValidationError &&
        err.problems.some((p) => p.includes(expected)),
      `${JSON.stringify(frame)} must be refused with "${expected}"`,
    );
  }
});

test("an inserted step may live in a frame, and its chain is validated the same way", () => {
  const inserted = applyProposal(
    FRAMED_SPEC,
    parseProposal({
      kind: "insert-step",
      beforeStepId: "st_pay",
      step: {
        action: "click",
        target: "#accept-terms",
        frame: [{ selector: "#checkout" }],
        assert: { testId: "terms-accepted", visible: true },
      },
      reason: "the checkout frame gained a terms gate",
    }),
    () => "st_new",
  );
  assert.deepEqual(inserted.steps[1].frame, [{ selector: "#checkout" }]);
  assert.throws(
    () =>
      parseProposal({
        kind: "insert-step",
        beforeStepId: "st_pay",
        step: {
          action: "click",
          target: "#accept-terms",
          frame: [{}],
          assert: { testId: "t", visible: true },
        },
        reason: "x",
      }),
    (err: unknown) =>
      err instanceof ProposalValidationError &&
      err.problems.some((p) => p.includes("names no frame")),
  );
});

test("the brief tells a healer where the failed step was resolved, and how to move it", () => {
  const brief = buildBrief({
    spec: FRAMED_SPEC,
    failure: {
      stepId: "st_pay",
      index: 2,
      action: "click",
      target: "#pay",
      phase: "action",
      error: "frame chain link 1 did not resolve",
    },
    failedStep: FRAMED_SPEC.steps[1],
    url: "http://127.0.0.1:4173/",
    ariaSnapshot: "- button: Pay",
    attempt: 1,
    priorAttempts: [],
  });
  assert.match(brief, /- frame: \[\{"selector":"#checkout"\}\]/);
  assert.match(HEALER_RULES, /OUTERMOST FIRST/);
  assert.match(HEALER_RULES, /may NEVER carry a frame/);
});
