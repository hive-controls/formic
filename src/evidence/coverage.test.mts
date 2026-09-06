/** Coverage proposals as a pure function of the audit record: no browser, no model. */
import { test } from "node:test";
import assert from "node:assert/strict";
import {
  parseInteractiveNodes,
  proposedStepYaml,
  uiDrift,
  untestedComponents,
} from "./coverage.mts";
import type { EvidenceRecord, StepRecord } from "./types.mts";

const SIGN_IN = /* export-denylist: ok */ `- banner: Forge Depot
- heading "Sign in" [level=1]
- text: Email
- textbox "Email":
  - /placeholder: you@example.com
- textbox "Password":
  - /placeholder: any 4+ characters
- button "Sign in"`;

const ORDERS = /* export-denylist: ok */ `- banner: Forge Depot ops@forgedepot.test
- heading "Open orders" [level=1]
- list:
  - listitem:
    - button "SO-4471 Northwind Traders $12,480.00"
  - listitem:
    - button "SO-4472 Contoso Rail $3,905.50"
  - listitem:
    - button "SO-4473 Fabrikam Metals $27,310.25"`;

const DETAIL = `- button "Back to orders"
- heading "Contoso Rail - 6 items" [level=1]
- textbox "Approval note":
  - /placeholder: Optional
- button "Approve order"
- link "Say \\"hi\\""`;

function step(
  index: number,
  action: string,
  targetNode: string | undefined,
  ariaSnapshot: string,
): StepRecord {
  return {
    id: `st_${index}`,
    index,
    action,
    targetNode,
    ariaSnapshot,
    startedAt: 1000 + index,
    endedAt: 1001 + index,
    outcome: "ok",
  };
}

function record(steps: StepRecord[]): EvidenceRecord {
  return {
    decisionId: "dec_cov",
    timestamp: "2026-09-02T00:00:00.000Z",
    systemVersion: "harness@test",
    modelVersion: null,
    specName: "approve-an-order",
    driver: "local-playwright",
    sessionId: null,
    outcome: "passed",
    recording: "captured",
    steps,
    segments: [],
  };
}

test("parseInteractiveNodes: only interactive roles, at any depth, names unescaped, static roles ignored", () => {
  assert.deepEqual(parseInteractiveNodes(SIGN_IN), [
    { role: "textbox", name: "Email" },
    { role: "textbox", name: "Password" },
    { role: "button", name: "Sign in" },
  ]);
  assert.equal(parseInteractiveNodes(ORDERS).length, 3, "nested list buttons");
  assert.deepEqual(parseInteractiveNodes(DETAIL).at(-1), {
    role: "link",
    name: 'Say "hi"',
  });
});

test("untestedComponents: everything a step acted on is subtracted; the rest is proposed once, in order of first appearance", () => {
  const rec = record([
    step(1, "goto", undefined, SIGN_IN),
    step(2, "fill", '- textbox "Email"', SIGN_IN),
    step(3, "fill", '- textbox "Password"', SIGN_IN),
    step(4, "click", '- button "Sign in"', ORDERS),
    step(5, "click", '- button "SO-4472 Contoso Rail $3,905.50"', DETAIL),
    step(6, "fill", '- textbox "Approval note"', DETAIL),
    step(7, "click", '- button "Approve order"', ORDERS),
  ]);
  const untested = untestedComponents(rec);
  assert.deepEqual(
    untested.map((c) => [c.role, c.name, c.firstSeenIndex]),
    [
      ["button", "SO-4471 Northwind Traders $12,480.00", 4],
      ["button", "SO-4473 Fabrikam Metals $27,310.25", 4],
      ["button", "Back to orders", 5],
      ["link", 'Say "hi"', 5],
    ],
  );
  assert.equal(untested[0].firstSeenStepId, "st_4");
  assert.equal(
    untested[0].suggestedTarget,
    'role=button[name="SO-4471 Northwind Traders $12,480.00"]',
  );
  assert.equal(untested[3].suggestedTarget, 'role=link[name="Say \\"hi\\""]');
});

test("a run with no snapshots proposes nothing; a fill target suggests a fill step with a value slot", () => {
  const bare = record([
    { ...step(1, "goto", undefined, ""), ariaSnapshot: undefined },
  ]);
  assert.deepEqual(untestedComponents(bare), []);
  const rec = record([step(1, "goto", undefined, SIGN_IN)]);
  const email = untestedComponents(rec)[0];
  assert.equal(email.suggestedAction, "fill");
  assert.match(
    proposedStepYaml(email),
    /^- action: fill\n  target: 'role=textbox\[name="Email"\]'\n  value: /,
  );
});

test("uiDrift: added and removed interactive components per shared step id; unmatched steps and unchanged steps are silent", () => {
  const previous = record([
    step(1, "goto", undefined, SIGN_IN),
    step(4, "click", '- button "Sign in"', ORDERS),
    step(9, "click", undefined, DETAIL),
  ]);
  previous.decisionId = "dec_prev";
  previous.timestamp = "2026-09-01T00:00:00.000Z";
  const current = record([
    step(1, "goto", undefined, SIGN_IN + '\n- link "Forgot password?"'),
    step(
      4,
      "click",
      '- button "Sign in"',
      ORDERS.replace(
        '- button "SO-4473 Fabrikam Metals $27,310.25"',
        '- button "Export CSV"',
      ),
    ),
    step(5, "click", undefined, DETAIL),
  ]);
  const drift = uiDrift(previous, current);
  assert.equal(drift.previousDecisionId, "dec_prev");
  assert.deepEqual(
    drift.steps.map((d) => [
      d.stepId,
      d.added.map((n) => n.name),
      d.removed.map((n) => n.name),
    ]),
    [
      ["st_1", ["Forgot password?"], []],
      ["st_4", ["Export CSV"], ["SO-4473 Fabrikam Metals $27,310.25"]],
    ],
  );
  assert.deepEqual(
    uiDrift(previous, previous).steps,
    [],
    "no drift against itself",
  );
});
