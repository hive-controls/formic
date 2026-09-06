/**
 * A recording derives locators and PROPOSES expectations. These tests pin the second
 * half: what is proposed is what a reviewer would have SEEN, every proposal is a valid
 * assertion the spec validator accepts, and nothing reaches the spec until it is
 * accepted.
 */
import { test } from "node:test";
import assert from "node:assert/strict";
import { mkdtempSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { LocalPlaywrightDriver } from "../driver/local-playwright.mts";
import { serveDirectory } from "../replay/sample-app-server.mts";
import { checkAssertion } from "../replay/assertions.mts";
import {
  applyProposals,
  describeAssertion,
  proposeAssertion,
} from "./proposals.mts";
import type { VisibleState } from "./events.mts";
import { validateSpec } from "../spec/parse.mts";
import type { Spec } from "../spec/types.mts";

const LOGIN: VisibleState = {
  url: "http://app.test/",
  nodes: [{ heading: true, text: "Sign in" }],
};

const CLICK_SIGNIN = {
  stepId: "st_1",
  action: "click",
  target: "#signin-button",
  targetFacts: { selector: "#signin-button", role: "button", name: "Sign in" },
};

test("a node that appeared is what gets proposed, keyed on its testId", () => {
  const after: VisibleState = {
    url: LOGIN.url,
    nodes: [
      { heading: true, text: "Sign in" },
      { testId: "current-user", text: "ops@example.test" },
    ],
  };
  const proposal = proposeAssertion(CLICK_SIGNIN, LOGIN, after);
  assert.ok(proposal);
  assert.equal(proposal.basis, "appeared");
  assert.deepEqual(proposal.assertion, {
    testId: "current-user",
    hasText: "ops@example.test",
  });
});

test("text that CHANGED inside an element already on screen counts as appeared", () => {
  const before: VisibleState = {
    url: LOGIN.url,
    nodes: [{ testId: "detail-customer", text: "Northwind Traders" }],
  };
  const after: VisibleState = {
    url: LOGIN.url,
    nodes: [{ testId: "detail-customer", text: "Contoso Rail" }],
  };
  const proposal = proposeAssertion(CLICK_SIGNIN, before, after);
  assert.ok(proposal);
  assert.deepEqual(proposal.assertion, {
    testId: "detail-customer",
    hasText: "Contoso Rail",
  });
});

test("a navigation is proposed as the destination a reviewer would see", () => {
  const after: VisibleState = {
    url: "http://app.test/orders",
    nodes: [{ heading: true, text: "Open orders" }],
  };
  const proposal = proposeAssertion(CLICK_SIGNIN, LOGIN, after);
  assert.ok(proposal);
  assert.equal(proposal.basis, "url");
  assert.deepEqual(proposal.assertion, {
    role: "heading",
    name: "Open orders",
    exact: true,
    hasText: "Open orders",
  });
  assert.equal(
    proposal.summary,
    describeAssertion(proposal.assertion),
    "the sentence the human approves is the assertion, with one source",
  );
});

test("a navigation with no heading does NOT promise a destination it cannot check", () => {
  // The summary used to say "the page navigated to <url>" while the assertion it
  // committed was only that a locator is visible — a promise the replay never checks.
  // The url is not in the grammar, so the summary must not claim it.
  const after: VisibleState = {
    url: "http://app.test/orders",
    nodes: [{ testId: "orders-table", text: "" }],
  };
  const proposal = proposeAssertion(CLICK_SIGNIN, LOGIN, after);
  assert.ok(proposal);
  assert.deepEqual(proposal.assertion, { testId: "orders-table" });
  assert.equal(proposal.summary, '[data-testid="orders-table"] is visible');
  assert.doesNotMatch(proposal.summary, /navigat|orders\/|http/);
});

test("a fill says only what the grammar can check — the field is on screen", () => {
  // No assertion field can express an input's VALUE, so a summary saying the field
  // "holds" what was typed asks for a yes to a check that will never run.
  const proposal = proposeAssertion(
    {
      stepId: "st_2",
      action: "fill",
      target: "#email",
      targetFacts: { selector: "#email", role: "textbox", name: "Email" },
    },
    LOGIN,
    LOGIN,
  );
  assert.ok(proposal);
  assert.equal(proposal.basis, "value");
  assert.deepEqual(proposal.assertion, { selector: "#email", visible: true });
  assert.equal(proposal.summary, "#email is visible");
  assert.doesNotMatch(proposal.summary, /holds/);
});

test("every proposal's summary is its own assertion, in words", () => {
  const cases: VisibleState[] = [
    { url: LOGIN.url, nodes: [{ testId: "current-user", text: "ops@x.test" }] },
    {
      url: "http://app.test/next",
      nodes: [{ testId: "next-panel", text: "" }],
    },
    LOGIN,
  ];
  for (const after of cases) {
    const proposal = proposeAssertion(CLICK_SIGNIN, LOGIN, after);
    assert.ok(proposal);
    assert.equal(proposal.summary, describeAssertion(proposal.assertion));
  }
});

test("a step with nothing visibly changed proposes a TRUE assertion, and it RESOLVES", async () => {
  // The old body compared the proposal to a literal and stopped there. The fixture did
  // not contain `#signin-button` in either state, so the "true assertion" it approved was
  // never resolved against anything — the one property the test is named for.
  const directory = mkdtempSync(join(tmpdir(), "formic-proposals-"));
  writeFileSync(
    join(directory, "index.html"),
    `<!doctype html><meta charset="utf-8"><h1>Sign in</h1>
     <button id="signin-button">Sign in</button>`,
  );
  const app = await serveDirectory(directory);
  const driver = new LocalPlaywrightDriver();
  const session = await driver.open();
  try {
    await session.page.goto(`${app.baseUrl}/`);
    const proposal = proposeAssertion(CLICK_SIGNIN, LOGIN, LOGIN);
    assert.ok(
      proposal,
      "a step whose page did not change still has something true",
    );
    assert.deepEqual(proposal.assertion, {
      selector: "#signin-button",
      visible: true,
    });
    await checkAssertion(session.page, proposal.assertion, 2000);
  } finally {
    await session.close();
    await app.close();
  }
});

test("a navigation that arrived at nothing nameable proposes NOTHING, never the page it left", () => {
  // The fallback was the element the step ACTED ON — which lives on the page the
  // navigation left behind. Proposing it after a navigation writes an assertion about a
  // document that is no longer loaded: a step whose check can only fail, adopted by a
  // human who was told the button is still visible.
  const after: VisibleState = { url: "http://app.test/orders", nodes: [] };
  assert.equal(proposeAssertion(CLICK_SIGNIN, LOGIN, after), null);
});

test("a classified node's typed text is never quoted back at it", () => {
  // The proposal path consumed the page's reported facts with no sensitivity metadata,
  // so a contenteditable holding a passport number proposed `hasText: "<the number>"` —
  // the value the capture binding had just withheld, committed as an assertion.
  const after: VisibleState = {
    url: LOGIN.url,
    nodes: [
      { heading: true, text: "Sign in" },
      {
        testId: "ident",
        text: "",
        classification: {
          category: "identification",
          source: "keyword",
          evidence: "passport",
        },
      },
    ],
  };
  const proposal = proposeAssertion(CLICK_SIGNIN, LOGIN, after);
  assert.ok(proposal);
  assert.deepEqual(
    proposal.assertion,
    { testId: "ident", visible: true },
    "a classified node may be asserted VISIBLE and nothing more",
  );
  assert.doesNotMatch(proposal.summary, /reads|contains/);
});

test("text the page had to truncate is proposed as containsText, never exact hasText", () => {
  // The page caps reported text at 120 characters and the proposal wrote that slice as
  // an EXACT `hasText`, so a long heading could never satisfy the assertion recorded
  // from it — a step that failed on the very page it was recorded against.
  const long = "A".repeat(120);
  const after: VisibleState = {
    url: LOGIN.url,
    nodes: [
      { heading: true, text: "Sign in" },
      { testId: "banner", text: long, truncated: true },
    ],
  };
  const proposal = proposeAssertion(CLICK_SIGNIN, LOGIN, after);
  assert.ok(proposal);
  assert.deepEqual(proposal.assertion, {
    testId: "banner",
    containsText: long,
  });
});

test("only ACCEPTED proposals reach the spec — a skipped one leaves the step bare", () => {
  const spec: Spec = {
    name: "recorded",
    startUrl: "http://app.test/",
    steps: [
      { id: "st_0", index: 1, action: "goto", target: "http://app.test/" },
      { id: "st_1", index: 2, action: "click", target: "#signin-button" },
      { id: "st_2", index: 3, action: "click", target: "#approve-button" },
    ],
  };
  const accepted = proposeAssertion(CLICK_SIGNIN, LOGIN, {
    url: LOGIN.url,
    nodes: [{ testId: "current-user", text: "ops@example.test" }],
  });
  assert.ok(accepted);
  const applied = applyProposals(spec, [accepted]);
  assert.deepEqual(applied.steps[1].assert, {
    testId: "current-user",
    hasText: "ops@example.test",
  });
  assert.equal(applied.steps[2].assert, undefined);
  // And the validator is what makes the skip visible instead of silent.
  assert.throws(() => validateSpec(applied), /must carry an assert/);
});
