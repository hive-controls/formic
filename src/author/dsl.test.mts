import { test } from "node:test";
import assert from "node:assert/strict";
import { DslParseError, parseDsl } from "./dsl.mts";

const VALID = `
name: sign-in
startUrl: http://127.0.0.1:4173/
steps:
  - goto: http://127.0.0.1:4173/
  - fill: "#email"
    value: ops@forgedepot.test
    assert:
      selector: "#email"
      visible: true
  - click: "#signin-button"
    assert:
      testId: current-user
      hasText: ops@forgedepot.test
`;

test("parses a valid authoring doc", () => {
  const doc = parseDsl(VALID);
  assert.equal(doc.name, "sign-in");
  assert.equal(doc.startUrl, "http://127.0.0.1:4173/");
  assert.equal(doc.steps.length, 3);
  assert.deepEqual(doc.steps[0], {
    action: "goto",
    target: "http://127.0.0.1:4173/",
  });
  assert.equal(doc.steps[1].action, "fill");
  assert.equal(doc.steps[1].value, "ops@forgedepot.test");
  assert.deepEqual(doc.steps[2].assert, {
    testId: "current-user",
    hasText: "ops@forgedepot.test",
  });
});

test("name/startUrl are optional at the DSL layer (extend additions omit them)", () => {
  const doc = parseDsl(`steps:\n  - goto: http://x\n`);
  assert.equal(doc.name, undefined);
  assert.equal(doc.startUrl, undefined);
  assert.equal(doc.steps.length, 1);
});

test("REVIEW LINE-NUMBERED — a missing action key is reported with the step's source line", () => {
  const bad = `steps:\n  - value: nope\n`;
  assert.throws(
    () => parseDsl(bad),
    (err: unknown) =>
      err instanceof DslParseError &&
      err.problems.some(
        (p) => p.startsWith("line 2") && p.includes("missing an action key"),
      ),
  );
});

test("two action keys on one step is rejected, naming both", () => {
  const bad = `steps:\n  - goto: http://x\n    click: "#a"\n`;
  assert.throws(
    () => parseDsl(bad),
    (err: unknown) =>
      err instanceof DslParseError &&
      err.problems.some((p) => p.includes("only one action key")),
  );
});

test("an unknown sibling field is rejected", () => {
  const bad = `steps:\n  - click: "#a"\n    typo: 1\n`;
  assert.throws(
    () => parseDsl(bad),
    (err: unknown) =>
      err instanceof DslParseError &&
      err.problems.some((p) => p.includes("unknown field(s) typo")),
  );
});

test("a blank target is rejected", () => {
  const bad = `steps:\n  - click: "   "\n`;
  assert.throws(
    () => parseDsl(bad),
    (err: unknown) =>
      err instanceof DslParseError &&
      err.problems.some((p) => p.includes("needs a non-empty target")),
  );
});

test("assert must be a mapping, not a scalar", () => {
  const bad = `steps:\n  - click: "#a"\n    assert: nope\n`;
  assert.throws(
    () => parseDsl(bad),
    (err: unknown) =>
      err instanceof DslParseError &&
      err.problems.some((p) => p.includes("assert must be a mapping")),
  );
});

test("an empty steps list is rejected", () => {
  assert.throws(() => parseDsl(`steps: []\n`), DslParseError);
});

test("a YAML syntax error surfaces yaml's own line", () => {
  const bad = "steps:\n  - click: [unterminated\n";
  assert.throws(
    () => parseDsl(bad),
    (err: unknown) =>
      err instanceof DslParseError &&
      err.problems.some((p) => /^line \d+:/.test(p)),
  );
});

test("a step may name its own id, and a blank one is refused", () => {
  const parsed = parseDsl(
    `name: t\nstartUrl: http://x/\nsteps:\n  - goto: http://x/\n    id: opening\n`,
  );
  assert.equal(parsed.steps[0].id, "opening");
  assert.throws(
    () =>
      parseDsl(
        `name: t\nstartUrl: http://x/\nsteps:\n  - goto: http://x/\n    id: "  "\n`,
      ),
    /id must be a non-empty string/,
  );
});
