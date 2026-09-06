/**
 * Spec format tests. The interesting ones are the two structural rules the format
 * exists to enforce — they encode findings, not preferences.
 */
import { test } from "node:test";
import assert from "node:assert/strict";
import { loadSpec, saveSpec, SpecValidationError } from "./parse.mts";
import type { Spec } from "./types.mts";

const VALID = `
name: approve-an-order
startUrl: http://127.0.0.1:4173/
steps:
  - id: st_0001
    index: 1
    action: goto
    target: http://127.0.0.1:4173/
  - id: st_0002
    index: 2
    action: fill
    target: "#email"
    value: ops@forgedepot.test
    assert:
      selector: "#email"
      visible: true
  - id: st_0003
    index: 3
    action: click
    target: "#signin-button"
    assert:
      testId: current-user
      hasText: ops@forgedepot.test
`;

test("loads a valid spec", () => {
  const spec = loadSpec(VALID);
  assert.equal(spec.name, "approve-an-order");
  assert.equal(spec.steps.length, 3);
  assert.equal(spec.steps[2].assert?.testId, "current-user");
});

test("RULE — a state-changing step without an assert is rejected (the state-change rule)", () => {
  // Data drift survives every locator; only an assertion catches it. The format makes
  // that structural rather than a review-time hope.
  const noAssert = VALID.replace(
    `    assert:
      testId: current-user
      hasText: ops@forgedepot.test`,
    "",
  );
  assert.throws(
    () => loadSpec(noAssert),
    (err: unknown) =>
      err instanceof SpecValidationError &&
      err.problems.some((p) => p.includes("must carry an assert")),
  );
});

test("RULE — goto and waitFor are exempt from the assert requirement", () => {
  // They assert by their own nature; requiring one would be noise.
  const spec = loadSpec(VALID);
  assert.equal(spec.steps[0].action, "goto");
  assert.equal(spec.steps[0].assert, undefined);
});

test("REVIEW REGRESSION (P2) — waitFor without a target is rejected at load, not at run time", () => {
  // The validator exempted waitFor from the target rule, so a spec with nothing to
  // wait for loaded fine, opened a browser, and failed as a REPLAY failure (exit 1)
  // instead of being refused as malformed input (exit 2).
  const targetless = `${VALID}  - id: st_0004
    index: 4
    action: waitFor
`;
  assert.throws(
    () => loadSpec(targetless),
    (err: unknown) =>
      err instanceof SpecValidationError &&
      err.problems.some((p) => p.includes("target is required")),
  );
  // A whitespace-only target is the same malformed input wearing a disguise: it
  // passed a `=== ""` check, opened a browser, and handed Playwright a blank selector.
  assert.throws(
    () => loadSpec(`${targetless}    target: "   "\n`),
    (err: unknown) =>
      err instanceof SpecValidationError &&
      err.problems.some((p) => p.includes("target is required")),
  );
  // With a target it is a perfectly good step, and still exempt from `assert`.
  const spec = loadSpec(`${targetless}    target: "#ready"\n`);
  assert.equal(spec.steps[3].action, "waitFor");
  assert.equal(spec.steps[3].assert, undefined);
});

test("rejects a step whose index does not match its position", () => {
  const misindexed = VALID.replace("    index: 3", "    index: 7");
  assert.throws(() => loadSpec(misindexed), SpecValidationError);
});

test("rejects an unknown action", () => {
  assert.throws(
    () => loadSpec(VALID.replace("action: click", "action: teleport")),
    SpecValidationError,
  );
});

test("rejects fill with no value", () => {
  assert.throws(
    () => loadSpec(VALID.replace("    value: ops@forgedepot.test\n", "")),
    SpecValidationError,
  );
});

test("REVIEW REGRESSION (P2) — assertion fields must have the right types", () => {
  // YAML `visible: "false"` is a STRING. Unvalidated, it read as truthy and the runner
  // asserted the opposite predicate — a spec that says "hidden" enforcing "visible".
  const stringVisible = VALID.replace(
    "      visible: true",
    '      visible: "false"',
  );
  assert.throws(
    () => loadSpec(stringVisible),
    (err: unknown) =>
      err instanceof SpecValidationError &&
      err.problems.some((p) => p.includes("visible must be a boolean")),
  );
  const numericText = VALID.replace(
    "      hasText: ops@forgedepot.test",
    "      hasText: 42",
  );
  assert.throws(
    () => loadSpec(numericText),
    (err: unknown) =>
      err instanceof SpecValidationError &&
      err.problems.some((p) => p.includes("hasText must be a string")),
  );
});

test("REVIEW REGRESSION (P2) — assert must be a mapping; null/false/scalar is malformed, not absent", () => {
  // `assert: null` is not `undefined`, so the class-4 presence check was satisfied,
  // and it is falsy, so every field check was skipped. The runner then dereferenced
  // null after opening a browser. Malformed is malformed at load.
  for (const bad of ["null", "false", "0", '""', "[]"]) {
    const spec = VALID.replace(
      `    assert:
      testId: current-user
      hasText: ops@forgedepot.test`,
      `    assert: ${bad}`,
    );
    assert.throws(
      () => loadSpec(spec),
      (err: unknown) =>
        err instanceof SpecValidationError &&
        err.problems.some((p) => p.includes("assert must be a mapping")),
      `assert: ${bad} must be rejected`,
    );
  }
});

test("REVIEW REGRESSION (P2) — a blank locator field is malformed, not a locator", () => {
  // `testId: "   "` passed the presence check and locatorFor targeted whitespace;
  // `testId: ""` next to a valid selector won the precedence and ignored the selector.
  const blank = VALID.replace(
    "      testId: current-user",
    '      testId: "   "',
  );
  assert.throws(
    () => loadSpec(blank),
    (err: unknown) =>
      err instanceof SpecValidationError &&
      err.problems.some((p) => p.includes("assert.testId must be a non-empty")),
  );
  const emptyBesideSelector = VALID.replace(
    "      testId: current-user",
    '      testId: ""\n      selector: "[data-testid=current-user]"',
  );
  assert.throws(
    () => loadSpec(emptyBesideSelector),
    (err: unknown) =>
      err instanceof SpecValidationError &&
      err.problems.some((p) => p.includes("assert.testId must be a non-empty")),
  );
});

test("rejects an assert with neither testId nor selector", () => {
  const vague = VALID.replace("      testId: current-user\n", "");
  assert.throws(() => loadSpec(vague), SpecValidationError);
});

test("RULE — role (+ optional name) and text are first-class assert locators", () => {
  // What three vendor agents reached for on the changed-flow dogfood round and were
  // rejected 6/6: there was no way to name a paragraph or a heading with no id.
  const byRole = VALID.replace(
    `    assert:
      testId: current-user
      hasText: ops@forgedepot.test`,
    `    assert:
      role: button
      name: Sign in`,
  );
  const spec = loadSpec(byRole);
  assert.deepEqual(spec.steps[2].assert, { role: "button", name: "Sign in" });

  const byRoleAlone = VALID.replace(
    `    assert:
      testId: current-user
      hasText: ops@forgedepot.test`,
    `    assert:
      role: heading`,
  );
  assert.equal(loadSpec(byRoleAlone).steps[2].assert?.role, "heading");

  const byText = VALID.replace(
    `    assert:
      testId: current-user
      hasText: ops@forgedepot.test`,
    `    assert:
      text: Order approved
      exact: true`,
  );
  const textSpec = loadSpec(byText);
  assert.deepEqual(textSpec.steps[2].assert, {
    text: "Order approved",
    exact: true,
  });
});

test("REVIEW REGRESSION — assert.name without assert.role is malformed (name has nothing to filter)", () => {
  const danglingName = VALID.replace(
    `    assert:
      testId: current-user
      hasText: ops@forgedepot.test`,
    `    assert:
      name: Sign in`,
  );
  assert.throws(
    () => loadSpec(danglingName),
    (err: unknown) =>
      err instanceof SpecValidationError &&
      err.problems.some((p) => p.includes("assert.name requires assert.role")),
  );
});

test("REVIEW REGRESSION — assert.role/text/exact field types and blank-string rules", () => {
  const blankRole = VALID.replace(
    `    assert:
      testId: current-user
      hasText: ops@forgedepot.test`,
    `    assert:
      role: "   "`,
  );
  assert.throws(
    () => loadSpec(blankRole),
    (err: unknown) =>
      err instanceof SpecValidationError &&
      err.problems.some((p) => p.includes("assert.role must be a non-empty")),
  );

  const stringExact = VALID.replace(
    `    assert:
      testId: current-user
      hasText: ops@forgedepot.test`,
    `    assert:
      text: Order approved
      exact: "true"`,
  );
  assert.throws(
    () => loadSpec(stringExact),
    (err: unknown) =>
      err instanceof SpecValidationError &&
      err.problems.some((p) => p.includes("assert.exact must be a boolean")),
  );
});

test("RULE — a step without an id is rejected (evidence could not be addressed)", () => {
  assert.throws(
    () =>
      loadSpec(VALID.replace("  - id: st_0002\n    index: 2", "  - index: 2")),
    (err: unknown) =>
      err instanceof SpecValidationError &&
      err.problems.some((p) => p.includes("id is required")),
  );
});

test("RULE — duplicate ids are rejected (evidence would be ambiguous)", () => {
  assert.throws(
    () => loadSpec(VALID.replace("id: st_0003", "id: st_0002")),
    (err: unknown) =>
      err instanceof SpecValidationError &&
      err.problems.some((p) => p.includes("duplicate id")),
  );
});

test("a repair rewrites target and carries id through untouched", () => {
  const spec: Spec = loadSpec(VALID);
  const idBefore = spec.steps[2].id;
  spec.steps[2].target = "#submit-login";
  const repaired = loadSpec(saveSpec(spec));
  assert.equal(
    repaired.steps[2].id,
    idBefore,
    "a locator repair must not change identity",
  );
  assert.equal(repaired.steps[2].target, "#submit-login");
});

test("an insertion renumbers index but leaves every existing id alone", () => {
  const spec: Spec = loadSpec(VALID);
  const idsBefore = spec.steps.map((s) => s.id);
  spec.steps.splice(1, 0, {
    id: "st_new01",
    index: 0,
    action: "click",
    target: "#confirm",
    assert: { testId: "confirmation", visible: true },
  });
  spec.steps.forEach((step, position) => {
    step.index = position + 1;
  });

  const reloaded = loadSpec(saveSpec(spec));
  assert.equal(reloaded.steps.length, 4);
  assert.deepEqual(
    reloaded.steps.filter((s) => s.id !== "st_new01").map((s) => s.id),
    idsBefore,
    "existing ids must survive an insertion in order",
  );
  // The step that was index 2 is now index 3 — which is exactly why index cannot key evidence.
  assert.equal(reloaded.steps.find((s) => s.id === idsBefore[1])?.index, 3);
});

test("REVIEW REGRESSION — press requires a value (it carries the key to send)", () => {
  // st_0003 is a click with a target and no value; as a press that is now invalid.
  const pressNoValue = VALID.replace("action: click", "action: press");
  assert.throws(
    () => loadSpec(pressNoValue),
    (err: unknown) =>
      err instanceof SpecValidationError &&
      err.problems.some((p) =>
        p.includes('value (or valueFrom) is required for action "press"'),
      ),
  );
});

test("round-trips without reordering (a repair must be a one-line diff)", () => {
  const spec = loadSpec(VALID);
  const reloaded = loadSpec(saveSpec(spec));
  assert.deepEqual(reloaded, spec);
});

test("a locator repair is exactly a one-line diff", () => {
  // The whole reason the format is data and not code.
  const before = saveSpec(loadSpec(VALID));
  const spec: Spec = loadSpec(VALID);
  spec.steps[2].target = "#submit-login";
  const after = saveSpec(spec);

  const beforeLines = before.split("\n");
  const afterLines = after.split("\n");
  assert.equal(
    beforeLines.length,
    afterLines.length,
    "a locator repair must not change line count",
  );
  const changed = beforeLines.filter((line, i) => line !== afterLines[i]);
  assert.equal(
    changed.length,
    1,
    `expected exactly 1 changed line, got ${changed.length}`,
  );
  assert.ok(
    changed[0].includes("signin-button"),
    "the changed line must be the target",
  );
});

test("RULE — url, urlPrefix, and urlPattern are each a locator on their own (page-level, not element)", () => {
  const byUrl = VALID.replace(
    `    assert:
      testId: current-user
      hasText: ops@forgedepot.test`,
    `    assert:
      url: http://127.0.0.1:4173/orders`,
  );
  assert.deepEqual(loadSpec(byUrl).steps[2].assert, {
    url: "http://127.0.0.1:4173/orders",
  });

  const byPrefix = VALID.replace(
    `    assert:
      testId: current-user
      hasText: ops@forgedepot.test`,
    `    assert:
      urlPrefix: http://127.0.0.1:4173/`,
  );
  assert.deepEqual(loadSpec(byPrefix).steps[2].assert, {
    urlPrefix: "http://127.0.0.1:4173/",
  });

  const byPattern = VALID.replace(
    `    assert:
      testId: current-user
      hasText: ops@forgedepot.test`,
    `    assert:
      urlPattern: "^http://127\\\\.0\\\\.0\\\\.1:4173/orders/\\\\d+$"`,
  );
  assert.deepEqual(loadSpec(byPattern).steps[2].assert, {
    urlPattern: "^http://127\\.0\\.0\\.1:4173/orders/\\d+$",
  });
});

test("RULE — a url predicate cannot mix with an element assertion field", () => {
  // Before this rule, `{url, hasText}` satisfied the "needs a locator" check via the
  // url field alone and loaded clean — then replay/export silently never checked
  // hasText at all, because it had no element locator to bind to.
  const mixedWithHasText = VALID.replace(
    `    assert:
      testId: current-user
      hasText: ops@forgedepot.test`,
    `    assert:
      url: http://127.0.0.1:4173/orders
      hasText: ops@forgedepot.test`,
  );
  assert.throws(
    () => loadSpec(mixedWithHasText),
    (err: unknown) =>
      err instanceof SpecValidationError &&
      err.problems.some((p) =>
        p.includes(
          "assert cannot combine a url/urlPrefix/urlPattern predicate with an element assertion field",
        ),
      ),
  );

  const mixedWithLocator = VALID.replace(
    `    assert:
      testId: current-user
      hasText: ops@forgedepot.test`,
    `    assert:
      url: http://127.0.0.1:4173/orders
      testId: current-user`,
  );
  assert.throws(
    () => loadSpec(mixedWithLocator),
    (err: unknown) =>
      err instanceof SpecValidationError &&
      err.problems.some((p) =>
        p.includes(
          "assert cannot combine a url/urlPrefix/urlPattern predicate with an element assertion field",
        ),
      ),
  );

  const mixedWithVisible = VALID.replace(
    `    assert:
      testId: current-user
      hasText: ops@forgedepot.test`,
    `    assert:
      urlPrefix: http://127.0.0.1:4173/
      visible: false`,
  );
  assert.throws(
    () => loadSpec(mixedWithVisible),
    (err: unknown) =>
      err instanceof SpecValidationError &&
      err.problems.some((p) =>
        p.includes(
          "assert cannot combine a url/urlPrefix/urlPattern predicate with an element assertion field",
        ),
      ),
  );
});

test("RULE — an assert may carry only ONE of url, urlPrefix, urlPattern, not several at once", () => {
  // The three are different matching modes for the same page-level check, not
  // independent facts to conjoin — a step that needs more than one mode is two
  // assertions on two steps, never one assert with two url keys.
  const twoUrlKeys = VALID.replace(
    `    assert:
      testId: current-user
      hasText: ops@forgedepot.test`,
    `    assert:
      url: http://127.0.0.1:4173/orders
      urlPrefix: http://127.0.0.1:4173/`,
  );
  assert.throws(
    () => loadSpec(twoUrlKeys),
    (err: unknown) =>
      err instanceof SpecValidationError &&
      err.problems.some((p) =>
        p.includes(
          "assert may carry only one of url, urlPrefix, urlPattern — got url, urlPrefix",
        ),
      ),
  );

  const allThreeUrlKeys = VALID.replace(
    `    assert:
      testId: current-user
      hasText: ops@forgedepot.test`,
    `    assert:
      url: http://127.0.0.1:4173/orders
      urlPrefix: http://127.0.0.1:4173/
      urlPattern: "^http://127\\\\.0\\\\.0\\\\.1:4173/orders$"`,
  );
  assert.throws(
    () => loadSpec(allThreeUrlKeys),
    (err: unknown) =>
      err instanceof SpecValidationError &&
      err.problems.some((p) =>
        p.includes(
          "assert may carry only one of url, urlPrefix, urlPattern — got url, urlPrefix, urlPattern",
        ),
      ),
  );
});

test("REVIEW REGRESSION — url/urlPrefix blank-string and urlPattern compilability rules", () => {
  const blankUrl = VALID.replace(
    `    assert:
      testId: current-user
      hasText: ops@forgedepot.test`,
    `    assert:
      url: "   "`,
  );
  assert.throws(
    () => loadSpec(blankUrl),
    (err: unknown) =>
      err instanceof SpecValidationError &&
      err.problems.some((p) => p.includes("assert.url must be a non-empty")),
  );

  const blankPattern = VALID.replace(
    `    assert:
      testId: current-user
      hasText: ops@forgedepot.test`,
    `    assert:
      urlPattern: "   "`,
  );
  assert.throws(
    () => loadSpec(blankPattern),
    (err: unknown) =>
      err instanceof SpecValidationError &&
      err.problems.some((p) =>
        p.includes("assert.urlPattern must be a non-empty"),
      ),
  );

  const uncompilablePattern = VALID.replace(
    `    assert:
      testId: current-user
      hasText: ops@forgedepot.test`,
    `    assert:
      urlPattern: "["`,
  );
  assert.throws(
    () => loadSpec(uncompilablePattern),
    (err: unknown) =>
      err instanceof SpecValidationError &&
      err.problems.some((p) =>
        p.includes("assert.urlPattern must be a valid regular expression"),
      ),
  );
});

test("RULE — url and urlPrefix must be absolute http(s) URLs", () => {
  // A relative value fed to Playwright's toHaveURL resolves through `new URL(value,
  // baseURL)`, which either throws (no base configured) or, if a consuming project's
  // own Playwright config sets one, starts comparing something other than what
  // Cypress/Puppeteer compare raw against `page.url()`. Absolute-only closes both.
  const relativeUrl = VALID.replace(
    `    assert:
      testId: current-user
      hasText: ops@forgedepot.test`,
    `    assert:
      url: /orders`,
  );
  assert.throws(
    () => loadSpec(relativeUrl),
    (err: unknown) =>
      err instanceof SpecValidationError &&
      err.problems.some((p) =>
        p.includes("assert.url must be an absolute http(s) URL"),
      ),
  );

  const relativePrefix = VALID.replace(
    `    assert:
      testId: current-user
      hasText: ops@forgedepot.test`,
    `    assert:
      urlPrefix: orders/`,
  );
  assert.throws(
    () => loadSpec(relativePrefix),
    (err: unknown) =>
      err instanceof SpecValidationError &&
      err.problems.some((p) =>
        p.includes("assert.urlPrefix must be an absolute http(s) URL"),
      ),
  );

  // urlPattern is a regex source, not itself a URL — it is exempt.
  const relativePattern = VALID.replace(
    `    assert:
      testId: current-user
      hasText: ops@forgedepot.test`,
    `    assert:
      urlPattern: "^/orders/\\\\d+$"`,
  );
  assert.deepEqual(loadSpec(relativePattern).steps[2].assert, {
    urlPattern: "^/orders/\\d+$",
  });

  // Absolute passes clean.
  const absoluteUrl = VALID.replace(
    `    assert:
      testId: current-user
      hasText: ops@forgedepot.test`,
    `    assert:
      url: https://example.com/orders`,
  );
  assert.deepEqual(loadSpec(absoluteUrl).steps[2].assert, {
    url: "https://example.com/orders",
  });

  const absolutePrefix = VALID.replace(
    `    assert:
      testId: current-user
      hasText: ops@forgedepot.test`,
    `    assert:
      urlPrefix: https://example.com/`,
  );
  assert.deepEqual(loadSpec(absolutePrefix).steps[2].assert, {
    urlPrefix: "https://example.com/",
  });
});

test("RULE — url and urlPrefix must be canonical (value === new URL(value).href)", () => {
  // Playwright's toHaveURL(string) normalizes its argument through `new URL()` before
  // comparing, so an absolute-but-non-canonical value (an uppercase scheme, an
  // unresolved "/a/../") would still PASS in replay/Playwright while Cypress's raw
  // `cy.url().should("eq", ...)` and Puppeteer's raw `===` would both read it as a
  // mismatch — the exact target divergence canonical-only closes.
  const nonCanonicalUrl = VALID.replace(
    `    assert:
      testId: current-user
      hasText: ops@forgedepot.test`,
    `    assert:
      url: "HTTP://127.0.0.1:4173/a/../"`,
  );
  assert.throws(
    () => loadSpec(nonCanonicalUrl),
    (err: unknown) =>
      err instanceof SpecValidationError &&
      err.problems.some(
        (p) =>
          p.includes("assert.url must be a canonical URL") &&
          p.includes('the canonical form is "http://127.0.0.1:4173/"'),
      ),
  );

  const nonCanonicalPrefix = VALID.replace(
    `    assert:
      testId: current-user
      hasText: ops@forgedepot.test`,
    `    assert:
      urlPrefix: "HTTP://127.0.0.1:4173/orders/../"`,
  );
  assert.throws(
    () => loadSpec(nonCanonicalPrefix),
    (err: unknown) =>
      err instanceof SpecValidationError &&
      err.problems.some(
        (p) =>
          p.includes("assert.urlPrefix must be a canonical URL") &&
          p.includes('the canonical form is "http://127.0.0.1:4173/"'),
      ),
  );

  // A trailing-slash-less bare origin canonicalizes to itself WITH a slash, so it is
  // rejected the same way — the note in the validator's own comment.
  const bareOriginPrefix = VALID.replace(
    `    assert:
      testId: current-user
      hasText: ops@forgedepot.test`,
    `    assert:
      urlPrefix: http://127.0.0.1:4173`,
  );
  assert.throws(
    () => loadSpec(bareOriginPrefix),
    (err: unknown) =>
      err instanceof SpecValidationError &&
      err.problems.some((p) =>
        p.includes("assert.urlPrefix must be a canonical URL"),
      ),
  );

  // The canonical form of each passes clean.
  const canonicalUrl = VALID.replace(
    `    assert:
      testId: current-user
      hasText: ops@forgedepot.test`,
    `    assert:
      url: http://127.0.0.1:4173/`,
  );
  assert.deepEqual(loadSpec(canonicalUrl).steps[2].assert, {
    url: "http://127.0.0.1:4173/",
  });

  const canonicalPrefix = VALID.replace(
    `    assert:
      testId: current-user
      hasText: ops@forgedepot.test`,
    `    assert:
      urlPrefix: http://127.0.0.1:4173/`,
  );
  assert.deepEqual(loadSpec(canonicalPrefix).steps[2].assert, {
    urlPrefix: "http://127.0.0.1:4173/",
  });
});

test("rejects an assert with neither a locator nor a url predicate", () => {
  const vague = VALID.replace(
    `    assert:
      testId: current-user
      hasText: ops@forgedepot.test`,
    `    assert:
      visible: true`,
  );
  assert.throws(
    () => loadSpec(vague),
    (err: unknown) =>
      err instanceof SpecValidationError &&
      err.problems.some((p) =>
        p.includes("assert needs a testId, selector, role, text, or url"),
      ),
  );
});

test("an unknown assertion field is refused — the grammar is closed for captured specs too", () => {
  // A typo in an assertion key is the quietest way to weaken a test: the spec loads, the
  // step replays, and the predicate the author wrote it for never runs.
  assert.throws(
    () =>
      loadSpec(
        VALID.replace(
          "      hasText: ops@forgedepot.test",
          "      containsTex: ops@forgedepot.test",
        ),
      ),
    /assert\.containsTex is not an assertion field/,
  );
});

/**
 * The secret-reference form. A spec is committed, reviewed in a PR and attached to
 * evidence, so a credential typed during capture must never be IN it — the spec names
 * where the value comes from and replay resolves it. These tests pin the grammar: the
 * reference is a reference, the two spellings are exclusive, and a placeholder left
 * over from a redacted recording is not a value.
 */
const VALUE_FROM = VALID.replace(
  "    value: ops@forgedepot.test",
  "    valueFrom: env.APPROVE_AN_ORDER_EMAIL",
);

test("a step may name where its value comes from instead of carrying it", () => {
  const spec = loadSpec(VALUE_FROM);
  assert.equal(spec.steps[1].valueFrom, "env.APPROVE_AN_ORDER_EMAIL");
  assert.equal(spec.steps[1].value, undefined);
});

test("RULE — value and valueFrom are exclusive: both is a contradiction", () => {
  assert.throws(
    () =>
      loadSpec(
        VALUE_FROM.replace(
          "    valueFrom: env.APPROVE_AN_ORDER_EMAIL",
          "    valueFrom: env.APPROVE_AN_ORDER_EMAIL\n    value: ops@forgedepot.test",
        ),
      ),
    (err: unknown) =>
      err instanceof SpecValidationError &&
      err.problems.some((p) => p.includes("carries both value and valueFrom")),
  );
});

test("RULE — neither value nor valueFrom, where a value is required, is still refused", () => {
  assert.throws(
    () => loadSpec(VALID.replace("    value: ops@forgedepot.test\n", "")),
    (err: unknown) =>
      err instanceof SpecValidationError &&
      err.problems.some((p) =>
        p.includes('value (or valueFrom) is required for action "fill"'),
      ),
  );
});

test("RULE — an unsupported valueFrom scheme is refused and the message names the one that works", () => {
  assert.throws(
    () =>
      loadSpec(
        VALUE_FROM.replace(
          "valueFrom: env.APPROVE_AN_ORDER_EMAIL",
          "valueFrom: vault.approve/email",
        ),
      ),
    (err: unknown) =>
      err instanceof SpecValidationError &&
      err.problems.some(
        (p) =>
          p.includes("valueFrom must match env.<NAME>") &&
          p.includes("vault.approve/email"),
      ),
  );
});

test("RULE — valueFrom names an environment variable, not a lowercase one", () => {
  assert.throws(
    () =>
      loadSpec(
        VALUE_FROM.replace(
          "valueFrom: env.APPROVE_AN_ORDER_EMAIL",
          "valueFrom: env.approve_an_order_email",
        ),
      ),
    /valueFrom must match env\.<NAME>/,
  );
});

test("RULE — a literal value that is a redaction placeholder is refused, not replayed", () => {
  // The recorder withholds a classified value; a spec still carrying the placeholder is
  // a recording nobody finished, and it would replay by typing "<secret:password>" into
  // the login form and reporting the failure as a broken locator.
  assert.throws(
    () =>
      loadSpec(
        VALID.replace(
          "    value: ops@forgedepot.test",
          "    value: <secret:email>",
        ),
      ),
    (err: unknown) =>
      err instanceof SpecValidationError &&
      err.problems.some(
        (p) =>
          p.includes("is a redaction placeholder, not a value") &&
          p.includes("valueFrom: env.<NAME>"),
      ),
  );
});

test("a valueFrom step survives a save/load round trip", () => {
  assert.equal(
    loadSpec(saveSpec(loadSpec(VALUE_FROM))).steps[1].valueFrom,
    "env.APPROVE_AN_ORDER_EMAIL",
  );
});

test("RULE — exclusivity is by PRESENCE: an explicitly null value beside a reference is still both", () => {
  // Checking the TYPE of `value` let two shapes through. `value: null` alongside
  // `valueFrom` read as "no literal" to the validator, while the audit's inputs field
  // — which prefers whichever spelling is not undefined — would have reported the
  // literal for a step replay resolved from the environment. A spec that says two
  // things must be refused for saying them, not silently reconciled. (`undefined` is
  // the one exception, and unreachable from YAML: see the note in checkStepValue.)
  for (const literal of ["    value: null", "    value: 1234"]) {
    assert.throws(
      () =>
        loadSpec(
          VALUE_FROM.replace(
            "    valueFrom: env.APPROVE_AN_ORDER_EMAIL",
            `    valueFrom: env.APPROVE_AN_ORDER_EMAIL\n${literal}`,
          ),
        ),
      (err: unknown) =>
        err instanceof SpecValidationError &&
        err.problems.some((p) =>
          p.includes("carries both value and valueFrom"),
        ),
      `${literal.trim()} beside a valueFrom must be refused`,
    );
  }
});

const FRAMED = `
name: framed
startUrl: http://127.0.0.1:4173/
steps:
  - id: st_0001
    index: 1
    action: goto
    target: http://127.0.0.1:4173/
  - id: st_0002
    index: 2
    action: click
    target: "#inner"
    frame:
      - selector: "#outer-frame"
      - name: payment
    assert:
      testId: receipt
      visible: true
`;

test("a step may name the frame chain its target is resolved in, outermost first", () => {
  const spec = loadSpec(FRAMED);
  assert.deepEqual(spec.steps[1].frame, [
    { selector: "#outer-frame" },
    { name: "payment" },
  ]);
});

test("a frame chain survives a save/load round trip", () => {
  assert.deepEqual(loadSpec(saveSpec(loadSpec(FRAMED))).steps[1].frame, [
    { selector: "#outer-frame" },
    { name: "payment" },
  ]);
});

test("RULE — an empty frame chain is rejected: absent means the page, empty means nothing", () => {
  assert.throws(
    () =>
      loadSpec(
        FRAMED.replace(/    frame:\n(      - .*\n)+/, "    frame: []\n"),
      ),
    (err: unknown) =>
      err instanceof SpecValidationError &&
      err.problems.some((p) => p.includes("frame is empty — omit it entirely")),
  );
});

test("RULE — a frame link names a frame exactly one way", () => {
  assert.throws(
    () =>
      loadSpec(
        FRAMED.replace(
          '      - selector: "#outer-frame"',
          '      - selector: "#outer-frame"\n        name: payment',
        ),
      ),
    (err: unknown) =>
      err instanceof SpecValidationError &&
      err.problems.some((p) =>
        p.includes("may name a frame only one way — got selector, name"),
      ),
  );
  assert.throws(
    () =>
      loadSpec(
        FRAMED.replace('      - selector: "#outer-frame"', "      - {}"),
      ),
    (err: unknown) =>
      err instanceof SpecValidationError &&
      err.problems.some((p) => p.includes("names no frame")),
  );
});

test("RULE — an unknown frame key is a load error, never a link that addresses nothing", () => {
  assert.throws(
    () =>
      loadSpec(
        FRAMED.replace(
          '      - selector: "#outer-frame"',
          "      - selctor: x",
        ),
      ),
    (err: unknown) =>
      err instanceof SpecValidationError &&
      err.problems.some((p) => p.includes("selctor is not a frame field")),
  );
});

test("RULE — a blank frame name is refused like a blank locator", () => {
  assert.throws(
    () =>
      loadSpec(FRAMED.replace("      - name: payment", '      - name: "   "')),
    (err: unknown) =>
      err instanceof SpecValidationError &&
      err.problems.some((p) => p.includes("name must be a non-empty string")),
  );
});

test("RULE — a frame on a url assertion is refused: there is one page URL, however many frames", () => {
  // A url assertion is page-level. Scoping it to a frame either means nothing while
  // reading as if it meant something, or silently asserts the frame's own document URL
  // — a different check wearing the same name.
  assert.throws(
    () =>
      loadSpec(
        FRAMED.replace(
          "      testId: receipt\n      visible: true",
          '      url: http://127.0.0.1:4173/done\n      frame:\n        - selector: "#outer-frame"',
        ),
      ),
    (err: unknown) =>
      err instanceof SpecValidationError &&
      err.problems.some((p) =>
        p.includes("cannot carry a frame beside url/urlPrefix/urlPattern"),
      ),
  );
});

test("an element assertion may name a frame of its own — the action's consequence can render elsewhere", () => {
  const spec = loadSpec(
    FRAMED.replace(
      "      testId: receipt\n      visible: true",
      "      testId: receipt\n      visible: true\n      frame:\n        - urlPrefix: http://127.0.0.1:4173/pay",
    ),
  );
  assert.deepEqual(spec.steps[1].assert?.frame, [
    { urlPrefix: "http://127.0.0.1:4173/pay" },
  ]);
});

test("RULE — a goto cannot carry a frame: there is no frame-scoped navigation here", () => {
  assert.throws(
    () =>
      loadSpec(
        FRAMED.replace(
          "    target: http://127.0.0.1:4173/\n",
          '    target: http://127.0.0.1:4173/\n    frame:\n      - selector: "#outer-frame"\n',
        ),
      ),
    (err: unknown) =>
      err instanceof SpecValidationError &&
      err.problems.some((p) =>
        p.includes("a goto navigates the page, so it cannot carry a frame"),
      ),
  );
});
