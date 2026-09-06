import { test } from "node:test";
import assert from "node:assert/strict";
import { authoredCapturedAt } from "./stamp.mts";

test("a usable SOURCE_DATE_EPOCH is the stamp", () => {
  const warnings: string[] = [];
  assert.equal(
    authoredCapturedAt({ SOURCE_DATE_EPOCH: "1700000000" }, (m) =>
      warnings.push(m),
    ),
    "2023-11-14T22:13:20.000Z",
  );
  assert.deepEqual(warnings, []);
});

test("an unusable value warns once and falls back to the wall clock, never throwing", () => {
  // An out-of-range value passes Number.isSafeInteger but makes toISOString throw, which
  // took down authoring entirely over a mistyped environment variable.
  for (const value of ["99999999999999", "nope", "-1", "1.5"]) {
    const warnings: string[] = [];
    const stamp = authoredCapturedAt({ SOURCE_DATE_EPOCH: value }, (m) =>
      warnings.push(m),
    );
    assert.match(
      stamp,
      /^\d{4}-\d{2}-\d{2}T/,
      `${value} still produced a stamp`,
    );
    assert.equal(warnings.length, 1, `${value} warned exactly once`);
    assert.match(warnings[0], /not reproducible/);
  }
});

test("an unset or empty variable is silent — nothing asked for reproducibility", () => {
  const warnings: string[] = [];
  authoredCapturedAt({}, (m) => warnings.push(m));
  authoredCapturedAt({ SOURCE_DATE_EPOCH: "  " }, (m) => warnings.push(m));
  assert.deepEqual(warnings, []);
});
