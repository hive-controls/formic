import { test } from "node:test";
import assert from "node:assert/strict";
import { authorSpec, extendSpec } from "./index.mts";
import { loadSpec, saveSpec, SpecValidationError } from "../spec/parse.mts";
import { DslParseError } from "./dsl.mts";

const DSL = `
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

test("authorSpec builds a spec that round-trips through spec/parse.mts", () => {
  const spec = authorSpec(DSL);
  assert.equal(spec.name, "sign-in");
  assert.equal(spec.steps.length, 3);
  assert.equal(spec.steps[0].index, 1);
  assert.equal(spec.steps[2].index, 3);
  assert.ok(spec.steps.every((s) => typeof s.id === "string" && s.id !== ""));
  const ids = new Set(spec.steps.map((s) => s.id));
  assert.equal(ids.size, 3, "every step gets a distinct id");

  const reloaded = loadSpec(saveSpec(spec));
  assert.deepEqual(reloaded, spec);
});

test("authorSpec requires name and startUrl", () => {
  assert.throws(
    () => authorSpec(`steps:\n  - goto: http://x\n`),
    DslParseError,
  );
});

test("authorSpec re-runs spec/parse.mts's own validation — a state change with no assert is rejected", () => {
  const noAssert = `
name: broken
startUrl: http://x
steps:
  - goto: http://x
  - click: "#a"
`;
  assert.throws(() => authorSpec(noAssert), SpecValidationError);
});

test("extendSpec appends new steps without touching existing ids or index", () => {
  const base = authorSpec(DSL);
  const idsBefore = base.steps.map((s) => s.id);
  const indexBefore = base.steps.map((s) => s.index);

  const extended = extendSpec(
    base,
    `steps:\n  - click: '[data-order-id="SO-1"]'\n    assert:\n      testId: detail\n      visible: true\n`,
  );

  assert.equal(extended.steps.length, 4);
  assert.deepEqual(
    extended.steps.slice(0, 3).map((s) => s.id),
    idsBefore,
    "extension must not reorder or renumber existing steps",
  );
  assert.deepEqual(
    extended.steps.slice(0, 3).map((s) => s.index),
    indexBefore,
    "extension must not renumber existing steps",
  );
  assert.equal(extended.steps[3].index, 4);
  assert.notEqual(extended.steps[3].id, idsBefore[0]);

  // original object is untouched
  assert.equal(base.steps.length, 3);

  const reloaded = loadSpec(saveSpec(extended));
  assert.deepEqual(reloaded, extended);
});

test("extendSpec ignores name/startUrl in additions", () => {
  const base = authorSpec(DSL);
  const extended = extendSpec(
    base,
    `name: renamed\nstartUrl: http://other\nsteps:\n  - waitFor: "#ready"\n`,
  );
  assert.equal(extended.name, base.name);
  assert.equal(extended.startUrl, base.startUrl);
});

test("extendSpec's added step still obeys the state-change rule", () => {
  const base = authorSpec(DSL);
  assert.throws(
    () => extendSpec(base, `steps:\n  - click: "#no-assert"\n`),
    SpecValidationError,
  );
});

test("authoring the same input twice produces the same bytes", () => {
  // Deterministic means byte-reproducible, not merely offline: an authored spec has to be
  // regenerable and diffable against the copy in the repo. Random ids and a wall-clock
  // stamp made every run differ.
  process.env.SOURCE_DATE_EPOCH = "1700000000";
  try {
    assert.equal(saveSpec(authorSpec(DSL)), saveSpec(authorSpec(DSL)));
    assert.deepEqual(
      authorSpec(DSL).steps.map((step) => step.id),
      ["s1", "s2", "s3"],
    );
    assert.equal(authorSpec(DSL).capturedAt, "2023-11-14T22:13:20.000Z");
  } finally {
    delete process.env.SOURCE_DATE_EPOCH;
  }
});

test("an explicit id in the input wins over the derived one, and extension continues numbering", () => {
  const spec = authorSpec(
    `name: t\nstartUrl: http://x/\nsteps:\n  - goto: http://x/\n    id: opening\n`,
  );
  assert.equal(spec.steps[0].id, "opening");
  const extended = extendSpec(spec, `steps:\n  - waitFor: "#ready"\n`);
  assert.equal(extended.steps[0].id, "opening", "existing id is untouched");
  assert.equal(
    extended.steps[1].id,
    "s2",
    "the appended step takes its position",
  );
});

test("a mistyped assertion key is refused, not silently ignored", () => {
  // `containsTex` used to author, save, and load clean while replay asserted nothing —
  // the step read as covered and could never fail.
  assert.throws(
    () =>
      authorSpec(
        `name: t\nstartUrl: http://x/\nsteps:\n  - goto: http://x/\n  - click: "#go"\n    assert:\n      testId: s\n      containsTex: hello\n`,
      ),
    /assert\.containsTex is not an assertion field/,
  );
});

test("a derived id steps past one the input already claims", () => {
  // `id: s2` on step 1 collided with the default for step 2, and a legal document was
  // refused as a duplicate.
  const spec = authorSpec(
    `name: t\nstartUrl: http://x/\nsteps:\n  - goto: http://x/\n    id: s2\n  - waitFor: "#r"\n`,
  );
  assert.deepEqual(
    spec.steps.map((step) => step.id),
    ["s2", "s2-2"],
  );

  const extended = extendSpec(
    authorSpec(
      `name: t\nstartUrl: http://x/\nsteps:\n  - goto: http://x/\n    id: s2\n`,
    ),
    `steps:\n  - waitFor: "#ready"\n`,
  );
  assert.deepEqual(
    extended.steps.map((step) => step.id),
    ["s2", "s2-2"],
  );
});

test("extending a spec carries a referenced value through untouched", () => {
  // The extend path re-validates the WHOLE spec, so a step it silently dropped a
  // `valueFrom` from would either fail that validation or — worse — pass as a step with
  // no value at all and replay by typing nothing into a login form.
  const existing = loadSpec(
    [
      "name: sign-in",
      "startUrl: http://127.0.0.1:4173/",
      "steps:",
      "  - id: st_0001",
      "    index: 1",
      "    action: goto",
      "    target: http://127.0.0.1:4173/",
      "  - id: st_0002",
      "    index: 2",
      "    action: fill",
      '    target: "#password"',
      "    valueFrom: env.SIGN_IN_PASSWORD",
      "    assert:",
      '      selector: "#password"',
      "      visible: true",
    ].join("\n"),
  );
  const extended = extendSpec(
    existing,
    'steps:\n  - click: "#signin-button"\n    assert:\n      testId: current-user\n      visible: true\n',
  );
  assert.equal(extended.steps[1].valueFrom, "env.SIGN_IN_PASSWORD");
  assert.equal(extended.steps[1].value, undefined);
  assert.equal(extended.steps.length, 3);
});
