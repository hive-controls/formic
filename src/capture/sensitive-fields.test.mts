/**
 * The classifier decides what never reaches a committed file, so both directions are
 * load-bearing and neither is symmetric with the other: a field wrongly redacted costs
 * a value the human can supply again, a field wrongly recorded puts someone's phone
 * number in a git history for good.
 *
 * One test per route (type, autocomplete, keyword) plus the negative case, because the
 * routes fail differently — an unannotated field falls through to the keyword list, and
 * that is the route that can over-match.
 *
 * The classification is ONE record — category, the route that decided it, and the
 * evidence that route matched on — because every projection downstream (the binding
 * payload, the proposal, the step log, the rrweb mask) consumes that record instead of
 * deciding again. A second decision site is a second answer.
 */
import { test } from "node:test";
import assert from "node:assert/strict";
import { LocalPlaywrightDriver } from "../driver/local-playwright.mts";
import {
  SENSITIVE_TABLE,
  secretPlaceholder,
  secretVariableName,
  classifySensitiveField,
  sensitiveMatcherSource,
  type SensitiveClassification,
  type SensitiveFieldDescriptor,
} from "./sensitive-fields.mts";

const classify = (field: SensitiveFieldDescriptor) =>
  classifySensitiveField(field, SENSITIVE_TABLE)?.category ?? null;

const record = (field: SensitiveFieldDescriptor) =>
  classifySensitiveField(field, SENSITIVE_TABLE);

test("route 1 — the input's own type is the browser's classification", () => {
  assert.equal(classify({ type: "password" }), "password");
  assert.equal(classify({ type: "email" }), "email");
  assert.equal(classify({ type: "tel" }), "phone");
  assert.equal(classify({ type: "text" }), null);
});

test("route 2 — the autocomplete token is what the author declared", () => {
  assert.equal(
    classify({ type: "text", autocomplete: "username" }),
    "username",
  );
  assert.equal(
    classify({ type: "text", autocomplete: "current-password" }),
    "password",
  );
  assert.equal(
    classify({ type: "text", autocomplete: "one-time-code" }),
    "password",
  );
  assert.equal(
    classify({ type: "text", autocomplete: "cc-number" }),
    "payment",
  );
  assert.equal(classify({ type: "text", autocomplete: "cc-csc" }), "payment");
  assert.equal(classify({ type: "text", autocomplete: "cc-name" }), "payment");
  assert.equal(
    classify({ type: "text", autocomplete: "tel-national" }),
    "phone",
  );
  assert.equal(
    classify({ type: "text", autocomplete: "street-address" }),
    "address",
  );
  assert.equal(
    classify({ type: "text", autocomplete: "postal-code" }),
    "address",
  );
  // A multi-token attribute (`shipping street-address`) still declares the field.
  assert.equal(
    classify({ type: "text", autocomplete: "shipping address-line1" }),
    "address",
  );
});

test("route 3 — an unannotated field is judged on the words around it", () => {
  assert.equal(classify({ type: "text", name: "cardNumber" }), "payment");
  assert.equal(classify({ type: "text", id: "card_number" }), "payment");
  assert.equal(classify({ type: "text", label: "Card Number" }), "payment");
  assert.equal(classify({ type: "text", name: "iban" }), "payment");
  assert.equal(
    classify({ type: "text", label: "Passport number" }),
    "identification",
  );
  assert.equal(classify({ type: "text", name: "ssn" }), "identification");
  assert.equal(classify({ type: "text", id: "postcode" }), "address");
  assert.equal(classify({ type: "text", label: "City" }), "address");
  assert.equal(
    classify({ type: "text", placeholder: "you@example.com" }),
    null,
  );
  assert.equal(classify({ type: "text", label: "Email address" }), "email");
  assert.equal(classify({ type: "text", name: "mobile" }), "phone");
  assert.equal(classify({ type: "text", name: "login" }), "username");
});

test("an ordinary field is NOT redacted — the over-match is the expensive mistake", () => {
  assert.equal(
    classify({
      type: "text",
      id: "note",
      label: "Approval note",
      placeholder: "Optional note for the audit trail",
    }),
    null,
    "an approval note is a note",
  );
  assert.equal(classify({ type: "text", name: "orderId" }), null);
  assert.equal(classify({ type: "text", label: "Quantity" }), null);
  assert.equal(classify({ type: "text", label: "Search" }), null);
  // Whole words only: `mailing` is not `mail`, and `discard` is not `card`.
  assert.equal(classify({ type: "text", name: "mailingListOptIn" }), null);
  assert.equal(classify({ type: "text", label: "Discard reason" }), null);
});

test("every route answers with ONE record naming the route AND its evidence", () => {
  // The record is what every projection consumes. A category with no provenance cannot
  // be reviewed, and a warning that cannot say WHY a field was withheld is a warning the
  // human has to take on faith.
  assert.deepEqual(record({ type: "password" }), {
    category: "password",
    source: "type",
    evidence: "password",
  });
  assert.deepEqual(record({ type: "text", autocomplete: "shipping cc-csc" }), {
    category: "payment",
    source: "autocomplete",
    evidence: "cc-csc",
  });
  assert.deepEqual(record({ type: "text", label: "Card Number" }), {
    category: "payment",
    source: "keyword",
    evidence: "card",
  });
  assert.equal(record({ type: "text", name: "orderId" }), null);
});

test("the placeholder names the category, so a reviewer knows what to supply", () => {
  assert.equal(secretPlaceholder("password"), "<secret:password>");
  assert.equal(secretPlaceholder("email"), "<secret:email>");
  assert.equal(secretPlaceholder("payment"), "<secret:payment>");
});

test("the injected matcher is the SAME function, evaluated — never a second copy", () => {
  const source = sensitiveMatcherSource();
  // The source is stringified, so a transpiler helper the page has never heard of turns
  // the whole matcher into a ReferenceError on injection — silently, since arming the
  // bundle is best-effort. Measured: esbuild's keepNames wraps every inner function in
  // `__name`, and the descriptor reader threw the moment it ran in a real page.
  assert.doesNotMatch(
    source,
    /__name\(/,
    "no transpiler helper may survive into the page",
  );
  const injected = new Function(
    `${source}\nreturn function (field) { return classifySensitiveField(field, SENSITIVE_TABLE); };`,
  )() as (field: SensitiveFieldDescriptor) => SensitiveClassification | null;
  const fields: SensitiveFieldDescriptor[] = [
    { type: "password" },
    { type: "text", autocomplete: "cc-number" },
    { type: "text", name: "cardNumber" },
    { type: "text", id: "note", label: "Approval note" },
  ];
  for (const field of fields) {
    assert.deepEqual(
      injected(field),
      record(field),
      `the page-side matcher must agree with the host's for ${JSON.stringify(field)}`,
    );
  }
});

/** The descriptor half, in a real browser: the accessible-name relationships only a live
 *  DOM can resolve. `label[for]` alone is what the recorder used to read, and every other
 *  labelling form a real form uses fell straight through it. */
async function describeInPage(
  html: string,
  selectors: string[],
): Promise<SensitiveFieldDescriptor[]> {
  const driver = new LocalPlaywrightDriver();
  const session = await driver.open();
  try {
    await session.page.setContent(html);
    return (await session.page.evaluate(
      ([source, targets]) =>
        new Function(
          "targets",
          `${source}\nreturn targets.map(function (one) { return sensitiveDescriptorOf(document.querySelector(one)); });`,
        )(targets),
      [sensitiveMatcherSource(), selectors] as [string, string[]],
    )) as SensitiveFieldDescriptor[];
  } finally {
    await session.close();
  }
}

const LABELLED_FORM = `<label id="lbl">Card number</label>
  <input id="a" type="text" aria-labelledby="lbl">
  <label>Passport number <input id="b" type="text"></label>
  <label for="c">Home</label><label for="c">address</label>
  <input id="c" type="text">
  <input id="d" type="text" aria-label="Mobile phone">
  <input id="e" type="text" name="orderId">`;

test("a field labelled only by aria-labelledby or a wrapping label still classifies", async () => {
  // `label[for]` alone was the whole descriptor, so a field named by `aria-labelledby`,
  // by the label that WRAPS it, or by a second `label[for]` carrying the other half of
  // the phrase was classified as if the page had said nothing about it at all.
  const expected: [string, string | null][] = [
    ["#a", "payment"],
    ["#b", "identification"],
    ["#c", "address"],
    ["#d", "phone"],
    ["#e", null],
  ];
  const described = await describeInPage(
    LABELLED_FORM,
    expected.map(([selector]) => selector),
  );
  for (const [index, [selector, category]] of expected.entries()) {
    assert.equal(
      classify(described[index]),
      category,
      `${selector} described as ${JSON.stringify(described[index])}`,
    );
  }
});

test("a withheld value's variable name is derived, deduped and collision-suffixed", () => {
  const none = new Set<string>();
  // Which flow, which field, what kind — with the part that repeats another dropped.
  assert.equal(
    secretVariableName("approve-an-order", "password", "Password", none),
    "APPROVE_AN_ORDER_PASSWORD",
  );
  assert.equal(
    secretVariableName("checkout", "payment", "cardNumber", none),
    "CHECKOUT_CARD_NUMBER_PAYMENT",
  );
  // DETERMINISTIC: the same flow recorded twice must ask for the same variable, or the
  // value the human already put in their .env is silently orphaned.
  assert.equal(
    secretVariableName("approve-an-order", "password", "Password", none),
    "APPROVE_AN_ORDER_PASSWORD",
  );
  // Two indistinguishable password fields in one flow are still two different values;
  // one name for both would replay the same string into both boxes.
  assert.equal(
    secretVariableName("reset", "password", undefined, new Set()),
    "RESET_PASSWORD",
  );
  assert.equal(
    secretVariableName(
      "reset",
      "password",
      undefined,
      new Set(["RESET_PASSWORD"]),
    ),
    "RESET_PASSWORD_2",
  );
  // A name is an environment variable, so it may not start with a digit — a flow named
  // for a year cannot lend it one.
  assert.equal(
    secretVariableName("2026 audit", "password", undefined, none),
    "AUDIT_PASSWORD",
  );
  // Nothing usable left to name it with still yields a resolvable variable.
  assert.equal(secretVariableName("", "email", "", none), "EMAIL");
});
